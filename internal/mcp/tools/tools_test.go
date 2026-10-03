package tools_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"tsb-service/internal/mcp/actions"
	"tsb-service/internal/mcp/changes"
	"tsb-service/internal/mcp/fakeupstream"
	"tsb-service/internal/mcp/tools"
	"tsb-service/internal/mcp/upstream"
)

var brussels, _ = time.LoadLocation("Europe/Brussels")

type harness struct {
	t     *testing.T
	fake  *fakeupstream.Server
	svc   *actions.Service
	cs    *mcp.ClientSession
	ctx   context.Context
	mu    sync.Mutex
	now   time.Time
	calls []string
}

func (h *harness) Now() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.now
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, ctx: context.Background(), now: time.Date(2026, 10, 3, 13, 0, 0, 0, brussels)}
	h.fake = fakeupstream.New()
	t.Cleanup(h.fake.Close)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sa := upstream.ServiceAccount{Issuer: h.fake.URL, ClientID: "mcp-client", ClientSecret: "mcp-secret", ProjectID: "1"}
	up := upstream.New(h.fake.URL, sa.TokenSource(context.Background()), log)
	store, err := changes.Open(filepath.Join(t.TempDir(), "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	env := &actions.Env{Up: up, Loc: brussels, PriceMaxPct: 50, Now: h.Now}
	h.svc = actions.NewService(env, store, 10*time.Minute, log)

	server := mcp.NewServer(&mcp.Implementation{Name: "tsb-mcp", Version: "test"}, &mcp.ServerOptions{Instructions: tools.Instructions})
	tools.Register(server, &tools.Deps{Svc: h.svc, Up: up, Loc: brussels, Log: log, Now: h.Now, ImageClient: http.DefaultClient, AllowHTTPImages: true})

	ct, st := mcp.NewInMemoryTransports()
	ss, err := server.Connect(h.ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil).Connect(h.ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	h.cs = cs
	return h
}

// call runs a tool and returns its structured output; it fails the test on a
// tool error unless wantErr is set.
func (h *harness) call(name string, args map[string]any) map[string]any {
	h.t.Helper()
	out, isErr, text := h.raw(name, args)
	if isErr {
		h.t.Fatalf("%s returned an error: %s", name, text)
	}
	return out
}

func (h *harness) callErr(name string, args map[string]any) string {
	h.t.Helper()
	_, isErr, text := h.raw(name, args)
	if !isErr {
		h.t.Fatalf("%s: expected a tool error, got %s", name, text)
	}
	return text
}

func (h *harness) raw(name string, args map[string]any) (map[string]any, bool, string) {
	h.t.Helper()
	h.calls = append(h.calls, name)
	res, err := h.cs.CallTool(h.ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		h.t.Fatalf("%s: protocol error: %v", name, err)
	}
	text := ""
	if len(res.Content) > 0 {
		if tc, ok := res.Content[0].(*mcp.TextContent); ok {
			text = tc.Text
		}
	}
	var out map[string]any
	if res.StructuredContent != nil {
		b, _ := json.Marshal(res.StructuredContent)
		_ = json.Unmarshal(b, &out)
	}
	return out, res.IsError, text
}

func (h *harness) apply(changeID string) {
	h.t.Helper()
	if _, err := h.svc.ApplyPending(h.ctx, changeID, "yes"); err != nil {
		h.t.Fatalf("apply %s: %v", changeID, err)
	}
}

func str(v any) string { s, _ := v.(string); return s }

func num(v any) int64 { f, _ := v.(float64); return int64(f) }

func list(v any) []any { l, _ := v.([]any); return l }

var expectedTools = []string{
	"deactivate_coupon", "get_coupon", "get_customer_stats", "get_daily_summary", "get_order", "get_product", "get_restaurant_config", "get_status",
	"list_categories", "list_orders", "list_schedule_overrides",
	"propose_bulk_availability", "propose_choice_change", "propose_choice_deletion", "propose_choice_group_change", "propose_choice_group_deletion",
	"propose_coupon_activation", "propose_coupon_creation", "propose_coupon_update", "propose_opening_hours", "propose_ordering_hours",
	"propose_price_change", "propose_product_creation", "propose_product_image", "propose_product_update", "propose_restaurant_closure",
	"propose_restaurant_reopening", "propose_schedule_override", "propose_schedule_override_removal", "propose_vat_category_change",
	"search_coupons", "search_products", "set_preparation_minutes", "set_product_availability", "set_product_visibility", "undo_last_change",
}

func TestToolList(t *testing.T) {
	h := newHarness(t)
	res, err := h.cs.ListTools(h.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		if !strings.Contains(tool.Description, "Example: "+tool.Name+"(") {
			t.Errorf("%s: description must contain one example call", tool.Name)
		}
		for _, banned := range []string{"apply", "confirm", "order_status", "cancel_order", "refund", "update_order", "create_order"} {
			if strings.Contains(tool.Name, banned) {
				t.Errorf("tool %s must not exist: no tool may confirm a change or write an order", tool.Name)
			}
		}
	}
	slices.Sort(names)
	if !slices.Equal(names, expectedTools) {
		t.Errorf("tools = %v\nwant %v", names, expectedTools)
	}
}

func TestReadTools(t *testing.T) {
	h := newHarness(t)

	r := h.call("search_products", map[string]any{"query": "三文鱼"})
	results := list(r["results"])
	if len(results) != 2 || str(results[0].(map[string]any)["product_id"]) != "p-maki-saumon" {
		t.Errorf("search_products: %v", results)
	}
	first := results[0].(map[string]any)
	if num(first["price_cents"]) != 450 || str(first["currency"]) != "EUR" {
		t.Errorf("price output: %v", first)
	}
	r = h.call("search_products", map[string]any{"query": "creme brulee", "limit": 1})
	if len(list(r["results"])) != 1 {
		t.Errorf("accent-insensitive search failed: %v", r)
	}

	r = h.call("get_product", map[string]any{"product_id": "p-maki-box"})
	groups := list(r["choice_groups"])
	if len(groups) != 1 || len(list(groups[0].(map[string]any)["choices"])) != 2 {
		t.Errorf("get_product choice groups: %v", r)
	}
	if !strings.Contains(h.callErr("get_product", map[string]any{"product_id": "nope"}), "search_products") {
		t.Error("unknown product error should point to search_products")
	}

	r = h.call("list_categories", map[string]any{"query": "刺身"})
	if c := list(r["categories"]); len(c) != 1 || str(c[0].(map[string]any)["category_id"]) != "cat-sashimi" {
		t.Errorf("list_categories: %v", r)
	}

	r = h.call("get_restaurant_config", map[string]any{})
	if !r["ordering_enabled"].(bool) || r["opening_hours"].(map[string]any)["monday"].(map[string]any)["closed"] != true {
		t.Errorf("get_restaurant_config: %v", r)
	}

	r = h.call("list_schedule_overrides", map[string]any{})
	if len(list(r["overrides"])) != 0 {
		t.Errorf("overrides: %v", r)
	}

	r = h.call("search_coupons", map[string]any{"query": "welc"})
	if c := list(r["coupons"]); len(c) != 1 || num(c[0].(map[string]any)["percent_off"]) != 10 {
		t.Errorf("search_coupons: %v", r)
	}
	r = h.call("get_coupon", map[string]any{"coupon_id": "cp-old"})
	if num(r["amount_off_cents"]) != 500 || num(r["min_order_cents"]) != 3000 {
		t.Errorf("get_coupon: %v", r)
	}

	r = h.call("list_orders", map[string]any{"from": "2026-10-03", "to": "2026-10-03"})
	if str(r["source"]) != "history" || len(list(r["orders"])) != 3 {
		t.Errorf("list_orders by date: %v", r)
	}
	r = h.call("list_orders", map[string]any{"status": "CANCELLED"})
	if str(r["source"]) != "recent" || len(list(r["orders"])) != 1 {
		t.Errorf("list_orders cancelled: %v", r)
	}
	r = h.call("list_orders", map[string]any{"limit": 2})
	if len(list(r["orders"])) != 2 || str(list(r["orders"])[0].(map[string]any)["order_id"]) != "o-4" {
		t.Errorf("list_orders recent: %v", r)
	}
	h.callErr("list_orders", map[string]any{"status": "LOST"})

	r = h.call("get_order", map[string]any{"order_id": "o-1"})
	if num(r["total_cents"]) != 3250 || num(r["delivery_fee_cents"]) != 200 || str(r["created_at"]) != "2026-10-03T12:05:00+02:00" {
		t.Errorf("get_order: %v", r)
	}

	r = h.call("get_daily_summary", map[string]any{"date": "2026-10-03"})
	// o-1 32.50 + o-2 20.00; o-3 pending and o-4 cancelled are excluded.
	if num(r["orders"]) != 2 || num(r["revenue_cents"]) != 5250 || num(r["pending"]) != 1 || num(r["cancelled"]) != 1 {
		t.Errorf("get_daily_summary: %v", r)
	}

	r = h.call("get_customer_stats", map[string]any{"query": "wang"})
	cust := list(r["top_customers"])
	if len(cust) != 1 || str(cust[0].(map[string]any)["last_name"]) != "Wang" {
		t.Errorf("get_customer_stats: %v", r)
	}
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "phone") || strings.Contains(string(b), "email") {
		t.Errorf("customer stats must not expose contact details: %s", b)
	}

	r = h.call("get_status", map[string]any{})
	if r["open"] != true || len(list(r["unavailable_products"])) != 1 || num(r["today"].(map[string]any)["revenue_cents"]) != 5250 {
		t.Errorf("get_status: %v", r)
	}
}

func TestLowRiskTools(t *testing.T) {
	h := newHarness(t)
	args := map[string]any{"product_id": "p-maki-saumon", "available": false, "request_context": "今天三文鱼卖完了"}
	r := h.call("set_product_availability", args)
	if r["applied"] != true || h.fake.ProductByID("p-maki-saumon").IsAvailable {
		t.Fatalf("set_product_availability: %v", r)
	}
	if str(r["summary_zh"]) != "「三文鱼卷」：可售 → 售罄" {
		t.Errorf("summary_zh: %v", r["summary_zh"])
	}
	r = h.call("set_product_availability", args)
	if r["no_op"] != true || !strings.Contains(str(r["summary"]), "already") {
		t.Errorf("repeat call must be a no-op: %v", r)
	}

	r = h.call("set_product_visibility", map[string]any{"product_id": "p-creme", "visible": false})
	if r["applied"] != true || h.fake.ProductByID("p-creme").IsVisible {
		t.Errorf("set_product_visibility: %v", r)
	}

	r = h.call("set_preparation_minutes", map[string]any{"minutes": 45})
	if r["applied"] != true || h.fake.Config.PreparationMinutes != 45 {
		t.Errorf("set_preparation_minutes: %v", r)
	}
	h.callErr("set_preparation_minutes", map[string]any{"minutes": 500})

	r = h.call("deactivate_coupon", map[string]any{"coupon_id": "cp-welcome"})
	if r["applied"] != true || h.fake.Coupons[0].IsActive {
		t.Errorf("deactivate_coupon: %v", r)
	}

	r = h.call("get_status", map[string]any{})
	changes := list(r["recent_changes"])
	if len(changes) != 4 || str(changes[3].(map[string]any)["request_context"]) != "今天三文鱼卖完了" {
		t.Errorf("audit in get_status: %v", changes)
	}
	if !strings.Contains(str(changes[3].(map[string]any)["summary_zh"]), "售罄") {
		t.Errorf("summary_zh in get_status: %v", changes[3])
	}

	r = h.call("undo_last_change", map[string]any{})
	if str(r["mode"]) != "applied" || !h.fake.Coupons[0].IsActive {
		t.Errorf("undo of a low-risk change applies directly: %v", r)
	}
	if !strings.HasPrefix(str(r["summary_zh"]), "已撤销：优惠码 WELCOME10") || !strings.Contains(str(r["undone_summary_zh"]), "启用 → 停用") {
		t.Errorf("undo summary_zh: %v", r)
	}
}

// proposeAndApply calls a propose tool, checks nothing changed upstream yet,
// then applies the change like the agent service would.
func (h *harness) proposeAndApply(name string, args map[string]any, check func()) map[string]any {
	h.t.Helper()
	before := len(writeOps(h.fake.Ops()))
	r := h.call(name, args)
	if str(r["change_id"]) == "" || str(r["summary"]) == "" || str(r["expires_at"]) == "" {
		h.t.Fatalf("%s: missing change_id/summary/expires_at: %v", name, r)
	}
	if after := len(writeOps(h.fake.Ops())); after != before {
		h.t.Fatalf("%s wrote upstream before confirmation", name)
	}
	h.apply(str(r["change_id"]))
	if check != nil {
		check()
	}
	return r
}

func writeOps(ops []string) []string {
	var out []string
	for _, op := range ops {
		if strings.Contains(upstream.Documents[op], "mutation ") {
			out = append(out, op)
		}
	}
	return out
}

func TestSensitiveTools(t *testing.T) {
	img := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("\x89PNG\r\n\x1a\n" + strings.Repeat("x", 100)))
	}))
	defer img.Close()

	h := newHarness(t)
	f := h.fake
	product := func(id string) upstream.Product { return f.ProductByID(id) }

	h.proposeAndApply("propose_price_change", map[string]any{"product_id": "p-maki-box", "new_price_cents": 1450}, func() {
		if product("p-maki-box").Price != "14.5" {
			t.Errorf("price not applied: %s", product("p-maki-box").Price)
		}
	})
	if msg := h.callErr("propose_price_change", map[string]any{"product_id": "p-maki-box", "new_price_cents": 100}); !strings.Contains(msg, "between") {
		t.Errorf("price bound error should state the range: %s", msg)
	}

	h.proposeAndApply("propose_vat_category_change", map[string]any{"product_id": "p-creme", "vat_category": "beverage"}, func() {
		if product("p-creme").VatCategory != "beverage" {
			t.Error("vat not applied")
		}
	})
	h.callErr("propose_vat_category_change", map[string]any{"product_id": "p-creme", "vat_category": "luxury"})

	h.proposeAndApply("propose_product_update", map[string]any{"product_id": "p-maki-saumon", "names": map[string]any{"zh": "三文鱼细卷"}, "is_spicy": true, "category_id": "cat-box"}, func() {
		p := product("p-maki-saumon")
		if p.NameIn("zh") != "三文鱼细卷" || !p.IsSpicy || p.Category.ID != "cat-box" {
			t.Errorf("product update not applied: %+v", p)
		}
	})

	h.proposeAndApply("propose_bulk_availability", map[string]any{"product_ids": []string{"p-maki-saumon", "p-sashimi-saumon"}, "available": false}, func() {
		if product("p-maki-saumon").IsAvailable || product("p-sashimi-saumon").IsAvailable {
			t.Error("bulk availability not applied")
		}
	})

	r := h.proposeAndApply("propose_product_creation", map[string]any{"category_id": "cat-maki", "names": map[string]any{"fr": "Maki avocat", "en": "Avocado maki", "zh": "牛油果卷"}, "price_cents": 450}, nil)
	if !strings.Contains(str(r["summary"]), "Maki avocat") || !strings.Contains(str(r["summary_zh"]), "「牛油果卷」") || len(f.Products) != 5 {
		t.Errorf("product creation: %v", r)
	}
	h.callErr("propose_product_creation", map[string]any{"category_id": "cat-maki", "names": map[string]any{"fr": "Maki X"}, "price_cents": 450})

	h.proposeAndApply("propose_product_image", map[string]any{"product_id": "p-maki-box", "image_url": img.URL + "/box.png", "remove_background": true}, func() {
		if len(f.Images["p-maki-box"]) == 0 {
			t.Error("image not uploaded")
		}
	})

	h.proposeAndApply("propose_choice_group_change", map[string]any{"group_id": "g-sauce", "max_selections": 2}, func() {
		if product("p-maki-box").ChoiceGroups[0].MaxSelections != 2 {
			t.Error("group change not applied")
		}
	})
	h.proposeAndApply("propose_choice_group_change", map[string]any{"product_id": "p-maki-box", "names": map[string]any{"fr": "Boisson", "zh": "饮料"}, "min_selections": 0}, func() {
		if len(product("p-maki-box").ChoiceGroups) != 2 {
			t.Error("group not created")
		}
	})
	h.proposeAndApply("propose_choice_change", map[string]any{"choice_id": "c-spicy", "price_modifier_cents": 70}, func() {
		if product("p-maki-box").ChoiceGroups[0].Choices[1].PriceModifier != "0.7" {
			t.Error("choice change not applied")
		}
	})
	h.proposeAndApply("propose_choice_change", map[string]any{"group_id": "g-sauce", "names": map[string]any{"fr": "Wasabi"}}, func() {
		if len(product("p-maki-box").ChoiceGroups[0].Choices) != 3 {
			t.Error("choice not created")
		}
	})
	h.proposeAndApply("propose_choice_deletion", map[string]any{"choice_id": "c-soja"}, func() {
		if len(product("p-maki-box").ChoiceGroups[0].Choices) != 2 {
			t.Error("choice not deleted")
		}
	})
	h.proposeAndApply("propose_choice_group_deletion", map[string]any{"group_id": "g-sauce"}, func() {
		if len(product("p-maki-box").ChoiceGroups) != 1 {
			t.Error("group not deleted")
		}
	})

	h.proposeAndApply("propose_opening_hours", map[string]any{"days": map[string]any{"monday": map[string]any{"open": "17:00", "close": "22:00"}, "sunday": map[string]any{"closed": true}}}, func() {
		if f.Opening["monday"] == nil || f.Opening["sunday"] != nil || f.Opening["tuesday"] == nil {
			t.Errorf("opening hours: %+v", f.Opening)
		}
	})
	h.callErr("propose_opening_hours", map[string]any{"days": map[string]any{"monday": map[string]any{"open": "22:00", "close": "17:00"}}})
	h.proposeAndApply("propose_ordering_hours", map[string]any{"days": map[string]any{"saturday": map[string]any{"open": "12:00", "close": "21:30"}}}, func() {
		if f.Ordering["saturday"].Close != "21:30" || f.Ordering["tuesday"] == nil {
			t.Errorf("ordering hours: %+v", f.Ordering)
		}
	})

	h.proposeAndApply("propose_schedule_override", map[string]any{"date": "2026-12-25", "end_date": "2026-12-26", "closed": true, "note": "Noël"}, func() {
		if !f.Overrides["2026-12-25"].Closed || !f.Overrides["2026-12-26"].Closed {
			t.Errorf("overrides: %+v", f.Overrides)
		}
	})
	h.proposeAndApply("propose_schedule_override_removal", map[string]any{"date": "2026-12-26"}, func() {
		if f.Overrides["2026-12-26"] != nil || f.Overrides["2026-12-25"] == nil {
			t.Errorf("override removal: %+v", f.Overrides)
		}
	})

	h.proposeAndApply("propose_coupon_creation", map[string]any{"code": "noel15", "discount_type": "percentage", "percent_off": 15, "min_order_cents": 3000}, func() {
		c := f.Coupons[len(f.Coupons)-1]
		if c.Code != "NOEL15" || c.DiscountValue != "15" || *c.MinOrderAmount != "30" {
			t.Errorf("coupon: %+v", c)
		}
	})
	h.callErr("propose_coupon_creation", map[string]any{"code": "WELCOME10", "discount_type": "fixed", "amount_off_cents": 500})
	h.proposeAndApply("propose_coupon_update", map[string]any{"coupon_id": "cp-welcome", "discount_type": "percentage", "percent_off": 12, "max_uses": 100}, func() {
		if f.Coupons[0].DiscountValue != "12" || *f.Coupons[0].MaxUses != 100 {
			t.Errorf("coupon update: %+v", f.Coupons[0])
		}
	})
	h.proposeAndApply("propose_coupon_activation", map[string]any{"coupon_id": "cp-old"}, func() {
		if !f.Coupons[1].IsActive {
			t.Error("coupon not activated")
		}
	})

	// No order write ever reached the backend.
	for _, op := range f.Ops() {
		for _, banned := range []string{"updateOrder(", "createOrder(", "updatePaymentStatus("} {
			if strings.Contains(upstream.Documents[op], banned) {
				t.Errorf("order write sent upstream: %s", op)
			}
		}
	}
}

func TestClosureTools(t *testing.T) {
	h := newHarness(t)
	f := h.fake

	r := h.call("propose_restaurant_closure", map[string]any{"from": "2026-10-03T20:00", "reopen_at": "2026-10-04T19:00", "reason": "family event", "request_context": "今晚八点关门，明天晚上七点开"})
	if !strings.Contains(str(r["summary"]), "2026-10-03 (saturday)") || !strings.Contains(str(r["summary_zh"]), "10月3日（周六）") {
		t.Errorf("summary: %v", r)
	}
	h.apply(str(r["change_id"]))
	if sat := f.Overrides["2026-10-03"].Schedule; sat == nil || sat.Open != "11:30" || sat.Close != "20:00" || sat.DinnerOpen != "" {
		t.Errorf("saturday 11:30-22:30 closed from 20:00 should become 11:30-20:00: %+v", sat)
	}
	if sun := f.Overrides["2026-10-04"].Schedule; sun == nil || sun.Open != "19:00" || sun.Close != "22:00" {
		t.Errorf("sunday 17:30-22:00 closed until 19:00 should become 19:00-22:00: %+v", sun)
	}

	h.callErr("propose_restaurant_closure", map[string]any{"reopen_at": "2026-10-20T12:00"})
	h.callErr("propose_restaurant_closure", map[string]any{"reopen_at": "2026-10-03T12:00"})

	r = h.call("propose_restaurant_reopening", map[string]any{})
	h.apply(str(r["change_id"]))
	if len(f.Overrides) != 0 {
		t.Errorf("reopening should remove closure overrides: %+v", f.Overrides)
	}

	r = h.call("propose_restaurant_closure", map[string]any{"reason": "rush"})
	if !strings.Contains(str(r["summary"]), "online ordering: on -> off") || !strings.Contains(str(r["summary_zh"]), "在线点餐：开启 → 关闭") {
		t.Errorf("pause summary: %v", r)
	}
	h.apply(str(r["change_id"]))
	if f.Config.OrderingEnabled {
		t.Error("ordering should be paused")
	}
	s := h.call("get_status", map[string]any{})
	if s["open"] != false || str(s["closed_reason"]) != "ordering_paused" {
		t.Errorf("status while paused: %v", s)
	}
	r = h.call("propose_restaurant_reopening", map[string]any{})
	h.apply(str(r["change_id"]))
	if !f.Config.OrderingEnabled {
		t.Error("ordering should be back on")
	}
	h.callErr("propose_restaurant_reopening", map[string]any{})
}

func TestUndoSensitiveCreatesPendingChange(t *testing.T) {
	h := newHarness(t)
	r := h.call("propose_price_change", map[string]any{"product_id": "p-maki-saumon", "new_price_cents": 500})
	h.apply(str(r["change_id"]))
	u := h.call("undo_last_change", map[string]any{"request_context": "撤销"})
	if str(u["mode"]) != "pending" || u["proposal"] == nil {
		t.Fatalf("undo: %v", u)
	}
	if h.fake.ProductByID("p-maki-saumon").Price != "5" {
		t.Error("undo of a sensitive change must wait for confirmation")
	}
	h.apply(str(u["proposal"].(map[string]any)["change_id"]))
	if h.fake.ProductByID("p-maki-saumon").Price != "4.5" {
		t.Error("undo not applied")
	}
}

func TestUpstreamErrorsAreSafe(t *testing.T) {
	h := newHarness(t)
	h.fake.FailOps["McpProducts"] = "INTERNAL_SERVER_ERROR"
	msg := h.callErr("search_products", map[string]any{"query": "maki"})
	if strings.Contains(msg, "injected") || strings.Contains(msg, fakeupstream.Token) || strings.Contains(msg, "graphql") {
		t.Errorf("error leaks details: %q", msg)
	}
	h.fake.FailOps["McpUpdateProduct"] = "UNAUTHENTICATED"
	if msg := h.callErr("set_product_availability", map[string]any{"product_id": "p-maki-saumon", "available": false}); msg == "" {
		t.Error("expected an error message")
	}
	delete(h.fake.FailOps, "McpProducts")
}
