package tools_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tsb-service/internal/mcp/upstream"
)

func (h *harness) addOrder(js string) {
	h.t.Helper()
	var o upstream.Order
	if err := json.Unmarshal([]byte(js), &o); err != nil {
		h.t.Fatal(err)
	}
	h.fake.Lock()
	h.fake.Orders = append(h.fake.Orders, &o)
	h.fake.Unlock()
}

func (h *harness) fail(op string) {
	h.fake.Lock()
	h.fake.FailOps[op] = "BOOM"
	h.fake.Unlock()
}

func (h *harness) unfail(op string) {
	h.fake.Lock()
	delete(h.fake.FailOps, op)
	h.fake.Unlock()
}

// lastVars returns the variables of the last call of op.
func (h *harness) lastVars(op string) map[string]any {
	h.t.Helper()
	h.fake.Lock()
	defer h.fake.Unlock()
	for i := len(h.fake.Calls) - 1; i >= 0; i-- {
		if h.fake.Calls[i].Op == op {
			return h.fake.Calls[i].Vars
		}
	}
	h.t.Fatalf("no %s call recorded", op)
	return nil
}

func (h *harness) countOps(op string) int {
	n := 0
	for _, o := range h.fake.Ops() {
		if o == op {
			n++
		}
	}
	return n
}

func mustContain(t *testing.T, what, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("%s = %q, missing %q", what, got, w)
		}
	}
}

// --- choices ---------------------------------------------------------------

func TestChoiceToolsNeedExactlyOneTarget(t *testing.T) {
	h := newHarness(t)
	mustContain(t, "group neither", h.callErr("propose_choice_group_change", map[string]any{"names": map[string]any{"fr": "X"}}), "Give either group_id")
	mustContain(t, "group both", h.callErr("propose_choice_group_change", map[string]any{"group_id": "g-sauce", "product_id": "p-maki-box"}), "Give either group_id")
	mustContain(t, "choice neither", h.callErr("propose_choice_change", map[string]any{"names": map[string]any{"fr": "X"}}), "Give either choice_id")
	mustContain(t, "choice both", h.callErr("propose_choice_change", map[string]any{"choice_id": "c-soja", "group_id": "g-sauce"}), "Give either choice_id")
	if n := len(writeOps(h.fake.Ops())); n != 0 {
		t.Errorf("nothing may be written: %v", h.fake.Ops())
	}
	if h.countOps("McpProducts") != 0 {
		t.Error("a malformed request must be refused before reading upstream")
	}
}

// --- coupons ---------------------------------------------------------------

func TestCouponReadTools(t *testing.T) {
	h := newHarness(t)
	from := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	until := time.Date(2026, 12, 31, 22, 0, 0, 0, time.UTC)
	h.fake.Lock()
	c := h.fake.Coupons[0]
	c.MaxUses, c.MaxUsesPerUser, c.ValidFrom, c.ValidUntil, c.UsedCount, c.MinOrderAmount = new(100), new(2), &from, &until, 7, new("15.5")
	h.fake.Unlock()

	r := h.call("get_coupon", map[string]any{"coupon_id": "cp-welcome"})
	if num(r["max_uses"]) != 100 || num(r["max_uses_per_user"]) != 2 || num(r["used_count"]) != 7 || num(r["min_order_cents"]) != 1550 || r["active"] != true ||
		str(r["valid_from"]) != "2026-10-01T10:00:00+02:00" || str(r["valid_until"]) != "2026-12-31T23:00:00+01:00" || str(r["discount_type"]) != "percentage" || num(r["percent_off"]) != 10 || str(r["currency"]) != "EUR" {
		t.Errorf("get_coupon: %v", r)
	}
	mustContain(t, "unknown coupon", h.callErr("get_coupon", map[string]any{"coupon_id": "cp-nope"}), `No coupon with id "cp-nope"`, "search_coupons")
	h.fail("McpCoupon")
	if msg := h.callErr("get_coupon", map[string]any{"coupon_id": "cp-welcome"}); strings.Contains(msg, "injected") {
		t.Errorf("error leaks details: %q", msg)
	}
	h.unfail("McpCoupon")

	// Status filter, with and without a query; the active coupon ranks first.
	r = h.call("search_coupons", map[string]any{"status": " inactive "})
	if cs := list(r["coupons"]); len(cs) != 1 || str(cs[0].(map[string]any)["code"]) != "SUMMER5" {
		t.Errorf("inactive coupons: %v", r)
	}
	r = h.call("search_coupons", map[string]any{})
	if cs := list(r["coupons"]); len(cs) != 2 {
		t.Errorf("all coupons: %v", r)
	}
	r = h.call("search_coupons", map[string]any{"query": "summer", "status": "ACTIVE"})
	if cs := list(r["coupons"]); len(cs) != 0 {
		t.Errorf("an inactive coupon must not match an ACTIVE filter: %v", r)
	}
	r = h.call("search_coupons", map[string]any{"query": "nothing-like-it"})
	if cs := list(r["coupons"]); cs == nil || len(cs) != 0 {
		t.Errorf("no match must be an empty list: %v", r)
	}
	h.fail("McpCoupons")
	h.callErr("search_coupons", map[string]any{})
}

func TestCouponWriteToolValidation(t *testing.T) {
	h := newHarness(t)
	tests := []struct {
		name string
		tool string
		args map[string]any
		want string
	}{
		{"create: no type", "propose_coupon_creation", map[string]any{"discount_type": "", "percent_off": 10}, "discount_type is required with percent_off or amount_off_cents"},
		{"create: no type at all", "propose_coupon_creation", map[string]any{"discount_type": ""}, "discount_type is required."},
		{"create: percentage without percent", "propose_coupon_creation", map[string]any{"discount_type": "percentage"}, "percent_off is required for a percentage discount"},
		{"create: fixed without amount", "propose_coupon_creation", map[string]any{"discount_type": "fixed", "percent_off": 10}, "amount_off_cents is required for a fixed discount"},
		{"create: unknown type", "propose_coupon_creation", map[string]any{"discount_type": "bogo", "percent_off": 10}, "discount_type must be percentage or fixed"},
		{"create: bad valid_from", "propose_coupon_creation", map[string]any{"discount_type": "percentage", "percent_off": 10, "valid_from": "next week"}, "valid_from: invalid time"},
		{"create: bad valid_until", "propose_coupon_creation", map[string]any{"discount_type": "percentage", "percent_off": 10, "valid_until": "never"}, "valid_until: invalid time"},
		{"update: type without value", "propose_coupon_update", map[string]any{"coupon_id": "cp-welcome", "discount_type": "fixed"}, "amount_off_cents is required"},
		{"update: value without type", "propose_coupon_update", map[string]any{"coupon_id": "cp-welcome", "amount_off_cents": 500}, "discount_type is required with"},
		{"update: bad valid_from", "propose_coupon_update", map[string]any{"coupon_id": "cp-welcome", "valid_from": "x"}, "valid_from: invalid time"},
		{"update: bad valid_until", "propose_coupon_update", map[string]any{"coupon_id": "cp-welcome", "valid_until": "x"}, "valid_until: invalid time"},
		{"update: unknown coupon", "propose_coupon_update", map[string]any{"coupon_id": "cp-nope", "max_uses": 5}, "No coupon"},
		{"activate: unknown coupon", "propose_coupon_activation", map[string]any{"coupon_id": "cp-nope"}, "No coupon"},
		{"deactivate: unknown coupon", "deactivate_coupon", map[string]any{"coupon_id": "cp-nope"}, "No coupon"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mustContain(t, "error", h.callErr(tt.tool, tt.args), tt.want)
		})
	}
	if n := len(writeOps(h.fake.Ops())); n != 0 {
		t.Errorf("nothing may be written: %v", h.fake.Ops())
	}
}

func TestCouponToolsBuildTheRightChange(t *testing.T) {
	h := newHarness(t)
	// Fixed amount, timestamps with and without offset, defaults: active.
	r := h.call("propose_coupon_creation", map[string]any{"code": "fix5", "discount_type": " FIXED ", "amount_off_cents": 500, "valid_from": "2026-10-10T08:00", "valid_until": "2026-12-31T23:59:00+01:00", "max_uses_per_user": 1})
	mustContain(t, "summary", str(r["summary"]), "New coupon FIX5: 5.00 EUR off", "max 1 per customer", "from 2026-10-10 08:00", "until 2026-12-31 23:59", "active")
	h.apply(str(r["change_id"]))
	c := h.fake.Coupons[len(h.fake.Coupons)-1]
	if c.Code != "FIX5" || c.DiscountType != "FIXED" || c.DiscountValue != "5" || !c.IsActive || c.ValidFrom == nil || !c.ValidFrom.Equal(time.Date(2026, 10, 10, 6, 0, 0, 0, time.UTC)) {
		t.Errorf("created coupon: %+v", c)
	}
	// active=false is honoured.
	r = h.call("propose_coupon_creation", map[string]any{"code": "OFF10", "discount_type": "percentage", "percent_off": 10, "active": false})
	mustContain(t, "inactive summary", str(r["summary"]), "inactive")

	// Update by code, other fields untouched.
	r = h.call("propose_coupon_update", map[string]any{"coupon_id": "cp-welcome", "code": "bienvenue", "valid_from": "2026-10-04 09:00", "valid_until": "2026-11-01T12:00:00Z", "max_uses_per_user": 3, "min_order_cents": 2500})
	mustContain(t, "update summary", str(r["summary"]), "code: WELCOME10 -> BIENVENUE", "valid from: none -> 2026-10-04 09:00", "valid until: none -> 2026-11-01 13:00", "max uses per customer: none -> 3", "minimum order: none -> 25.00 EUR")
	h.apply(str(r["change_id"]))
	got := h.fake.Coupons[0]
	if got.Code != "BIENVENUE" || *got.MaxUsesPerUser != 3 || *got.MinOrderAmount != "25" || got.DiscountValue != "10" {
		t.Errorf("updated coupon: %+v", got)
	}
}

// --- orders ----------------------------------------------------------------

func TestListOrdersFilters(t *testing.T) {
	h := newHarness(t)

	r := h.call("list_orders", map[string]any{"from": "2026-10-03", "to": "2026-10-03", "status": " delivered "})
	if str(r["source"]) != "history" || len(list(r["orders"])) != 1 || str(list(r["orders"])[0].(map[string]any)["order_id"]) != "o-1" {
		t.Errorf("history with status: %v", r)
	}
	in := h.lastVars("McpOrderHistory")["input"].(map[string]any)
	if in["status"] != "DELIVERED" || in["startDate"] == nil || in["endDate"] == nil || num(in["first"]) != 20 {
		t.Errorf("history input: %v", in)
	}
	if str(in["startDate"]) != "2026-10-03T00:00:00+02:00" || str(in["endDate"]) != "2026-10-03T23:59:59.999+02:00" {
		t.Errorf("a whole Brussels day: %v .. %v", in["startDate"], in["endDate"])
	}

	// More matches than the limit: the note says so.
	r = h.call("list_orders", map[string]any{"from": "2026-10-03", "limit": 1})
	if len(list(r["orders"])) != 1 || str(r["note"]) != "showing the newest 1 of 3 orders" {
		t.Errorf("limited history: %v", r)
	}
	// Only a start: still history, the end is open.
	r = h.call("list_orders", map[string]any{"from": "2026-10-03T11:15"})
	if str(r["source"]) != "history" {
		t.Errorf("only from: %v", r)
	}
	if h.lastVars("McpOrderHistory")["input"].(map[string]any)["endDate"] != nil {
		t.Error("no end date was given")
	}

	// Cancelled orders are only in the recent list; dates and status still filter it.
	r = h.call("list_orders", map[string]any{"status": "CANCELLED", "from": "2026-10-03", "to": "2026-10-03"})
	if str(r["source"]) != "recent" || len(list(r["orders"])) != 1 || str(r["note"]) != "searched the latest 200 orders only" {
		t.Errorf("recent cancelled: %v", r)
	}
	r = h.call("list_orders", map[string]any{"status": "CANCELLED", "from": "2026-10-04"})
	if len(list(r["orders"])) != 0 {
		t.Errorf("cancelled orders from a later day: %v", r)
	}
	r = h.call("list_orders", map[string]any{"status": "FAILED", "to": "2026-10-01"})
	if len(list(r["orders"])) != 0 {
		t.Errorf("failed orders before any order: %v", r)
	}
	// Recent list without dates: newest first, status filter.
	r = h.call("list_orders", map[string]any{"status": "delivered"})
	ids := []string{}
	for _, o := range list(r["orders"]) {
		ids = append(ids, str(o.(map[string]any)["order_id"]))
	}
	if strings.Join(ids, ",") != "o-1,o-5" {
		t.Errorf("recent delivered: %v", ids)
	}

	mustContain(t, "bad status", h.callErr("list_orders", map[string]any{"status": "LOST"}), `Unknown status "LOST"`, "PENDING")
	mustContain(t, "bad from", h.callErr("list_orders", map[string]any{"from": "yesterday"}), "from: invalid time")
	mustContain(t, "bad to", h.callErr("list_orders", map[string]any{"to": "tomorrow"}), "to: invalid time")
	h.fail("McpOrderHistory")
	h.callErr("list_orders", map[string]any{"from": "2026-10-03"})
	h.unfail("McpOrderHistory")
	h.fail("McpOrders")
	h.callErr("list_orders", map[string]any{})
}

func TestListOrdersLimitIsCapped(t *testing.T) {
	h := newHarness(t)
	h.call("list_orders", map[string]any{"from": "2026-10-01", "limit": 5000})
	if got := num(h.lastVars("McpOrderHistory")["input"].(map[string]any)["first"]); got != 100 {
		t.Errorf("limit sent upstream = %d, want 100", got)
	}
}

func TestGetOrderDetails(t *testing.T) {
	h := newHarness(t)
	h.addOrder(`{"id":"o-7","createdAt":"2026-10-03T09:00:00Z","status":"CANCELLED","type":"DELIVERY","isOnlinePayment":true,"totalPrice":"27.4","discountAmount":"1.5","deliveryFee":"2.5",
		"couponCode":"NOEL","estimatedReadyTime":"2026-10-03T10:30:00Z","preferredReadyTime":"2026-10-03T10:00:00Z","displayAddress":"Rue Y 2, 4000 Liège","addressExtra":"sonnette cassée, appelez 0470 12 34 56",
		"orderNote":"sans wasabi","cancellationReason":"client injoignable (marie@example.com)","payment":{"status":"failed"},
		"items":[
		  {"quantity":2,"unitPrice":"6","totalPrice":"12","product":{"id":"p1","name":"Maki","category":{"id":"c1","name":""},"translations":[{"language":"zh","name":"卷"}]},"choice":{"name":"Large"},
		   "selections":[{"quantity":1,"choice":{"name":"Soja"}},{"quantity":3,"choice":{"name":"Wasabi"}}]},
		  {"quantity":1,"unitPrice":"13.9","totalPrice":"13.9"}],
		"statusHistory":[{"status":"PENDING","changedAt":"2026-10-03T09:00:00Z"},{"status":"CANCELLED","changedAt":"2026-10-03T09:30:00Z"}]}`)
	r := h.call("get_order", map[string]any{"order_id": "o-7"})
	if num(r["total_cents"]) != 2740 || num(r["discount_cents"]) != 150 || num(r["delivery_fee_cents"]) != 250 || str(r["coupon_code"]) != "NOEL" || str(r["payment_status"]) != "failed" ||
		str(r["estimated_ready_time"]) != "2026-10-03T12:30:00+02:00" || str(r["ready_time"]) != "2026-10-03T12:00:00+02:00" || str(r["address"]) != "Rue Y 2, 4000 Liège" || str(r["customer"]) != "guest" || num(r["item_count"]) != 3 {
		t.Errorf("order: %v", r)
	}
	// Free text is masked.
	for _, f := range []string{"address_extra", "cancellation_reason"} {
		mustContain(t, f, str(r[f]), "[hidden")
		if strings.Contains(str(r[f]), "0470") || strings.Contains(str(r[f]), "marie@") {
			t.Errorf("%s leaks contact details: %q", f, r[f])
		}
	}
	if str(r["note"]) != "sans wasabi" {
		t.Errorf("note: %v", r["note"])
	}
	items := list(r["items"])
	if len(items) != 2 {
		t.Fatalf("items: %v", items)
	}
	first := items[0].(map[string]any)
	opts := list(first["options"])
	if len(opts) != 3 || str(opts[0]) != "Large" || str(opts[1]) != "Soja" || str(opts[2]) != "Wasabi x3" {
		t.Errorf("options: %v", opts)
	}
	if num(first["unit_cents"]) != 600 || num(first["total_cents"]) != 1200 || str(first["product"]) != "Maki" {
		t.Errorf("first item: %v", first)
	}
	labels := first["product_labels"].(map[string]any)
	// No category: the label is just the name, never a leading space.
	if str(labels["fr"]) != "Maki" || str(labels["zh"]) != "卷" {
		t.Errorf("labels: %v", labels)
	}
	second := items[1].(map[string]any)
	if second["product"] != "" || second["product_labels"] != nil || len(list(second["options"])) != 0 {
		t.Errorf("item without product: %v", second)
	}
	hist := list(r["status_history"])
	if len(hist) != 2 || str(hist[1].(map[string]any)["status"]) != "CANCELLED" || str(hist[1].(map[string]any)["at"]) != "2026-10-03T11:30:00+02:00" {
		t.Errorf("history: %v", hist)
	}

	mustContain(t, "unknown order", h.callErr("get_order", map[string]any{"order_id": "o-nope"}), `No order with id "o-nope"`, "list_orders")
	h.fail("McpOrder")
	h.callErr("get_order", map[string]any{"order_id": "o-1"})
}

func TestDailySummaryEdgeCases(t *testing.T) {
	h := newHarness(t)
	// Default date is today in Brussels.
	r := h.call("get_daily_summary", map[string]any{})
	if str(r["date"]) != "2026-10-03" || num(r["orders"]) != 2 {
		t.Errorf("today: %v", r)
	}
	// A day without orders: zero average, no division by zero.
	r = h.call("get_daily_summary", map[string]any{"date": "2026-01-01"})
	if num(r["orders"]) != 0 || num(r["average_cents"]) != 0 || num(r["revenue_cents"]) != 0 || num(r["cancelled"]) != 0 {
		t.Errorf("empty day: %v", r)
	}
	r = h.call("get_daily_summary", map[string]any{"date": "2026-10-03"})
	if num(r["average_cents"]) != 2625 || num(r["by_status"].(map[string]any)["DELIVERED"]) != 1 || num(r["by_type"].(map[string]any)["PICKUP"]) != 1 || num(r["by_type"].(map[string]any)["DELIVERY"]) != 1 {
		t.Errorf("breakdown: %v", r)
	}
	mustContain(t, "bad date", h.callErr("get_daily_summary", map[string]any{"date": "03/10/2026"}), "date: invalid date")

	// The recent-orders lookup only feeds the cancelled count: losing it must
	// not fail the summary.
	h.fail("McpOrders")
	r = h.call("get_daily_summary", map[string]any{"date": "2026-10-03"})
	if num(r["orders"]) != 2 || num(r["cancelled"]) != 0 {
		t.Errorf("without recent orders: %v", r)
	}
	h.unfail("McpOrders")
	h.fail("McpOrderHistory")
	h.callErr("get_daily_summary", map[string]any{"date": "2026-10-03"})
}

func TestDailySummaryReadsEveryPage(t *testing.T) {
	h := newHarness(t)
	h.fake.Lock()
	for i := range 150 {
		h.fake.Orders = append(h.fake.Orders, &upstream.Order{ID: "bulk-" + string(rune('a'+i%26)) + string(rune('a'+i/26)), CreatedAt: time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC),
			Status: "DELIVERED", Type: "PICKUP", TotalPrice: "10", DiscountAmount: "0"})
	}
	h.fake.Unlock()
	r := h.call("get_daily_summary", map[string]any{"date": "2026-10-03"})
	// 150 bulk + o-1 and o-2.
	if num(r["orders"]) != 152 || num(r["revenue_cents"]) != 150*1000+3250+2000 {
		t.Errorf("summary over several pages: %v", r)
	}
	if got := h.countOps("McpOrderHistory"); got != 2 {
		t.Errorf("history pages fetched = %d, want 2", got)
	}
	pages := []int64{}
	h.fake.Lock()
	for _, c := range h.fake.Calls {
		if c.Op == "McpOrderHistory" {
			pages = append(pages, num(c.Vars["input"].(map[string]any)["page"]))
		}
	}
	h.fake.Unlock()
	if len(pages) != 2 || pages[0] != 1 || pages[1] != 2 {
		t.Errorf("pages = %v", pages)
	}
}

// --- customers -------------------------------------------------------------

func TestCustomerStatsFilters(t *testing.T) {
	h := newHarness(t)
	r := h.call("get_customer_stats", map[string]any{"from": "2026-10-01", "to": "2026-10-03", "min_orders": 2, "order_type": " delivery "})
	in := h.lastVars("McpCustomerStats")["input"].(map[string]any)
	if in["orderType"] != "DELIVERY" || num(in["minOrders"]) != 2 || str(in["startDate"]) != "2026-10-01T00:00:00+02:00" || str(in["endDate"]) != "2026-10-03T23:59:59.999+02:00" {
		t.Errorf("stats input: %v", in)
	}
	if num(r["customers"]) != 2 || num(r["orders"]) != 3 || num(r["revenue_cents"]) != 10250 || num(r["average_order_cents"]) != 3417 || num(r["total_matching"]) != 2 {
		t.Errorf("stats: %v", r)
	}
	top := list(r["top_customers"])
	c := top[0].(map[string]any)
	if str(c["name"]) != "Marie D." || num(c["orders"]) != 2 || num(c["total_cents"]) != 8250 || num(c["average_cents"]) != 4125 || str(c["preferred_order_type"]) != "DELIVERY" || num(c["delivery_count"]) != 2 ||
		str(c["first_order"]) != "2026-10-02T21:00:00+02:00" || str(c["customer_id"]) != "u-1" {
		t.Errorf("customer: %v", c)
	}

	// No filters at all send an empty input.
	h.call("get_customer_stats", map[string]any{})
	in = h.lastVars("McpCustomerStats")["input"].(map[string]any)
	if len(in) != 0 {
		t.Errorf("unexpected filters: %v", in)
	}
	h.call("get_customer_stats", map[string]any{"order_type": "PICKUP"})
	if h.lastVars("McpCustomerStats")["input"].(map[string]any)["orderType"] != "PICKUP" {
		t.Error("PICKUP")
	}

	// The limit truncates the list but not total_matching.
	r = h.call("get_customer_stats", map[string]any{"limit": 1})
	if len(list(r["top_customers"])) != 1 || num(r["total_matching"]) != 2 {
		t.Errorf("limit: %v", r)
	}
	// First name plus initial narrows to one customer.
	r = h.call("get_customer_stats", map[string]any{"query": "Marie D"})
	if top := list(r["top_customers"]); len(top) != 1 || str(top[0].(map[string]any)["name"]) != "Marie D." {
		t.Errorf("query by name and initial: %v", r)
	}

	mustContain(t, "order type", h.callErr("get_customer_stats", map[string]any{"order_type": "DRONE"}), "order_type must be DELIVERY or PICKUP")
	mustContain(t, "from", h.callErr("get_customer_stats", map[string]any{"from": "soon"}), "from: invalid time")
	mustContain(t, "to", h.callErr("get_customer_stats", map[string]any{"to": "later"}), "to: invalid time")
	h.fail("McpCustomerStats")
	h.callErr("get_customer_stats", map[string]any{})
}

// --- status ----------------------------------------------------------------

func TestStatusClosedReasons(t *testing.T) {
	today := "2026-10-03"
	tests := []struct {
		name       string
		setup      func(h *harness)
		wantOpen   bool
		wantReason string
		wantNote   string
		wantHours  string
		reopens    string
	}{
		{"open", func(h *harness) {}, true, "", "", "11:30-22:30", ""},
		{"ordering paused", func(h *harness) { h.fake.Config.OrderingEnabled = false }, false, "ordering_paused", "", "11:30-22:30", ""},
		{"closed today by override", func(h *harness) {
			h.fake.Config.IsOrderingCurrentlyOpen = false
			h.fake.Overrides[today] = &upstream.ScheduleOverride{Date: time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC), Closed: true, Note: new("panne de gaz")}
		}, false, "closed_today", "panne de gaz", "closed", ""},
		{"special hours", func(h *harness) {
			h.fake.Config.IsOrderingCurrentlyOpen = false
			h.fake.Overrides[today] = &upstream.ScheduleOverride{Date: time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC), Schedule: &upstream.DaySchedule{Open: "17:00", Close: "21:00"}}
		}, false, "special_hours", "", "17:00-21:00", ""},
		{"outside opening hours", func(h *harness) {
			h.fake.Config.IsOrderingCurrentlyOpen = false
			h.fake.Config.NextOpeningAt = new(time.Date(2026, 10, 4, 15, 30, 0, 0, time.UTC))
		}, false, "outside_opening_hours", "", "11:30-22:30", "2026-10-04T17:30:00+02:00"},
		{"paused ignores the next opening", func(h *harness) {
			h.fake.Config.OrderingEnabled = false
			h.fake.Config.NextOpeningAt = new(time.Date(2026, 10, 4, 15, 30, 0, 0, time.UTC))
		}, false, "ordering_paused", "", "11:30-22:30", ""},
		{"an override on another day is ignored", func(h *harness) {
			h.fake.Overrides["2026-10-04"] = &upstream.ScheduleOverride{Date: time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC), Closed: true}
		}, true, "", "", "11:30-22:30", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.fake.Lock()
			tt.setup(h)
			h.fake.Unlock()
			r := h.call("get_status", map[string]any{})
			hours := r["today_hours"].(map[string]any)
			gotHours := "closed"
			if hours["closed"] != true {
				gotHours = str(hours["open"]) + "-" + str(hours["close"])
			}
			if (r["open"] == true) != tt.wantOpen || str(r["closed_reason"]) != tt.wantReason || str(r["closed_note"]) != tt.wantNote || gotHours != tt.wantHours || str(r["reopens_at"]) != tt.reopens {
				t.Errorf("status: %v (hours %s)", r, gotHours)
			}
			if str(r["now"]) != "2026-10-03T13:00:00+02:00" {
				t.Errorf("now = %v", r["now"])
			}
		})
	}
}

func TestStatusReportsWhatNeedsAttention(t *testing.T) {
	h := newHarness(t)
	r := h.call("get_status", map[string]any{})
	un := list(r["unavailable_products"])
	if len(un) != 1 || str(un[0].(map[string]any)["product_id"]) != "p-creme" || str(un[0].(map[string]any)["name_zh"]) != "焦糖布丁" || num(r["preparation_minutes"]) != 30 {
		t.Errorf("status: %v", r)
	}
	// A hidden product that is sold out is not worth mentioning.
	h.fake.Lock()
	h.fake.Products[3].IsVisible = false
	h.fake.Unlock()
	r = h.call("get_status", map[string]any{})
	if len(list(r["unavailable_products"])) != 0 {
		t.Errorf("hidden products: %v", r)
	}

	// Recent changes: newest first, capped at 10, with undone flag and context.
	for i := range 12 {
		h.call("set_preparation_minutes", map[string]any{"minutes": 31 + i, "request_context": "ctx"})
	}
	r = h.call("get_status", map[string]any{})
	ch := list(r["recent_changes"])
	if len(ch) != 10 {
		t.Fatalf("recent changes = %d", len(ch))
	}
	if first := ch[0].(map[string]any); str(first["kind"]) != "restaurant.preparation_minutes" || str(first["outcome"]) != "applied" || str(first["risk"]) != "low" || first["undone"] != false || str(first["source"]) != "set_preparation_minutes" {
		t.Errorf("first change: %v", first)
	}
	h.call("undo_last_change", map[string]any{})
	ch = list(h.call("get_status", map[string]any{})["recent_changes"])
	undone := 0
	for _, c := range ch {
		if c.(map[string]any)["undone"] == true {
			undone++
		}
	}
	if undone != 1 {
		t.Errorf("exactly one change was undone, got %d", undone)
	}
}

func TestStatusFailsCleanlyWhenAPartFails(t *testing.T) {
	for _, op := range []string{"McpRestaurantConfig", "McpScheduleOverrides", "McpProducts", "McpOrderHistory"} {
		t.Run(op, func(t *testing.T) {
			h := newHarness(t)
			h.fail(op)
			msg := h.callErr("get_status", map[string]any{})
			if strings.Contains(msg, "injected") || strings.Contains(msg, "graphql") {
				t.Errorf("leaks details: %q", msg)
			}
		})
	}
	t.Run("audit store", func(t *testing.T) {
		h := newHarness(t)
		_ = h.svc.Store().Close()
		if msg := h.callErr("get_status", map[string]any{}); !strings.Contains(msg, "Something went wrong") || strings.Contains(msg, "sql") {
			t.Errorf("message: %q", msg)
		}
	})
}

func TestUndoToolErrors(t *testing.T) {
	h := newHarness(t)
	mustContain(t, "nothing to undo", h.callErr("undo_last_change", map[string]any{}), "no change from the last 30 minutes")
	// Photos cannot be undone: the owner is told, nothing is changed.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("\x89PNG\r\n\x1a\n" + strings.Repeat("x", 50)))
	}))
	defer srv.Close()
	r := h.call("propose_product_image", map[string]any{"product_id": "p-maki-box", "image_url": srv.URL + "/a.png"})
	h.apply(str(r["change_id"]))
	mustContain(t, "photo undo", h.callErr("undo_last_change", map[string]any{}), "cannot be undone automatically")
}
