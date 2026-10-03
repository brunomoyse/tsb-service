package tools_test

import (
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"tsb-service/internal/mcp/fakeupstream/realmenu"
	"tsb-service/internal/mcp/tools"
	"tsb-service/internal/mcp/upstream"
)

// The restaurant's real menu names many products alike: 三文鱼 alone is a
// maki, a sushi, a sashimi and a poke bowl, and about fifteen products are
// a "salmon roll". A product is identified by its category and name
// together. These tests run the product tools on that menu.

func newMenuHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.fake.Lock()
	h.fake.Categories, h.fake.Products = realmenu.Load()
	h.fake.Unlock()
	return h
}

type searchResult struct {
	matchCount, exact int
	note              string
	labels            []string // zh labels, best first
	exactLabels       []string
	ids               []string
}

func (h *harness) search(query string, limit int) searchResult {
	h.t.Helper()
	out := h.call("search_products", map[string]any{"query": query, "limit": limit})
	r := searchResult{matchCount: int(num(out["match_count"])), exact: int(num(out["exact_matches"])), note: str(out["note"])}
	for _, x := range list(out["results"]) {
		m := x.(map[string]any)
		label := str(m["labels"].(map[string]any)["zh"])
		r.labels = append(r.labels, label)
		r.ids = append(r.ids, str(m["product_id"]))
		if m["exact"] == true {
			r.exactLabels = append(r.exactLabels, label)
		}
	}
	return r
}

const (
	noteAsk   = "ask them"
	noteOne   = "Exactly one product"
	notePart  = "Only one product matches, partly"
	allResult = 50
)

func TestSearchRealMenuAmbiguity(t *testing.T) {
	h := newMenuHarness(t)
	salmonRolls := []string{
		"卷寿司 三文鱼", "卷寿司 辣三文鱼", "卷寿司 三文鱼芝士卷",
		"加州卷 三文鱼牛油果", "加州卷 三文鱼芝士", "加州卷 三文鱼芒果",
		"鱼子卷 三文鱼牛油果",
		"春卷 三文鱼牛油果", "春卷 三文鱼芝士", "春卷 三文鱼芒果",
		"特色卷 脆三文鱼", "特色卷 龙卷三文鱼", "特色卷 皇家三文鱼", "特色卷 辣三文鱼",
		"手卷 三文鱼牛油果黄瓜",
	}
	notRolls := []string{"寿司 三文鱼", "刺身 三文鱼", "夏威夷鱼生饭 三文鱼", "拼盘套餐 三文鱼拼盘", "东京热食 三文鱼串"}
	salmonEverywhere := []string{"卷寿司 三文鱼", "夏威夷鱼生饭 三文鱼", "刺身 三文鱼", "寿司 三文鱼"}
	salmonAvocado := []string{"加州卷 三文鱼牛油果", "军舰寿司 三文鱼牛油果", "鱼子卷 三文鱼牛油果", "春卷 三文鱼牛油果", "寿司 三文鱼牛油果"}

	tests := []struct {
		name, query string
		// exact lists every product named exactly, in any order.
		exact []string
		// include must be among the matches; exclude must not.
		include, exclude []string
		note             string
		minMatches       int
	}{
		// The same name in several categories: ask.
		{name: "chinese name in four categories", query: "三文鱼", exact: salmonEverywhere, note: noteAsk},
		{name: "french name in four categories", query: "saumon", exact: salmonEverywhere, note: noteAsk},
		{name: "english name in four categories", query: "Salmon", exact: salmonEverywhere, note: noteAsk},
		{name: "salmon avocado, five categories (zh)", query: "三文鱼牛油果", exact: salmonAvocado, note: noteAsk},
		{name: "salmon avocado, five categories (fr)", query: "saumon avocat", exact: salmonAvocado, note: noteAsk},
		{name: "salmon cheese, three categories", query: "三文鱼芝士", exact: []string{"加州卷 三文鱼芝士", "春卷 三文鱼芝士", "寿司 三文鱼芝士"}, note: noteAsk},
		{name: "tuna in four categories", query: "金枪鱼", exact: []string{"卷寿司 金枪鱼", "夏威夷鱼生饭 金枪鱼", "刺身 金枪鱼", "寿司 金枪鱼"}, note: noteAsk},
		{name: "eel in two categories", query: "鳗鱼", exact: []string{"卷寿司 鳗鱼", "寿司 鳗鱼"}, include: []string{"特色卷 鳗鱼卷"}, note: noteAsk},
		{name: "spicy salmon in two categories", query: "辣三文鱼", exact: []string{"卷寿司 辣三文鱼", "特色卷 辣三文鱼"}, note: noteAsk},

		// "Salmon roll" is a kind, not a product: every salmon roll in every
		// roll category matches, nothing else does, and none exactly.
		{name: "salmon roll (zh)", query: "三文鱼卷", include: salmonRolls, exclude: notRolls, note: noteAsk, minMatches: len(salmonRolls)},
		{name: "salmon roll (fr)", query: "saumon roll", include: []string{"卷寿司 三文鱼芝士卷", "加州卷 三文鱼牛油果", "春卷 三文鱼牛油果", "特色卷 皇家三文鱼"}, exclude: notRolls, note: noteAsk},
		{name: "salmon roll (en)", query: "salmon roll", include: []string{"加州卷 三文鱼牛油果", "春卷 三文鱼芝士", "鱼子卷 三文鱼牛油果"}, exclude: notRolls, note: noteAsk},
		{name: "tuna roll (zh)", query: "金枪鱼卷", include: []string{"卷寿司 金枪鱼", "加州卷 金枪鱼牛油果", "春卷 金枪鱼牛油果", "鱼子卷 金枪鱼芒果"}, exclude: []string{"寿司 金枪鱼", "刺身 金枪鱼"}, note: noteAsk},
		{name: "a dish kind without exact name", query: "可乐", include: []string{"饮品 可口可乐", "饮品 零度可乐"}, note: noteAsk},
		{name: "dragon rolls", query: "dragon", include: []string{"特色卷 龙卷三文鱼", "特色卷 龙卷虾"}, note: noteAsk},

		// Category + name in one language: exactly one product.
		{name: "zh category + name", query: "卷寿司三文鱼", exact: []string{"卷寿司 三文鱼"}, note: noteOne},
		{name: "zh name + category", query: "三文鱼卷寿司", exact: []string{"卷寿司 三文鱼"}, note: noteOne},
		{name: "zh with a space", query: "刺身 三文鱼", exact: []string{"刺身 三文鱼"}, note: noteOne},
		{name: "zh name + category, sashimi", query: "三文鱼刺身", exact: []string{"刺身 三文鱼"}, note: noteOne},
		{name: "zh spring roll", query: "春卷三文鱼牛油果", exact: []string{"春卷 三文鱼牛油果"}, note: noteOne},
		{name: "fr category + name", query: "maki saumon", exact: []string{"卷寿司 三文鱼"}, note: noteOne},
		{name: "fr name + category", query: "Saumon avocat spring roll", exact: []string{"春卷 三文鱼牛油果"}, note: noteOne},
		{name: "fr category + name, california", query: "California roll saumon cheese", exact: []string{"加州卷 三文鱼芝士"}, note: noteOne},
		{name: "en category + name", query: "Sashimi salmon tuna", exact: []string{"刺身 三文鱼金枪鱼"}, note: noteOne},
		{name: "fr without accents", query: "bento vegetarien", exact: []string{"便当套餐 素食便当"}, note: noteOne},

		// Chinese sentence, French category or name, in any order.
		{name: "fr category + zh name", query: "Maki 三文鱼", exact: []string{"卷寿司 三文鱼"}, note: noteOne},
		{name: "zh name + fr category", query: "三文鱼 maki", exact: []string{"卷寿司 三文鱼"}, note: noteOne},
		{name: "fr category glued to zh name", query: "Maki三文鱼", exact: []string{"卷寿司 三文鱼"}, note: noteOne},
		{name: "fr category + zh name, sashimi", query: "Sashimi 三文鱼", exact: []string{"刺身 三文鱼"}, note: noteOne},
		{name: "zh category + fr name", query: "春卷 saumon avocat", exact: []string{"春卷 三文鱼牛油果"}, note: noteOne},
		{name: "fr name + zh category", query: "saumon 刺身", exact: []string{"刺身 三文鱼"}, note: noteOne},
		{name: "fr category + zh name, california", query: "California roll 三文鱼芝士", exact: []string{"加州卷 三文鱼芝士"}, note: noteOne},
		{name: "fr category + zh name, poke", query: "Poke bowl 三文鱼", exact: []string{"夏威夷鱼生饭 三文鱼"}, note: noteOne},
		{name: "fr category + zh name, spicy", query: "maki 辣三文鱼", exact: []string{"卷寿司 辣三文鱼"}, note: noteOne},
		{name: "fr category + zh name, tuna", query: "thon 卷寿司", exact: []string{"卷寿司 金枪鱼"}, note: noteOne},
		{name: "fr category word + zh name", query: "Poke 三文鱼", include: []string{"夏威夷鱼生饭 三文鱼"}, note: notePart},

		// Unique names and codes.
		{name: "code", query: "B8", exact: []string{"卷寿司 三文鱼"}, note: noteOne},
		{name: "code, lower case", query: "i1", exact: []string{"刺身 三文鱼"}, note: noteOne},
		{name: "unique zh name", query: "毛豆", exact: []string{"配菜 毛豆"}, note: noteOne},
		{name: "unique fr name", query: "Edamame", exact: []string{"配菜 毛豆"}, note: noteOne},
		{name: "unique name with roll in it", query: "龙卷三文鱼", exact: []string{"特色卷 龙卷三文鱼"}, note: noteOne},
		{name: "exact name, related products partly match", query: "鳗鱼卷", exact: []string{"特色卷 鳗鱼卷"}, include: []string{"卷寿司 鳗鱼"}, note: noteOne},
		{name: "platter", query: "三文鱼拼盘", exact: []string{"拼盘套餐 三文鱼拼盘"}, note: noteOne},

		// Chinese has no word spaces: a space inside a name changes nothing.
		{name: "space inside a zh name", query: "龙卷 三文鱼", exact: []string{"特色卷 龙卷三文鱼"}, note: noteOne},
		{name: "spaces inside a zh name, five categories", query: "三文鱼 牛油果", exact: salmonAvocado, note: noteAsk},
		{name: "space inside a zh name with its category", query: "春卷 三文鱼 牛油果", exact: []string{"春卷 三文鱼牛油果"}, note: noteOne},
		{name: "zh category with a space inside", query: "夏威夷 鱼生饭 三文鱼", exact: []string{"夏威夷鱼生饭 三文鱼"}, note: noteOne},

		// Nothing.
		{name: "no match", query: "披萨", note: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h.t = t
			r := h.search(tt.query, allResult)
			got := slices.Clone(r.exactLabels)
			slices.Sort(got)
			want := slices.Clone(tt.exact)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("%q: exact matches %v, want %v (all: %v)", tt.query, got, want, r.labels)
			}
			if r.exact != len(tt.exact) {
				t.Errorf("%q: exact_matches %d, want %d", tt.query, r.exact, len(tt.exact))
			}
			for _, l := range tt.include {
				if !slices.Contains(r.labels, l) {
					t.Errorf("%q: %s missing from %v", tt.query, l, r.labels)
				}
			}
			for _, l := range tt.exclude {
				if slices.Contains(r.labels, l) {
					t.Errorf("%q: %s should not match", tt.query, l)
				}
			}
			if r.matchCount < tt.minMatches {
				t.Errorf("%q: %d matches, want at least %d", tt.query, r.matchCount, tt.minMatches)
			}
			if tt.note == "" && r.note != "" || !strings.Contains(r.note, tt.note) {
				t.Errorf("%q: note %q, want it to contain %q", tt.query, r.note, tt.note)
			}
			// Exact matches always rank first.
			for i, l := range r.labels[:len(r.exactLabels)] {
				if !slices.Contains(r.exactLabels, l) {
					t.Errorf("%q: result %d (%s) is not exact but ranks above an exact one", tt.query, i, l)
				}
			}
		})
	}
}

// Every product on the menu is named exactly, and alone, by its category +
// name in any language and any language mix, and by its code.
func TestSearchRealMenuEveryProductByCategoryAndName(t *testing.T) {
	_, products := realmenu.Load()
	catalogue := make([]upstream.Product, len(products))
	for i, p := range products {
		catalogue[i] = *p
	}
	name := func(p *upstream.Product, l string) string { return p.NameIn(l) }
	cat := func(p *upstream.Product, l string) string {
		for _, tr := range p.Category.Translations {
			if tr.Language == l {
				return tr.Name
			}
		}
		return p.Category.Name
	}
	ix := tools.NewProductIndex(catalogue)
	var checked atomic.Int64
	t.Cleanup(func() {
		if n := checked.Load(); n < 1900 {
			t.Errorf("only %d queries checked", n)
		}
	})
	for _, p := range products {
		t.Run(p.Slug, func(t *testing.T) {
			t.Parallel()
			queries := []string{
				cat(p, "zh") + name(p, "zh"),
				cat(p, "zh") + " " + name(p, "zh"),
				name(p, "zh") + cat(p, "zh"),
				cat(p, "fr") + " " + name(p, "fr"),
				name(p, "fr") + " " + cat(p, "fr"),
				cat(p, "en") + " " + name(p, "en"),
				cat(p, "fr") + " " + name(p, "zh"),
				name(p, "zh") + " " + cat(p, "fr"),
				cat(p, "zh") + " " + name(p, "fr"),
				strings.ToUpper(cat(p, "fr")) + " " + strings.ToLower(name(p, "fr")),
				// A space inside the Chinese name, as a model may write it.
				cat(p, "zh") + " " + spaced(name(p, "zh")),
			}
			if p.Code != nil {
				queries = append(queries, *p.Code)
			}
			for _, q := range queries {
				r := ix.Search(q, 5)
				if r.ExactMatches != 1 || len(r.Results) == 0 || r.Results[0].ProductID != p.ID || !r.Results[0].Exact {
					var got []string
					for _, m := range r.Results {
						got = append(got, m.Labels["zh"])
					}
					t.Errorf("%q: exact_matches %d, results %v, want only %s", q, r.ExactMatches, got, cat(p, "zh")+" "+name(p, "zh"))
				}
				if !strings.Contains(r.Note, noteOne) {
					t.Errorf("%q: note %q", q, r.Note)
				}
				checked.Add(1)
			}
		})
	}
}

// Labels are unique on the menu in every language: category + name is a
// sufficient identity.
func TestRealMenuLabelsAreUnique(t *testing.T) {
	h := newMenuHarness(t)
	_, products := realmenu.Load()
	for _, lang := range []string{"zh", "fr", "en"} {
		seen := map[string]string{}
		for _, p := range products {
			out := h.call("get_product", map[string]any{"product_id": p.ID})
			label := str(out["labels"].(map[string]any)[lang])
			if label == "" {
				t.Fatalf("%s: no %s label", p.ID, lang)
			}
			if other, dup := seen[label]; dup {
				t.Errorf("%s label %q names %s and %s", lang, label, other, p.ID)
			}
			seen[label] = p.ID
		}
	}
}

func TestProductOutputsCarryCategory(t *testing.T) {
	h := newMenuHarness(t)
	r := h.call("search_products", map[string]any{"query": "B8"})
	got := list(r["results"])[0].(map[string]any)
	labels := got["labels"].(map[string]any)
	cats := got["category_names"].(map[string]any)
	if labels["zh"] != "卷寿司 三文鱼" || labels["fr"] != "Maki Saumon" || labels["en"] != "Maki Salmon" || cats["zh"] != "卷寿司" || cats["fr"] != "Maki" {
		t.Errorf("labels %v, category names %v", labels, cats)
	}
	if !strings.Contains(str(r["note"]), "卷寿司 三文鱼") {
		t.Errorf("note %q does not name the product by category + name", r["note"])
	}
}

func TestSummariesNameCategoryAndProduct(t *testing.T) {
	h := newMenuHarness(t)
	ids := map[string]string{}
	for _, q := range []string{"B8", "I1", "A1", "R5"} {
		ids[q] = h.search(q, 1).ids[0]
	}
	tests := []struct {
		code, zh, fr string
	}{
		{"B8", "卷寿司「三文鱼」价格：5.50 欧元 → 5.70 欧元", `Maki "Saumon" (卷寿司「三文鱼」): price 5.50 EUR -> 5.70 EUR`},
		{"I1", "刺身「三文鱼」价格：9.80 欧元 → 10.00 欧元", `Sashimi "Saumon" (刺身「三文鱼」): price 9.80 EUR -> 10.00 EUR`},
		{"A1", "寿司「三文鱼」价格：2.10 欧元 → 2.30 欧元", `Sushi "Saumon" (寿司「三文鱼」): price 2.10 EUR -> 2.30 EUR`},
		{"R5", "夏威夷鱼生饭「三文鱼」价格：15.80 欧元 → 16.00 欧元", `Poke bowl "Saumon" (夏威夷鱼生饭「三文鱼」): price 15.80 EUR -> 16.00 EUR`},
	}
	for _, tt := range tests {
		cents := map[string]int{"B8": 570, "I1": 1000, "A1": 230, "R5": 1600}[tt.code]
		out := h.call("propose_price_change", map[string]any{"product_id": ids[tt.code], "new_price_cents": cents, "request_context": "test"})
		if str(out["summary_zh"]) != tt.zh || str(out["summary"]) != tt.fr {
			t.Errorf("%s: summary_zh %q, summary %q", tt.code, out["summary_zh"], out["summary"])
		}
	}

	// Sold out acts at once; its summary names the category too.
	out := h.call("set_product_availability", map[string]any{"product_id": ids["I1"], "available": false, "request_context": "test"})
	if !strings.HasPrefix(str(out["summary_zh"]), "刺身「三文鱼」：") {
		t.Errorf("availability summary_zh %q", out["summary_zh"])
	}
}

func TestOrderItemsNameCategoryAndProduct(t *testing.T) {
	h := newMenuHarness(t)
	_, products := realmenu.Load()
	var maki *upstream.Product
	for _, p := range products {
		if p.Code != nil && *p.Code == "B8" {
			maki = p
		}
	}
	h.fake.Lock()
	o := h.fake.Orders[0]
	o.Items[0].Product = &struct {
		ID           string                 `json:"id"`
		Name         string                 `json:"name"`
		Category     upstream.CategoryRef   `json:"category"`
		Translations []upstream.Translation `json:"translations"`
	}{ID: maki.ID, Name: maki.Name, Category: maki.Category, Translations: maki.Translations}
	h.fake.Unlock()
	out := h.call("get_order", map[string]any{"order_id": o.ID})
	item := list(out["items"])[0].(map[string]any)
	labels, _ := item["product_labels"].(map[string]any)
	if item["product"] != "Saumon" || labels["zh"] != "卷寿司 三文鱼" || labels["fr"] != "Maki Saumon" {
		t.Errorf("item %v", item)
	}
}

func TestSearchNoteLimitDoesNotHideAmbiguity(t *testing.T) {
	h := newMenuHarness(t)
	r := h.search("三文鱼卷", 3)
	if len(r.ids) != 3 || r.matchCount < 15 || !strings.Contains(r.note, fmt.Sprintf("%d products match", r.matchCount)) {
		t.Errorf("limit 3: %d results, match_count %d, note %q", len(r.ids), r.matchCount, r.note)
	}
}

// spaced puts a space after the second character of a Chinese name of
// four characters or more: 龙卷三文鱼 becomes 龙卷 三文鱼.
func spaced(s string) string {
	r := []rune(s)
	if len(r) < 4 {
		return s
	}
	return string(r[:2]) + " " + string(r[2:])
}
