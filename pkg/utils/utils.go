package utils

import (
	"context"
	"regexp"
	"strings"
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

// Slugify converts a given string into a URL-friendly slug.
func Slugify(s string) string {
	// Convert the string to lowercase and trim any surrounding whitespace.
	s = strings.ToLower(strings.TrimSpace(s))
	// Replace spaces (and underscores) with hyphens.
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.ReplaceAll(s, "_", "-")

	// Remove any character that is not a lowercase letter, number, or hyphen.
	reg, _ := regexp.Compile("[^a-z0-9-]+")
	s = reg.ReplaceAllString(s, "")

	// Replace multiple hyphens with a single hyphen.
	regHyphen, _ := regexp.Compile("-+")
	s = regHyphen.ReplaceAllString(s, "-")

	// Remove any leading or trailing hyphens.
	s = strings.Trim(s, "-")

	return s
}
