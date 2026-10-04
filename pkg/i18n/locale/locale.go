// Package locale normalises the language tags that reach the backend
// (order.Language, Accept-Language-derived values, user preferences) to the
// small set of languages customer-facing content is written in.
//
// French is the owner-mandated fallback: whatever the input, Normalize always
// returns a supported language so callers can index their translation tables
// without handling a miss.
package locale

import "strings"

// Default is the fallback language for every customer-facing text.
const Default = "fr"

var supported = map[string]struct{}{
	"fr": {},
	"en": {},
	"nl": {},
	"zh": {},
}

// Normalize trims and lowercases tag, reduces a region or script variant to its
// base language ("fr-BE" -> "fr", "zh_Hans" -> "zh") and returns it when it is
// supported ("fr", "en", "nl" or "zh"). Anything else (empty, "de", garbage)
// yields Default.
func Normalize(tag string) string {
	base := strings.ToLower(strings.TrimSpace(tag))
	if i := strings.IndexAny(base, "-_"); i >= 0 {
		base = base[:i]
	}
	if _, ok := supported[base]; ok {
		return base
	}
	return Default
}
