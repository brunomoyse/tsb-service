package locale

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"fr", "fr"}, {"en", "en"}, {"nl", "nl"}, {"zh", "zh"},
		{"EN", "en"}, {"Fr", "fr"}, {"  nl  ", "nl"}, {"\tzh\n", "zh"},
		{"fr-BE", "fr"}, {"fr_FR", "fr"}, {"en-GB", "en"}, {"EN-us", "en"},
		{"nl-BE", "nl"}, {"zh-CN", "zh"}, {"zh-Hans", "zh"}, {"zh_TW", "zh"}, {"zh-Hant-HK", "zh"},
		{"de", "fr"}, {"de-DE", "fr"}, {"", "fr"}, {"   ", "fr"}, {"-", "fr"}, {"_en", "fr"},
		{"english", "fr"}, {"français", "fr"}, {"zho", "fr"}, {"%%", "fr"}, {"en us", "fr"},
	}
	for _, c := range cases {
		require.Equal(t, c.want, Normalize(c.in), "%q", c.in)
	}
}

func TestNormalizeAlwaysReturnsASupportedLanguage(t *testing.T) {
	for _, in := range []string{"", "x", "de", "ja-JP", "\x00", "zh-", "-fr"} {
		_, ok := supported[Normalize(in)]
		require.True(t, ok, "%q", in)
	}
	require.Equal(t, "fr", Default)
}
