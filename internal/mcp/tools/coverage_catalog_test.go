package tools_test

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"tsb-service/internal/mcp/upstream"
)

// --- products and categories ---------------------------------------------------

func TestGetProductWithDescriptions(t *testing.T) {
	h := newHarness(t)
	h.fake.Lock()
	h.fake.Products[0].Translations[0].Description = new("Rouleau de riz au saumon")
	h.fake.Products[0].Translations[2].Description = new("三文鱼卷")
	h.fake.Unlock()
	r := h.call("get_product", map[string]any{"product_id": "p-maki-saumon"})
	d := r["descriptions"].(map[string]any)
	if len(d) != 2 || str(d["fr"]) != "Rouleau de riz au saumon" || str(d["zh"]) != "三文鱼卷" || d["en"] != nil {
		t.Errorf("descriptions: %v", d)
	}
	if str(r["code"]) != "M1" || num(r["piece_count"]) != 6 || str(r["vat_category"]) != "food" || r["available"] != true || r["is_discountable"] != true || len(list(r["choice_groups"])) != 0 {
		t.Errorf("product: %v", r)
	}
	// Choice groups carry names per language and surcharges in cents.
	r = h.call("get_product", map[string]any{"product_id": "p-maki-box"})
	g := list(r["choice_groups"])[0].(map[string]any)
	choices := list(g["choices"])
	if str(g["group_id"]) != "g-sauce" || g["names"].(map[string]any)["zh"] != "酱汁" || num(g["min_selections"]) != 1 || num(choices[1].(map[string]any)["price_modifier_cents"]) != 50 || str(choices[1].(map[string]any)["choice_id"]) != "c-spicy" {
		t.Errorf("group: %v", g)
	}

	h.fail("McpProduct")
	h.callErr("get_product", map[string]any{"product_id": "p-maki-saumon"})
}

func TestListCategories(t *testing.T) {
	h := newHarness(t)
	h.fake.Lock()
	slices.Reverse(h.fake.Categories)
	h.fake.Unlock()
	r := h.call("list_categories", map[string]any{})
	var ids []string
	for _, c := range list(r["categories"]) {
		ids = append(ids, str(c.(map[string]any)["category_id"]))
	}
	if strings.Join(ids, ",") != "cat-maki,cat-sashimi,cat-box" {
		t.Errorf("categories must be sorted by position: %v", ids)
	}
	c := list(r["categories"])[1].(map[string]any)
	if num(c["position"]) != 2 || c["names"].(map[string]any)["zh"] != "刺身" || str(c["name"]) != "Sashimis" {
		t.Errorf("category: %v", c)
	}
	r = h.call("list_categories", map[string]any{"query": "套餐"})
	if cs := list(r["categories"]); len(cs) != 1 || str(cs[0].(map[string]any)["category_id"]) != "cat-box" {
		t.Errorf("query in Chinese: %v", r)
	}
	r = h.call("list_categories", map[string]any{"query": "zzz"})
	if cs := list(r["categories"]); cs == nil || len(cs) != 0 {
		t.Errorf("no match: %v", r)
	}
	h.fail("McpCategories")
	h.callErr("list_categories", map[string]any{})
}

func TestSearchProductsFailureAndLimits(t *testing.T) {
	h := newHarness(t)
	r := h.call("search_products", map[string]any{"query": "maki", "limit": 1})
	if len(list(r["results"])) != 1 || num(r["match_count"]) < 2 {
		t.Errorf("limit must cut the results but not the count: %v", r)
	}
	r = h.call("search_products", map[string]any{"query": "zzzz"})
	if len(list(r["results"])) != 0 || str(r["note"]) != "" {
		t.Errorf("no match: %v", r)
	}
}

func TestProductUpdateAndCreationTools(t *testing.T) {
	h := newHarness(t)
	r := h.proposeAndApply("propose_product_update", map[string]any{"product_id": "p-sashimi-saumon", "code": "SX", "piece_count": 5, "descriptions": map[string]any{"fr": "Frais du jour"},
		"is_halal": true, "is_vegetarian": false, "is_lunch_only": true, "is_discountable": false}, nil)
	mustContain(t, "summary", str(r["summary"]), `code: "S1" -> "SX"`, "piece count: none -> 5", "description (fr) changed", "halal: no -> yes", "lunch only: no -> yes", "discountable: yes -> no")
	if strings.Contains(str(r["summary"]), "vegetarian") {
		t.Errorf("an unchanged flag must not be listed: %s", r["summary"])
	}
	p := h.fake.ProductByID("p-sashimi-saumon")
	if *p.Code != "SX" || *p.PieceCount != 5 || !p.IsHalal || !p.IsLunchOnly || p.IsDiscountable {
		t.Errorf("product: %+v", p)
	}
	mustContain(t, "no-op", str(h.call("propose_product_update", map[string]any{"product_id": "p-sashimi-saumon", "code": "SX"})["summary"]), "already has these details")
	mustContain(t, "unknown product", h.callErr("propose_product_update", map[string]any{"product_id": "p-nope", "code": "X"}), "No product")

	// Creation: names are required, defaults are available + visible + discountable.
	mustContain(t, "no names", h.callErr("propose_product_creation", map[string]any{"category_id": "cat-maki", "names": map[string]any{}, "price_cents": 450}), "French name")
	r = h.proposeAndApply("propose_product_creation", map[string]any{"category_id": "cat-maki", "names": map[string]any{"fr": "Maki thon", "en": "Tuna maki", "zh": "金枪鱼卷"}, "price_cents": 520,
		"code": "M9", "piece_count": 8, "is_spicy": true, "vat_category": "food", "descriptions": map[string]any{"en": "Fresh tuna"}}, nil)
	mustContain(t, "summary", str(r["summary"]), "available, visible")
	created := h.fake.Products[len(h.fake.Products)-1]
	if *created.Code != "M9" || *created.PieceCount != 8 || !created.IsSpicy || !created.IsDiscountable || !created.IsAvailable || !created.IsVisible || created.Price != "5.2" {
		t.Errorf("created: %+v", created)
	}
	r = h.call("propose_product_creation", map[string]any{"category_id": "cat-maki", "names": map[string]any{"fr": "Maki thon rouge"}, "price_cents": 520, "available": false, "visible": false, "is_discountable": false})
	mustContain(t, "explicit flags", str(r["summary"]), "unavailable, hidden")
}

func TestProductImageToolErrors(t *testing.T) {
	h := newHarness(t)
	mustContain(t, "scheme", h.callErr("propose_product_image", map[string]any{"product_id": "p-maki-box", "image_url": "ftp://example.com/a.png"}), "must be an https link")
	mustContain(t, "empty", h.callErr("propose_product_image", map[string]any{"product_id": "p-maki-box", "image_url": ""}), "must be an https link")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("\x89PNG\r\n\x1a\n" + strings.Repeat("x", 50)))
	}))
	defer srv.Close()
	// The photo is only proposed for a product that exists.
	mustContain(t, "unknown product", h.callErr("propose_product_image", map[string]any{"product_id": "p-nope", "image_url": srv.URL + "/a.png"}), "No product")
	r := h.call("propose_product_image", map[string]any{"product_id": "p-maki-box", "image_url": srv.URL + "/box.png"})
	mustContain(t, "summary", str(r["summary"]), "new photo (image/png, 1 KB)")
	// The downloaded bytes are what gets uploaded, even if the URL changes later.
	h.apply(str(r["change_id"]))
	if string(h.fake.Images["p-maki-box"]) != "\x89PNG\r\n\x1a\n"+strings.Repeat("x", 50) {
		t.Errorf("uploaded %q", h.fake.Images["p-maki-box"])
	}
}

// --- restaurant ------------------------------------------------------------

func TestRestaurantConfigTool(t *testing.T) {
	h := newHarness(t)
	r := h.call("get_restaurant_config", map[string]any{})
	if r["ordering_hours"] != nil {
		t.Errorf("ordering hours follow the opening hours until set: %v", r["ordering_hours"])
	}
	if r["opening_hours"].(map[string]any)["tuesday"].(map[string]any)["dinner_open"] != "18:00" || num(r["preparation_minutes"]) != 30 {
		t.Errorf("config: %v", r)
	}
	h.fake.Lock()
	h.fake.Ordering = upstream.Week{"saturday": {Open: "12:00", Close: "21:00"}}
	h.fake.Config.NextOpeningAt = new(time.Date(2026, 10, 4, 15, 30, 0, 0, time.UTC))
	h.fake.Unlock()
	r = h.call("get_restaurant_config", map[string]any{})
	ord := r["ordering_hours"].(map[string]any)
	if len(ord) != 7 || ord["saturday"].(map[string]any)["close"] != "21:00" || ord["monday"].(map[string]any)["closed"] != true || str(r["next_opening_at"]) != "2026-10-04T17:30:00+02:00" {
		t.Errorf("config with ordering hours: %v", r)
	}
	h.fail("McpRestaurantConfig")
	h.callErr("get_restaurant_config", map[string]any{})
}

func TestListScheduleOverridesRange(t *testing.T) {
	h := newHarness(t)
	h.fake.Lock()
	h.fake.Overrides["2026-10-10"] = &upstream.ScheduleOverride{Date: time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC), Closed: true, Note: new("mariage")}
	h.fake.Overrides["2026-12-02"] = &upstream.ScheduleOverride{Date: time.Date(2026, 12, 2, 0, 0, 0, 0, time.UTC), Schedule: &upstream.DaySchedule{Open: "17:00", Close: "21:00"}}
	h.fake.Overrides["2026-12-10"] = &upstream.ScheduleOverride{Date: time.Date(2026, 12, 10, 0, 0, 0, 0, time.UTC), Closed: true}
	h.fake.Unlock()

	// Default: today and the next 60 days.
	r := h.call("list_schedule_overrides", map[string]any{})
	vars := h.lastVars("McpScheduleOverrides")
	if str(vars["from"]) != "2026-10-03T00:00:00Z" || str(vars["to"]) != "2026-12-02T23:00:00Z" {
		t.Errorf("default range: %v", vars)
	}
	ovs := list(r["overrides"])
	if len(ovs) != 2 {
		t.Fatalf("overrides: %v", ovs)
	}
	first := ovs[0].(map[string]any)
	if str(first["date"]) != "2026-10-10" || str(first["weekday"]) != "saturday" || first["hours"].(map[string]any)["closed"] != true || str(first["note"]) != "mariage" {
		t.Errorf("first: %v", first)
	}
	second := ovs[1].(map[string]any)
	if second["hours"].(map[string]any)["open"] != "17:00" || second["note"] != nil {
		t.Errorf("second: %v", second)
	}

	// Explicit range.
	r = h.call("list_schedule_overrides", map[string]any{"from": "2026-12-05", "to": "2026-12-31"})
	if ovs := list(r["overrides"]); len(ovs) != 1 || str(ovs[0].(map[string]any)["date"]) != "2026-12-10" {
		t.Errorf("explicit range: %v", r)
	}
	vars = h.lastVars("McpScheduleOverrides")
	if str(vars["from"]) != "2026-12-05T00:00:00Z" || str(vars["to"]) != "2026-12-31T23:00:00Z" {
		t.Errorf("explicit range vars: %v", vars)
	}
	// Only from: 60 days after it.
	h.call("list_schedule_overrides", map[string]any{"from": "2026-11-01"})
	if str(h.lastVars("McpScheduleOverrides")["to"]) != "2026-12-31T23:00:00Z" {
		t.Errorf("60 days after from: %v", h.lastVars("McpScheduleOverrides"))
	}

	mustContain(t, "bad from", h.callErr("list_schedule_overrides", map[string]any{"from": "x"}), "from: invalid date")
	mustContain(t, "bad to", h.callErr("list_schedule_overrides", map[string]any{"to": "x"}), "to: invalid date")
	h.fail("McpScheduleOverrides")
	h.callErr("list_schedule_overrides", map[string]any{})
}

func TestClosureToolVariants(t *testing.T) {
	h := newHarness(t)
	// Only "from": closed from then until the end of that day.
	r := h.call("propose_restaurant_closure", map[string]any{"from": "2026-10-03 20:00", "reason": "  fuite d'eau  "})
	mustContain(t, "summary", str(r["summary"]), "2026-10-03 (saturday)", "regular hours (11:30-22:30) -> special hours 11:30-20:00", `(note: "fuite d'eau")`)
	h.apply(str(r["change_id"]))
	if sat := h.fake.Overrides["2026-10-03"]; sat == nil || sat.Schedule == nil || sat.Schedule.Close != "20:00" || *sat.Note != "fuite d'eau" {
		t.Errorf("override: %+v", sat)
	}
	if len(h.fake.Overrides) != 1 {
		t.Errorf("only today is affected: %v", h.fake.Overrides)
	}

	// "from" defaults to now: closed from now until the evening.
	h2 := newHarness(t)
	r = h2.call("propose_restaurant_closure", map[string]any{"reopen_at": "2026-10-03T18:00"})
	mustContain(t, "default from", str(r["summary"]), "11:30-13:00, 18:00-22:30")

	// Errors.
	mustContain(t, "bad from", h2.callErr("propose_restaurant_closure", map[string]any{"from": "x", "reopen_at": "2026-10-03T18:00"}), "from: invalid time")
	mustContain(t, "bad reopen", h2.callErr("propose_restaurant_closure", map[string]any{"reopen_at": "x"}), "reopen_at: invalid time")
	mustContain(t, "bad from alone", h2.callErr("propose_restaurant_closure", map[string]any{"from": "x"}), "from: invalid time")
	h2.fail("McpRestaurantConfig")
	h2.callErr("propose_restaurant_closure", map[string]any{"reopen_at": "2026-10-03T18:00"})
	h2.unfail("McpRestaurantConfig")
	h2.fail("McpScheduleOverrides")
	h2.callErr("propose_restaurant_closure", map[string]any{"reopen_at": "2026-10-03T18:00"})
	if n := len(writeOps(h2.fake.Ops())); n != 0 {
		t.Errorf("nothing may be written: %v", h2.fake.Ops())
	}
}

func TestReopeningToolNotesAndFailures(t *testing.T) {
	h := newHarness(t)
	h.fake.Lock()
	h.fake.Config.OrderingEnabled = false
	h.fake.Overrides["2026-10-03"] = &upstream.ScheduleOverride{Date: time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC), Closed: true, Note: new("panne")}
	h.fake.Unlock()
	r := h.call("propose_restaurant_reopening", map[string]any{})
	notes := list(r["notes"])
	if len(notes) != 1 || !strings.Contains(str(notes[0]), "today has a closure set in the dashboard") || !strings.Contains(str(r["summary"]), "online ordering: off -> on") {
		t.Errorf("reopening: %v", r)
	}

	// Plan succeeds but the proposal cannot be built: the error surfaces.
	h2 := newHarness(t)
	h2.fake.Lock()
	h2.fake.Config.OrderingEnabled = false
	h2.fake.FailOps["McpRestaurantConfig"] = "BOOM"
	h2.fake.FailAfter["McpRestaurantConfig"] = 1 // PlanReopening reads it once, the proposal a second time
	h2.fake.Unlock()
	msg := h2.callErr("propose_restaurant_reopening", map[string]any{})
	if strings.Contains(msg, "injected") {
		t.Errorf("leak: %s", msg)
	}
	// The owner is told what the restaurant system said, in the assistant's own words, not a raw
	// transport error.
	if want := "The restaurant system refused the change (BOOM)."; msg != want {
		t.Errorf("owner message = %q, want %q", msg, want)
	}
	if got := h2.countOps("McpRestaurantConfig"); got != 2 {
		t.Errorf("config reads = %d, want 2 (plan + proposal)", got)
	}
}

func TestHoursToolVariants(t *testing.T) {
	h := newHarness(t)
	mustContain(t, "no days", h.callErr("propose_opening_hours", map[string]any{"days": map[string]any{}}), "at least one day")
	mustContain(t, "no days ordering", h.callErr("propose_ordering_hours", map[string]any{"days": map[string]any{}}), "at least one day")
	mustContain(t, "unknown day", h.callErr("propose_opening_hours", map[string]any{"days": map[string]any{"funday": map[string]any{"open": "10:00", "close": "11:00"}}}), `Unknown day "funday"`)

	// Day names are normalised; two periods and closed days are understood.
	r := h.proposeAndApply("propose_opening_hours", map[string]any{"days": map[string]any{
		" Monday ": map[string]any{"open": " 11:00 ", "close": "14:00", "dinner_open": "18:00", "dinner_close": "21:00"},
		"sunday":   map[string]any{"closed": true},
	}}, nil)
	mustContain(t, "summary", str(r["summary"]), "monday: closed -> 11:00-14:00, 18:00-21:00", "sunday: 17:30-22:00 -> closed")
	if m := h.fake.Opening["monday"]; m == nil || m.DinnerClose != "21:00" || m.Open != "11:00" {
		t.Errorf("monday: %+v", m)
	}

	// Ordering hours already set: only the given day changes against them, not against the opening hours.
	h.fake.Lock()
	h.fake.Ordering = upstream.Week{"saturday": {Open: "12:00", Close: "21:00"}, "tuesday": {Open: "12:00", Close: "13:00"}}
	h.fake.Unlock()
	r = h.call("propose_ordering_hours", map[string]any{"days": map[string]any{"saturday": map[string]any{"open": "12:00", "close": "20:00"}}})
	if strings.Contains(str(r["summary"]), "monday") || !strings.Contains(str(r["summary"]), "saturday: 12:00-21:00 -> 12:00-20:00") {
		t.Errorf("ordering hours diff: %v", r["summary"])
	}

	// No opening hours at all: a week is built from the given days only.
	h2 := newHarness(t)
	h2.fake.Lock()
	h2.fake.Opening = nil
	h2.fake.Unlock()
	h2.proposeAndApply("propose_opening_hours", map[string]any{"days": map[string]any{"friday": map[string]any{"open": "12:00", "close": "20:00"}}}, func() {
		if len(h2.fake.Opening) != 1 || h2.fake.Opening["friday"] == nil {
			t.Errorf("opening hours: %+v", h2.fake.Opening)
		}
	})

	h3 := newHarness(t)
	h3.fail("McpRestaurantConfig")
	h3.callErr("propose_opening_hours", map[string]any{"days": map[string]any{"friday": map[string]any{"open": "12:00", "close": "20:00"}}})
}

func TestScheduleOverrideToolVariants(t *testing.T) {
	h := newHarness(t)
	mustContain(t, "bad date", h.callErr("propose_schedule_override", map[string]any{"date": "25/12/2026", "closed": true}), "date: invalid date")
	mustContain(t, "bad end", h.callErr("propose_schedule_override", map[string]any{"date": "2026-12-25", "end_date": "x", "closed": true}), "end_date: invalid date")
	mustContain(t, "end before start", h.callErr("propose_schedule_override", map[string]any{"date": "2026-12-25", "end_date": "2026-12-24", "closed": true}), "end_date must not be before date")
	mustContain(t, "no hours", h.callErr("propose_schedule_override", map[string]any{"date": "2026-12-25"}), "Give open and close times, or set closed to true")
	mustContain(t, "bad hours", h.callErr("propose_schedule_override", map[string]any{"date": "2026-12-25", "open": "22:00", "close": "10:00"}), "Opening time 22:00 must be before closing time 10:00")
	mustContain(t, "range too long", h.callErr("propose_schedule_override", map[string]any{"date": "2026-11-01", "end_date": "2026-12-05", "closed": true}), "at most 31 days")
	mustContain(t, "removal bad date", h.callErr("propose_schedule_override_removal", map[string]any{"date": "x"}), "date: invalid date")
	mustContain(t, "removal range too long", h.callErr("propose_schedule_override_removal", map[string]any{"date": "2026-11-01", "end_date": "2026-12-05"}), "at most 31 days")

	// 31 days is the limit.
	r := h.call("propose_schedule_override", map[string]any{"date": "2026-11-01", "end_date": "2026-12-01", "open": "17:00", "close": "21:00", "note": "  rénovation "})
	mustContain(t, "31 days", str(r["summary"]), "2026-11-01 (sunday)", "2026-12-01 (tuesday)", `special hours 17:00-21:00 (note: "rénovation")`)
	h.apply(str(r["change_id"]))
	if len(h.fake.Overrides) != 31 || h.fake.Overrides["2026-11-15"].Schedule.Close != "21:00" || *h.fake.Overrides["2026-11-15"].Note != "rénovation" {
		t.Errorf("overrides: %d", len(h.fake.Overrides))
	}

	// A single date with two periods, no note.
	h.proposeAndApply("propose_schedule_override", map[string]any{"date": "2026-12-24", "open": "11:00", "close": "14:00", "dinner_open": "17:00", "dinner_close": "20:00"}, nil)
	if s := h.fake.Overrides["2026-12-24"].Schedule; s.DinnerOpen != "17:00" || h.fake.Overrides["2026-12-24"].Note != nil {
		t.Errorf("override: %+v", h.fake.Overrides["2026-12-24"])
	}

	// Removing a range deletes only what exists.
	r = h.call("propose_schedule_override_removal", map[string]any{"date": "2026-12-23", "end_date": "2026-12-25"})
	if strings.Count(str(r["summary"]), "regular hours") != 1 || !strings.Contains(str(r["summary"]), "2026-12-24") {
		t.Errorf("removal summary: %v", r["summary"])
	}
	mustContain(t, "nothing to remove", str(h.call("propose_schedule_override_removal", map[string]any{"date": "2027-03-01"})["summary"]), "already like this")
}
