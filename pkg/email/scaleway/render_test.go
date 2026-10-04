package scaleway

import (
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	userDomain "tsb-service/internal/modules/user/domain"
)

func TestFormatUserName(t *testing.T) {
	cases := []struct {
		name string
		u    userDomain.User
		want string
	}{
		{"first and last", userDomain.User{FirstName: "Jeanne", LastName: "Dupont", Email: "j@x.test"}, "Jeanne Dupont"},
		{"first only has no trailing space", userDomain.User{FirstName: "Jeanne", Email: "j@x.test"}, "Jeanne"},
		{"last only has no leading space", userDomain.User{LastName: "Dupont", Email: "j@x.test"}, "Dupont"},
		{"whitespace-only names fall back to the e-mail local part", userDomain.User{FirstName: " ", LastName: "\t", Email: "social.only@x.test"}, "social.only"},
		{"no name at all uses the local part", userDomain.User{Email: "anna@x.test"}, "anna"},
		{"an address without @ is used whole", userDomain.User{Email: "weird"}, "weird"},
		{"a leading @ is not treated as a local part", userDomain.User{Email: "@x.test"}, "@x.test"},
		{"nothing known yields an empty name", userDomain.User{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { require.Equal(t, c.want, formatUserName(c.u)) })
	}
}

func TestFormatEstimatedReadyTime(t *testing.T) {
	ts := func(y int, m time.Month, d, h, min int) *time.Time {
		v := time.Date(y, m, d, h, min, 0, 0, time.UTC)
		return &v
	}
	cases := []struct {
		name string
		in   *time.Time
		lang string
		want string
	}{
		{"nil", nil, "fr", ""},
		// Summer (UTC+2): 17:30Z -> 19:30, Wednesday.
		{"fr summer", ts(2026, 7, 1, 17, 30), "fr", "mercredi 1 juillet 2026 à 19:30"},
		{"en summer", ts(2026, 7, 1, 17, 30), "en", "Wednesday, July 1, 2026 at 7:30 PM"},
		{"zh summer", ts(2026, 7, 1, 17, 30), "zh", "2026年7月1日 星期三 19:30"},
		{"nl summer", ts(2026, 7, 1, 17, 30), "nl", "woensdag 1 juli 2026 om 19:30"},
		// Winter (UTC+1): 11:00Z -> 12:00 (noon), Thursday 15 Jan.
		{"en noon is 12 PM", ts(2026, 1, 15, 11, 0), "en", "Thursday, January 15, 2026 at 12:00 PM"},
		{"en 00:30 local is 12:30 AM", ts(2026, 1, 15, 23, 30), "en", "Friday, January 16, 2026 at 12:30 AM"},
		{"en morning keeps AM", ts(2026, 1, 15, 8, 5), "en", "Thursday, January 15, 2026 at 9:05 AM"},
		// The local date, not the UTC date, is shown across midnight.
		{"fr rolls over to the next local day", ts(2026, 12, 31, 23, 30), "fr", "vendredi 1 janvier 2027 à 00:30"},
		{"fr sunday", ts(2026, 7, 5, 10, 0), "fr", "dimanche 5 juillet 2026 à 12:00"},
		{"zh sunday", ts(2026, 7, 5, 10, 0), "zh", "2026年7月5日 星期日 12:00"},
		{"nl sunday december", ts(2026, 12, 6, 10, 0), "nl", "zondag 6 december 2026 om 11:00"},
		{"unknown language defaults to French", ts(2026, 7, 1, 17, 30), "de", "mercredi 1 juillet 2026 à 19:30"},
		{"region tag reduces to base language", ts(2026, 7, 1, 17, 30), "en-GB", "Wednesday, July 1, 2026 at 7:30 PM"},
		{"nl-BE is Dutch", ts(2026, 7, 1, 17, 30), "nl-BE", "woensdag 1 juli 2026 om 19:30"},
		{"zh-Hans is Chinese", ts(2026, 7, 1, 17, 30), "zh-Hans", "2026年7月1日 星期三 19:30"},
		{"language is case-insensitive", ts(2026, 7, 1, 17, 30), " EN ", "Wednesday, July 1, 2026 at 7:30 PM"},
		{"fr-BE is French", ts(2026, 7, 1, 17, 30), "fr-BE", "mercredi 1 juillet 2026 à 19:30"},
		{"empty language defaults to French", ts(2026, 7, 1, 17, 30), "", "mercredi 1 juillet 2026 à 19:30"},
		{"fr minutes are zero padded", ts(2026, 8, 3, 7, 5), "fr", "lundi 3 août 2026 à 09:05"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { require.Equal(t, c.want, formatEstimatedReadyTime(c.in, c.lang)) })
	}
}

func TestFormatEstimatedReadyTimeMonthAndWeekdayNames(t *testing.T) {
	frMonths := []string{"janvier", "février", "mars", "avril", "mai", "juin", "juillet", "août", "septembre", "octobre", "novembre", "décembre"}
	nlMonths := []string{"januari", "februari", "maart", "april", "mei", "juni", "juli", "augustus", "september", "oktober", "november", "december"}
	enMonths := []string{"January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"}
	for i := range 12 {
		at := time.Date(2026, time.Month(i+1), 15, 10, 0, 0, 0, time.UTC)
		require.Contains(t, formatEstimatedReadyTime(&at, "fr"), " "+frMonths[i]+" ")
		require.Contains(t, formatEstimatedReadyTime(&at, "nl"), " "+nlMonths[i]+" ")
		require.Contains(t, formatEstimatedReadyTime(&at, "en"), enMonths[i]+" 15")
		require.Contains(t, formatEstimatedReadyTime(&at, "zh"), fmt.Sprintf("2026年%d月15日", i+1))
	}
}

func TestRenderEmailErrors(t *testing.T) {
	t.Run("unknown template", func(t *testing.T) {
		_, err := renderEmail("templates/en/nope", struct{}{}, loadHTMLTemplate)
		require.ErrorContains(t, err, "failed to parse HTML template")
		_, err = renderEmail("templates/en/nope", struct{}{}, loadTextTemplate)
		require.ErrorContains(t, err, "failed to parse text template")
	})

	t.Run("data missing a field the template needs", func(t *testing.T) {
		for _, loader := range []func(string) (templateExecutor, error){loadHTMLTemplate, loadTextTemplate} {
			_, err := renderEmail("templates/en/verify", struct{ Unrelated string }{"x"}, loader)
			require.ErrorContains(t, err, "failed to execute template")
		}
	})
}

// TestAllTemplatesParseAndPair checks the embedded template sets: every HTML
// has a text twin (and vice versa), all parse, and the four customer languages
// ship the same set of emails.
func TestAllTemplatesParseAndPair(t *testing.T) {
	collect := func(ext string, read func(string) ([]string, error)) map[string]map[string]bool {
		out := map[string]map[string]bool{}
		for _, lang := range allLangs {
			files, err := read("templates/" + lang)
			require.NoError(t, err)
			out[lang] = map[string]bool{}
			for _, f := range files {
				if strings.HasSuffix(f, ext) {
					out[lang][strings.TrimSuffix(f, ext)] = true
				}
			}
		}
		return out
	}
	readHTML := func(dir string) ([]string, error) { return readNames(htmlEmailFS, dir) }
	readText := func(dir string) ([]string, error) { return readNames(textEmailFS, dir) }
	html, text := collect(".html", readHTML), collect(".txt", readText)

	for _, lang := range allLangs {
		require.Equal(t, html[lang], text[lang], "%s: html and txt templates must pair up", lang)
		require.Equal(t, html["fr"], func() map[string]bool {
			m := map[string]bool{}
			for k := range html[lang] {
				if k != "feedback" && k != "assistant-disconnected" { // en-only / zh-only by design
					m[k] = true
				}
			}
			return m
		}(), "%s ships a different customer e-mail set than fr", lang)
		for name := range html[lang] {
			_, err := loadHTMLTemplate("templates/" + lang + "/" + name)
			require.NoError(t, err, "%s/%s html", lang, name)
			_, err = loadTextTemplate("templates/" + lang + "/" + name)
			require.NoError(t, err, "%s/%s txt", lang, name)
		}
	}
}

func readNames(fsys fs.ReadDirFS, dir string) ([]string, error) {
	entries, err := fsys.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names, nil
}
