package search

import (
	"reflect"
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
