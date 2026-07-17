package scaleway

import (
	"fmt"
	"strings"
	"testing"

	userDomain "tsb-service/internal/modules/user/domain"
	"tsb-service/pkg/brand"
)

// TestWelcomeEmailBrandName verifies the {{restaurantName}} template function:
// default config renders the Tokyo Sushi Bar name, and a RESTAURANT_NAME env
// override flows through to every language variant, HTML and text alike.
func TestWelcomeEmailBrandName(t *testing.T) {
	user := userDomain.User{FirstName: "Jane", LastName: "Doe", Email: "jane@example.com"}
	langs := []string{"fr", "en", "nl", "zh"}

	render := func(t *testing.T, lang string) (string, string) {
		t.Helper()
		path := fmt.Sprintf("templates/%s/welcome", lang)
		html, err := renderWelcomeEmailHTML(path, user, "https://example.com/menu")
		if err != nil {
			t.Fatalf("render HTML (%s): %v", lang, err)
		}
		text, err := renderWelcomeEmailText(path, user, "https://example.com/menu")
		if err != nil {
			t.Fatalf("render text (%s): %v", lang, err)
		}
		return html, text
	}

	t.Run("default is Tokyo Sushi Bar", func(t *testing.T) {
		brand.Load()
		for _, lang := range langs {
			html, text := render(t, lang)
			if !strings.Contains(html, "Tokyo Sushi Bar") {
				t.Errorf("HTML (%s) missing default brand name", lang)
			}
			if !strings.Contains(text, "Tokyo Sushi Bar") {
				t.Errorf("text (%s) missing default brand name", lang)
			}
		}
	})

	t.Run("RESTAURANT_NAME override", func(t *testing.T) {
		// Registered before t.Setenv so LIFO cleanup order restores the env
		// var first, then reloads the default config.
		t.Cleanup(func() { brand.Load() })
		t.Setenv("RESTAURANT_NAME", "Sakura House")
		brand.Load()

		for _, lang := range langs {
			html, text := render(t, lang)
			for name, out := range map[string]string{"HTML": html, "text": text} {
				if strings.Contains(out, "Tokyo Sushi Bar") {
					t.Errorf("%s (%s) still contains default brand name", name, lang)
				}
				if !strings.Contains(out, "Sakura House") {
					t.Errorf("%s (%s) missing overridden brand name", name, lang)
				}
			}
		}
	})
}
