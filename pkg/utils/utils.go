package utils

import (
	"context"
	"regexp"
	"strconv"
)

type contextKey string

const LangKey contextKey = "lang"

// SetLang stores the language in the context.
func SetLang(ctx context.Context, lang string) context.Context {
	return context.WithValue(ctx, LangKey, lang)
}

// GetLang retrieves the language from the context.
func GetLang(ctx context.Context) string {
	lang, _ := ctx.Value(LangKey).(string)
	if lang == "" {
		return "fr"
	}
	return lang
}

var (
	alphaRegexp = regexp.MustCompile(`^[A-Za-z]+`)
	numRegexp   = regexp.MustCompile(`\d+`)
)

// ParseCode takes a pointer to a code (e.g., "A10")
// and returns the alphabetical prefix (e.g., "A") and numeric part (10).
func ParseCode(code *string) (string, int) {
	if code == nil {
		// No code? Return empty alpha and 0 for the numeric part
		return "", 0
	}

	alpha := alphaRegexp.FindString(*code)
	numStr := numRegexp.FindString(*code)
	num := 0
	if numStr != "" {
		if n, err := strconv.Atoi(numStr); err == nil {
			num = n
		}
	}
	return alpha, num
}
