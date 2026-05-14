package domain

// translationFallbackOrder returns the preferred language lookup order.
//
// French is always tried immediately after the requested language because
// FR is the authoring language for every product and is guaranteed to be
// present in the DB — a missing or empty NL/EN/ZH translation falls back
// to FR rather than to another partially-translated locale.
func translationFallbackOrder(language string) []string {
	switch language {
	case "fr":
		return []string{"fr", "en", "nl", "zh"}
	case "en":
		return []string{"en", "fr", "nl", "zh"}
	case "nl":
		return []string{"nl", "fr", "en", "zh"}
	case "zh":
		return []string{"zh", "fr", "en", "nl"}
	default:
		return []string{language, "fr", "en", "nl", "zh"}
	}
}
