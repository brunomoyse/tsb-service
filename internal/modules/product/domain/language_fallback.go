package domain

// translationFallbackOrder returns the preferred language lookup order.
//
// For Dutch requests, we explicitly use: nl -> en -> fr -> zh.
// For other languages, we still prefer the requested language first,
// then fall back to common menu languages in a stable order.
func translationFallbackOrder(language string) []string {
	switch language {
	case "nl":
		return []string{"nl", "en", "fr", "zh"}
	case "en":
		return []string{"en", "fr", "nl", "zh"}
	case "fr":
		return []string{"fr", "en", "nl", "zh"}
	case "zh":
		return []string{"zh", "en", "fr", "nl"}
	default:
		return []string{language, "en", "fr", "nl", "zh"}
	}
}
