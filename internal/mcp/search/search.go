// Package search ranks catalogue entities against a free-text query. It is
// case, accent and width insensitive, matches partial words and Chinese
// characters, and ranks exact and prefix matches first.
package search

import (
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Match scores, highest first.
const (
	ScoreExact      = 100
	ScorePrefix     = 80
	ScoreWordPrefix = 60
	ScoreSubstring  = 40
	ScoreFuzzyWhole = 30
	ScoreFuzzy      = 20
	// SecondaryPenalty is subtracted for matches on secondary fields such as
	// the category name, so a product named "Saumon" beats one merely filed
	// under a "Saumon" category.
	SecondaryPenalty = 15
)

// Candidate is one searchable entity.
type Candidate struct {
	ID string
	// Primary fields: names in every language, product code.
	Primary []string
	// Secondary fields: category name, etc.
	Secondary []string
	// Preferred breaks ties (e.g. available products first).
	Preferred bool
	// SortKey breaks remaining ties alphabetically.
	SortKey string
}

// Result is a ranked match.
type Result struct {
	ID    string
	Score int
	// Matched is the original field value that produced the best score.
	Matched string
}

// Normalize folds case, accents and full-width forms, and replaces
// punctuation with spaces. CJK characters are kept as-is.
func Normalize(s string) string {
	var b strings.Builder
	space := true
	for _, r := range norm.NFKD.String(s) {
		switch {
		case unicode.Is(unicode.Mn, r):
			continue
		case unicode.IsLetter(r) || unicode.IsNumber(r):
			b.WriteRune(unicode.ToLower(r))
			space = false
		default:
			if !space {
				b.WriteByte(' ')
				space = true
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// Rank returns the candidates matching query, best first, at most limit
// (limit <= 0 means no limit). An empty query matches nothing.
func Rank(query string, candidates []Candidate, limit int) []Result {
	q := Normalize(query)
	if q == "" {
		return nil
	}
	qTokens := strings.Fields(q)

	type scored struct {
		Result
		c *Candidate
	}
	var out []scored
	for i := range candidates {
		c := &candidates[i]
		best, matched := 0, ""
		for _, f := range c.Primary {
			if s := scoreField(q, qTokens, f); s > best {
				best, matched = s, f
			}
		}
		for _, f := range c.Secondary {
			if s := scoreField(q, qTokens, f) - SecondaryPenalty; s > best {
				best, matched = s, f
			}
		}
		if best > 0 {
			out = append(out, scored{Result{ID: c.ID, Score: best, Matched: matched}, c})
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].c.Preferred != out[j].c.Preferred {
			return out[i].c.Preferred
		}
		return Normalize(out[i].c.SortKey) < Normalize(out[j].c.SortKey)
	})

	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	res := make([]Result, len(out))
	for i, s := range out {
		res[i] = s.Result
	}
	return res
}

func scoreField(q string, qTokens []string, field string) int {
	f := Normalize(field)
	if f == "" {
		return 0
	}
	switch {
	case f == q:
		return ScoreExact
	case strings.HasPrefix(f, q):
		return ScorePrefix
	}
	words := strings.Fields(f)
	if allTokens(qTokens, func(t string) bool { return anyWord(words, func(w string) bool { return strings.HasPrefix(w, t) }) }) {
		return ScoreWordPrefix
	}
	if strings.Contains(f, q) || allTokens(qTokens, func(t string) bool { return strings.Contains(f, t) }) {
		return ScoreSubstring
	}
	if fuzzyToken(q, []string{f}) && len([]rune(f)) <= len([]rune(q))+1 {
		return ScoreFuzzyWhole
	}
	if allTokens(qTokens, func(t string) bool { return fuzzyToken(t, words) }) {
		return ScoreFuzzy
	}
	return 0
}

func allTokens(tokens []string, pred func(string) bool) bool {
	for _, t := range tokens {
		if !pred(t) {
			return false
		}
	}
	return len(tokens) > 0
}

func anyWord(words []string, pred func(string) bool) bool {
	for _, w := range words {
		if pred(w) {
			return true
		}
	}
	return false
}

// fuzzyToken tolerates one typo for Latin tokens of at least 4 letters,
// against a whole word or the same-length prefix of a longer word.
func fuzzyToken(t string, words []string) bool {
	tr := []rune(t)
	if len(tr) < 4 || hasHan(tr) {
		return false
	}
	for _, w := range words {
		wr := []rune(w)
		if levenshtein(tr, wr) <= 1 {
			return true
		}
		if len(wr) > len(tr) && levenshtein(tr, wr[:len(tr)]) <= 1 {
			return true
		}
	}
	return false
}

func hasHan(rs []rune) bool {
	for _, r := range rs {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

func levenshtein(a, b []rune) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
