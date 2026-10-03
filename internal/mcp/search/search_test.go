package search

import (
	"reflect"
	"slices"
	"testing"
)

func TestNormalize(t *testing.T) {
	tests := map[string]string{
		"Crème Brûlée":    "creme brulee",
		"  SAUMON  ":      "saumon",
		"Maki-Saumon (6)": "maki saumon 6",
		"ＭＡＫＩ":            "maki",
		"三文鱼 寿司":          "三文鱼 寿司",
		"Œuf":             "œuf",
		"":                "",
	}
	for in, want := range tests {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func catalogue() []Candidate {
	return []Candidate{
		{ID: "maki-saumon", Primary: []string{"Maki saumon", "Salmon maki", "三文鱼卷"}, Secondary: []string{"Makis"}, Preferred: true, SortKey: "Maki saumon"},
		{ID: "saumon", Primary: []string{"Saumon", "Salmon", "三文鱼"}, Secondary: []string{"Sashimis"}, Preferred: true, SortKey: "Saumon"},
		{ID: "sashimi-saumon", Primary: []string{"Sashimi saumon", "Salmon sashimi", "三文鱼刺身"}, Secondary: []string{"Sashimis"}, Preferred: false, SortKey: "Sashimi saumon"},
		{ID: "creme", Primary: []string{"Crème brûlée", "Creme brulee", "焦糖布丁"}, Secondary: []string{"Desserts"}, Preferred: true, SortKey: "Crème brûlée"},
		{ID: "gyoza", Primary: []string{"Gyoza poulet", "Chicken gyoza", "鸡肉饺子", "G12"}, Secondary: []string{"Entrées"}, Preferred: true, SortKey: "Gyoza poulet"},
		{ID: "box", Primary: []string{"Maki box", "Maki box", "卷寿司套餐"}, Secondary: []string{"Boxes"}, Preferred: true, SortKey: "Maki box"},
	}
}

func ids(rs []Result) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.ID
	}
	return out
}

func TestRank(t *testing.T) {
	tests := []struct {
		name  string
		query string
		limit int
		want  []string
	}{
		{"exact beats prefix and substring", "saumon", 0, []string{"saumon", "maki-saumon", "sashimi-saumon"}},
		{"chinese exact", "三文鱼", 0, []string{"saumon", "maki-saumon", "sashimi-saumon"}},
		{"chinese partial", "刺身", 0, []string{"sashimi-saumon"}},
		{"chinese prefix", "饺子", 0, []string{"gyoza"}},
		{"accent folding", "creme brulee", 0, []string{"creme"}},
		{"accents in query", "CRÈME", 0, []string{"creme"}},
		{"partial word", "gyo", 0, []string{"gyoza"}},
		{"word order free", "saumon maki", 0, []string{"maki-saumon"}},
		{"code", "g12", 0, []string{"gyoza"}},
		{"typo", "saumom", 0, []string{"saumon", "maki-saumon", "sashimi-saumon"}},
		{"prefix ties sorted by availability then name", "maki", 0, []string{"box", "maki-saumon"}},
		{"category is secondary", "dessert", 0, []string{"creme"}},
		{"limit", "saumon", 1, []string{"saumon"}},
		{"no match", "pizza", 0, []string{}},
		{"empty query", "  ", 0, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ids(Rank(tt.query, catalogue(), tt.limit))
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Rank(%q) = %v, want %v", tt.query, got, tt.want)
			}
		})
	}
}

func TestRankPrefersAvailable(t *testing.T) {
	cs := []Candidate{
		{ID: "a", Primary: []string{"Maki thon"}, Preferred: false, SortKey: "a"},
		{ID: "b", Primary: []string{"Maki thon"}, Preferred: true, SortKey: "b"},
	}
	if got := ids(Rank("maki thon", cs, 0)); !reflect.DeepEqual(got, []string{"b", "a"}) {
		t.Errorf("got %v", got)
	}
}

func TestRankExactFlag(t *testing.T) {
	for _, tt := range []struct {
		query string
		exact map[string]bool
	}{
		{"saumon", map[string]bool{"saumon": true, "maki-saumon": false, "sashimi-saumon": false}},
		{"G12", map[string]bool{"gyoza": true}},
		// A secondary field (the category) is never exact.
		{"desserts", map[string]bool{"creme": false}},
		{"saumom", map[string]bool{"saumon": false}},
	} {
		for _, r := range Rank(tt.query, catalogue(), 0) {
			if want, ok := tt.exact[r.ID]; ok && r.Exact != want {
				t.Errorf("Rank(%q): %s exact %v, want %v", tt.query, r.ID, r.Exact, want)
			}
		}
	}
}

func TestSplitMatch(t *testing.T) {
	cs := []Candidate{
		{ID: "california", Primary: []string{"三文鱼牛油果"}, Pairs: [][2]string{{"三文鱼牛油果", "加州卷"}, {"Saumon avocat", "California roll"}}},
		{ID: "sushi", Primary: []string{"三文鱼"}, Pairs: [][2]string{{"三文鱼", "寿司"}}},
		{ID: "temaki", Primary: []string{"三文鱼牛油果黄瓜"}, Pairs: [][2]string{{"三文鱼牛油果黄瓜", "手卷"}}},
	}
	tests := []struct {
		query string
		want  []string
	}{
		{"三文鱼卷", []string{"california", "temaki"}},
		{"卷三文鱼", []string{"california", "temaki"}},
		{"加州卷三文鱼", []string{"california"}},
		{"牛油果手卷", []string{"temaki"}},
		{"三文鱼寿司", []string{"sushi"}},
		// Two characters cannot be split into a name part of two plus a
		// category part.
		{"鱼卷", []string{}},
		// Latin text is not split: words match one by one instead.
		{"saumonroll", []string{}},
		{"三文鱼披萨", []string{}},
	}
	for _, tt := range tests {
		got := ids(Rank(tt.query, cs, 0))
		slices.Sort(got)
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Rank(%q) = %v, want %v", tt.query, got, tt.want)
		}
		for _, r := range Rank(tt.query, cs, 0) {
			if r.Score == ScoreSplit && r.Exact {
				t.Errorf("Rank(%q): a split match is not exact", tt.query)
			}
		}
	}
}

func TestIndexMatchesRank(t *testing.T) {
	ix := NewIndex(catalogue())
	for _, q := range []string{"saumon", "三文鱼", "maki", "g12", "saumom", "pizza", ""} {
		if a, b := ix.Rank(q, 0), Rank(q, catalogue(), 0); !reflect.DeepEqual(a, b) {
			t.Errorf("%q: index %v, rank %v", q, a, b)
		}
	}
}

func TestRankChineseIgnoresSpaces(t *testing.T) {
	cs := []Candidate{
		{ID: "dragon", Primary: []string{"龙卷三文鱼", "Dragon Rolls saumon"}},
		{ID: "box", Primary: []string{"卷寿司套餐", "Maki box"}},
	}
	for _, tt := range []struct {
		query string
		exact string
	}{
		{"龙卷 三文鱼", "dragon"},
		{" 龙 卷 三 文 鱼 ", "dragon"},
		{"卷寿司 套餐", "box"},
	} {
		rs := Rank(tt.query, cs, 0)
		if len(rs) == 0 || rs[0].ID != tt.exact || !rs[0].Exact {
			t.Errorf("Rank(%q) = %+v, want %s exact first", tt.query, rs, tt.exact)
		}
	}
	// Latin words keep their spaces: "maki box" is exact, "makibox" is not.
	if rs := Rank("makibox", cs, 0); len(rs) > 0 && rs[0].Exact {
		t.Errorf("makibox matched exactly: %+v", rs)
	}
}
