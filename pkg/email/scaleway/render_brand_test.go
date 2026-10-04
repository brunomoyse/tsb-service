package scaleway

import (
	"fmt"
	"html"
	"strings"
	"testing"

	userDomain "tsb-service/internal/modules/user/domain"
	"tsb-service/pkg/brand"
	"tsb-service/pkg/email/smtptest"
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

// TestRestaurantPhoneTemplateFunc verifies {{restaurantPhone}} end to end: the e-mails that tell the
// customer to call the restaurant (cancellation, refund, account linked) print the brand's own
// number in every language, HTML and text, so a second brand never shows the first one's phone.
func TestRestaurantPhoneTemplateFunc(t *testing.T) {
	const defaultPhone, otherPhone = "+32 4 222 98 88", "+32 4 000 00 00"
	user := sampleUser()
	sends := map[string]func(lang string) error{
		"order canceled": func(l string) error { return SendOrderCanceledEmail(user, l, testOrderID.String(), nil) },
		"refund issued":  func(l string) error { return SendRefundIssuedEmail(user, l, testOrderID.String(), "12,50") },
		"account linked": func(l string) error { return SendAccountLinkedEmail(user, l) },
	}
	for name, send := range sends {
		for _, lang := range allLangs {
			for phone, setup := range map[string]func(*testing.T){
				defaultPhone: func(t *testing.T) { brand.Load() },
				otherPhone: func(t *testing.T) {
					t.Cleanup(func() { brand.Load() })
					t.Setenv("RESTAURANT_PHONE", otherPhone)
					brand.Load()
				},
			} {
				t.Run(name+"/"+lang+"/"+phone, func(t *testing.T) {
					setup(t)
					srv := smtptest.Start(t)
					useSMTP(t, srv)
					if err := send(lang); err != nil {
						t.Fatal(err)
					}
					m := srv.Only(t)
					// html/template escapes "+" as &#43;; a mail client shows it as "+".
					for part, body := range map[string]string{"text": m.Text, "html": html.UnescapeString(m.HTML)} {
						if !strings.Contains(body, phone) {
							t.Errorf("%s body lacks %s", part, phone)
						}
						if phone == otherPhone && strings.Contains(body, defaultPhone) {
							t.Errorf("%s body still prints the default phone", part)
						}
					}
				})
			}
		}
	}
}
