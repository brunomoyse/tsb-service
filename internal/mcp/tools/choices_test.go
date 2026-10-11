package tools_test

import (
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"tsb-service/internal/mcp/upstream"
)

// --- helpers ----------------------------------------------------------------------

// rawAny calls a tool and reports a protocol-level rejection (schema
// validation) as well as a tool error: both mean "refused, nothing proposed".
func (h *harness) rawAny(name string, args map[string]any) (refused bool, text string) {
	h.t.Helper()
	res, err := h.cs.CallTool(h.ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return true, err.Error()
	}
	if len(res.Content) > 0 {
		if tc, ok := res.Content[0].(*mcp.TextContent); ok {
			text = tc.Text
		}
	}
	return res.IsError, text
}

// refused asserts the call is refused, writes nothing and creates no pending
// change.
func (h *harness) refused(name string, args map[string]any, wantInText ...string) {
	h.t.Helper()
	writes := len(writeOps(h.fake.Ops()))
	bad, text := h.rawAny(name, args)
	if !bad {
		h.t.Fatalf("%s(%v) must be refused, got %s", name, args, text)
	}
	for _, w := range wantInText {
		if !strings.Contains(text, w) {
			h.t.Errorf("%s(%v): %q does not mention %q", name, args, text, w)
		}
	}
	if got := len(writeOps(h.fake.Ops())); got != writes {
		h.t.Errorf("%s wrote upstream", name)
	}
}

func choicesOf(g any) []map[string]any {
	var out []map[string]any
	for _, c := range list(g.(map[string]any)["choices"]) {
		out = append(out, c.(map[string]any))
	}
	return out
}

// --- reading --------------------------------------------------------------------------

func TestGetProductChoiceDetails(t *testing.T) {
	h := newHarness(t)
	h.fake.Lock()
	g := &h.fake.Products[2].ChoiceGroups[0]
	g.Translations = append(g.Translations, upstream.ChoiceTranslation{Locale: "en", Name: "Sauce EN"})
	g.Choices[1].Translations = append(g.Choices[1].Translations, upstream.ChoiceTranslation{Locale: "zh", Name: "辣蛋黄酱"})
	// A second, optional multi-pick group listed after the first.
	h.fake.Products[2].ChoiceGroups = append(h.fake.Products[2].ChoiceGroups, upstream.ChoiceGroup{ID: "g-extra", ProductID: "p-maki-box", MinSelections: 0, MaxSelections: 4, SortOrder: 1, Name: "Extras",
		Translations: []upstream.ChoiceTranslation{{Locale: "fr", Name: "Extras"}, {Locale: "zh", Name: "加料"}},
		Choices:      []upstream.Choice{{ID: "c-gingembre", ChoiceGroupID: "g-extra", PriceModifier: "1.25", SortOrder: 0, Name: "Gingembre", Translations: []upstream.ChoiceTranslation{{Locale: "fr", Name: "Gingembre"}, {Locale: "zh", Name: "姜"}}}}})
	h.fake.Unlock()

	r := h.call("get_product", map[string]any{"product_id": "p-maki-box"})
	groups := list(r["choice_groups"])
	if len(groups) != 2 {
		t.Fatalf("groups = %v", groups)
	}
	sauce, extras := groups[0].(map[string]any), groups[1].(map[string]any)
	if str(sauce["group_id"]) != "g-sauce" || str(sauce["name"]) != "Sauce" || num(sauce["min_selections"]) != 1 || num(sauce["max_selections"]) != 1 || num(sauce["sort_order"]) != 0 {
		t.Errorf("sauce group: %v", sauce)
	}
	names := sauce["names"].(map[string]any)
	if names["fr"] != "Sauce" || names["zh"] != "酱汁" || names["en"] != "Sauce EN" {
		t.Errorf("group names: %v", names)
	}
	if str(extras["group_id"]) != "g-extra" || num(extras["min_selections"]) != 0 || num(extras["max_selections"]) != 4 || num(extras["sort_order"]) != 1 {
		t.Errorf("extras group (optional, up to 4): %v", extras)
	}
	cs := choicesOf(sauce)
	if len(cs) != 2 || str(cs[0]["choice_id"]) != "c-soja" || num(cs[0]["price_modifier_cents"]) != 0 || str(cs[1]["choice_id"]) != "c-spicy" || num(cs[1]["price_modifier_cents"]) != 50 || num(cs[1]["sort_order"]) != 1 {
		t.Errorf("sauce choices: %v", cs)
	}
	if n := cs[1]["names"].(map[string]any); n["fr"] != "Mayo épicée" || n["zh"] != "辣蛋黄酱" {
		t.Errorf("choice names: %v", n)
	}
	if got := choicesOf(extras)[0]; num(got["price_modifier_cents"]) != 125 {
		t.Errorf("a 1.25 EUR surcharge must read 125 cents: %v", got)
	}
	// The product price is separate from the surcharges.
	if num(r["price_cents"]) != 1390 {
		t.Errorf("price_cents = %v", r["price_cents"])
	}
}

func TestGetProductWithoutChoicesReturnsEmptyList(t *testing.T) {
	h := newHarness(t)
	r := h.call("get_product", map[string]any{"product_id": "p-maki-saumon"})
	groups, ok := r["choice_groups"].([]any)
	if !ok || len(groups) != 0 {
		t.Errorf("choice_groups = %#v, want an empty list", r["choice_groups"])
	}
}

func TestGetProductAfterChoiceEditsShowsTheNewState(t *testing.T) {
	h := newHarness(t)
	r := h.call("propose_choice_change", map[string]any{"choice_id": "c-spicy", "price_modifier_cents": 65, "names": map[string]any{"zh": "辣酱"}})
	h.apply(str(r["change_id"]))
	r = h.call("propose_choice_group_change", map[string]any{"group_id": "g-sauce", "min_selections": 0, "max_selections": 2})
	h.apply(str(r["change_id"]))
	g := list(h.call("get_product", map[string]any{"product_id": "p-maki-box"})["choice_groups"])[0].(map[string]any)
	c := choicesOf(g)[1]
	if num(c["price_modifier_cents"]) != 65 || c["names"].(map[string]any)["zh"] != "辣酱" || c["names"].(map[string]any)["fr"] != "Mayo épicée" {
		t.Errorf("choice: %v", c)
	}
	if num(g["min_selections"]) != 0 || num(g["max_selections"]) != 2 {
		t.Errorf("group: %v", g)
	}
}

// Choices are not products: they never count in match_count, but a query that
// matches an option name lists it, with the dish and group it belongs to.
func TestSearchProductsListsOptionsSeparately(t *testing.T) {
	h := newHarness(t)
	for _, q := range []string{"Mayo épicée", "mayo epicee", "Mayo"} {
		out := h.call("search_products", map[string]any{"query": q})
		opts := list(out["options"])
		if num(out["match_count"]) != 0 || num(out["option_match_count"]) != 1 || len(opts) != 1 {
			t.Fatalf("%q: %v", q, out)
		}
		o := opts[0].(map[string]any)
		if str(o["choice_id"]) != "c-spicy" || str(o["group_id"]) != "g-sauce" || str(o["product_id"]) != "p-maki-box" || num(o["price_modifier_cents"]) != 50 {
			t.Errorf("%q option: %v", q, o)
		}
		if got := str(o["also_option_of"]); got != "This is also an option of: 套餐 卷寿司套餐 > group Sauce" {
			t.Errorf("also_option_of = %q", got)
		}
		if str(o["product_labels"].(map[string]any)["fr"]) != "Boxes Maki box" {
			t.Errorf("labels: %v", o["product_labels"])
		}
		if !strings.Contains(str(out["note"]), "No product matches, but it is an option of one dish") {
			t.Errorf("note = %q", out["note"])
		}
	}
	// Typos do not turn into options, ids and group names are not option names.
	for _, q := range []string{"Mayyo epicee", "c-spicy", "g-sauce", "Sauce", "酱汁", "zzz"} {
		out := h.call("search_products", map[string]any{"query": q})
		if num(out["option_match_count"]) != 0 || len(list(out["options"])) != 0 {
			t.Errorf("%q matched options: %v", q, out["options"])
		}
	}
	// No option anywhere: the list is empty, not missing, and the note is unchanged.
	out := h.call("search_products", map[string]any{"query": "maki saumon"})
	if _, ok := out["options"].([]any); !ok || num(out["option_match_count"]) != 0 || strings.Contains(str(out["note"]), "option") {
		t.Errorf("maki saumon: %v", out)
	}
}

// 辣汤 is a dish of 东京热食 and a choice in two platters: the model must be
// told the name is ambiguous.
func TestSearchNameBothProductAndOption(t *testing.T) {
	h := newMenuChoicesHarness(t)
	h.fake.Lock()
	for _, id := range []string{idPlateauM1, "9e199415-0f31-46a9-ae14-f0dc0899e681"} {
		for _, p := range h.fake.Products {
			if p.ID == id {
				if len(p.ChoiceGroups) == 0 {
					p.ChoiceGroups = []upstream.ChoiceGroup{group("g-"+id[:8], id, "Soupe", "汤", 1, 1, 0)}
				}
				g := &p.ChoiceGroups[0]
				g.Choices = append(g.Choices, choice("c-"+id[:8]+"-hot", g.ID, "Soupe piquante", "辣汤", "0", 5))
			}
		}
	}
	h.fake.Unlock()
	out := h.call("search_products", map[string]any{"query": "辣汤"})
	if num(out["exact_matches"]) != 1 || num(out["match_count"]) < 1 || num(out["option_match_count"]) != 2 {
		t.Fatalf("counts: %v", out)
	}
	note := str(out["note"])
	for _, want := range []string{"also matches 2 option(s)", "ask before changing anything", "propose_choice_change", "propose_price_change"} {
		if !strings.Contains(note, want) {
			t.Errorf("note %q lacks %q", note, want)
		}
	}
	first := list(out["results"])[0].(map[string]any)
	if str(first["labels"].(map[string]any)["zh"]) != "东京热食 辣汤" {
		t.Errorf("product result: %v", first["labels"])
	}
	var of []string
	for _, o := range list(out["options"]) {
		of = append(of, str(o.(map[string]any)["also_option_of"]))
	}
	if len(of) != 2 || !strings.Contains(strings.Join(of, "|"), "拼盘套餐 M1 > group Soupe") || !strings.Contains(strings.Join(of, "|"), "拼盘套餐 M2 > group Soupe") {
		t.Errorf("options: %v", of)
	}
	// Chinese written with spaces and the French name find the same options.
	for _, q := range []string{"辣 汤", "Soupe piquante"} {
		o := h.call("search_products", map[string]any{"query": q})
		if num(o["option_match_count"]) != 2 {
			t.Errorf("%q: %v", q, o["option_match_count"])
		}
	}
}

// An option of several dishes, and no dish of that name: ask which dish.
func TestSearchOptionOfSeveralProducts(t *testing.T) {
	h := newMenuChoicesHarness(t)
	h.fake.Lock()
	for _, id := range []string{idMakiSaumon, idSushiSaumon, idSashimiSaumon} {
		for _, p := range h.fake.Products {
			if p.ID == id {
				g := &p.ChoiceGroups[0]
				g.Choices = append(g.Choices, choice("c-ponzu-"+id[:8], g.ID, "Ponzu maison", "柚子醋", "0.30", 9))
			}
		}
	}
	h.fake.Unlock()
	out := h.call("search_products", map[string]any{"query": "Ponzu"})
	if num(out["match_count"]) != 0 || num(out["option_match_count"]) != 3 || len(list(out["options"])) != 3 {
		t.Fatalf("Ponzu: %v", out)
	}
	if note := str(out["note"]); !strings.Contains(note, "ask which one, listing the dishes by category + name") {
		t.Errorf("note = %q", note)
	}
	ids := map[string]bool{}
	for _, o := range list(out["options"]) {
		ids[str(o.(map[string]any)["product_id"])] = true
	}
	if !ids[idMakiSaumon] || !ids[idSushiSaumon] || !ids[idSashimiSaumon] {
		t.Errorf("dishes: %v", ids)
	}
	// Chinese name: the same three dishes, labelled by category.
	zh := h.call("search_products", map[string]any{"query": "柚子醋"})
	var labels []string
	for _, o := range list(zh["options"]) {
		labels = append(labels, str(o.(map[string]any)["product_labels"].(map[string]any)["zh"]))
	}
	if len(labels) != 3 || !strings.Contains(strings.Join(labels, "|"), "卷寿司 三文鱼") || !strings.Contains(strings.Join(labels, "|"), "刺身 三文鱼") {
		t.Errorf("labels: %v", labels)
	}
}

func TestSearchOptionListIsCapped(t *testing.T) {
	h := newMenuChoicesHarness(t)
	h.fake.Lock()
	for _, p := range h.fake.Products {
		if p.ID == idPokeSaumon {
			g := &p.ChoiceGroups[0]
			for i := range 14 {
				g.Choices = append(g.Choices, choice("c-extra-"+string(rune('a'+i)), g.ID, "Sauce extra "+string(rune('a'+i)), "", "0", 20+i))
			}
		}
	}
	h.fake.Unlock()
	out := h.call("search_products", map[string]any{"query": "Sauce extra"})
	if num(out["option_match_count"]) != 14 || len(list(out["options"])) != 10 {
		t.Errorf("count %v listed %d", out["option_match_count"], len(list(out["options"])))
	}
}

func TestSearchOptionsCarryNoPrivateData(t *testing.T) {
	h := newMenuChoicesHarness(t)
	out := h.call("search_products", map[string]any{"query": "Soupe miso"})
	if len(list(out["options"])) == 0 {
		t.Fatal("no option listed")
	}
	for _, o := range list(out["options"]) {
		for k := range o.(map[string]any) {
			switch k {
			case "choice_id", "name", "names", "group_id", "group_name", "product_id", "product_labels", "also_option_of", "price_modifier_cents":
			default:
				t.Errorf("unexpected option field %q", k)
			}
		}
	}
}

func TestGetProductNeverLeaksAnotherProductsChoices(t *testing.T) {
	h := newHarness(t)
	for _, id := range []string{"p-maki-saumon", "p-sashimi-saumon", "p-creme"} {
		if got := list(h.call("get_product", map[string]any{"product_id": id})["choice_groups"]); len(got) != 0 {
			t.Errorf("%s shows choices of another product: %v", id, got)
		}
	}
}

// --- proposals --------------------------------------------------------------------------

func TestProposeChoiceToolsReturnBothLanguages(t *testing.T) {
	const prodFr = `Boxes "Maki box" (套餐「卷寿司套餐」)`
	const prodZh = "套餐「卷寿司套餐」"
	tests := []struct {
		tool   string
		args   map[string]any
		en, zh string
	}{
		{"propose_choice_group_change", map[string]any{"group_id": "g-sauce", "max_selections": 2, "request_context": "酱可以选两个"},
			prodFr + `, choice group "Sauce": max selections: 1 -> 2`, prodZh + "的选项组「酱汁」：最多选择：1 → 2"},
		{"propose_choice_group_change", map[string]any{"group_id": "g-sauce", "min_selections": 0},
			"min selections: 1 -> 0", "最少选择：1 → 0"},
		{"propose_choice_group_change", map[string]any{"group_id": "g-sauce", "names": map[string]any{"en": "Sauces"}, "sort_order": 2},
			`name (en): "" -> "Sauces"; position: 0 -> 2`, "英语名称：无 → 「Sauces」；排序位置：0 → 2"},
		{"propose_choice_group_change", map[string]any{"product_id": "p-maki-box", "names": map[string]any{"fr": "Boisson", "zh": "饮料"}, "min_selections": 0, "max_selections": 2},
			prodFr + ", new choice group", prodZh + "的新选项组"},
		{"propose_choice_change", map[string]any{"choice_id": "c-spicy", "price_modifier_cents": 70, "request_context": "辣酱加70分"},
			prodFr + `, choice "Mayo épicée": surcharge: 0.50 EUR -> 0.70 EUR`, prodZh + "的选项「Mayo épicée」：加价：0.50 欧元 → 0.70 欧元"},
		{"propose_choice_change", map[string]any{"choice_id": "c-soja", "names": map[string]any{"zh": "酱油"}},
			`name (zh): "" -> "酱油"`, "中文名称：无 → 「酱油」"},
		{"propose_choice_change", map[string]any{"group_id": "g-sauce", "names": map[string]any{"fr": "Wasabi", "zh": "芥末"}, "price_modifier_cents": 100},
			`group "Sauce", new choice`, prodZh + "的选项组「酱汁」中的新选项"},
		{"propose_choice_deletion", map[string]any{"choice_id": "c-spicy", "request_context": "去掉辣酱"},
			`delete choice "Mayo épicée" from group "Sauce". This cannot be undone.`, prodZh + "：从选项组「酱汁」中删除选项「Mayo épicée」。此操作无法撤销。"},
		{"propose_choice_group_deletion", map[string]any{"group_id": "g-sauce", "request_context": "不要酱汁选项了"},
			`delete choice group "Sauce" and its 2 choices (Soja, Mayo épicée)`, prodZh + "：删除选项组「酱汁」及其 2 个选项（Soja、Mayo épicée）"},
	}
	for _, tt := range tests {
		t.Run(tt.tool+"/"+tt.en[:min(len(tt.en), 30)], func(t *testing.T) {
			h := newHarness(t)
			writes := len(writeOps(h.fake.Ops()))
			r := h.call(tt.tool, tt.args)
			if !strings.Contains(str(r["summary"]), tt.en) {
				t.Errorf("summary = %q, want %q", r["summary"], tt.en)
			}
			if !strings.Contains(str(r["summary_zh"]), tt.zh) {
				t.Errorf("summary_zh = %q, want %q", r["summary_zh"], tt.zh)
			}
			if str(r["status"]) != "pending" || str(r["change_id"]) == "" || str(r["expires_at"]) == "" {
				t.Errorf("not a pending proposal: %v", r)
			}
			if len(writeOps(h.fake.Ops())) != writes {
				t.Error("a proposal must not write upstream")
			}
			if p := h.fake.ProductByID("p-maki-box"); p.Price != "13.9" || len(p.ChoiceGroups) != 1 || len(p.ChoiceGroups[0].Choices) != 2 {
				t.Errorf("proposal changed the product: %+v", p)
			}
		})
	}
}

func TestProposeChoiceNoOpHasNoChangeID(t *testing.T) {
	h := newHarness(t)
	for tool, args := range map[string]map[string]any{
		"propose_choice_change":       {"choice_id": "c-spicy", "price_modifier_cents": 50},
		"propose_choice_group_change": {"group_id": "g-sauce", "min_selections": 1, "max_selections": 1},
	} {
		r := h.call(tool, args)
		if r["no_op"] != true || str(r["change_id"]) != "" || !strings.Contains(str(r["summary_zh"]), "无需更改") {
			t.Errorf("%s: %v", tool, r)
		}
	}
	if len(writeOps(h.fake.Ops())) != 0 {
		t.Error("a no-op wrote upstream")
	}
}

func TestProposeChoiceToolsRefuseBadInput(t *testing.T) {
	h := newHarness(t)
	t.Run("surcharge is not a number", func(t *testing.T) {
		h.refused("propose_choice_change", map[string]any{"choice_id": "c-spicy", "price_modifier_cents": "50"}, "price_modifier_cents")
	})
	t.Run("surcharge in euros with decimals", func(t *testing.T) {
		h.refused("propose_choice_change", map[string]any{"choice_id": "c-spicy", "price_modifier_cents": 0.7}, "price_modifier_cents")
	})
	t.Run("surcharge beyond int64", func(t *testing.T) {
		h.refused("propose_choice_change", map[string]any{"choice_id": "c-spicy", "price_modifier_cents": 1e30})
	})
	t.Run("negative surcharge", func(t *testing.T) {
		h.refused("propose_choice_change", map[string]any{"choice_id": "c-spicy", "price_modifier_cents": -1}, "cannot be negative")
		h.refused("propose_choice_change", map[string]any{"group_id": "g-sauce", "names": map[string]any{"fr": "X"}, "price_modifier_cents": -100}, "cannot be negative")
	})
	t.Run("surcharge jump beyond the guard", func(t *testing.T) {
		h.refused("propose_choice_change", map[string]any{"choice_id": "c-spicy", "price_modifier_cents": 150}, "Surcharge change refused", "0.25 EUR", "0.75 EUR")
	})
	t.Run("selections are not integers", func(t *testing.T) {
		h.refused("propose_choice_group_change", map[string]any{"group_id": "g-sauce", "max_selections": "2"}, "max_selections")
		h.refused("propose_choice_group_change", map[string]any{"group_id": "g-sauce", "min_selections": 1.5}, "min_selections")
		h.refused("propose_choice_group_change", map[string]any{"group_id": "g-sauce", "sort_order": "first"}, "sort_order")
	})
	t.Run("min above max", func(t *testing.T) {
		h.refused("propose_choice_group_change", map[string]any{"group_id": "g-sauce", "min_selections": 5, "max_selections": 3}, "0 <= min <= max", "min 5", "max 3")
		h.refused("propose_choice_group_change", map[string]any{"group_id": "g-sauce", "min_selections": 2}, "0 <= min <= max")
	})
	t.Run("max below one", func(t *testing.T) {
		h.refused("propose_choice_group_change", map[string]any{"group_id": "g-sauce", "max_selections": 0}, "max >= 1")
		h.refused("propose_choice_group_change", map[string]any{"group_id": "g-sauce", "min_selections": -1}, "0 <= min <= max")
	})
	t.Run("a new group needs a French name", func(t *testing.T) {
		h.refused("propose_choice_group_change", map[string]any{"product_id": "p-maki-box"}, "French name")
		h.refused("propose_choice_group_change", map[string]any{"product_id": "p-maki-box", "names": map[string]any{"zh": "饮料"}}, "French name")
		h.refused("propose_choice_change", map[string]any{"group_id": "g-sauce", "names": map[string]any{"zh": "芥末"}}, "French name")
		h.refused("propose_choice_change", map[string]any{"group_id": "g-sauce"}, "French name")
	})
	t.Run("blank and unknown languages", func(t *testing.T) {
		h.refused("propose_choice_change", map[string]any{"choice_id": "c-soja", "names": map[string]any{"zh": "  "}}, "cannot be empty")
		h.refused("propose_choice_change", map[string]any{"choice_id": "c-soja", "names": map[string]any{"de": "Sojasoße"}}, "Unknown language")
		h.refused("propose_choice_group_change", map[string]any{"group_id": "g-sauce", "names": map[string]any{"jp": "ソース"}}, "Unknown language")
	})
	t.Run("unknown ids", func(t *testing.T) {
		h.refused("propose_choice_change", map[string]any{"choice_id": "c-nope", "price_modifier_cents": 10}, `No choice with id "c-nope"`, "get_product")
		h.refused("propose_choice_change", map[string]any{"group_id": "g-nope", "names": map[string]any{"fr": "X"}}, `No choice group with id "g-nope"`)
		h.refused("propose_choice_group_change", map[string]any{"group_id": "g-nope", "max_selections": 2}, `No choice group with id "g-nope"`)
		h.refused("propose_choice_group_change", map[string]any{"product_id": "p-nope", "names": map[string]any{"fr": "X"}}, `No product with id "p-nope"`)
		h.refused("propose_choice_deletion", map[string]any{"choice_id": "c-nope"}, `No choice with id "c-nope"`)
		h.refused("propose_choice_group_deletion", map[string]any{"group_id": "g-nope"}, `No choice group with id "g-nope"`)
	})
	t.Run("a name is not an id", func(t *testing.T) {
		// The agent must pass the id from get_product, not the choice's name.
		h.refused("propose_choice_deletion", map[string]any{"choice_id": "Mayo épicée"}, "No choice with id")
		h.refused("propose_choice_group_deletion", map[string]any{"group_id": "Sauce"}, "No choice group with id")
		h.refused("propose_choice_group_deletion", map[string]any{"group_id": "酱汁"}, "No choice group with id")
	})
	t.Run("a choice id is not a group id", func(t *testing.T) {
		h.refused("propose_choice_group_deletion", map[string]any{"group_id": "c-spicy"}, "No choice group")
		h.refused("propose_choice_deletion", map[string]any{"choice_id": "g-sauce"}, "No choice with id")
		h.refused("propose_choice_change", map[string]any{"choice_id": "g-sauce", "price_modifier_cents": 10}, "No choice with id")
	})
	t.Run("missing ids on deletions", func(t *testing.T) {
		h.refused("propose_choice_deletion", map[string]any{})
		h.refused("propose_choice_group_deletion", map[string]any{})
		h.refused("propose_choice_deletion", map[string]any{"choice_id": ""}, "No choice with id")
		h.refused("propose_choice_group_deletion", map[string]any{"group_id": ""}, "No choice group with id")
	})
	t.Run("both targets or neither", func(t *testing.T) {
		h.refused("propose_choice_change", map[string]any{"price_modifier_cents": 10}, "Give either choice_id")
		h.refused("propose_choice_group_change", map[string]any{"max_selections": 2}, "Give either group_id")
		h.refused("propose_choice_change", map[string]any{"choice_id": "c-soja", "group_id": "g-sauce", "names": map[string]any{"fr": "X"}}, "Give either choice_id")
		h.refused("propose_choice_group_change", map[string]any{"group_id": "g-sauce", "product_id": "p-maki-box"}, "Give either group_id")
	})
	t.Run("nothing was proposed", func(t *testing.T) {
		if got := len(h.fake.ProductByID("p-maki-box").ChoiceGroups[0].Choices); got != 2 {
			t.Errorf("choices = %d", got)
		}
	})
}

func TestProposeChoiceToolsUpstreamDown(t *testing.T) {
	h := newHarness(t)
	h.fake.Lock()
	h.fake.FailOps["McpProducts"] = "INTERNAL"
	h.fake.Unlock()
	for tool, args := range map[string]map[string]any{
		"propose_choice_change":         {"choice_id": "c-spicy", "price_modifier_cents": 60},
		"propose_choice_group_change":   {"group_id": "g-sauce", "max_selections": 2},
		"propose_choice_deletion":       {"choice_id": "c-spicy"},
		"propose_choice_group_deletion": {"group_id": "g-sauce"},
	} {
		h.refused(tool, args)
	}
}

// --- apply through the tools ----------------------------------------------------------------

func TestChoiceChangeEndToEnd(t *testing.T) {
	h := newHarness(t)
	product := func() upstream.Product { return h.fake.ProductByID("p-maki-box") }

	// The owner builds a composer: at least two, at most three extras.
	r := h.call("propose_choice_group_change", map[string]any{"product_id": "p-maki-box", "names": map[string]any{"fr": "Extras", "zh": "加料"}, "min_selections": 2, "max_selections": 3})
	h.apply(str(r["change_id"]))
	var extras upstream.ChoiceGroup
	for _, g := range product().ChoiceGroups {
		if g.Name == "Extras" {
			extras = g
		}
	}
	if extras.ID == "" || extras.MinSelections != 2 || extras.MaxSelections != 3 || extras.SortOrder != 1 {
		t.Fatalf("extras = %+v", extras)
	}
	for i, n := range []string{"Gingembre", "Wasabi", "Avocat"} {
		r = h.call("propose_choice_change", map[string]any{"group_id": extras.ID, "names": map[string]any{"fr": n}, "price_modifier_cents": 50 * (i + 1)})
		h.apply(str(r["change_id"]))
	}
	got := product().ChoiceGroups[1].Choices
	if len(got) != 3 || got[0].Name != "Gingembre" || got[2].PriceModifier != "1.5" || got[1].SortOrder != 1 || got[2].SortOrder != 2 {
		t.Fatalf("choices = %+v", got)
	}

	// Reading it back through get_product matches what was applied.
	g := list(h.call("get_product", map[string]any{"product_id": "p-maki-box"})["choice_groups"])[1].(map[string]any)
	cs := choicesOf(g)
	if len(cs) != 3 || num(cs[2]["price_modifier_cents"]) != 150 || num(g["min_selections"]) != 2 {
		t.Errorf("get_product: %v", g)
	}

	// Rename, reprice then delete one, and the whole group.
	r = h.call("propose_choice_change", map[string]any{"choice_id": got[1].ID, "names": map[string]any{"fr": "Wasabi fort", "zh": "强力芥末"}, "price_modifier_cents": 100})
	h.apply(str(r["change_id"]))
	if c := product().ChoiceGroups[1].Choices[1]; c.Name != "Wasabi fort" || c.PriceModifier != "1" {
		t.Errorf("choice = %+v", c)
	}
	r = h.call("propose_choice_deletion", map[string]any{"choice_id": got[0].ID})
	h.apply(str(r["change_id"]))
	if n := len(product().ChoiceGroups[1].Choices); n != 2 {
		t.Errorf("choices = %d", n)
	}
	r = h.call("propose_choice_group_deletion", map[string]any{"group_id": extras.ID})
	h.apply(str(r["change_id"]))
	if n := len(product().ChoiceGroups); n != 1 {
		t.Errorf("groups = %d", n)
	}
	if p := product(); p.Price != "13.9" {
		t.Errorf("the base price moved: %s", p.Price)
	}
}

func TestChoiceChangeConfirmedAfterTheChoiceWasRemoved(t *testing.T) {
	h := newHarness(t)
	r := h.call("propose_choice_change", map[string]any{"choice_id": "c-spicy", "price_modifier_cents": 60})
	del := h.call("propose_choice_deletion", map[string]any{"choice_id": "c-spicy"})
	h.apply(str(del["change_id"]))
	writes := len(writeOps(h.fake.Ops()))
	if _, err := h.svc.ApplyPending(h.ctx, str(r["change_id"]), "yes"); err == nil {
		t.Fatal("a stale surcharge change must not apply")
	}
	if len(writeOps(h.fake.Ops())) != writes {
		t.Error("a stale change wrote upstream")
	}
}

func TestChoiceChangeRejectedLeavesTheChoice(t *testing.T) {
	h := newHarness(t)
	r := h.call("propose_choice_deletion", map[string]any{"choice_id": "c-spicy"})
	if _, err := h.svc.Reject(h.ctx, str(r["change_id"]), "不要"); err != nil {
		t.Fatal(err)
	}
	if got := len(h.fake.ProductByID("p-maki-box").ChoiceGroups[0].Choices); got != 2 {
		t.Errorf("choices = %d", got)
	}
	if _, err := h.svc.ApplyPending(h.ctx, str(r["change_id"]), "yes"); err == nil {
		t.Error("a rejected change must not apply")
	}
}

func TestUndoLastChoiceChangeThroughTheTool(t *testing.T) {
	h := newHarness(t)
	r := h.call("propose_choice_change", map[string]any{"choice_id": "c-spicy", "price_modifier_cents": 70})
	h.apply(str(r["change_id"]))
	u := h.call("undo_last_change", map[string]any{})
	if str(u["mode"]) != "pending" {
		t.Fatalf("undo: %v", u)
	}
	if !strings.Contains(str(u["summary_zh"]), "加价：0.70 欧元 → 0.50 欧元") {
		t.Errorf("undo summary_zh = %q", u["summary_zh"])
	}
	h.apply(str(u["proposal"].(map[string]any)["change_id"]))
	if got := h.fake.ProductByID("p-maki-box").ChoiceGroups[0].Choices[1].PriceModifier; got != "0.5" {
		t.Errorf("surcharge = %s", got)
	}

	r = h.call("propose_choice_deletion", map[string]any{"choice_id": "c-soja"})
	h.apply(str(r["change_id"]))
	if msg := h.callErr("undo_last_change", map[string]any{}); !strings.Contains(msg, "cannot be undone automatically") {
		t.Errorf("undo of a deletion: %s", msg)
	}
}

// --- the real menu -----------------------------------------------------------------------------

const (
	idMakiSaumon    = "9e199544-8c16-4c76-8a9d-44252a31237d"
	idSushiSaumon   = "9e19953a-26ae-4cae-b821-56e909229ba0"
	idSashimiSaumon = "9e19955b-f33b-4f01-b882-fc3eba770b10"
	idPlateauM1     = "9e199414-bb2f-4aed-ac7f-a6423a2d7092"
	idPokeSaumon    = "9e19955d-6aaf-4cc4-9979-e20e6c856a13"
	idRamenPoulet   = "82c1f284-a92d-4e3b-9da7-a3d264c6d5d7"
)

func choice(id, group, fr, zh, price string, sort int) upstream.Choice {
	ts := []upstream.ChoiceTranslation{{Locale: "fr", Name: fr}}
	if zh != "" {
		ts = append(ts, upstream.ChoiceTranslation{Locale: "zh", Name: zh})
	}
	return upstream.Choice{ID: id, ChoiceGroupID: group, PriceModifier: price, SortOrder: sort, Name: fr, Translations: ts}
}

func group(id, product, fr, zh string, minSel, maxSel, sort int, cs ...upstream.Choice) upstream.ChoiceGroup {
	return upstream.ChoiceGroup{ID: id, ProductID: product, MinSelections: minSel, MaxSelections: maxSel, SortOrder: sort, Name: fr,
		Translations: []upstream.ChoiceTranslation{{Locale: "fr", Name: fr}, {Locale: "zh", Name: zh}}, Choices: cs}
}

// newMenuChoicesHarness attaches option groups to real menu products: a sauce
// on the three "salmon" items of three categories (same names everywhere), a
// soup on a platter, a pick-at-least-five composer on a poke bowl and a spice
// level on a ramen.
func newMenuChoicesHarness(t *testing.T) *harness {
	t.Helper()
	h := newMenuHarness(t)
	h.fake.Lock()
	defer h.fake.Unlock()
	attach := func(id string, gs ...upstream.ChoiceGroup) {
		for _, p := range h.fake.Products {
			if p.ID == id {
				p.ChoiceGroups = gs
				return
			}
		}
		t.Fatalf("no product %s", id)
	}
	sauce := func(prefix, product string) upstream.ChoiceGroup {
		return group("g-"+prefix+"-sauce", product, "Sauce", "酱汁", 1, 1, 0,
			choice("c-"+prefix+"-soja", "g-"+prefix+"-sauce", "Soja", "酱油", "0", 0),
			choice("c-"+prefix+"-spicy", "g-"+prefix+"-sauce", "Sauce piquante", "辣酱", "0.50", 1))
	}
	attach(idMakiSaumon, sauce("maki", idMakiSaumon))
	attach(idSushiSaumon, sauce("sushi", idSushiSaumon))
	attach(idSashimiSaumon, sauce("sashimi", idSashimiSaumon))
	attach(idPlateauM1, group("g-m1-soupe", idPlateauM1, "Soupe", "汤", 1, 1, 0,
		choice("c-m1-miso", "g-m1-soupe", "Soupe miso", "味噌汤", "0", 0),
		choice("c-m1-raviolis", "g-m1-soupe", "Soupe raviolis", "饺子汤", "1.00", 1)))
	var ingredients []upstream.Choice
	for i, n := range []string{"Avocat", "Concombre", "Edamame", "Mangue", "Maïs", "Radis", "Carotte"} {
		ingredients = append(ingredients, choice("c-poke-"+string(rune('a'+i)), "g-poke-compose", n, "", "0", i))
	}
	ingredients = append(ingredients, choice("c-poke-premium", "g-poke-compose", "Crevettes", "虾", "2.00", 7))
	attach(idPokeSaumon, group("g-poke-compose", idPokeSaumon, "Ingrédients", "配料", 5, 8, 0, ingredients...))
	attach(idRamenPoulet, group("g-ramen-spice", idRamenPoulet, "Piment", "辣度", 0, 1, 0,
		choice("c-ramen-doux", "g-ramen-spice", "Doux", "微辣", "0", 0),
		choice("c-ramen-fort", "g-ramen-spice", "Fort", "特辣", "0", 1)))
	return h
}

func TestRealMenuGetProductShowsChoiceGroups(t *testing.T) {
	h := newMenuChoicesHarness(t)

	poke := h.call("get_product", map[string]any{"product_id": idPokeSaumon})
	g := list(poke["choice_groups"])[0].(map[string]any)
	if g["names"].(map[string]any)["zh"] != "配料" || num(g["min_selections"]) != 5 || num(g["max_selections"]) != 8 || len(choicesOf(g)) != 8 {
		t.Errorf("composer: %v", g)
	}
	cs := choicesOf(g)
	if cs[0]["names"].(map[string]any)["fr"] != "Avocat" || len(cs[0]["names"].(map[string]any)) != 1 {
		t.Errorf("a choice named only in French: %v", cs[0])
	}
	if num(cs[7]["price_modifier_cents"]) != 200 {
		t.Errorf("premium ingredient: %v", cs[7])
	}
	// The product's own price is separate from the surcharges.
	if num(poke["price_cents"]) != 1580 {
		t.Errorf("price_cents = %v", poke["price_cents"])
	}

	m1 := list(h.call("get_product", map[string]any{"product_id": idPlateauM1})["choice_groups"])[0].(map[string]any)
	if m1["names"].(map[string]any)["zh"] != "汤" || num(choicesOf(m1)[1]["price_modifier_cents"]) != 100 {
		t.Errorf("platter soup: %v", m1)
	}
	ramen := list(h.call("get_product", map[string]any{"product_id": idRamenPoulet})["choice_groups"])[0].(map[string]any)
	if num(ramen["min_selections"]) != 0 || num(ramen["max_selections"]) != 1 {
		t.Errorf("optional spice level: %v", ramen)
	}
}

// Three products are called 三文鱼 (maki, sushi, sashimi) and carry the same
// "Sauce"/"Soja" options: only the ids tell them apart, and each summary names
// the product's category.
func TestRealMenuSameChoiceNamesInSeveralCategories(t *testing.T) {
	h := newMenuChoicesHarness(t)
	for _, tt := range []struct{ prefix, category string }{{"maki", "卷寿司「三文鱼」"}, {"sushi", "寿司「三文鱼」"}, {"sashimi", "刺身「三文鱼」"}} {
		r := h.call("propose_choice_deletion", map[string]any{"choice_id": "c-" + tt.prefix + "-soja"})
		zh := str(r["summary_zh"])
		if !strings.HasPrefix(zh, tt.category+"：") {
			t.Errorf("%s summary_zh = %q, want it to start with %q", tt.prefix, zh, tt.category)
		}
		if !strings.Contains(str(r["summary"]), "三文鱼") {
			t.Errorf("%s summary = %q", tt.prefix, r["summary"])
		}
	}
	// Deleting one product's choice leaves the two others' choices alone.
	r := h.call("propose_choice_deletion", map[string]any{"choice_id": "c-sushi-spicy"})
	h.apply(str(r["change_id"]))
	count := func(id string) int {
		return len(h.fake.ProductByID(id).ChoiceGroups[0].Choices)
	}
	if count(idSushiSaumon) != 1 || count(idMakiSaumon) != 2 || count(idSashimiSaumon) != 2 {
		t.Errorf("choices: sushi %d maki %d sashimi %d", count(idSushiSaumon), count(idMakiSaumon), count(idSashimiSaumon))
	}
}

func TestRealMenuPlatterSoupSurcharge(t *testing.T) {
	h := newMenuChoicesHarness(t)
	r := h.call("propose_choice_change", map[string]any{"choice_id": "c-m1-raviolis", "price_modifier_cents": 120})
	for _, want := range []string{"拼盘套餐「M1」", "的选项「饺子汤」", "加价：1.00 欧元 → 1.20 欧元"} {
		if !strings.Contains(str(r["summary_zh"]), want) {
			t.Errorf("summary_zh = %q, missing %q", r["summary_zh"], want)
		}
	}
	for _, want := range []string{`Menu plateau "M1"`, `choice "Soupe raviolis"`, "surcharge: 1.00 EUR -> 1.20 EUR"} {
		if !strings.Contains(str(r["summary"]), want) {
			t.Errorf("summary = %q, missing %q", r["summary"], want)
		}
	}
	// 1.00 -> 1.60 is +60%: beyond the guard.
	h.refused("propose_choice_change", map[string]any{"choice_id": "c-m1-raviolis", "price_modifier_cents": 160}, "Surcharge change refused")
	h.apply(str(r["change_id"]))
	if got := h.fake.ProductByID(idPlateauM1); got.Price != "17.90" && got.Price != "17.9" {
		t.Errorf("platter price = %s", got.Price)
	}
}

// "At least five ingredients": the composer's minimum is kept consistent with
// its maximum and its number of choices is untouched by selection rules.
func TestRealMenuComposerSelectionRules(t *testing.T) {
	h := newMenuChoicesHarness(t)
	r := h.call("propose_choice_group_change", map[string]any{"group_id": "g-poke-compose", "min_selections": 6})
	if !strings.Contains(str(r["summary_zh"]), "最少选择：5 → 6") || !strings.Contains(str(r["summary_zh"]), "夏威夷鱼生饭「三文鱼」") {
		t.Errorf("summary_zh = %q", r["summary_zh"])
	}
	h.apply(str(r["change_id"]))
	g := h.fake.ProductByID(idPokeSaumon).ChoiceGroups[0]
	if g.MinSelections != 6 || g.MaxSelections != 8 || len(g.Choices) != 8 {
		t.Errorf("group = %+v", g)
	}
	h.refused("propose_choice_group_change", map[string]any{"group_id": "g-poke-compose", "min_selections": 9}, "0 <= min <= max")
	h.refused("propose_choice_group_change", map[string]any{"group_id": "g-poke-compose", "max_selections": 5}, "0 <= min <= max")
	// Making the composer optional.
	r = h.call("propose_choice_group_change", map[string]any{"group_id": "g-poke-compose", "min_selections": 0})
	h.apply(str(r["change_id"]))
	if got := h.fake.ProductByID(idPokeSaumon).ChoiceGroups[0].MinSelections; got != 0 {
		t.Errorf("min = %d", got)
	}
}

func TestRealMenuAddOptionGroupToAProduct(t *testing.T) {
	h := newMenuChoicesHarness(t)
	// The sashimi has a sauce group; a new group is appended after it.
	r := h.call("propose_choice_group_change", map[string]any{"product_id": idSashimiSaumon, "names": map[string]any{"fr": "Accompagnement", "zh": "配菜"}, "min_selections": 0, "max_selections": 2})
	if !strings.Contains(str(r["summary_zh"]), "刺身「三文鱼」的新选项组") {
		t.Errorf("summary_zh = %q", r["summary_zh"])
	}
	h.apply(str(r["change_id"]))
	gs := h.fake.ProductByID(idSashimiSaumon).ChoiceGroups
	if len(gs) != 2 || gs[1].SortOrder != 1 || gs[1].MaxSelections != 2 {
		t.Fatalf("groups = %+v", gs)
	}
	// The same-named products keep theirs.
	for _, id := range []string{idMakiSaumon, idSushiSaumon} {
		if n := len(h.fake.ProductByID(id).ChoiceGroups); n != 1 {
			t.Errorf("%s has %d groups", id, n)
		}
	}
}

func TestRealMenuGroupDeletionNamesProductCategoryAndChoices(t *testing.T) {
	h := newMenuChoicesHarness(t)
	r := h.call("propose_choice_group_deletion", map[string]any{"group_id": "g-m1-soupe"})
	for _, want := range []string{"拼盘套餐「M1」：删除选项组「汤」及其 2 个选项（味噌汤、饺子汤）", "无法撤销"} {
		if !strings.Contains(str(r["summary_zh"]), want) {
			t.Errorf("summary_zh = %q, missing %q", r["summary_zh"], want)
		}
	}
	h.apply(str(r["change_id"]))
	if n := len(h.fake.ProductByID(idPlateauM1).ChoiceGroups); n != 0 {
		t.Errorf("groups = %d", n)
	}
	// The product is still there, with its price, and still found by search.
	if got := h.call("get_product", map[string]any{"product_id": idPlateauM1}); num(got["price_cents"]) != 1790 {
		t.Errorf("platter after deleting its soup: %v", got)
	}
}
