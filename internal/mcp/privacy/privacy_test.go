package privacy

import "testing"

func TestScrub(t *testing.T) {
	tests := map[string]string{
		"":                                       "",
		"sonnez au 3e, code 1234":                "sonnez au 3e, code 1234",
		"appelez-moi au 0470 12 34 56 svp":       "appelez-moi au " + Hidden + " svp",
		"+32 470 12 34 56":                       Hidden,
		"+32(0)470/12.34.56":                     Hidden,
		"0470-123456":                            Hidden,
		"tel 04 222 33 44":                       "tel " + Hidden,
		"电话13812345678，到了打电话":                    "电话" + Hidden + "，到了打电话",
		"mail: marie.dupont+sushi@example.co.uk": "mail: " + Hidden,
		"王丽 wangli@qq.com":                       "王丽 " + Hidden,
		"2 personnes, 19h30":                     "2 personnes, 19h30",
		"commande 2026-10-03":                    "commande 2026-10-03",
		"pour le 03/10/2026":                     "pour le 03/10/2026",
		"le 3.10.2026 à 19h":                     "le 3.10.2026 à 19h",
		"boîte 12, étage 3":                      "boîte 12, étage 3",
	}
	for in, want := range tests {
		if got := Scrub(in); got != want {
			t.Errorf("Scrub(%q) = %q, want %q", in, got, want)
		}
	}
}
