package auth

import "testing"

// TestIsReviewAppleUser covers the Apple App Store reviewer matcher: it must
// recognize the "John Apple"/"John Appleseed" relay identity (so their order is
// flagged as a test order) while never matching a real customer.
func TestIsReviewAppleUser(t *testing.T) {
	cases := []struct {
		name                       string
		email, firstName, lastName string
		want                       bool
	}{
		{"reviewer John Apple", "ydgynb8c26@privaterelay.appleid.com", "John", "Apple", true},
		{"reviewer John Appleseed", "abc@privaterelay.appleid.com", "John", "Appleseed", true},
		{"case + whitespace tolerant", "ABC@Privaterelay.AppleID.com ", " john ", " apple ", true},
		{"right name but real email", "john@gmail.com", "John", "Apple", false},
		{"relay but different first name", "abc@privaterelay.appleid.com", "Jane", "Apple", false},
		{"relay but different last name", "abc@privaterelay.appleid.com", "John", "Doe", false},
		{"real customer named John on relay", "abc@privaterelay.appleid.com", "John", "Smith", false},
		{"empty", "", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsReviewAppleUser(tc.email, tc.firstName, tc.lastName); got != tc.want {
				t.Errorf("IsReviewAppleUser(%q,%q,%q) = %v, want %v",
					tc.email, tc.firstName, tc.lastName, got, tc.want)
			}
		})
	}
}
