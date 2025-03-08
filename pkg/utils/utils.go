package utils

import (
	"context"
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
