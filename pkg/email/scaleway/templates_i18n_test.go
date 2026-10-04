package scaleway

import (
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"testing"

	"tsb-service/pkg/i18n/locale"
)

// customerTemplates are the emails a customer receives, in every language the shops offer.
// (feedback is staff-facing English and assistant-disconnected staff-facing Chinese: not listed.)
var customerTemplates = []string{
	"account-linked", "login-otp", "order-canceled", "order-completed", "order-confirmed", "order-pending",
	"order-ready", "ready-time-updated", "reengagement", "refund-issued", "verify", "welcome",
}

var templateLanguages = []string{"fr", "nl", "en", "zh"}

// templateFields lists the data fields ({{.UserName}}, {{.Address.Postcode}}, ...) and template functions a template uses.
var templateFieldPattern = regexp.MustCompile(`\.([A-Z][A-Za-z.]*)|\b(restaurantName|restaurantPhone)\b`)

func templateFields(src string) []string {
	seen := map[string]bool{}
	for _, action := range regexp.MustCompile(`\{\{[^}]*\}\}`).FindAllString(src, -1) {
		for _, m := range templateFieldPattern.FindAllStringSubmatch(action, -1) {
			seen[m[1]+m[2]] = true
		}
	}
	fields := make([]string, 0, len(seen))
	for f := range seen {
		fields = append(fields, f)
	}
	slices.Sort(fields)
	return fields
}

// TestCustomerTemplatesExistInEveryLanguage: a missing language would silently fall back to French.
func TestCustomerTemplatesExistInEveryLanguage(t *testing.T) {
	if !slices.Equal(slices.Sorted(slices.Values(templateLanguages)), locale.Supported()) {
		t.Fatalf("templateLanguages %v must be exactly the supported languages %v", templateLanguages, locale.Supported())
	}
	for _, name := range customerTemplates {
		for _, lang := range templateLanguages {
			base := fmt.Sprintf("templates/%s/%s", lang, name)
			if _, err := loadHTMLTemplate(base); err != nil {
				t.Errorf("%s.html: %v", base, err)
			}
			if _, err := loadTextTemplate(base); err != nil {
				t.Errorf("%s.txt: %v", base, err)
			}
		}
	}
}

// TestCustomerTemplatesUseTheSameFields: every language of an email shows the same data (a field dropped from
// one translation — the order total, the address, the tracking link — would go unnoticed otherwise).
func TestCustomerTemplatesUseTheSameFields(t *testing.T) {
	for _, name := range customerTemplates {
		for _, ext := range []string{"html", "txt"} {
			fsys := fs.FS(htmlEmailFS)
			if ext == "txt" {
				fsys = textEmailFS
			}
			want := ""
			for _, lang := range templateLanguages {
				src, err := fs.ReadFile(fsys, fmt.Sprintf("templates/%s/%s.%s", lang, name, ext))
				if err != nil {
					t.Fatalf("%s/%s.%s: %v", lang, name, ext, err)
				}
				got := strings.Join(templateFields(string(src)), ",")
				if lang == "fr" {
					want = got
					continue
				}
				if got != want {
					t.Errorf("%s.%s: %s uses {%s}, fr uses {%s}", name, ext, lang, got, want)
				}
			}
		}
	}
}

// TestCustomerTemplatesAreBrandNeutral: one binary serves several restaurants (pkg/brand), so a template must not
// print one restaurant's phone number or a hard-coded discount rate.
func TestCustomerTemplatesAreBrandNeutral(t *testing.T) {
	forbidden := []string{"04 222 98 88", "-10%", "sushi", "お待ちしております"}
	for _, name := range customerTemplates {
		for _, lang := range templateLanguages {
			for _, ext := range []string{"html", "txt"} {
				fsys := fs.FS(htmlEmailFS)
				if ext == "txt" {
					fsys = textEmailFS
				}
				src, err := fs.ReadFile(fsys, fmt.Sprintf("templates/%s/%s.%s", lang, name, ext))
				if err != nil {
					t.Fatalf("%s/%s.%s: %v", lang, name, ext, err)
				}
				for _, f := range forbidden {
					if strings.Contains(strings.ToLower(string(src)), strings.ToLower(f)) {
						t.Errorf("%s/%s.%s contains %q", lang, name, ext, f)
					}
				}
			}
		}
	}
}
