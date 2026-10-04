// Package fakeupstream is an in-memory tsb-service GraphQL API for tests. It
// answers the MCP's named documents with the same response shapes as the real
// backend (decimal-string money, `categoryID` on update, `locale` on choice
// translations, DATE-backed override dates) and records every call.
package fakeupstream

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"tsb-service/internal/mcp/upstream"
)

// Token is the bearer token the fake expects on /graphql.
const Token = "fake-service-account-token"

// Call is one recorded GraphQL operation.
type Call struct {
	Op   string
	Vars map[string]any
}

// Server is the fake API.
type Server struct {
	*httptest.Server

	mu         sync.Mutex
	Products   []*upstream.Product
	Categories []upstream.Category
	Config     upstream.RestaurantConfig
	Opening    upstream.Week
	Ordering   upstream.Week
	Overrides  map[string]*upstream.ScheduleOverride
	Coupons    []*upstream.Coupon
	Orders     []*upstream.Order
	// Contacts are customers' full details by order id. The fake returns
	// them with every order, like a backend that over-returns, although
	// tsb-mcp never asks for them: tests prove they never reach a tool.
	Contacts map[string]Contact
	Images   map[string][]byte
	Calls    []Call
	// FailOps makes the named operations return a GraphQL error with this code.
	FailOps map[string]string
	// FailAfter[op] = n lets the first n calls (counted since the fake started) of op succeed before FailOps
	// applies to it (to fail a later step of a multi-call flow).
	FailAfter map[string]int
	// TokenRequests counts /oauth/v2/token calls.
	TokenRequests int

	nextID int
	seen   map[string]int
}

// New starts a fake seeded with a small realistic catalogue.
func New() *Server {
	s := &Server{
		Overrides: map[string]*upstream.ScheduleOverride{},
		Images:    map[string][]byte{},
		FailOps:   map[string]string{},
		FailAfter: map[string]int{},
		seen:      map[string]int{},
	}
	s.seed()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /graphql", s.graphql)
	mux.HandleFunc("POST /oauth/v2/token", s.token)
	s.Server = httptest.NewServer(mux)
	return s
}

// Lock/Unlock give tests direct access to the state.
func (s *Server) Lock()   { s.mu.Lock() }
func (s *Server) Unlock() { s.mu.Unlock() }

// Ops returns the recorded operation names.
func (s *Server) Ops() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.Calls))
	for i, c := range s.Calls {
		out[i] = c.Op
	}
	return out
}

// ProductByID returns a copy of a product.
func (s *Server) ProductByID(id string) upstream.Product {
	s.mu.Lock()
	defer s.mu.Unlock()
	return *s.product(id)
}

func (s *Server) seed() {
	s.Categories = []upstream.Category{
		{ID: "cat-maki", Order: 1, Slug: "makis", Name: "Makis", Translations: []upstream.Translation{{Language: "fr", Name: "Makis"}, {Language: "zh", Name: "卷"}}},
		{ID: "cat-sashimi", Order: 2, Slug: "sashimis", Name: "Sashimis", Translations: []upstream.Translation{{Language: "fr", Name: "Sashimis"}, {Language: "zh", Name: "刺身"}}},
		{ID: "cat-box", Order: 3, Slug: "boxes", Name: "Boxes", Translations: []upstream.Translation{{Language: "fr", Name: "Boxes"}, {Language: "zh", Name: "套餐"}}},
	}
	tr := func(fr, en, zh string) []upstream.Translation {
		return []upstream.Translation{{Language: "fr", Name: fr}, {Language: "en", Name: en}, {Language: "zh", Name: zh}}
	}
	s.Products = []*upstream.Product{
		{ID: "p-maki-saumon", Code: new("M1"), Slug: "maki-saumon", Name: "Maki saumon", Price: "4.5", VatCategory: "food", PieceCount: new(6),
			IsAvailable: true, IsVisible: true, IsDiscountable: true, Category: s.categoryRef("cat-maki"), Translations: tr("Maki saumon", "Salmon maki", "三文鱼卷")},
		{ID: "p-sashimi-saumon", Code: new("S1"), Slug: "sashimi-saumon", Name: "Sashimi saumon", Price: "12", VatCategory: "food",
			IsAvailable: true, IsVisible: true, IsDiscountable: true, Category: s.categoryRef("cat-sashimi"), Translations: tr("Sashimi saumon", "Salmon sashimi", "三文鱼刺身")},
		{ID: "p-maki-box", Code: new("B1"), Slug: "maki-box", Name: "Maki box", Price: "13.9", VatCategory: "food",
			IsAvailable: true, IsVisible: true, Category: s.categoryRef("cat-box"), Translations: tr("Maki box", "Maki box", "卷寿司套餐"),
			ChoiceGroups: []upstream.ChoiceGroup{{ID: "g-sauce", MinSelections: 1, MaxSelections: 1, SortOrder: 0, Name: "Sauce",
				Translations: []upstream.ChoiceTranslation{{Locale: "fr", Name: "Sauce"}, {Locale: "zh", Name: "酱汁"}},
				Choices: []upstream.Choice{
					{ID: "c-soja", ChoiceGroupID: "g-sauce", PriceModifier: "0", SortOrder: 0, Name: "Soja", Translations: []upstream.ChoiceTranslation{{Locale: "fr", Name: "Soja"}}},
					{ID: "c-spicy", ChoiceGroupID: "g-sauce", PriceModifier: "0.5", SortOrder: 1, Name: "Mayo épicée", Translations: []upstream.ChoiceTranslation{{Locale: "fr", Name: "Mayo épicée"}}},
				}}}},
		{ID: "p-creme", Slug: "creme-brulee", Name: "Crème brûlée", Price: "5", VatCategory: "food",
			IsAvailable: false, IsVisible: true, Category: s.categoryRef("cat-box"), Translations: tr("Crème brûlée", "Creme brulee", "焦糖布丁")},
	}
	s.Opening = upstream.Week{
		"tuesday":   {Open: "11:30", Close: "14:30", DinnerOpen: "18:00", DinnerClose: "22:00"},
		"wednesday": {Open: "11:30", Close: "14:30", DinnerOpen: "18:00", DinnerClose: "22:00"},
		"thursday":  {Open: "11:30", Close: "14:30", DinnerOpen: "18:00", DinnerClose: "22:00"},
		"friday":    {Open: "11:30", Close: "14:30", DinnerOpen: "18:00", DinnerClose: "22:30"},
		"saturday":  {Open: "11:30", Close: "22:30"},
		"sunday":    {Open: "17:30", Close: "22:00"},
	}
	s.Config = upstream.RestaurantConfig{OrderingEnabled: true, PreparationMinutes: 30, IsCurrentlyOpen: true, IsOrderingCurrentlyOpen: true, UpdatedAt: time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)}
	s.Coupons = []*upstream.Coupon{
		{ID: "cp-welcome", Code: "WELCOME10", DiscountType: "PERCENTAGE", DiscountValue: "10", IsActive: true, Status: "ACTIVE", CreatedAt: time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)},
		{ID: "cp-old", Code: "SUMMER5", DiscountType: "FIXED", DiscountValue: "5", MinOrderAmount: new("30"), IsActive: false, Status: "INACTIVE", CreatedAt: time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)},
	}
	pay := func(st string) *struct {
		Status string `json:"status"`
	} {
		return &struct {
			Status string `json:"status"`
		}{Status: st}
	}
	s.Contacts = map[string]Contact{
		"o-1": {FirstName: "Marie", LastName: "Dupont", Phone: "+32 470 12 34 56", Email: "marie.dupont@example.com"},
		"o-2": {FirstName: "Li", LastName: "Wang", Phone: "0498 76 54 32", Email: "wang.li@qq.com"},
		"o-3": {FirstName: "Paul", LastName: "Martin", Phone: "+32 471 11 22 33", Email: "paul.martin@example.be"},
		"o-5": {FirstName: "Marie", LastName: "Dupont", Phone: "+32 470 12 34 56", Email: "marie.dupont@example.com"},
	}
	s.Orders = []*upstream.Order{
		{ID: "o-1", CreatedAt: time.Date(2026, 10, 3, 10, 5, 0, 0, time.UTC), Status: "DELIVERED", Type: "DELIVERY", IsOnlinePayment: true, TotalPrice: "32.5", DiscountAmount: "0", DeliveryFee: new("2"), DisplayAddress: "Rue X 1, 4000 Liège", Payment: pay("paid"),
			Items: []upstream.OrderItem{{Quantity: 2, UnitPrice: "4.5", TotalPrice: "9"}}},
		{ID: "o-2", CreatedAt: time.Date(2026, 10, 3, 11, 0, 0, 0, time.UTC), Status: "PICKED_UP", Type: "PICKUP", TotalPrice: "20", DiscountAmount: "2",
			Items: []upstream.OrderItem{{Quantity: 1, UnitPrice: "20", TotalPrice: "20"}}},
		{ID: "o-3", CreatedAt: time.Date(2026, 10, 3, 11, 30, 0, 0, time.UTC), Status: "PENDING", Type: "PICKUP", TotalPrice: "15", DiscountAmount: "0",
			OrderNote: new("Sonnez au 0471 11 22 33 ou paul.martin@example.be, code porte 1234"), AddressExtra: new("Appartement 3, tel +32 471 11 22 33"),
			Items: []upstream.OrderItem{{Quantity: 1, UnitPrice: "15", TotalPrice: "15"}}},
		{ID: "o-4", CreatedAt: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC), Status: "CANCELLED", Type: "DELIVERY", TotalPrice: "40", DiscountAmount: "0",
			Items: []upstream.OrderItem{{Quantity: 1, UnitPrice: "40", TotalPrice: "40"}}},
		{ID: "o-5", CreatedAt: time.Date(2026, 10, 2, 19, 0, 0, 0, time.UTC), Status: "DELIVERED", Type: "DELIVERY", TotalPrice: "50", DiscountAmount: "0",
			Items: []upstream.OrderItem{{Quantity: 1, UnitPrice: "50", TotalPrice: "50"}}},
	}
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.TokenRequests++
	s.mu.Unlock()
	if u, p, ok := r.BasicAuth(); !ok || u != "mcp-client" || p != "mcp-secret" {
		http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
		return
	}
	_ = r.ParseForm()
	if r.Form.Get("grant_type") != "client_credentials" || !strings.Contains(r.Form.Get("scope"), "urn:zitadel:iam:org:projects:roles") {
		http.Error(w, `{"error":"invalid_request"}`, http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"access_token":"`+Token+`","token_type":"Bearer","expires_in":3600}`)
}

type request struct {
	OperationName string         `json:"operationName"`
	Query         string         `json:"query"`
	Variables     map[string]any `json:"variables"`
}

func (s *Server) graphql(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+Token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req request
	var upload []byte
	mt, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mt == "multipart/form-data" {
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			b, _ := io.ReadAll(p)
			switch p.FormName() {
			case "operations":
				_ = json.Unmarshal(b, &req)
			case "0":
				upload = b
			}
		}
	} else if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.Calls = append(s.Calls, Call{Op: req.OperationName, Vars: req.Variables})

	w.Header().Set("Content-Type", "application/json")
	s.seen[req.OperationName]++
	if code, ok := s.FailOps[req.OperationName]; ok && s.seen[req.OperationName] > s.FailAfter[req.OperationName] {
		writeError(w, "injected failure", code)
		return
	}
	data, err := s.dispatch(req, upload)
	if err != nil {
		code := "BAD_USER_INPUT"
		if strings.Contains(err.Error(), "not found") {
			code = "NOT_FOUND"
		}
		writeError(w, err.Error(), code)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func writeError(w http.ResponseWriter, msg, code string) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"errors": []map[string]any{{"message": msg, "extensions": map[string]any{"code": code}}},
		"data":   nil,
	})
}

func decode[T any](v any) T {
	var out T
	b, _ := json.Marshal(v)
	_ = json.Unmarshal(b, &out)
	return out
}

func (s *Server) id(prefix string) string {
	s.nextID++
	return prefix + "-new-" + strconv.Itoa(s.nextID)
}

func (s *Server) product(id string) *upstream.Product {
	for _, p := range s.Products {
		if p.ID == id {
			return p
		}
	}
	return nil
}

func (s *Server) group(id string) (*upstream.Product, *upstream.ChoiceGroup) {
	for _, p := range s.Products {
		for i := range p.ChoiceGroups {
			if p.ChoiceGroups[i].ID == id {
				return p, &p.ChoiceGroups[i]
			}
		}
	}
	return nil, nil
}

func (s *Server) choice(id string) (*upstream.ChoiceGroup, *upstream.Choice) {
	for _, p := range s.Products {
		for i := range p.ChoiceGroups {
			g := &p.ChoiceGroups[i]
			for j := range g.Choices {
				if g.Choices[j].ID == id {
					return g, &g.Choices[j]
				}
			}
		}
	}
	return nil, nil
}

func (s *Server) config() map[string]any {
	opening, _ := json.Marshal(s.Opening)
	var ordering json.RawMessage = []byte("null")
	if s.Ordering != nil {
		ordering, _ = json.Marshal(s.Ordering)
	}
	return map[string]any{
		"orderingEnabled": s.Config.OrderingEnabled, "openingHours": json.RawMessage(opening), "orderingHours": ordering,
		"preparationMinutes": s.Config.PreparationMinutes, "isCurrentlyOpen": s.Config.IsCurrentlyOpen,
		"isOrderingCurrentlyOpen": s.Config.IsOrderingCurrentlyOpen && s.Config.OrderingEnabled,
		"nextOpeningAt":           s.Config.NextOpeningAt, "updatedAt": s.Config.UpdatedAt,
	}
}

func dateKey(v any) (string, error) {
	t, err := time.Parse(time.RFC3339, fmt.Sprint(v))
	if err != nil {
		return "", fmt.Errorf("invalid date %v", v)
	}
	// Mirrors tsb-service: the UTC date part is written into a DATE column.
	return t.UTC().Format(time.DateOnly), nil
}

func (s *Server) dispatch(req request, upload []byte) (any, error) {
	v := req.Variables
	switch req.OperationName {
	case "McpProducts":
		return map[string]any{"products": s.Products}, nil
	case "McpProduct":
		p := s.product(fmt.Sprint(v["id"]))
		if p == nil {
			return nil, fmt.Errorf("product not found")
		}
		return map[string]any{"product": p}, nil
	case "McpCategories":
		return map[string]any{"productCategories": s.Categories}, nil
	case "McpRestaurantConfig":
		return map[string]any{"restaurantConfig": s.config()}, nil
	case "McpScheduleOverrides":
		from, _ := dateKey(v["from"])
		to, _ := dateKey(v["to"])
		keys := make([]string, 0, len(s.Overrides))
		for k := range s.Overrides {
			if k >= from && k <= to {
				keys = append(keys, k)
			}
		}
		slices.Sort(keys)
		out := []*upstream.ScheduleOverride{}
		for _, k := range keys {
			out = append(out, s.Overrides[k])
		}
		return map[string]any{"scheduleOverrides": out}, nil
	case "McpUpdateOrderingEnabled":
		s.Config.OrderingEnabled = v["enabled"] == true
		return map[string]any{"updateOrderingEnabled": s.config()}, nil
	case "McpUpdateOpeningHours":
		s.Opening = decode[upstream.Week](v["hours"])
		s.Opening, _ = upstream.ParseWeek(mustJSON(s.Opening))
		return map[string]any{"updateOpeningHours": s.config()}, nil
	case "McpUpdateOrderingHours":
		s.Ordering, _ = upstream.ParseWeek(mustJSON(decode[upstream.Week](v["hours"])))
		return map[string]any{"updateOrderingHours": s.config()}, nil
	case "McpUpdatePreparationMinutes":
		m := int(v["minutes"].(float64))
		if m < 1 || m > 240 {
			return nil, fmt.Errorf("preparation minutes must be between 1 and 240")
		}
		s.Config.PreparationMinutes = m
		return map[string]any{"updatePreparationMinutes": s.config()}, nil
	case "McpUpsertScheduleOverride":
		in := decode[map[string]any](v["input"])
		key, err := dateKey(in["date"])
		if err != nil {
			return nil, err
		}
		ov := &upstream.ScheduleOverride{Date: mustDate(key), Closed: in["closed"] == true, UpdatedAt: time.Now().UTC()}
		if sch, ok := in["schedule"]; ok && sch != nil && !ov.Closed {
			d := decode[upstream.DaySchedule](sch)
			ov.Schedule = &d
		}
		if n, ok := in["note"].(string); ok {
			ov.Note = &n
		}
		s.Overrides[key] = ov
		return map[string]any{"upsertScheduleOverride": ov}, nil
	case "McpDeleteScheduleOverride":
		key, err := dateKey(v["date"])
		if err != nil {
			return nil, err
		}
		delete(s.Overrides, key)
		return map[string]any{"deleteScheduleOverride": true}, nil
	case "McpUpdateProduct":
		return s.updateProduct(v, upload)
	case "McpCreateProduct":
		in := decode[upstream.CreateProductInput](v["input"])
		cat := s.categoryRef(in.CategoryID)
		for _, c := range s.Categories {
			if c.ID == in.CategoryID {
				cat.Name = c.Name
			}
		}
		if cat.Name == "" {
			return nil, fmt.Errorf("category not found")
		}
		p := &upstream.Product{ID: s.id("p"), Code: in.Code, Slug: "new", Name: in.Translations[0].Name, Price: trimPrice(in.Price), VatCategory: in.VatCategory, PieceCount: in.PieceCount,
			IsAvailable: in.IsAvailable, IsVisible: in.IsVisible, IsDiscountable: in.IsDiscountable, IsHalal: in.IsHalal, IsLunchOnly: in.IsLunchOnly, IsSpicy: in.IsSpicy, IsVegetarian: in.IsVegetarian,
			Category: cat, Translations: in.Translations}
		s.Products = append(s.Products, p)
		return map[string]any{"createProduct": p}, nil
	case "McpCreateChoiceGroup":
		in := decode[map[string]any](v["input"])
		p := s.product(fmt.Sprint(in["productId"]))
		if p == nil {
			return nil, fmt.Errorf("product not found")
		}
		g := upstream.ChoiceGroup{ID: s.id("g"), ProductID: p.ID, MinSelections: toInt(in["minSelections"]), MaxSelections: toInt(in["maxSelections"]), SortOrder: toInt(in["sortOrder"]),
			Translations: decode[[]upstream.ChoiceTranslation](in["translations"]), Choices: []upstream.Choice{}}
		g.Name = firstChoiceName(g.Translations)
		p.ChoiceGroups = append(p.ChoiceGroups, g)
		return map[string]any{"createProductChoiceGroup": g}, nil
	case "McpUpdateChoiceGroup":
		_, g := s.group(fmt.Sprint(v["id"]))
		if g == nil {
			return nil, fmt.Errorf("choice group not found")
		}
		in := decode[upstream.ChoiceGroupInput](v["input"])
		if in.MinSelections != nil {
			g.MinSelections = *in.MinSelections
		}
		if in.MaxSelections != nil {
			g.MaxSelections = *in.MaxSelections
		}
		if in.SortOrder != nil {
			g.SortOrder = *in.SortOrder
		}
		if in.Translations != nil {
			g.Translations = in.Translations
			g.Name = firstChoiceName(in.Translations)
		}
		return map[string]any{"updateProductChoiceGroup": g}, nil
	case "McpDeleteChoiceGroup":
		p, g := s.group(fmt.Sprint(v["id"]))
		if g == nil {
			return nil, fmt.Errorf("choice group not found")
		}
		for i := range p.ChoiceGroups {
			if p.ChoiceGroups[i].ID == g.ID {
				p.ChoiceGroups = append(p.ChoiceGroups[:i], p.ChoiceGroups[i+1:]...)
				break
			}
		}
		return map[string]any{"deleteProductChoiceGroup": true}, nil
	case "McpCreateChoice":
		in := decode[upstream.ChoiceInput](v["input"])
		_, g := s.group(in.ChoiceGroupID)
		if g == nil {
			return nil, fmt.Errorf("choice group not found")
		}
		c := upstream.Choice{ID: s.id("c"), ChoiceGroupID: g.ID, PriceModifier: trimPrice(deref(in.PriceModifier)), Translations: in.Translations, Name: firstChoiceName(in.Translations)}
		if in.SortOrder != nil {
			c.SortOrder = *in.SortOrder
		}
		g.Choices = append(g.Choices, c)
		return map[string]any{"createProductChoice": c}, nil
	case "McpUpdateChoice":
		_, c := s.choice(fmt.Sprint(v["id"]))
		if c == nil {
			return nil, fmt.Errorf("choice not found")
		}
		in := decode[upstream.ChoiceInput](v["input"])
		if in.PriceModifier != nil {
			c.PriceModifier = trimPrice(*in.PriceModifier)
		}
		if in.SortOrder != nil {
			c.SortOrder = *in.SortOrder
		}
		if in.Translations != nil {
			c.Translations = in.Translations
			c.Name = firstChoiceName(in.Translations)
		}
		return map[string]any{"updateProductChoice": c}, nil
	case "McpDeleteChoice":
		g, c := s.choice(fmt.Sprint(v["id"]))
		if c == nil {
			return nil, fmt.Errorf("choice not found")
		}
		for i := range g.Choices {
			if g.Choices[i].ID == c.ID {
				g.Choices = append(g.Choices[:i], g.Choices[i+1:]...)
				break
			}
		}
		return map[string]any{"deleteProductChoice": true}, nil
	case "McpCoupons":
		return map[string]any{"coupons": s.Coupons}, nil
	case "McpCoupon":
		for _, c := range s.Coupons {
			if c.ID == fmt.Sprint(v["id"]) {
				return map[string]any{"coupon": c}, nil
			}
		}
		return nil, fmt.Errorf("coupon not found")
	case "McpCreateCoupon":
		in := decode[upstream.CouponInput](v["input"])
		c := &upstream.Coupon{ID: s.id("cp"), CreatedAt: time.Now().UTC()}
		applyCoupon(c, in)
		if c.Code == "" {
			c.Code = "AUTO" + strconv.Itoa(s.nextID)
		}
		s.Coupons = append(s.Coupons, c)
		return map[string]any{"createCoupon": c}, nil
	case "McpUpdateCoupon":
		for _, c := range s.Coupons {
			if c.ID == fmt.Sprint(v["id"]) {
				applyCoupon(c, decode[upstream.CouponInput](v["input"]))
				return map[string]any{"updateCoupon": c}, nil
			}
		}
		return nil, fmt.Errorf("coupon not found")
	case "McpOrders":
		orders := make([]map[string]any, len(s.Orders))
		for i, o := range s.Orders {
			orders[i] = s.orderJSON(o)
		}
		return map[string]any{"orders": orders}, nil
	case "McpOrder":
		for _, o := range s.Orders {
			if o.ID == fmt.Sprint(v["id"]) {
				return map[string]any{"order": s.orderJSON(o)}, nil
			}
		}
		return nil, fmt.Errorf("order not found")
	case "McpOrderHistory":
		return s.orderHistory(decode[upstream.OrderHistoryInput](v["input"])), nil
	case "McpCustomerStats":
		return map[string]any{"customerStats": map[string]any{
			"summary": map[string]any{"totalCustomers": 2, "totalRevenue": "102.5", "averageOrderValue": "34.17", "totalOrders": 3},
			"customers": []map[string]any{
				{"userId": "u-1", "firstName": "Marie", "lastName": "Dupont", "registeredAt": "2026-01-01T10:00:00Z", "totalOrders": 2, "totalAmount": "82.5", "averageOrderAmount": "41.25",
					"firstOrderDate": "2026-10-02T19:00:00Z", "lastOrderDate": "2026-10-03T10:05:00Z", "preferredOrderType": "DELIVERY", "deliveryCount": 2, "pickupCount": 0},
				{"userId": "u-2", "firstName": "Li", "lastName": "Wang", "registeredAt": "2026-02-01T10:00:00Z", "totalOrders": 1, "totalAmount": "20", "averageOrderAmount": "20",
					"firstOrderDate": "2026-10-03T11:00:00Z", "lastOrderDate": "2026-10-03T11:00:00Z", "preferredOrderType": "PICKUP", "deliveryCount": 0, "pickupCount": 1},
			},
		}}, nil
	}
	return nil, fmt.Errorf("unknown operation %s", req.OperationName)
}

func (s *Server) updateProduct(v map[string]any, upload []byte) (any, error) {
	p := s.product(fmt.Sprint(v["id"]))
	if p == nil {
		return nil, fmt.Errorf("product not found")
	}
	in := decode[upstream.UpdateProductInput](v["input"])
	if in.Price != nil {
		p.Price = trimPrice(*in.Price)
	}
	if in.IsAvailable != nil {
		p.IsAvailable = *in.IsAvailable
	}
	if in.IsVisible != nil {
		if *in.IsVisible && len(p.Translations) < 3 {
			return nil, fmt.Errorf("a visible product needs at least 3 translations")
		}
		p.IsVisible = *in.IsVisible
	}
	set := func(dst *bool, src *bool) {
		if src != nil {
			*dst = *src
		}
	}
	set(&p.IsDiscountable, in.IsDiscountable)
	set(&p.IsHalal, in.IsHalal)
	set(&p.IsLunchOnly, in.IsLunchOnly)
	set(&p.IsSpicy, in.IsSpicy)
	set(&p.IsVegetarian, in.IsVegetarian)
	if in.VatCategory != nil {
		p.VatCategory = *in.VatCategory
	}
	if in.Code != nil {
		p.Code = in.Code
	}
	if in.PieceCount != nil {
		p.PieceCount = in.PieceCount
	}
	if in.CategoryID != nil {
		found := false
		for _, c := range s.Categories {
			if c.ID == *in.CategoryID {
				p.Category = s.categoryRef(c.ID)
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("category not found")
		}
	}
	for _, t := range in.Translations {
		replaced := false
		for i := range p.Translations {
			if p.Translations[i].Language == t.Language {
				p.Translations[i] = t
				replaced = true
			}
		}
		if !replaced {
			p.Translations = append(p.Translations, t)
		}
		if t.Language == "fr" {
			p.Name = t.Name
		}
	}
	if upload != nil {
		s.Images[p.ID] = upload
	}
	return map[string]any{"updateProduct": p}, nil
}

func (s *Server) orderHistory(in upstream.OrderHistoryInput) any {
	first, page := in.First, in.Page
	if first <= 0 {
		first = 20
	}
	if page <= 0 {
		page = 1
	}
	var matched []*upstream.Order
	for _, o := range s.Orders {
		if o.Status == "CANCELLED" || o.Status == "FAILED" {
			continue
		}
		if in.StartDate != nil && o.CreatedAt.Before(*in.StartDate) {
			continue
		}
		if in.EndDate != nil && o.CreatedAt.After(*in.EndDate) {
			continue
		}
		if in.Status != nil && o.Status != *in.Status {
			continue
		}
		matched = append(matched, o)
	}
	slices.SortFunc(matched, func(a, b *upstream.Order) int { return b.CreatedAt.Compare(a.CreatedAt) })
	var total int64
	for _, o := range matched {
		c, _ := toCents(o.TotalPrice)
		total += c
	}
	avg := int64(0)
	if len(matched) > 0 {
		avg = total / int64(len(matched))
	}
	start := (page - 1) * first
	end := min(start+first, len(matched))
	pageOrders := []map[string]any{}
	if start < len(matched) {
		for _, o := range matched[start:end] {
			pageOrders = append(pageOrders, s.orderJSON(o))
		}
	}
	return map[string]any{"orderHistory": map[string]any{
		"summary": map[string]any{"totalOrders": len(matched), "totalRevenue": centsStr(total), "averageOrder": centsStr(avg)},
		"orders":  pageOrders,
	}}
}

func applyCoupon(c *upstream.Coupon, in upstream.CouponInput) {
	if in.Code != nil {
		c.Code = strings.ToUpper(*in.Code)
	}
	if in.DiscountType != nil {
		c.DiscountType = strings.ToUpper(*in.DiscountType)
	}
	if in.DiscountValue != nil {
		c.DiscountValue = trimPrice(*in.DiscountValue)
	}
	if in.MinOrderAmount != nil {
		c.MinOrderAmount = new(trimPrice(*in.MinOrderAmount))
	}
	if in.MaxUses != nil {
		c.MaxUses = in.MaxUses
	}
	if in.MaxUsesPerUser != nil {
		c.MaxUsesPerUser = in.MaxUsesPerUser
	}
	if in.IsActive != nil {
		c.IsActive = *in.IsActive
	}
	if in.ValidFrom != nil {
		c.ValidFrom = in.ValidFrom
	}
	if in.ValidUntil != nil {
		c.ValidUntil = in.ValidUntil
	}
	c.Status = "INACTIVE"
	if c.IsActive {
		c.Status = "ACTIVE"
	}
}

// trimPrice mimics decimal.String(): "12.50" -> "12.5", "4.00" -> "4".
func trimPrice(s string) string {
	if !strings.Contains(s, ".") {
		return s
	}
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}

func toCents(s string) (int64, error) {
	whole, frac, _ := strings.Cut(s, ".")
	frac = (frac + "00")[:2]
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, err
	}
	f, _ := strconv.ParseInt(frac, 10, 64)
	return w*100 + f, nil
}

func centsStr(c int64) string { return trimPrice(fmt.Sprintf("%d.%02d", c/100, c%100)) }

func toInt(v any) int {
	f, _ := v.(float64)
	return int(f)
}

func deref(s *string) string {
	if s == nil {
		return "0"
	}
	return *s
}

func firstChoiceName(ts []upstream.ChoiceTranslation) string {
	for _, t := range ts {
		if t.Locale == "fr" {
			return t.Name
		}
	}
	if len(ts) > 0 {
		return ts[0].Name
	}
	return ""
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func mustDate(key string) time.Time {
	t, _ := time.Parse(time.DateOnly, key)
	return t
}

// categoryRef is the category as embedded in a product.
func (s *Server) categoryRef(id string) upstream.CategoryRef {
	ref := upstream.CategoryRef{ID: id}
	for _, c := range s.Categories {
		if c.ID == id {
			ref.Name, ref.Translations = c.Name, c.Translations
		}
	}
	return ref
}

// Contact is a customer's full details.
type Contact struct {
	FirstName, LastName, Phone, Email string
}

// orderJSON is an order as tsb-service would return it with every customer
// field filled in, whatever the query asked for.
func (s *Server) orderJSON(o *upstream.Order) map[string]any {
	b, _ := json.Marshal(o)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if c, ok := s.Contacts[o.ID]; ok {
		m["displayCustomerName"] = strings.ToUpper(c.LastName) + " " + c.FirstName
		m["customer"] = map[string]any{"firstName": c.FirstName, "lastName": c.LastName, "phoneNumber": c.Phone, "email": c.Email}
	} else {
		m["displayCustomerName"] = "Guest"
	}
	return m
}
