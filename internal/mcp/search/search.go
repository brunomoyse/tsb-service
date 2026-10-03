// Package search ranks catalogue entities against a free-text query. It is
// case, accent and width insensitive, matches partial words and Chinese
// characters, and ranks exact and prefix matches first.
package search

import (
	"cmp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Match scores, highest first.
const (
	ScoreExact      = 100
	ScorePrefix     = 80
	ScoreWordPrefix = 60
	ScoreSubstring  = 40
	// ScoreSplit: a query without spaces made of part of a name and part of
	// its category, e.g. 三文鱼卷 for 三文鱼牛油果 filed under 加州卷.
	ScoreSplit      = 35
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
	// Pairs are (name, category) pairs for split matches (ScoreSplit).
	Pairs [][2]string
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
	// Exact is set when the query is exactly a primary field (a name, a
	// code, a category + name combination).
	Exact bool
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

// field is a candidate field prepared for matching.
type field struct {
	raw   string
	norm  string
	words []string
	runes int
	// compact is norm without spaces, for Chinese queries.
	compact string
}

func prepareField(s string) field {
	n := Normalize(s)
	return field{raw: s, norm: n, words: strings.Fields(n), runes: utf8.RuneCountInString(n), compact: strings.ReplaceAll(n, " ", "")}
}

// pair is a (name, category) pair, normalized without spaces.
type pair struct{ name, cat, label string }

type prepared struct {
	c                  *Candidate
	primary, secondary []field
	pairs              []pair
	sortKey            string
}

// Index is a candidate list prepared once for ranking many queries.
type Index struct{ cs []prepared }

// NewIndex prepares candidates for ranking.
func NewIndex(candidates []Candidate) *Index {
	ix := &Index{cs: make([]prepared, len(candidates))}
	nospace := func(s string) string { return strings.ReplaceAll(Normalize(s), " ", "") }
	for i := range candidates {
		c := &candidates[i]
		p := prepared{c: c, sortKey: Normalize(c.SortKey)}
		for _, f := range c.Primary {
			p.primary = append(p.primary, prepareField(f))
		}
		for _, f := range c.Secondary {
			p.secondary = append(p.secondary, prepareField(f))
		}
		for _, pr := range c.Pairs {
			if n, cat := nospace(pr[0]), nospace(pr[1]); n != "" && cat != "" {
				p.pairs = append(p.pairs, pair{name: n, cat: cat, label: pr[1] + " " + pr[0]})
			}
		}
		ix.cs[i] = p
	}
	return ix
}

// Rank returns the candidates matching query, best first, at most limit
// (limit <= 0 means no limit). An empty query matches nothing.
func Rank(query string, candidates []Candidate, limit int) []Result {
	return NewIndex(candidates).Rank(query, limit)
}

// Rank is Rank on a prepared index.
func (ix *Index) Rank(query string, limit int) []Result {
	q := Normalize(query)
	if q == "" {
		return nil
	}
	qTokens := strings.Fields(q)
	qRunes := utf8.RuneCountInString(q)
	qSplit := []rune(strings.ReplaceAll(q, " ", ""))
	// Chinese has no spaces between words: 龙卷 三文鱼 is 龙卷三文鱼.
	qCompact := ""
	if hasHan(qSplit) {
		qCompact = string(qSplit)
	}

	var out []*prepared
	var res []Result
	for i := range ix.cs {
		c := &ix.cs[i]
		best, matched := 0, ""
		for _, f := range c.primary {
			s := scoreField(q, qRunes, qTokens, f)
			if qCompact != "" && f.compact == qCompact {
				s = ScoreExact
			}
			if s > best {
				best, matched = s, f.raw
			}
		}
		exact := best == ScoreExact
		if best < ScoreSplit {
			for _, p := range c.pairs {
				if splitMatch(qSplit, p) {
					best, matched = ScoreSplit, p.label
					break
				}
			}
		}
		for _, f := range c.secondary {
			if s := scoreField(q, qRunes, qTokens, f) - SecondaryPenalty; s > best {
				best, matched = s, f.raw
			}
		}
		if best > 0 {
			out = append(out, c)
			res = append(res, Result{ID: c.c.ID, Score: best, Matched: matched, Exact: exact})
		}
	}

	idx := make([]int, len(out))
	for i := range idx {
		idx[i] = i
	}
	slices.SortStableFunc(idx, func(i, j int) int {
		if c := cmp.Compare(res[j].Score, res[i].Score); c != 0 {
			return c
		}
		if out[i].c.Preferred != out[j].c.Preferred {
			if out[i].c.Preferred {
				return -1
			}
			return 1
		}
		return strings.Compare(out[i].sortKey, out[j].sortKey)
	})
	if limit > 0 && len(idx) > limit {
		idx = idx[:limit]
	}
	ranked := make([]Result, len(idx))
	for k, i := range idx {
		ranked[k] = res[i]
	}
	return ranked
}

func scoreField(q string, qRunes int, qTokens []string, f field) int {
	if f.norm == "" {
		return 0
	}
	switch {
	case f.norm == q:
		return ScoreExact
	case strings.HasPrefix(f.norm, q):
		return ScorePrefix
	}
	if allTokens(qTokens, func(t string) bool { return anyWord(f.words, func(w string) bool { return strings.HasPrefix(w, t) }) }) {
		return ScoreWordPrefix
	}
	if strings.Contains(f.norm, q) || allTokens(qTokens, func(t string) bool { return strings.Contains(f.norm, t) }) {
		return ScoreSubstring
	}
	if f.runes <= qRunes+1 && fuzzyToken(q, []string{f.norm}) {
		return ScoreFuzzyWhole
	}
	if allTokens(qTokens, func(t string) bool { return fuzzyToken(t, f.words) }) {
		return ScoreFuzzy
	}
	return 0
}

// splitMatch reports whether the query, without spaces, is a part of the
// name (at least two characters) followed by a part of the category, or the
// reverse. Only Chinese queries are split: Chinese has no spaces between
// words, while Latin words already match one by one (see scoreField).
func splitMatch(qr []rune, p pair) bool {
	if len(qr) < 3 || !hasHan(qr) {
		return false
	}
	for i := 1; i < len(qr); i++ {
		a, b := string(qr[:i]), string(qr[i:])
		if (i >= 2 && strings.Contains(p.name, a) && strings.Contains(p.cat, b)) || (len(qr)-i >= 2 && strings.Contains(p.cat, a) && strings.Contains(p.name, b)) {
			return true
		}
	}
	return false
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
	return slices.ContainsFunc(words, pred)
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
		// One edit changes the length by at most one.
		if d := len(wr) - len(tr); d >= -1 && d <= 1 && levenshtein(tr, wr) <= 1 {
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
