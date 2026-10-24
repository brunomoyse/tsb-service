package hubrisetest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"tsb-service/pkg/hubrise"
)

// decodeStrict decodes a request body into v and refuses a body with a field the types do not
// model, as HubRise refuses unknown fields.
func decodeStrict(body []byte, v any) *apiError {
	if err := json.Unmarshal(body, v); err != nil {
		return errField("body", "invalid JSON: "+err.Error())
	}
	dropped, err := DroppedFields(body, v)
	if err != nil {
		return errField("body", err.Error())
	}
	if len(dropped) > 0 {
		fields := make([]hubrise.FieldError, 0, len(dropped))
		for _, d := range dropped {
			fields = append(fields, hubrise.FieldError{Field: strings.TrimPrefix(d, "."), Message: "unknown field"})
		}
		return errFields(fields)
	}
	return nil
}

// cloneOrder deep copies an order.
func cloneOrder(o hubrise.Order) hubrise.Order {
	raw, _ := json.Marshal(o)
	var out hubrise.Order
	_ = json.Unmarshal(raw, &out)
	return out
}

// viewOrder is the order as client token sees it: private_ref (of the order and of its elements)
// is only visible to the client that set it.
func (s *Server) viewOrder(so *storedOrder, token string) hubrise.Order {
	o := cloneOrder(so.order)
	if token == so.owner {
		return o
	}
	o.PrivateRef = ""
	for i := range o.Items {
		o.Items[i].PrivateRef = ""
	}
	for i := range o.Discounts {
		o.Discounts[i].PrivateRef = ""
	}
	for i := range o.Charges {
		o.Charges[i].PrivateRef = ""
	}
	for i := range o.Payments {
		o.Payments[i].PrivateRef = ""
	}
	return o
}

// ---- validation ----

func money(field string, m hubrise.Money, required bool, errs *[]hubrise.FieldError) {
	if m == "" {
		if required {
			*errs = append(*errs, hubrise.FieldError{Field: field, Message: "is required"})
		}
		return
	}
	if _, err := m.Decimal(); err != nil {
		*errs = append(*errs, hubrise.FieldError{Field: field, Message: fmt.Sprintf("%q is not a valid monetary amount", string(m))})
	}
}

func validateOrder(o *hubrise.Order) *apiError {
	var errs []hubrise.FieldError
	add := func(field, msg string) { errs = append(errs, hubrise.FieldError{Field: field, Message: msg}) }

	if o.Status == "" {
		add("status", "is required")
	} else if !o.Status.Valid() {
		add("status", fmt.Sprintf("unknown status %q", o.Status))
	}
	switch o.ServiceType {
	case "", hubrise.ServiceDelivery, hubrise.ServiceCollection, hubrise.ServiceEatIn:
	default:
		add("service_type", fmt.Sprintf("unknown service type %q", o.ServiceType))
	}
	if o.CustomerID != "" && (o.Customer != nil || o.CustomerListID != "" || o.CustomerPrivateRef != "") {
		add("customer_id", "is mutually exclusive with customer, customer_list_id and customer_private_ref")
	}
	if o.Customer != nil && (o.CustomerListID != "" || o.CustomerPrivateRef != "") {
		add("customer", "is mutually exclusive with customer_list_id and customer_private_ref")
	}
	for i, it := range o.Items {
		p := fmt.Sprintf("items[%d].", i)
		if it.Deleted {
			continue
		}
		if it.ProductName == "" {
			add(p+"product_name", "is required")
		}
		money(p+"price", it.Price, true, &errs)
		if q, err := hubrise.ParseQuantity(it.Quantity); err != nil || !q.IsPositive() {
			add(p+"quantity", fmt.Sprintf("%q is not a valid quantity", it.Quantity))
		}
		if it.TaxRate != "" {
			if _, err := hubrise.ParseTaxRate(it.TaxRate); err != nil {
				add(p+"tax_rate", fmt.Sprintf("%q is not a percent between 0 and 100", it.TaxRate))
			}
		}
		for j, op := range it.Options {
			q := fmt.Sprintf("%soptions[%d].", p, j)
			if op.OptionListName == "" {
				add(q+"option_list_name", "is required")
			}
			if op.Name == "" {
				add(q+"name", "is required")
			}
			if op.Quantity < 1 {
				add(q+"quantity", "must be at least 1")
			}
			money(q+"price", op.Price, false, &errs)
		}
	}
	for i, c := range o.Charges {
		if c.Deleted {
			continue
		}
		if c.Name == "" {
			add(fmt.Sprintf("charges[%d].name", i), "is required")
		}
		money(fmt.Sprintf("charges[%d].price", i), c.Price, true, &errs)
	}
	for i, d := range o.Discounts {
		if d.Deleted {
			continue
		}
		if d.Name == "" {
			add(fmt.Sprintf("discounts[%d].name", i), "is required")
		}
		money(fmt.Sprintf("discounts[%d].price_off", i), d.PriceOff, true, &errs)
	}
	for i, p := range o.Payments {
		if p.Deleted {
			continue
		}
		money(fmt.Sprintf("payments[%d].amount", i), p.Amount, true, &errs)
	}
	return errFields(errs)
}

// ---- totals ----

// finalize gives the new elements their ids and calculates the subtotals, the total and the
// payment discrepancy, as HubRise does: total = items + charges - discounts.
func (s *Server) finalize(o *hubrise.Order) {
	zero := decimal.Zero
	total := zero
	for i := range o.Items {
		it := &o.Items[i]
		if it.ID == "" {
			it.ID = s.nextID("i")
		}
		if it.Deleted {
			continue
		}
		unit, _ := it.Price.Decimal()
		for _, op := range it.Options {
			if op.Removed {
				continue
			}
			p, _ := op.Price.Decimal()
			unit = unit.Add(p.Mul(decimal.NewFromInt(int64(op.Quantity))))
		}
		q, _ := hubrise.ParseQuantity(it.Quantity)
		sub := unit.Mul(q)
		it.Subtotal = hubrise.MoneyFromDecimal(sub)
		total = total.Add(sub)
	}
	for i := range o.Charges {
		c := &o.Charges[i]
		if c.ID == "" {
			c.ID = s.nextID("c")
		}
		if !c.Deleted {
			p, _ := c.Price.Decimal()
			total = total.Add(p)
		}
	}
	for i := range o.Discounts {
		d := &o.Discounts[i]
		if d.ID == "" {
			d.ID = s.nextID("d")
		}
		if !d.Deleted {
			p, _ := d.PriceOff.Decimal()
			total = total.Sub(p)
		}
	}
	paid := zero
	for i := range o.Payments {
		p := &o.Payments[i]
		if p.ID == "" {
			p.ID = s.nextID("p")
		}
		if !p.Deleted {
			a, _ := p.Amount.Decimal()
			paid = paid.Add(a)
		}
	}
	o.Total = hubrise.MoneyFromDecimal(total)
	o.PaymentDiscrepancy = hubrise.MoneyFromDecimal(paid.Sub(total))
}

// ---- statuses ----

// transitionAllowed says whether an order may go from one status to another. HubRise documents no
// graph, so by default any documented status may follow any other; strict forbids leaving a final
// status and going back to new.
func transitionAllowed(strict bool, from, to hubrise.OrderStatus) bool {
	if !to.Valid() {
		return false
	}
	if !strict || from == to {
		return true
	}
	return !from.Final() && to != hubrise.StatusNew
}

// ---- handlers ----

func createOrder(s *Server, c *client, _ *http.Request, body []byte, _ http.Header) (int, any, *apiError) {
	var o hubrise.Order
	if err := decodeStrict(body, &o); err != nil {
		return 0, nil, err
	}
	out, err := s.createAs(c, o)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, out, nil
}

// createAs validates and stores a new order created by client c, and returns it as c sees it.
func (s *Server) createAs(c *client, o hubrise.Order) (hubrise.Order, *apiError) {
	if err := validateOrder(&o); err != nil {
		return hubrise.Order{}, err
	}
	if o.CustomerID != "" {
		if sc := s.customers[o.CustomerID]; sc == nil || sc.customer.Anonymised {
			return hubrise.Order{}, errField("customer_id", "customer not found")
		}
	}
	o.ID = s.nextID("o")
	o.LocationID = LocationID
	o.CreatedAt = s.now()
	o.CreatedBy = c.name
	if o.Channel == "" {
		o.Channel = c.name
	}
	if o.Asap == nil && o.ExpectedTime == nil {
		o.Asap = hubrise.Bool(true)
	}
	s.finalize(&o)
	return s.storeOrder(c.token, o), nil
}

// storeOrder saves a new order, tells the other clients and returns the order as its creator sees it.
func (s *Server) storeOrder(owner string, o hubrise.Order) hubrise.Order {
	so := &storedOrder{order: o, owner: owner}
	s.orders[o.ID] = so
	s.orderID = append(s.orderID, o.ID)
	s.emit(owner, "order", "create", func(e *hubrise.Event, recipient string) {
		e.OrderID = o.ID
		e.NewState = mustJSON(s.viewOrder(so, recipient))
	})
	return s.viewOrder(so, owner)
}

func getOrder(s *Server, c *client, r *http.Request, _ []byte, _ http.Header) (int, any, *apiError) {
	so := s.orders[r.PathValue("id")]
	if so == nil {
		return 0, nil, errStatus(http.StatusNotFound, "order not found")
	}
	return http.StatusOK, s.viewOrder(so, c.token), nil
}

func patchOrder(s *Server, c *client, r *http.Request, body []byte, _ http.Header) (int, any, *apiError) {
	var p hubrise.OrderPatch
	if err := decodeStrict(body, &p); err != nil {
		return 0, nil, err
	}
	out, err := s.applyPatch(c.token, r.PathValue("id"), &p)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, out, nil
}

// applyPatch applies a PATCH as source and returns the order as source sees it.
func (s *Server) applyPatch(source, id string, p *hubrise.OrderPatch) (hubrise.Order, *apiError) {
	so := s.orders[id]
	if so == nil {
		return hubrise.Order{}, errStatus(http.StatusNotFound, "order not found")
	}
	prev := cloneOrder(so.order)
	next, err := s.patched(prev, p)
	if err != nil {
		return hubrise.Order{}, err
	}
	so.order = next
	s.emit(source, "order", "update", func(e *hubrise.Event, recipient string) {
		e.OrderID = id
		e.PreviousState = mustJSON(s.viewOrder(&storedOrder{order: prev, owner: so.owner}, recipient))
		e.NewState = mustJSON(s.viewOrder(so, recipient))
	})
	return s.viewOrder(so, source), nil
}

// patched returns prev with the patch applied.
func (s *Server) patched(prev hubrise.Order, p *hubrise.OrderPatch) (hubrise.Order, *apiError) {
	if p.Status != "" {
		if !transitionAllowed(s.strict, prev.Status, p.Status) {
			return hubrise.Order{}, errField("status", fmt.Sprintf("status cannot change from %s to %s", prev.Status, p.Status))
		}
	}
	// The collections are patched on the JSON form, where "delete", "add" and "set private_ref" are
	// the only operations HubRise knows.
	var m map[string]any
	raw, _ := json.Marshal(prev)
	_ = json.Unmarshal(raw, &m)
	for _, col := range []struct {
		key string
		ops any
	}{{"items", p.Items}, {"discounts", p.Discounts}, {"charges", p.Charges}, {"payments", p.Payments}} {
		opsRaw, _ := json.Marshal(col.ops)
		var list []map[string]any
		_ = json.Unmarshal(opsRaw, &list)
		if err := applyCollectionOps(m, col.key, list); err != nil {
			return hubrise.Order{}, err
		}
	}
	raw, _ = json.Marshal(m)
	var next hubrise.Order
	_ = json.Unmarshal(raw, &next)

	if p.Status != "" {
		next.Status = p.Status
	}
	if p.ConfirmedTime != nil {
		next.ConfirmedTime = p.ConfirmedTime
	}
	if p.DriverPickupURL != "" {
		next.DriverPickupURL = p.DriverPickupURL
	}
	if p.SellerNotes != "" {
		next.SellerNotes = p.SellerNotes
	}
	if p.CollectionCode != "" {
		next.CollectionCode = p.CollectionCode
	}
	if p.PrivateRef != "" {
		next.PrivateRef = p.PrivateRef
	}
	if len(p.CustomFields) > 0 {
		next.CustomFields = p.CustomFields
	}
	if err := validateOrder(&next); err != nil {
		return hubrise.Order{}, err
	}
	s.finalize(&next)
	return next, nil
}

// applyCollectionOps applies the operations of one collection to the order's JSON form.
func applyCollectionOps(order map[string]any, key string, ops []map[string]any) *apiError {
	if len(ops) == 0 {
		return nil
	}
	list, _ := order[key].([]any)
	for _, op := range ops {
		id, _ := op["id"].(string)
		if id == "" {
			list = append(list, op) // an addition; it gets its id in finalize
			continue
		}
		idx := slices.IndexFunc(list, func(e any) bool {
			m, _ := e.(map[string]any)
			return m["id"] == id
		})
		if idx < 0 {
			return errField(key, fmt.Sprintf("%s %s not found", key, id))
		}
		el, _ := list[idx].(map[string]any)
		switch {
		case op["deleted"] == true:
			el["deleted"] = true
		case op["private_ref"] != nil && len(op) == 2:
			el["private_ref"] = op["private_ref"]
		default:
			return errField(key, "an element cannot be modified, delete it and add a new one")
		}
	}
	order[key] = list
	return nil
}

func listOrders(s *Server, c *client, r *http.Request, _ []byte, h http.Header) (int, any, *apiError) {
	q := r.URL.Query()
	var after, before time.Time
	for name, dst := range map[string]*time.Time{"after": &after, "before": &before} {
		if v := q.Get(name); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return 0, nil, errField(name, fmt.Sprintf("%q is not an ISO 8601 date time", v))
			}
			*dst = t
		}
	}
	var matches []hubrise.Order
	for i := len(s.orderID) - 1; i >= 0; i-- { // newest first
		so := s.orders[s.orderID[i]]
		o := so.order
		switch {
		case q.Get("private_ref") != "" && (so.owner != c.token || o.PrivateRef != q.Get("private_ref")):
			continue
		case q.Get("status") != "" && string(o.Status) != q.Get("status"):
			continue
		case q.Get("created_by") != "" && o.CreatedBy != q.Get("created_by"):
			continue
		case q.Get("customer_id") != "" && o.CustomerID != q.Get("customer_id"):
			continue
		case !after.IsZero() && o.CreatedAt.Before(after):
			continue
		case !before.IsZero() && !o.CreatedAt.Before(before):
			continue
		}
		matches = append(matches, s.viewOrder(so, c.token))
	}
	return page(h, q.Get("count"), q.Get("cursor"), matches)
}

// page cuts one page out of items: the cursor is the offset of the next page.
func page[T any](h http.Header, count, cursor string, items []T) (int, any, *apiError) {
	size := 100
	if count != "" {
		n, err := strconv.Atoi(count)
		if err != nil || n < 1 || n > 100 {
			return 0, nil, errField("count", "must be between 1 and 100")
		}
		size = n
	}
	start := 0
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n < 0 {
			return 0, nil, errField("cursor", "is invalid")
		}
		start = n
	}
	if start > len(items) {
		start = len(items)
	}
	end := min(start+size, len(items))
	if end < len(items) {
		h.Set("X-Cursor-Next", strconv.Itoa(end))
	}
	out := items[start:end]
	if out == nil {
		out = []T{}
	}
	return http.StatusOK, out, nil
}

// mustJSON marshals a value of the mock's own types, which cannot fail.
func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
