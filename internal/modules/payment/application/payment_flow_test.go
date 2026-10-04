package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"tsb-service/pkg/email/scaleway/scalewaytest"
	"tsb-service/pkg/email/smtptest"

	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	addressDomain "tsb-service/internal/modules/address/domain"
	orderApplication "tsb-service/internal/modules/order/application"
	orderDomain "tsb-service/internal/modules/order/domain"
	"tsb-service/internal/modules/payment/domain"
	productApplication "tsb-service/internal/modules/product/application"
	productDomain "tsb-service/internal/modules/product/domain"
	userApplication "tsb-service/internal/modules/user/application"
	userDomain "tsb-service/internal/modules/user/domain"
	"tsb-service/pkg/brand"
)

// flowMollie is a configurable fake of the Mollie REST API.
type flowMollie struct {
	mu       sync.Mutex
	requests []flowRequest
	// status is the status returned by GET /v2/payments/{id}.
	status string
	// getErr / createErr make the matching endpoint answer 5xx / 422.
	getStatusCode    int
	createStatusCode int
	createDelay      time.Duration
	paidAt           string
	// notCancelable makes GET report isCancelable=false; refunded / remaining are the
	// amountRefunded / amountRemaining it reports when set.
	notCancelable       bool
	refunded, remaining string
}

type flowRequest struct {
	Method string
	Path   string
	Body   []byte
}

func (f *flowMollie) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.requests = append(f.requests, flowRequest{Method: r.Method, Path: r.URL.Path, Body: body})
	status, getCode, createCode, delay, paidAt := f.status, f.getStatusCode, f.createStatusCode, f.createDelay, f.paidAt
	notCancelable, refunded, remaining := f.notCancelable, f.refunded, f.remaining
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/hal+json")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v2/payments":
		time.Sleep(delay)
		if createCode != 0 {
			w.WriteHeader(createCode)
			_, _ = w.Write([]byte(`{"status":422,"title":"Unprocessable Entity","detail":"The sum of line totals does not match"}`))
			return
		}
		var req struct {
			Amount struct{ Value, Currency string } `json:"amount"`
		}
		_ = json.Unmarshal(body, &req)
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"resource":"payment","id":"tr_new","status":"open","isCancelable":true,"createdAt":"2026-01-02T10:00:00+00:00","amount":{"value":%q,"currency":"EUR"},"_links":{"checkout":{"href":"https://mollie.test/checkout","type":"text/html"}}}`, req.Amount.Value)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v2/payments/"):
		if getCode != 0 {
			w.WriteHeader(getCode)
			_, _ = w.Write([]byte(`{"status":500,"title":"Internal Server Error","detail":"boom"}`))
			return
		}
		extra := ""
		if paidAt != "" {
			extra = fmt.Sprintf(`,"paidAt":%q`, paidAt)
		}
		if refunded != "" {
			extra += fmt.Sprintf(`,"amountRefunded":{"value":%q,"currency":"EUR"}`, refunded)
		}
		if remaining != "" {
			extra += fmt.Sprintf(`,"amountRemaining":{"value":%q,"currency":"EUR"}`, remaining)
		}
		_, _ = fmt.Fprintf(w, `{"resource":"payment","id":%q,"status":%q,"isCancelable":%t,"amount":{"value":"20.00","currency":"EUR"}%s}`,
			strings.TrimPrefix(r.URL.Path, "/v2/payments/"), status, !notCancelable, extra)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/refunds"):
		var req struct {
			Amount struct{ Value string } `json:"amount"`
		}
		_ = json.Unmarshal(body, &req)
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"resource":"refund","id":"re_1","amount":{"currency":"EUR","value":%q},"status":"pending"}`, req.Amount.Value)
	case r.Method == http.MethodDelete:
		_, _ = w.Write([]byte(`{"resource":"payment","id":"tr_1","status":"canceled"}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *flowMollie) find(method, path string) []flowRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []flowRequest
	for _, r := range f.requests {
		if r.Method == method && r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

func newFlowClient(t *testing.T, fm *flowMollie, timeout time.Duration) mollie.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(fm.handler))
	t.Cleanup(srv.Close)
	hc := srv.Client()
	if timeout > 0 {
		hc.Timeout = timeout
	}
	client, err := mollie.NewClient(hc, mollie.NewAPITestingConfig(false))
	if err != nil {
		t.Fatalf("mollie client: %v", err)
	}
	if err := client.WithAuthenticationValue("test_dummydummydummydummydummydummy"); err != nil {
		t.Fatalf("mollie auth: %v", err)
	}
	client.BaseURL, _ = url.Parse(srv.URL + "/")
	return *client
}

// memRepo is an in-memory domain.PaymentRepository.
type memRepo struct {
	mu        sync.Mutex
	payments  map[string]*domain.MolliePayment
	saveErr   error
	refreshes []domain.PaymentStatusUpdate
	findErr   error
}

func newMemRepo(ps ...*domain.MolliePayment) *memRepo {
	r := &memRepo{payments: map[string]*domain.MolliePayment{}}
	for _, p := range ps {
		r.payments[p.MolliePaymentID] = p
	}
	return r
}

func (r *memRepo) Save(_ context.Context, p *domain.MolliePayment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.saveErr != nil {
		return r.saveErr
	}
	cp := *p
	r.payments[p.MolliePaymentID] = &cp
	return nil
}

func (r *memRepo) MarkAsRefund(_ context.Context, id string, amount decimal.Decimal) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.payments[id]
	if !ok {
		return fmt.Errorf("no payment found for id %s", id)
	}
	p.AmountRefunded = amount
	return nil
}

func (r *memRepo) RefreshStatus(_ context.Context, id string, u *domain.PaymentStatusUpdate) (*uuid.UUID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.payments[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	p.Status = u.Status
	r.refreshes = append(r.refreshes, *u)
	return &p.OrderID, nil
}

func (r *memRepo) UpdateStatusByOrderID(_ context.Context, orderID uuid.UUID, status domain.PaymentStatus) (*domain.MolliePayment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.payments {
		if p.OrderID == orderID {
			p.Status = status
			cp := *p
			return &cp, nil
		}
	}
	return nil, sql.ErrNoRows
}

func (r *memRepo) FindByOrderID(_ context.Context, orderID uuid.UUID) (*domain.MolliePayment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.findErr != nil {
		return nil, r.findErr
	}
	for _, p := range r.payments {
		if p.OrderID == orderID {
			cp := *p
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("failed to find payment by order ID: %w", sql.ErrNoRows)
}

func (r *memRepo) FindByExternalID(_ context.Context, id string) (*domain.MolliePayment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.findErr != nil {
		return nil, r.findErr
	}
	p, ok := r.payments[id]
	if !ok {
		return nil, fmt.Errorf("failed to find payment by ID: %w", sql.ErrNoRows)
	}
	cp := *p
	return &cp, nil
}

func (r *memRepo) FindByOrderIDs(_ context.Context, ids []string) (map[string][]*domain.MolliePayment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string][]*domain.MolliePayment{}
	for _, p := range r.payments {
		for _, id := range ids {
			if p.OrderID.String() == id {
				cp := *p
				out[id] = append(out[id], &cp)
			}
		}
	}
	return out, nil
}

func (r *memRepo) WithPaymentLock(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

// flowOrders is a scripted orderApplication.OrderService.
type flowOrders struct {
	orderApplication.OrderService
	order        *orderDomain.Order
	products     *[]orderDomain.OrderProductRaw
	getErr       error
	getErrAfter  bool // fail GetOrderByID only after UpdateOrder ran
	updateErr    error
	updates      []orderDomain.OrderStatus
	updateCalled bool
}

func (f *flowOrders) GetOrderByID(_ context.Context, _ uuid.UUID) (*orderDomain.Order, *[]orderDomain.OrderProductRaw, error) {
	if f.getErr != nil && (!f.getErrAfter || f.updateCalled) {
		return nil, nil, f.getErr
	}
	if f.order == nil {
		return nil, nil, nil
	}
	cp := *f.order
	return &cp, f.products, nil
}

func (f *flowOrders) UpdateOrder(_ context.Context, _ uuid.UUID, s *orderDomain.OrderStatus, _ *time.Time, _ *orderDomain.OrderCancellationReason) error {
	f.updateCalled = true
	if f.updateErr != nil {
		return f.updateErr
	}
	if s != nil {
		f.updates = append(f.updates, *s)
		f.order.OrderStatus = *s
	}
	return nil
}

type flowProducts struct {
	productApplication.ProductService
	byID map[uuid.UUID]*productDomain.ProductOrderDetails
	err  error
}

func (f *flowProducts) GetProductNamesForInvoice(_ context.Context, ids []string) ([]*productDomain.ProductOrderDetails, error) {
	if f.err != nil {
		return nil, f.err
	}
	var out []*productDomain.ProductOrderDetails
	for _, id := range ids {
		if p, ok := f.byID[uuid.MustParse(id)]; ok {
			out = append(out, p)
		}
	}
	return out, nil
}

type flowUsers struct {
	userApplication.UserService
	user *userDomain.User
	err  error
}

func (f *flowUsers) GetUserByID(_ context.Context, _ string) (*userDomain.User, error) {
	return f.user, f.err
}

// startSMTPSink starts a fake SMTP server and routes the e-mail backend to it for this test (the
// previous backend is restored when the test ends).
func startSMTPSink(t *testing.T) *smtptest.Server {
	t.Helper()
	s := smtptest.Start(t)
	scalewaytest.Use(t, s)
	return s
}

type flowFixture struct {
	svc      *paymentService
	mollie   *flowMollie
	repo     *memRepo
	orders   *flowOrders
	products *flowProducts
	users    *flowUsers
	order    *orderDomain.Order
}

func newFlow(t *testing.T, orderStatus orderDomain.OrderStatus, paymentStatus domain.PaymentStatus) *flowFixture {
	t.Helper()
	order := &orderDomain.Order{
		ID:          uuid.New(),
		UserID:      uuid.New(),
		OrderStatus: orderStatus,
		OrderType:   orderDomain.OrderTypePickUp,
		Language:    "fr",
		TotalPrice:  decimal.RequireFromString("20.00"),
	}
	productID := uuid.New()
	code := "A1"
	fm := &flowMollie{status: string(paymentStatus)}
	payment := &domain.MolliePayment{
		MolliePaymentID: "tr_1",
		OrderID:         order.ID,
		Status:          paymentStatus,
		Amount:          decimal.RequireFromString("20.00"),
		IsCancelable:    true,
	}
	f := &flowFixture{
		mollie: fm,
		repo:   newMemRepo(payment),
		order:  order,
		orders: &flowOrders{
			order: order,
			products: &[]orderDomain.OrderProductRaw{{
				ProductID:  productID,
				Quantity:   2,
				UnitPrice:  decimal.RequireFromString("10.00"),
				TotalPrice: decimal.RequireFromString("20.00"),
			}},
		},
		products: &flowProducts{byID: map[uuid.UUID]*productDomain.ProductOrderDetails{
			productID: {ID: productID, Code: &code, CategoryName: "Sushi", Name: "Maki", VatCategory: productDomain.VatCategoryFood},
		}},
		users: &flowUsers{user: &userDomain.User{ID: order.UserID, Email: "c@example.test", FirstName: "C", LastName: "D"}},
	}
	f.svc = &paymentService{
		repo:           f.repo,
		mollieClient:   newFlowClient(t, fm, 0),
		orderService:   f.orders,
		userService:    f.users,
		productService: f.products,
	}
	return f
}

// ---------------------------------------------------------------------------
// CreatePayment
// ---------------------------------------------------------------------------

func createPaymentOrder(total string) (orderDomain.Order, []orderDomain.OrderProduct) {
	code := "A1"
	o := orderDomain.Order{
		ID:         uuid.New(),
		OrderType:  orderDomain.OrderTypePickUp,
		Language:   "fr",
		TotalPrice: decimal.RequireFromString(total),
	}
	op := []orderDomain.OrderProduct{{
		Product:    orderDomain.Product{ID: uuid.New(), Code: &code, CategoryName: "Sushi", Name: "Maki", VatCategory: string(productDomain.VatCategoryFood)},
		Quantity:   2,
		UnitPrice:  decimal.RequireFromString("10.00"),
		TotalPrice: decimal.RequireFromString("20.00"),
		VatRate:    decimal.RequireFromString("6"),
	}}
	return o, op
}

type createdPayment struct {
	Amount      mollie.Amount         `json:"amount"`
	Description string                `json:"description"`
	RedirectURL string                `json:"redirectUrl"`
	CancelURL   string                `json:"cancelUrl"`
	WebhookURL  string                `json:"webhookUrl"`
	Locale      string                `json:"locale"`
	Lines       []mollie.PaymentLines `json:"lines"`
	Shipping    *mollie.Address       `json:"shippingAddress"`
}

func lastCreated(t *testing.T, fm *flowMollie) createdPayment {
	t.Helper()
	reqs := fm.find(http.MethodPost, "/v2/payments")
	if len(reqs) == 0 {
		t.Fatal("no create payment call reached Mollie")
	}
	var cp createdPayment
	if err := json.Unmarshal(reqs[len(reqs)-1].Body, &cp); err != nil {
		t.Fatalf("decode create body: %v", err)
	}
	return cp
}

func setPaymentEnv(t *testing.T) {
	t.Helper()
	t.Setenv("APP_BASE_URL", "https://shop.example.test")
	t.Setenv("MOLLIE_WEBHOOK_URL", "https://api.example.test/api/v1/payments/webhook")
}

func TestCreatePayment_RequestAndPersistence(t *testing.T) {
	setPaymentEnv(t)
	f := newFlow(t, orderDomain.OrderStatusPending, domain.PaymentStatusOpen)
	o, op := createPaymentOrder("20.00")

	p, err := f.svc.CreatePayment(t.Context(), o, op, userDomain.User{}, nil, nil)
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if p.MolliePaymentID != "tr_new" || p.OrderID != o.ID || p.Status != domain.PaymentStatusOpen {
		t.Fatalf("unexpected payment: %+v", p)
	}
	if saved := f.repo.payments["tr_new"]; saved == nil || !saved.Amount.Equal(o.TotalPrice) {
		t.Fatalf("payment not persisted with the order amount: %+v", saved)
	}

	req := lastCreated(t, f.mollie)
	if req.Amount.Value != "20.00" || req.Amount.Currency != "EUR" {
		t.Errorf("amount = %+v", req.Amount)
	}
	if req.RedirectURL != "https://shop.example.test/order-completed/"+o.ID.String() {
		t.Errorf("redirect = %s", req.RedirectURL)
	}
	if req.CancelURL != "https://shop.example.test/checkout" {
		t.Errorf("cancel = %s", req.CancelURL)
	}
	if req.WebhookURL != "https://api.example.test/api/v1/payments/webhook" {
		t.Errorf("webhook = %s", req.WebhookURL)
	}
	if req.Locale != "fr_BE" {
		t.Errorf("locale = %s", req.Locale)
	}
	if req.Description == "" {
		t.Error("empty payment description")
	}
	if len(req.Lines) != 1 || req.Lines[0].Description != "A1 ‒ Sushi Maki" || req.Lines[0].Quantity != 2 || req.Lines[0].UnitPrice.Value != "10.00" {
		t.Errorf("lines = %+v", req.Lines)
	}
	if req.Shipping != nil {
		t.Error("pickup payment must not carry a shipping address")
	}
}

func TestCreatePayment_PerBrandURLsAndDescription(t *testing.T) {
	cases := []struct {
		name, appBase, redirectOverride, wantRedirectPrefix, brandName string
	}{
		{"tokyosushi default", "https://tokyosushibarliege.be", "", "https://tokyosushibarliege.be/order-completed/", "Tokyo Sushi Bar"},
		{"ygfliege brand", "https://ygfliege.be", "", "https://ygfliege.be/order-completed/", "Yangguofu Malatang Liege"},
		{"custom redirect (mobile deep link)", "https://ygfliege.be", "tsbapp://order-completed", "tsbapp://order-completed/", "Tokyo Sushi Bar"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("APP_BASE_URL", tc.appBase)
			t.Setenv("MOLLIE_WEBHOOK_URL", tc.appBase+"/api/v1/payments/webhook")
			t.Setenv("RESTAURANT_NAME", tc.brandName)
			brandReload(t)
			f := newFlow(t, orderDomain.OrderStatusPending, domain.PaymentStatusOpen)
			o, op := createPaymentOrder("20.00")
			var custom *string
			if tc.redirectOverride != "" {
				custom = &tc.redirectOverride
			}
			if _, err := f.svc.CreatePayment(t.Context(), o, op, userDomain.User{}, nil, custom); err != nil {
				t.Fatalf("CreatePayment: %v", err)
			}
			req := lastCreated(t, f.mollie)
			if req.RedirectURL != tc.wantRedirectPrefix+o.ID.String() {
				t.Errorf("redirect = %s, want prefix %s", req.RedirectURL, tc.wantRedirectPrefix)
			}
			if req.CancelURL != tc.appBase+"/checkout" {
				t.Errorf("cancel = %s", req.CancelURL)
			}
			if req.WebhookURL != tc.appBase+"/api/v1/payments/webhook" {
				t.Errorf("webhook = %s", req.WebhookURL)
			}
			if req.Description != tc.brandName {
				t.Errorf("description = %q, want brand name %q", req.Description, tc.brandName)
			}
		})
	}
}

// brandReload re-reads RESTAURANT_* env vars and restores the default config
// when the test ends.
func brandReload(t *testing.T) {
	t.Helper()
	brand.Load()
	t.Cleanup(func() {
		t.Setenv("RESTAURANT_NAME", "")
		brand.Load()
	})
}

func TestCreatePayment_EmptyCustomRedirectFallsBack(t *testing.T) {
	setPaymentEnv(t)
	f := newFlow(t, orderDomain.OrderStatusPending, domain.PaymentStatusOpen)
	o, op := createPaymentOrder("20.00")
	empty := ""
	if _, err := f.svc.CreatePayment(t.Context(), o, op, userDomain.User{}, nil, &empty); err != nil {
		t.Fatal(err)
	}
	if got := lastCreated(t, f.mollie).RedirectURL; got != "https://shop.example.test/order-completed/"+o.ID.String() {
		t.Fatalf("redirect = %s", got)
	}
}

func TestCreatePayment_MissingEnv(t *testing.T) {
	f := newFlow(t, orderDomain.OrderStatusPending, domain.PaymentStatusOpen)
	o, op := createPaymentOrder("20.00")

	t.Run("APP_BASE_URL", func(t *testing.T) {
		t.Setenv("APP_BASE_URL", "")
		t.Setenv("MOLLIE_WEBHOOK_URL", "https://api.example.test/hook")
		if _, err := f.svc.CreatePayment(t.Context(), o, op, userDomain.User{}, nil, nil); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("MOLLIE_WEBHOOK_URL", func(t *testing.T) {
		t.Setenv("APP_BASE_URL", "https://shop.example.test")
		t.Setenv("MOLLIE_WEBHOOK_URL", "")
		if _, err := f.svc.CreatePayment(t.Context(), o, op, userDomain.User{}, nil, nil); err == nil {
			t.Fatal("want error")
		}
	})
	if n := len(f.mollie.find(http.MethodPost, "/v2/payments")); n != 0 {
		t.Fatalf("Mollie must not be called with a broken config, got %d calls", n)
	}
}

func TestCreatePayment_Locales(t *testing.T) {
	setPaymentEnv(t)
	for lang, want := range map[string]string{"fr": "fr_BE", "en": "en_US", "nl": "nl_BE", "zh": "zh_CN", "de": "fr_BE", "": "fr_BE"} {
		t.Run("lang="+lang, func(t *testing.T) {
			f := newFlow(t, orderDomain.OrderStatusPending, domain.PaymentStatusOpen)
			o, op := createPaymentOrder("20.00")
			o.Language = lang
			if _, err := f.svc.CreatePayment(t.Context(), o, op, userDomain.User{}, nil, nil); err != nil {
				t.Fatal(err)
			}
			if got := lastCreated(t, f.mollie).Locale; got != want {
				t.Fatalf("locale = %s, want %s", got, want)
			}
		})
	}
}

func TestCreatePayment_AmountsFeesAndRounding(t *testing.T) {
	setPaymentEnv(t)
	dec := decimal.RequireFromString
	fee := dec("3.50")
	code := "SAVE10"

	t.Run("lines sum exactly to the amount with fee, discounts and delivery", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusPending, domain.PaymentStatusOpen)
		o, op := createPaymentOrder("21.50")
		o.OrderType = orderDomain.OrderTypeDelivery
		o.DeliveryFee = &fee
		o.TakeawayDiscount = dec("1.00")
		o.CouponDiscount = dec("2.00")
		o.CouponCode = &code
		o.TransactionFee = dec("1.00")
		addr := &addressDomain.Address{StreetName: "Rue X", HouseNumber: "5", Postcode: "4000", MunicipalityName: "Liege"}
		u := userDomain.User{FirstName: "A", LastName: "B"}

		if _, err := f.svc.CreatePayment(t.Context(), o, op, u, addr, nil); err != nil {
			t.Fatal(err)
		}
		req := lastCreated(t, f.mollie)
		if req.Amount.Value != "21.50" {
			t.Fatalf("amount = %s", req.Amount.Value)
		}
		sum := decimal.Zero
		var types []string
		for _, l := range req.Lines {
			sum = sum.Add(dec(l.TotalAmount.Value))
			types = append(types, string(l.Type))
		}
		if !sum.Equal(dec("21.50")) {
			t.Fatalf("line sum %s != amount 21.50 (%v)", sum, types)
		}
		want := []string{"physical", "shipping_fee", "discount", "discount", "surcharge"}
		if strings.Join(types, ",") != strings.Join(want, ",") {
			t.Fatalf("line types = %v, want %v", types, want)
		}
		if req.Lines[3].Description != "Coupon SAVE10" {
			t.Errorf("coupon line = %q", req.Lines[3].Description)
		}
		if req.Shipping == nil || req.Shipping.StreetAndNumber != "Rue X 5" || req.Shipping.Country != "BE" {
			t.Errorf("shipping = %+v", req.Shipping)
		}
	})

	t.Run("total snapped to 10 cents adds a rounding line so Mollie accepts the sum", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusPending, domain.PaymentStatusOpen)
		o, op := createPaymentOrder("20.05") // lines sum to 20.00
		if _, err := f.svc.CreatePayment(t.Context(), o, op, userDomain.User{}, nil, nil); err != nil {
			t.Fatal(err)
		}
		req := lastCreated(t, f.mollie)
		last := req.Lines[len(req.Lines)-1]
		if last.Description != "Ajustement" || last.TotalAmount.Value != "0.05" {
			t.Fatalf("last line = %+v", last)
		}
	})

	t.Run("coupon without code uses the generic label", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusPending, domain.PaymentStatusOpen)
		o, op := createPaymentOrder("18.00")
		o.CouponDiscount = dec("2.00")
		if _, err := f.svc.CreatePayment(t.Context(), o, op, userDomain.User{}, nil, nil); err != nil {
			t.Fatal(err)
		}
		if got := lastCreated(t, f.mollie).Lines[1].Description; got != "Réduction coupon" {
			t.Fatalf("label = %q", got)
		}
	})

	t.Run("zero VAT rate falls back to the category rate", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusPending, domain.PaymentStatusOpen)
		o, op := createPaymentOrder("20.00")
		op[0].VatRate = decimal.Zero
		if _, err := f.svc.CreatePayment(t.Context(), o, op, userDomain.User{}, nil, nil); err != nil {
			t.Fatal(err)
		}
		if got := lastCreated(t, f.mollie).Lines[0].VATRate; got == "" || got == "0.00" {
			t.Fatalf("vat rate = %q, want category fallback", got)
		}
	})

	t.Run("unit price that does not multiply back is sent as one line at the total", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusPending, domain.PaymentStatusOpen)
		o, op := createPaymentOrder("30.50")
		op[0].Quantity = 3
		op[0].UnitPrice = dec("10.17")
		op[0].TotalPrice = dec("30.50")
		if _, err := f.svc.CreatePayment(t.Context(), o, op, userDomain.User{}, nil, nil); err != nil {
			t.Fatal(err)
		}
		l := lastCreated(t, f.mollie).Lines[0]
		if l.Quantity != 1 || l.UnitPrice.Value != "30.50" || !strings.HasPrefix(l.Description, "3 × ") {
			t.Fatalf("line = %+v", l)
		}
	})
}

func TestCreatePayment_MollieFailures(t *testing.T) {
	setPaymentEnv(t)

	t.Run("422 from Mollie returns an error and saves nothing", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusPending, domain.PaymentStatusOpen)
		f.mollie.createStatusCode = http.StatusUnprocessableEntity
		o, op := createPaymentOrder("20.00")
		p, err := f.svc.CreatePayment(t.Context(), o, op, userDomain.User{}, nil, nil)
		if err == nil || p != nil {
			t.Fatalf("p=%v err=%v, want nil + error", p, err)
		}
		if _, ok := f.repo.payments["tr_new"]; ok {
			t.Fatal("a failed creation must not be persisted")
		}
	})

	t.Run("5xx from Mollie returns an error", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusPending, domain.PaymentStatusOpen)
		f.mollie.createStatusCode = http.StatusBadGateway
		o, op := createPaymentOrder("20.00")
		if _, err := f.svc.CreatePayment(t.Context(), o, op, userDomain.User{}, nil, nil); err == nil {
			t.Fatal("want error")
		}
	})

	t.Run("timeout returns an error instead of hanging", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusPending, domain.PaymentStatusOpen)
		f.mollie.createDelay = 300 * time.Millisecond
		f.svc.mollieClient = newFlowClient(t, f.mollie, 50*time.Millisecond)
		o, op := createPaymentOrder("20.00")
		start := time.Now()
		_, err := f.svc.CreatePayment(t.Context(), o, op, userDomain.User{}, nil, nil)
		if err == nil {
			t.Fatal("want timeout error")
		}
		if time.Since(start) > 2*time.Second {
			t.Fatalf("took %s", time.Since(start))
		}
	})

	t.Run("cancelled context returns an error", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusPending, domain.PaymentStatusOpen)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		o, op := createPaymentOrder("20.00")
		if _, err := f.svc.CreatePayment(ctx, o, op, userDomain.User{}, nil, nil); err == nil {
			t.Fatal("want error")
		}
	})

	t.Run("repository failure after Mollie succeeded surfaces the error", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusPending, domain.PaymentStatusOpen)
		f.repo.saveErr = errors.New("db down")
		o, op := createPaymentOrder("20.00")
		p, err := f.svc.CreatePayment(t.Context(), o, op, userDomain.User{}, nil, nil)
		if err == nil || p != nil {
			t.Fatalf("p=%v err=%v", p, err)
		}
	})
}

// A product without any translation yields an empty name; the Mollie line description must
// still be a real, non-blank string (Mollie rejects blank descriptions).
func TestCreatePayment_ProductWithoutTranslationKeepsNonBlankDescription(t *testing.T) {
	setPaymentEnv(t)
	f := newFlow(t, orderDomain.OrderStatusPending, domain.PaymentStatusOpen)
	o, op := createPaymentOrder("20.00")
	op[0].Product.Name = ""
	op[0].Product.CategoryName = ""
	op[0].Product.Code = nil
	if _, err := f.svc.CreatePayment(t.Context(), o, op, userDomain.User{}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if desc := lastCreated(t, f.mollie).Lines[0].Description; strings.TrimSpace(desc) == "" {
		t.Fatal("blank line description")
	}
}

func TestDescribe(t *testing.T) {
	code := "B12"
	empty := ""
	id := uuid.New()
	cases := []struct {
		name string
		p    orderDomain.Product
		want string
	}{
		{"code and names", orderDomain.Product{ID: id, Code: &code, CategoryName: "Sushi", Name: "Maki"}, "B12 ‒ Sushi Maki"},
		{"names only", orderDomain.Product{ID: id, CategoryName: "Sushi", Name: "Maki"}, "Sushi Maki"},
		{"empty code, names", orderDomain.Product{ID: id, Code: &empty, CategoryName: "Sushi", Name: "Maki"}, "Sushi Maki"},
		{"name without category", orderDomain.Product{ID: id, Name: "Maki"}, "Maki"},
		{"code only", orderDomain.Product{ID: id, Code: &code}, "B12"},
		{"nothing falls back to the product id", orderDomain.Product{ID: id}, id.String()},
		{"empty code and nothing", orderDomain.Product{ID: id, Code: &empty}, id.String()},
	}
	for _, tc := range cases {
		if got := describe(tc.p); got != tc.want {
			t.Errorf("%s: %q != %q", tc.name, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// mapExternalPayment
// ---------------------------------------------------------------------------

func TestMapExternalPayment(t *testing.T) {
	now := time.Now()
	id := uuid.New()
	ext := &mollie.Payment{
		ID:                "tr_x",
		Status:            "paid",
		Amount:            &mollie.Amount{Value: "12.30", Currency: "EUR"},
		AmountRefunded:    &mollie.Amount{Value: "1.10", Currency: "EUR"},
		AmountRemaining:   &mollie.Amount{Value: "11.20", Currency: "EUR"},
		AmountCaptured:    &mollie.Amount{Value: "12.30", Currency: "EUR"},
		AmountChargedBack: &mollie.Amount{Value: "0.00", Currency: "EUR"},
		SettlementAmount:  &mollie.Amount{Value: "12.00", Currency: "EUR"},
		CreatedAt:         &now,
		PaidAt:            &now,
		IsCancelable:      true,
		Metadata:          map[string]string{"k": "v"},
	}
	p, err := mapExternalPayment(ext, id)
	if err != nil {
		t.Fatal(err)
	}
	if p.OrderID != id || p.MolliePaymentID != "tr_x" || p.Status != domain.PaymentStatusPaid || !p.IsCancelable {
		t.Fatalf("%+v", p)
	}
	for name, pair := range map[string][2]decimal.Decimal{
		"amount":    {p.Amount, decimal.RequireFromString("12.30")},
		"refunded":  {p.AmountRefunded, decimal.RequireFromString("1.10")},
		"remaining": {p.AmountRemaining, decimal.RequireFromString("11.20")},
		"settle":    {p.SettlementAmount, decimal.RequireFromString("12.00")},
	} {
		if !pair[0].Equal(pair[1]) {
			t.Errorf("%s = %s want %s", name, pair[0], pair[1])
		}
	}
	if string(p.Metadata) != `{"k":"v"}` {
		t.Errorf("metadata = %s", p.Metadata)
	}

	t.Run("optional amounts default to zero and metadata to null", func(t *testing.T) {
		p, err := mapExternalPayment(&mollie.Payment{ID: "tr_y", Amount: &mollie.Amount{Value: "5.00"}, CreatedAt: &now}, id)
		if err != nil {
			t.Fatal(err)
		}
		if !p.AmountRefunded.IsZero() || !p.SettlementAmount.IsZero() || string(p.Metadata) != "null" {
			t.Fatalf("%+v", p)
		}
	})

	for field, mut := range map[string]func(*mollie.Payment){
		"amount":            func(m *mollie.Payment) { m.Amount.Value = "abc" },
		"amountRefunded":    func(m *mollie.Payment) { m.AmountRefunded = &mollie.Amount{Value: "x"} },
		"amountRemaining":   func(m *mollie.Payment) { m.AmountRemaining = &mollie.Amount{Value: "x"} },
		"amountCaptured":    func(m *mollie.Payment) { m.AmountCaptured = &mollie.Amount{Value: "x"} },
		"amountChargedBack": func(m *mollie.Payment) { m.AmountChargedBack = &mollie.Amount{Value: "x"} },
		"settlementAmount":  func(m *mollie.Payment) { m.SettlementAmount = &mollie.Amount{Value: "x"} },
		"metadata":          func(m *mollie.Payment) { m.Metadata = make(chan int) },
	} {
		t.Run("invalid "+field, func(t *testing.T) {
			e := &mollie.Payment{ID: "tr_z", Amount: &mollie.Amount{Value: "5.00"}, CreatedAt: &now}
			mut(e)
			if _, err := mapExternalPayment(e, id); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// FetchMollieStatus / Persist / simple passthroughs
// ---------------------------------------------------------------------------

func TestFetchMollieStatus_EveryStatus(t *testing.T) {
	for _, st := range []string{"open", "pending", "authorized", "paid", "failed", "canceled", "expired"} {
		t.Run(st, func(t *testing.T) {
			f := newFlow(t, orderDomain.OrderStatusPending, domain.PaymentStatusOpen)
			f.mollie.status = st
			f.mollie.paidAt = "2026-01-02T10:00:00+00:00"
			u, err := f.svc.FetchMollieStatus(t.Context(), "tr_1")
			if err != nil {
				t.Fatal(err)
			}
			if string(u.Status) != st {
				t.Fatalf("status = %s", u.Status)
			}
			if u.PaidAt == nil {
				t.Fatal("paidAt not carried over")
			}
		})
	}

	t.Run("Mollie error is returned, nothing persisted", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusPending, domain.PaymentStatusOpen)
		f.mollie.getStatusCode = http.StatusInternalServerError
		u, err := f.svc.FetchMollieStatus(t.Context(), "tr_1")
		if err == nil || u != nil {
			t.Fatalf("u=%v err=%v", u, err)
		}
		if len(f.repo.refreshes) != 0 {
			t.Fatal("FetchMollieStatus must not write to the DB")
		}
	})

	t.Run("404 from Mollie is an error", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusPending, domain.PaymentStatusOpen)
		f.mollie.getStatusCode = http.StatusNotFound
		if _, err := f.svc.FetchMollieStatus(t.Context(), "tr_gone"); err == nil {
			t.Fatal("want error")
		}
	})
}

func TestPersistPaymentStatus(t *testing.T) {
	f := newFlow(t, orderDomain.OrderStatusPending, domain.PaymentStatusOpen)
	if err := f.svc.PersistPaymentStatus(t.Context(), "tr_1", &domain.PaymentStatusUpdate{Status: domain.PaymentStatusPaid}); err != nil {
		t.Fatal(err)
	}
	if f.repo.payments["tr_1"].Status != domain.PaymentStatusPaid {
		t.Fatal("status not persisted")
	}
	if err := f.svc.PersistPaymentStatus(t.Context(), "tr_missing", &domain.PaymentStatusUpdate{Status: domain.PaymentStatusPaid}); err == nil {
		t.Fatal("want error for unknown payment")
	}
}

func TestServicePassthroughs(t *testing.T) {
	f := newFlow(t, orderDomain.OrderStatusPending, domain.PaymentStatusOpen)
	ctx := t.Context()

	if p, err := f.svc.GetPaymentByExternalID(ctx, "tr_1"); err != nil || p.MolliePaymentID != "tr_1" {
		t.Fatalf("by external id: %v %v", p, err)
	}
	if _, err := f.svc.GetPaymentByExternalID(ctx, "tr_none"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unknown id must wrap sql.ErrNoRows so the webhook can tell not-found from a DB outage, got %v", err)
	}
	if p, err := f.svc.GetPaymentByOrderID(ctx, f.order.ID); err != nil || p == nil {
		t.Fatalf("by order id: %v %v", p, err)
	}
	if _, err := f.svc.GetPaymentByOrderID(ctx, uuid.New()); err == nil {
		t.Fatal("want error")
	}
	if p, err := f.svc.UpdatePaymentStatusByOrderID(ctx, f.order.ID, "canceled"); err != nil || p.Status != domain.PaymentStatusCanceled {
		t.Fatalf("update by order: %v %v", p, err)
	}
	if _, err := f.svc.UpdatePaymentStatusByOrderID(ctx, uuid.New(), "canceled"); err == nil {
		t.Fatal("want error")
	}
	if m, err := f.svc.BatchGetPaymentsByOrderIDs(ctx, []string{f.order.ID.String()}); err != nil || len(m[f.order.ID.String()]) != 1 {
		t.Fatalf("batch: %v %v", m, err)
	}
	ran := false
	if err := f.svc.WithPaymentLock(ctx, "tr_1", func(context.Context) error { ran = true; return nil }); err != nil || !ran {
		t.Fatalf("lock: %v ran=%v", err, ran)
	}
}

// ---------------------------------------------------------------------------
// Refunds
// ---------------------------------------------------------------------------

func TestCreateFullRefund(t *testing.T) {
	t.Run("refunds a paid payment once", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusConfirmed, domain.PaymentStatusPaid)
		if err := f.svc.CreateFullRefund(t.Context(), "tr_1"); err != nil {
			t.Fatal(err)
		}
		if err := f.svc.CreateFullRefund(t.Context(), "tr_1"); err != nil {
			t.Fatal(err)
		}
		reqs := f.mollie.find(http.MethodPost, "/v2/payments/tr_1/refunds")
		if len(reqs) != 1 {
			t.Fatalf("refund calls = %d", len(reqs))
		}
		var body struct{ Amount mollie.Amount }
		_ = json.Unmarshal(reqs[0].Body, &body)
		if body.Amount.Value != "20.00" || body.Amount.Currency != "EUR" {
			t.Fatalf("refund body = %s", reqs[0].Body)
		}
		if !f.repo.payments["tr_1"].AmountRefunded.Equal(decimal.RequireFromString("20.00")) {
			t.Fatal("refund not recorded")
		}
	})

	for _, st := range []domain.PaymentStatus{domain.PaymentStatusOpen, domain.PaymentStatusPending, domain.PaymentStatusFailed, domain.PaymentStatusCanceled, domain.PaymentStatusExpired, domain.PaymentStatusAuthorized} {
		t.Run("refuses a "+string(st)+" payment", func(t *testing.T) {
			f := newFlow(t, orderDomain.OrderStatusConfirmed, st)
			if err := f.svc.CreateFullRefund(t.Context(), "tr_1"); err == nil {
				t.Fatal("want error")
			}
			if len(f.mollie.find(http.MethodPost, "/v2/payments/tr_1/refunds")) != 0 {
				t.Fatal("must not call Mollie")
			}
		})
	}

	t.Run("unknown payment", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusConfirmed, domain.PaymentStatusPaid)
		if err := f.svc.CreateFullRefund(t.Context(), "tr_none"); err == nil {
			t.Fatal("want error")
		}
	})
}

func TestRefundRemaining_Failures(t *testing.T) {
	t.Run("Mollie rejects the refund: error, not recorded", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusConfirmed, domain.PaymentStatusPaid)
		failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"status":422,"title":"x","detail":"x"}`))
		}))
		t.Cleanup(failing.Close)
		c, _ := mollie.NewClient(failing.Client(), mollie.NewAPITestingConfig(false))
		_ = c.WithAuthenticationValue("test_dummydummydummydummydummydummy")
		c.BaseURL, _ = url.Parse(failing.URL + "/")
		f.svc.mollieClient = *c

		refunded, err := f.svc.refundRemaining(t.Context(), f.repo.payments["tr_1"])
		if err == nil || refunded {
			t.Fatalf("refunded=%v err=%v", refunded, err)
		}
		if !f.repo.payments["tr_1"].AmountRefunded.IsZero() {
			t.Fatal("a failed refund must not be recorded")
		}
	})

	t.Run("partially refunded payment is refunded for what is left only", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusConfirmed, domain.PaymentStatusPaid)
		f.repo.payments["tr_1"].AmountRefunded = decimal.RequireFromString("5.00")
		refunded, err := f.svc.refundRemaining(t.Context(), f.repo.payments["tr_1"])
		if err != nil || !refunded {
			t.Fatalf("refunded=%v err=%v", refunded, err)
		}
		if got := f.mollie.refundAmounts(t); len(got) != 1 || got[0] != "15.00" {
			t.Fatalf("refund amounts asked of Mollie = %v, want [15.00]", got)
		}
	})
}

// refundAmounts lists the amount asked by each refund request.
func (f *flowMollie) refundAmounts(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, r := range f.find(http.MethodPost, "/v2/payments/tr_1/refunds") {
		var body struct {
			Amount struct{ Value, Currency string } `json:"amount"`
		}
		if err := json.Unmarshal(r.Body, &body); err != nil {
			t.Fatalf("refund body %q: %v", r.Body, err)
		}
		if body.Amount.Currency != "EUR" {
			t.Fatalf("refund currency = %q", body.Amount.Currency)
		}
		out = append(out, body.Amount.Value)
	}
	return out
}

// What is refundable comes from Mollie as well as from our row: the row only knows the refunds this
// service made, while staff can also refund in the Mollie dashboard.
func TestRefundRemaining_UsesMolliesView(t *testing.T) {
	d := decimal.RequireFromString

	t.Run("a larger amountRefunded at Mollie than in our row wins, and the running total is recorded", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusConfirmed, domain.PaymentStatusPaid)
		f.repo.payments["tr_1"].AmountRefunded = d("5.00")
		f.mollie.refunded = "8.00"

		refunded, err := f.svc.refundRemaining(t.Context(), f.repo.payments["tr_1"])

		if err != nil || !refunded {
			t.Fatalf("refunded=%v err=%v", refunded, err)
		}
		if got := f.mollie.refundAmounts(t); len(got) != 1 || got[0] != "12.00" {
			t.Fatalf("refund amounts = %v, want [12.00]", got)
		}
		// 8.00 already returned + this 12.00 refund: the running total, not just the last refund.
		if got := f.repo.payments["tr_1"].AmountRefunded; !got.Equal(d("20.00")) {
			t.Fatalf("recorded amount_refunded = %s, want 20.00", got)
		}
	})

	// A refund can fail or be cancelled at Mollie after it was accepted: Mollie then reports less as
	// refunded than our row, and the difference is refundable again.
	t.Run("a smaller amountRefunded at Mollie than in our row wins: the failed refund is made again", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusConfirmed, domain.PaymentStatusPaid)
		f.repo.payments["tr_1"].AmountRefunded = d("20.00")
		f.mollie.refunded = "5.00"

		refunded, err := f.svc.refundRemaining(t.Context(), f.repo.payments["tr_1"])

		if err != nil || !refunded {
			t.Fatalf("refunded=%v err=%v", refunded, err)
		}
		if got := f.mollie.refundAmounts(t); len(got) != 1 || got[0] != "15.00" {
			t.Fatalf("refund amounts = %v, want [15.00]", got)
		}
		if got := f.repo.payments["tr_1"].AmountRefunded; !got.Equal(d("20.00")) {
			t.Fatalf("recorded amount_refunded = %s, want 5.00 + 15.00", got)
		}
	})

	t.Run("a row that says fully refunded is corrected when Mollie says part of it came back", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusConfirmed, domain.PaymentStatusPaid)
		f.repo.payments["tr_1"].AmountRefunded = d("20.00")
		f.mollie.refunded = "12.00"
		f.mollie.remaining = "0.00" // e.g. a chargeback took the rest: nothing can be refunded

		refunded, err := f.svc.refundRemaining(t.Context(), f.repo.payments["tr_1"])

		if err != nil || refunded {
			t.Fatalf("refunded=%v err=%v, want false nil", refunded, err)
		}
		if got := f.repo.payments["tr_1"].AmountRefunded; !got.Equal(d("12.00")) {
			t.Fatalf("recorded amount_refunded = %s, want Mollie's 12.00", got)
		}
	})

	t.Run("without amountRefunded from Mollie, our row is used", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusConfirmed, domain.PaymentStatusPaid)
		f.repo.payments["tr_1"].AmountRefunded = d("8.00")

		if _, err := f.svc.refundRemaining(t.Context(), f.repo.payments["tr_1"]); err != nil {
			t.Fatal(err)
		}
		if got := f.mollie.refundAmounts(t); len(got) != 1 || got[0] != "12.00" {
			t.Fatalf("refund amounts = %v, want [12.00]", got)
		}
	})

	t.Run("amountRemaining caps the refund (chargebacks)", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusConfirmed, domain.PaymentStatusPaid)
		f.mollie.remaining = "12.50"

		if _, err := f.svc.refundRemaining(t.Context(), f.repo.payments["tr_1"]); err != nil {
			t.Fatal(err)
		}
		if got := f.mollie.refundAmounts(t); len(got) != 1 || got[0] != "12.50" {
			t.Fatalf("refund amounts = %v, want [12.50]", got)
		}
	})

	t.Run("a payment Mollie reports as fully refunded is not refunded again and our row catches up", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusConfirmed, domain.PaymentStatusPaid)
		f.mollie.refunded = "20.00"
		f.mollie.remaining = "0.00"

		refunded, err := f.svc.refundRemaining(t.Context(), f.repo.payments["tr_1"])

		if err != nil || refunded {
			t.Fatalf("refunded=%v err=%v, want false nil", refunded, err)
		}
		if got := f.mollie.refundAmounts(t); len(got) != 0 {
			t.Fatalf("Mollie was asked to refund %v", got)
		}
		if got := f.repo.payments["tr_1"].AmountRefunded; !got.Equal(d("20.00")) {
			t.Fatalf("recorded amount_refunded = %s, want 20.00", got)
		}
	})

	t.Run("failing to record a refund Mollie already made is not an error", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusConfirmed, domain.PaymentStatusPaid)
		f.mollie.refunded = "20.00"
		f.svc.repo = markFailingRepo{f.repo}

		refunded, err := f.svc.refundRemaining(t.Context(), f.repo.payments["tr_1"])

		if err != nil || refunded {
			t.Fatalf("refunded=%v err=%v, want false nil", refunded, err)
		}
	})

	t.Run("a Mollie lookup failure stops the refund before any money moves", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusConfirmed, domain.PaymentStatusPaid)
		f.mollie.getStatusCode = http.StatusInternalServerError

		refunded, err := f.svc.refundRemaining(t.Context(), f.repo.payments["tr_1"])

		if err == nil || refunded {
			t.Fatalf("refunded=%v err=%v, want an error", refunded, err)
		}
		if got := f.mollie.refundAmounts(t); len(got) != 0 {
			t.Fatalf("Mollie was asked to refund %v", got)
		}
	})

	t.Run("a malformed amount from Mollie is an error, not a guess", func(t *testing.T) {
		for name, set := range map[string]func(*flowMollie){
			"amountRefunded":  func(m *flowMollie) { m.refunded = "abc" },
			"amountRemaining": func(m *flowMollie) { m.remaining = "abc" },
		} {
			f := newFlow(t, orderDomain.OrderStatusConfirmed, domain.PaymentStatusPaid)
			set(f.mollie)

			_, err := f.svc.refundRemaining(t.Context(), f.repo.payments["tr_1"])

			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("%s: err = %v", name, err)
			}
			if got := f.mollie.refundAmounts(t); len(got) != 0 {
				t.Fatalf("%s: Mollie was asked to refund %v", name, got)
			}
		}
	})
}

func TestSettleCancelledOrderPayment_EdgeCases(t *testing.T) {
	t.Run("non cancelable open payment is left alone", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusCanceled, domain.PaymentStatusOpen)
		f.mollie.notCancelable = true
		settled, err := f.svc.SettleCancelledOrderPayment(t.Context(), dummyPayment(domain.PaymentStatusOpen))
		refunded := settled.Refunded
		if err != nil || refunded {
			t.Fatalf("refunded=%v err=%v", refunded, err)
		}
		if len(f.mollie.requests) != 1 || f.mollie.requests[0].Method != http.MethodGet {
			t.Fatalf("want the lookup only, got Mollie calls %v", f.mollie.requests)
		}
	})
	for _, st := range []domain.PaymentStatus{domain.PaymentStatusPending, domain.PaymentStatusAuthorized} {
		t.Run(string(st)+" payment is cancelled at Mollie", func(t *testing.T) {
			f := newFlow(t, orderDomain.OrderStatusCanceled, st)
			if _, err := f.svc.SettleCancelledOrderPayment(t.Context(), dummyPayment(st)); err != nil {
				t.Fatal(err)
			}
			if len(f.mollie.find(http.MethodDelete, "/v2/payments/tr_1")) != 1 {
				t.Fatal("want one cancel call")
			}
		})
	}
	for _, st := range []domain.PaymentStatus{domain.PaymentStatusCanceled, domain.PaymentStatusExpired} {
		t.Run(string(st)+" payment needs nothing", func(t *testing.T) {
			f := newFlow(t, orderDomain.OrderStatusCanceled, st)
			if _, err := f.svc.SettleCancelledOrderPayment(t.Context(), dummyPayment(st)); err != nil || len(f.mollie.requests) != 0 {
				t.Fatalf("err=%v calls=%v", err, f.mollie.requests)
			}
		})
	}
	t.Run("Mollie cancel failure is returned so the caller can retry", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusCanceled, domain.PaymentStatusOpen)
		// Mollie shows the payment as open and cancelable, then refuses the cancel.
		bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				w.Header().Set("Content-Type", "application/hal+json")
				_, _ = w.Write([]byte(`{"resource":"payment","id":"tr_1","status":"open","isCancelable":true,"amount":{"value":"20.00","currency":"EUR"}}`))
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(bad.Close)
		c, _ := mollie.NewClient(bad.Client(), mollie.NewAPITestingConfig(false))
		_ = c.WithAuthenticationValue("test_dummydummydummydummydummydummy")
		c.BaseURL, _ = url.Parse(bad.URL + "/")
		f.svc.mollieClient = *c
		_, err := f.svc.SettleCancelledOrderPayment(t.Context(), dummyPayment(domain.PaymentStatusOpen))
		if err == nil || !strings.Contains(err.Error(), "failed to cancel payment") {
			t.Fatalf("err = %v, want the cancel failure", err)
		}
		if len(f.repo.refreshes) != 0 {
			t.Fatal("a cancel Mollie refused must not be recorded")
		}
	})
}

// Settling is decided on Mollie's state and has to be safe to repeat: the caller retries the whole
// cancellation until it works.
func TestSettleCancelledOrderPayment_FollowsMollie(t *testing.T) {
	d := decimal.RequireFromString

	t.Run("paid at Mollie since the last webhook: refunded instead of cancelled", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusCanceled, domain.PaymentStatusOpen)
		f.mollie.status = "paid"

		settled, err := f.svc.SettleCancelledOrderPayment(t.Context(), f.repo.payments["tr_1"])
		refunded := settled.Refunded

		if err != nil || !refunded {
			t.Fatalf("refunded=%v err=%v", refunded, err)
		}
		if got := f.mollie.refundAmounts(t); len(got) != 1 || got[0] != "20.00" {
			t.Fatalf("refund amounts = %v", got)
		}
		if len(f.mollie.find(http.MethodDelete, "/v2/payments/tr_1")) != 0 {
			t.Fatal("a paid payment cannot be cancelled")
		}
	})

	t.Run("already cancelled at Mollie by an earlier attempt: nothing to do, no second cancel", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusCanceled, domain.PaymentStatusOpen)
		f.mollie.status = "canceled"

		settled, err := f.svc.SettleCancelledOrderPayment(t.Context(), f.repo.payments["tr_1"])
		refunded := settled.Refunded

		if err != nil || refunded {
			t.Fatalf("refunded=%v err=%v", refunded, err)
		}
		if len(f.mollie.find(http.MethodDelete, "/v2/payments/tr_1")) != 0 {
			t.Fatal("cancelling an already cancelled payment is refused by Mollie")
		}
	})

	t.Run("not cancelable at Mollie although our row says it is: left alone", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusCanceled, domain.PaymentStatusOpen)
		f.mollie.notCancelable = true

		settled, err := f.svc.SettleCancelledOrderPayment(t.Context(), f.repo.payments["tr_1"])
		refunded := settled.Refunded

		if err != nil || refunded {
			t.Fatalf("refunded=%v err=%v", refunded, err)
		}
		if len(f.mollie.find(http.MethodDelete, "/v2/payments/tr_1")) != 0 {
			t.Fatal("no cancel call expected")
		}
	})

	t.Run("a Mollie lookup failure is returned and nothing is changed", func(t *testing.T) {
		for _, st := range []domain.PaymentStatus{domain.PaymentStatusPaid, domain.PaymentStatusOpen} {
			f := newFlow(t, orderDomain.OrderStatusCanceled, st)
			f.mollie.getStatusCode = http.StatusInternalServerError

			_, err := f.svc.SettleCancelledOrderPayment(t.Context(), f.repo.payments["tr_1"])

			if err == nil {
				t.Fatalf("%s: want an error", st)
			}
			if len(f.mollie.find(http.MethodDelete, "/v2/payments/tr_1"))+len(f.mollie.refundAmounts(t)) != 0 {
				t.Fatalf("%s: money moved although Mollie could not be read", st)
			}
		}
	})

	// The settlement does not write the payment's new status: the caller does so once the order is
	// saved (see PersistPaymentStatus), so that a failed save leaves our row "open" and Mollie's
	// canceled webhook still cancels the order.
	t.Run("a cancelled open payment is handed back as canceled, not recorded by the settlement", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusCanceled, domain.PaymentStatusOpen)

		settled, err := f.svc.SettleCancelledOrderPayment(t.Context(), f.repo.payments["tr_1"])
		if err != nil {
			t.Fatal(err)
		}

		if settled.Refunded {
			t.Fatal("no refund for an open payment")
		}
		if u := settled.StatusUpdate; u == nil || u.Status != domain.PaymentStatusCanceled || u.CanceledAt == nil {
			t.Fatalf("status update = %+v, want canceled with a canceledAt timestamp", u)
		}
		if got := f.repo.payments["tr_1"].Status; got != domain.PaymentStatusOpen {
			t.Fatalf("stored status = %q, want it untouched (open) until the order is saved", got)
		}
		if len(f.repo.refreshes) != 0 {
			t.Fatalf("the settlement wrote the status: %+v", f.repo.refreshes)
		}
	})

	t.Run("already cancelled / expired / failed at Mollie: our row catches up with Mollie's status", func(t *testing.T) {
		for _, st := range []string{"canceled", "expired", "failed"} {
			f := newFlow(t, orderDomain.OrderStatusCanceled, domain.PaymentStatusOpen)
			f.mollie.status = st

			settled, err := f.svc.SettleCancelledOrderPayment(t.Context(), f.repo.payments["tr_1"])

			if err != nil || settled.Refunded {
				t.Fatalf("%s: settled=%+v err=%v", st, settled, err)
			}
			if u := settled.StatusUpdate; u == nil || string(u.Status) != st {
				t.Fatalf("%s: status update = %+v", st, u)
			}
		}
	})

	t.Run("nothing to record when nothing changed at the payment", func(t *testing.T) {
		for name, setup := range map[string]func(*flowMollie){
			"refunded":          func(m *flowMollie) { m.status = "paid" },
			"not cancelable":    func(m *flowMollie) { m.notCancelable = true },
		} {
			f := newFlow(t, orderDomain.OrderStatusCanceled, domain.PaymentStatusOpen)
			setup(f.mollie)

			settled, err := f.svc.SettleCancelledOrderPayment(t.Context(), f.repo.payments["tr_1"])

			if err != nil || settled.StatusUpdate != nil {
				t.Fatalf("%s: settled=%+v err=%v", name, settled, err)
			}
		}
	})

	t.Run("a refund Mollie accepted but we could not record is not repeated on the retry", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusCanceled, domain.PaymentStatusPaid)
		f.svc.repo = markFailingRepo{f.repo}

		if _, err := f.svc.SettleCancelledOrderPayment(t.Context(), f.repo.payments["tr_1"]); err == nil {
			t.Fatal("the first attempt reports the bookkeeping failure")
		}
		// Mollie now reports what the first attempt refunded; the database is healthy again.
		f.mollie.refunded = "20.00"
		f.mollie.remaining = "0.00"
		f.svc.repo = f.repo

		settled, err := f.svc.SettleCancelledOrderPayment(t.Context(), f.repo.payments["tr_1"])
		refunded := settled.Refunded

		if err != nil || refunded {
			t.Fatalf("retry: refunded=%v err=%v, want false nil", refunded, err)
		}
		if got := f.mollie.refundAmounts(t); len(got) != 1 {
			t.Fatalf("refund requests = %v, want exactly the first one", got)
		}
		if got := f.repo.payments["tr_1"].AmountRefunded; !got.Equal(d("20.00")) {
			t.Fatalf("recorded amount_refunded = %s, want 20.00 after the retry", got)
		}
	})
}

// ---------------------------------------------------------------------------
// HandlePaymentPaid
// ---------------------------------------------------------------------------

func TestHandlePaymentPaid_SendsNothing(t *testing.T) {
	sink := startSMTPSink(t)
	f := newFlow(t, orderDomain.OrderStatusPending, domain.PaymentStatusOpen)
	f.users.user.NotifyOrderUpdates = true

	got, err := f.svc.HandlePaymentPaid(t.Context(), f.order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != f.order.ID {
		t.Fatalf("order = %+v", got)
	}
	if sink.Count() != 0 {
		t.Fatalf("emails = %d, the email is SendPaidOrderConfirmation's job", sink.Count())
	}
	if len(f.orders.updates) != 0 {
		t.Fatalf("a paid payment must not change the order status here: %v", f.orders.updates)
	}
}

func TestHandlePaymentPaid_DoesNotNeedProductsOrUser(t *testing.T) {
	f := newFlow(t, orderDomain.OrderStatusPending, domain.PaymentStatusOpen)
	f.products.err = errors.New("db down")
	f.users.user, f.users.err = nil, errors.New("db down")
	if got, err := f.svc.HandlePaymentPaid(t.Context(), f.order.ID); err != nil || got == nil {
		t.Fatalf("got=%v err=%v", got, err)
	}
}

func TestSendPaidOrderConfirmation(t *testing.T) {
	t.Run("sends exactly one email", func(t *testing.T) {
		sink := startSMTPSink(t)
		f := newFlow(t, orderDomain.OrderStatusPending, domain.PaymentStatusOpen)
		f.users.user.NotifyOrderUpdates = true
		if err := f.svc.SendPaidOrderConfirmation(t.Context(), f.order.ID); err != nil {
			t.Fatal(err)
		}
		if sink.Count() != 1 {
			t.Fatalf("emails = %d, want 1", sink.Count())
		}
	})
	t.Run("no email when opted out", func(t *testing.T) {
		sink := startSMTPSink(t)
		f := newFlow(t, orderDomain.OrderStatusPending, domain.PaymentStatusOpen)
		f.users.user.NotifyOrderUpdates = false
		if err := f.svc.SendPaidOrderConfirmation(t.Context(), f.order.ID); err != nil {
			t.Fatal(err)
		}
		if sink.Count() != 0 {
			t.Fatalf("emails = %d", sink.Count())
		}
	})
	t.Run("email backend failure is returned, not panicked", func(t *testing.T) {
		scalewaytest.UseDead(t) // nothing listens on the SMTP port
		f := newFlow(t, orderDomain.OrderStatusPending, domain.PaymentStatusOpen)
		f.users.user.NotifyOrderUpdates = true
		if err := f.svc.SendPaidOrderConfirmation(t.Context(), f.order.ID); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("lookup failures are returned", func(t *testing.T) {
		mut := map[string]func(*flowFixture){
			"order":           func(f *flowFixture) { f.orders.getErr = errors.New("x") },
			"order missing":   func(f *flowFixture) { f.orders.order = nil },
			"no products":     func(f *flowFixture) { f.orders.products = nil },
			"product lookup":  func(f *flowFixture) { f.products.err = errors.New("x") },
			"product missing": func(f *flowFixture) { f.products.byID = map[uuid.UUID]*productDomain.ProductOrderDetails{} },
			"user":            func(f *flowFixture) { f.users.user, f.users.err = nil, errors.New("x") },
		}
		for name, m := range mut {
			f := newFlow(t, orderDomain.OrderStatusPending, domain.PaymentStatusOpen)
			f.users.user.NotifyOrderUpdates = true
			m(f)
			if err := f.svc.SendPaidOrderConfirmation(t.Context(), f.order.ID); err == nil {
				t.Errorf("%s: want error", name)
			}
		}
	})
}

func TestHandlePaymentPaid_Errors(t *testing.T) {
	t.Run("order lookup fails", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusPending, domain.PaymentStatusOpen)
		f.orders.getErr = errors.New("db down")
		if got, err := f.svc.HandlePaymentPaid(t.Context(), f.order.ID); err == nil || got != nil {
			t.Fatalf("got=%v err=%v", got, err)
		}
	})
	t.Run("order missing", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusPending, domain.PaymentStatusOpen)
		f.orders.order = nil
		if got, err := f.svc.HandlePaymentPaid(t.Context(), f.order.ID); err == nil || got != nil {
			t.Fatalf("got=%v err=%v", got, err)
		}
	})
	t.Run("order without products", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusPending, domain.PaymentStatusOpen)
		f.orders.products = nil
		if _, err := f.svc.HandlePaymentPaid(t.Context(), f.order.ID); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("amount mismatch is logged but never blocks the order", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusPending, domain.PaymentStatusOpen)
		f.repo.payments["tr_1"].Amount = decimal.RequireFromString("19.00")
		got, err := f.svc.HandlePaymentPaid(t.Context(), f.order.ID)
		if err != nil || got == nil {
			t.Fatalf("got=%v err=%v", got, err)
		}
	})
	t.Run("payment row missing does not block the order", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusPending, domain.PaymentStatusOpen)
		f.repo.findErr = sql.ErrNoRows
		if got, err := f.svc.HandlePaymentPaid(t.Context(), f.order.ID); err != nil || got == nil {
			t.Fatalf("got=%v err=%v", got, err)
		}
	})
}

func TestHandlePaymentPaid_CancelledOrderEdgeCases(t *testing.T) {
	t.Run("FAILED order is refunded too", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusFailed, domain.PaymentStatusOpen)
		if _, err := f.svc.HandlePaymentPaid(t.Context(), f.order.ID); err != nil {
			t.Fatal(err)
		}
		if len(f.mollie.find(http.MethodPost, "/v2/payments/tr_1/refunds")) != 1 {
			t.Fatal("want one refund")
		}
	})
	t.Run("refund failure returns an error so Mollie retries", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusCanceled, domain.PaymentStatusOpen)
		bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
		t.Cleanup(bad.Close)
		c, _ := mollie.NewClient(bad.Client(), mollie.NewAPITestingConfig(false))
		_ = c.WithAuthenticationValue("test_dummydummydummydummydummydummy")
		c.BaseURL, _ = url.Parse(bad.URL + "/")
		f.svc.mollieClient = *c
		if _, err := f.svc.HandlePaymentPaid(t.Context(), f.order.ID); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("payment row missing for a cancelled order is an error", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusCanceled, domain.PaymentStatusOpen)
		f.repo.payments = map[string]*domain.MolliePayment{}
		if _, err := f.svc.HandlePaymentPaid(t.Context(), f.order.ID); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("refund email goes out once when the customer opted in", func(t *testing.T) {
		sink := startSMTPSink(t)
		f := newFlow(t, orderDomain.OrderStatusCanceled, domain.PaymentStatusOpen)
		f.users.user.NotifyOrderUpdates = true
		if _, err := f.svc.HandlePaymentPaid(t.Context(), f.order.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.HandlePaymentPaid(t.Context(), f.order.ID); err != nil {
			t.Fatal(err)
		}
		if sink.Count() != 1 {
			t.Fatalf("refund emails = %d, want 1", sink.Count())
		}
	})
	t.Run("no refund email when already refunded", func(t *testing.T) {
		sink := startSMTPSink(t)
		f := newFlow(t, orderDomain.OrderStatusCanceled, domain.PaymentStatusPaid)
		f.users.user.NotifyOrderUpdates = true
		f.repo.payments["tr_1"].AmountRefunded = decimal.RequireFromString("20.00")
		if _, err := f.svc.HandlePaymentPaid(t.Context(), f.order.ID); err != nil {
			t.Fatal(err)
		}
		f.mollie.refunded = "20.00" // and Mollie agrees
		if _, err := f.svc.HandlePaymentPaid(t.Context(), f.order.ID); err != nil {
			t.Fatal(err)
		}
		if sink.Count() != 0 || len(f.mollie.refundAmounts(t)) != 0 {
			t.Fatalf("emails=%d refunds=%v", sink.Count(), f.mollie.refundAmounts(t))
		}
	})
}

// ---------------------------------------------------------------------------
// HandlePaymentFailed
// ---------------------------------------------------------------------------

func TestHandlePaymentFailed(t *testing.T) {
	t.Run("cancels the order and returns the refreshed order", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusPending, domain.PaymentStatusOpen)
		got, err := f.svc.HandlePaymentFailed(t.Context(), f.order.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(f.orders.updates) != 1 || f.orders.updates[0] != orderDomain.OrderStatusCanceled {
			t.Fatalf("updates = %v", f.orders.updates)
		}
		if got == nil || got.OrderStatus != orderDomain.OrderStatusCanceled {
			t.Fatalf("order = %+v", got)
		}
	})
	t.Run("update failure is an error so Mollie retries", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusPending, domain.PaymentStatusOpen)
		f.orders.updateErr = errors.New("db down")
		if got, err := f.svc.HandlePaymentFailed(t.Context(), f.order.ID); err == nil || got != nil {
			t.Fatalf("got=%v err=%v", got, err)
		}
	})
	t.Run("reload failure after the cancel is not an error (nothing to publish)", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusPending, domain.PaymentStatusOpen)
		f.orders.getErr, f.orders.getErrAfter = errors.New("db down"), true
		got, err := f.svc.HandlePaymentFailed(t.Context(), f.order.ID)
		if err != nil || got != nil {
			t.Fatalf("got=%v err=%v", got, err)
		}
		if len(f.orders.updates) != 1 {
			t.Fatal("the cancellation must still have happened")
		}
	})
	t.Run("never sends an email", func(t *testing.T) {
		sink := startSMTPSink(t)
		f := newFlow(t, orderDomain.OrderStatusPending, domain.PaymentStatusOpen)
		f.users.user.NotifyOrderUpdates = true
		if _, err := f.svc.HandlePaymentFailed(t.Context(), f.order.ID); err != nil {
			t.Fatal(err)
		}
		if sink.Count() != 0 {
			t.Fatalf("emails = %d", sink.Count())
		}
	})
}

// ---------------------------------------------------------------------------
// small helpers
// ---------------------------------------------------------------------------

func TestVatAmountFromGross(t *testing.T) {
	d := decimal.RequireFromString
	if got := vatAmountFromGross(d("10.60"), d("6")); got.StringFixed(2) != "0.60" {
		t.Errorf("6%%: %s", got)
	}
	if got := vatAmountFromGross(d("12.10"), d("21")); got.StringFixed(2) != "2.10" {
		t.Errorf("21%%: %s", got)
	}
	if got := vatAmountFromGross(d("10"), decimal.Zero); !got.IsZero() {
		t.Errorf("0%%: %s", got)
	}
}

func TestServiceTypeFromOrderType(t *testing.T) {
	if serviceTypeFromOrderType(orderDomain.OrderTypeDelivery) != productDomain.ServiceTypeDelivery {
		t.Error("delivery")
	}
	if serviceTypeFromOrderType(orderDomain.OrderTypePickUp) != productDomain.ServiceTypeTakeaway {
		t.Error("pickup")
	}
}

func TestRoundingCorrectionLine_BadLineAmount(t *testing.T) {
	if _, err := roundingCorrectionLine(decimal.RequireFromString("1.00"), []mollie.PaymentLines{line("abc")}); err == nil {
		t.Fatal("want parse error")
	}
}
