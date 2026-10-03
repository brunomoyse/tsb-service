package auth

import "testing"

// TestIsReviewUser covers the store-review matcher: only an allowlisted
// Zitadel sub qualifies, never self-editable profile data.
func TestIsReviewUser(t *testing.T) {
	t.Setenv("REVIEW_ZITADEL_SUBS", " 3712345 , 3799999,,")

	sub := func(s string) *string { return &s }
	cases := []struct {
		name string
		sub  *string
		want bool
	}{
		{"allowlisted sub", sub("3712345"), true},
		{"second allowlisted sub", sub("3799999"), true},
		{"unknown sub", sub("3700000"), false},
		{"empty sub", sub(""), false},
		{"no sub (e.g. legacy row)", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsReviewUser(tc.sub); got != tc.want {
				t.Errorf("IsReviewUser = %v, want %v", got, tc.want)
			}
		})
	}
}
