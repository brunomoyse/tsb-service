package money

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestRoundToNearest10Cents_AllDigits(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// All ten last-digit cases at the same base.
		{"24.40", "24.40"},
		{"24.41", "24.40"},
		{"24.42", "24.40"},
		{"24.43", "24.40"},
		{"24.44", "24.40"},
		{"24.45", "24.50"}, // tie → up
		{"24.46", "24.50"},
		{"24.47", "24.50"},
		{"24.48", "24.50"},
		{"24.49", "24.50"},

		// Carry across whole euros.
		{"12.95", "13.00"},
		{"12.99", "13.00"},
		{"99.95", "100.00"},

		// Real cases from the Chinese POS receipt.
		{"2.68", "2.70"},
		{"24.42", "24.40"},

		// Already-rounded value is unchanged (idempotence).
		{"24.40", "24.40"},
		{"0.00", "0.00"},
		{"10.00", "10.00"},
	}

	for _, c := range cases {
		in := decimal.RequireFromString(c.in)
		want := decimal.RequireFromString(c.want)
		got := RoundToNearest10Cents(in)
		if !got.Equal(want) {
			t.Errorf("RoundToNearest10Cents(%s) = %s, want %s", c.in, got.String(), c.want)
		}
	}
}

func TestRoundToNearest10Cents_Idempotent(t *testing.T) {
	cases := []string{"24.42", "24.45", "12.93", "12.97", "0.03", "999.99"}
	for _, s := range cases {
		first := RoundToNearest10Cents(decimal.RequireFromString(s))
		second := RoundToNearest10Cents(first)
		if !first.Equal(second) {
			t.Errorf("not idempotent for %s: first=%s second=%s", s, first.String(), second.String())
		}
	}
}

func TestRoundToNearest10Cents_Negative(t *testing.T) {
	// Negative values (e.g. a discount represented as a negative amount on the
	// receipt) round symmetrically: -2.68 → -2.70.
	cases := []struct {
		in   string
		want string
	}{
		{"-2.68", "-2.70"},
		{"-2.42", "-2.40"},
		{"-2.45", "-2.50"},
		{"-0.01", "0.00"},
	}
	for _, c := range cases {
		got := RoundToNearest10Cents(decimal.RequireFromString(c.in))
		want := decimal.RequireFromString(c.want)
		if !got.Equal(want) {
			t.Errorf("RoundToNearest10Cents(%s) = %s, want %s", c.in, got.String(), c.want)
		}
	}
}

func TestRoundToNearest10Cents_SubCentInput(t *testing.T) {
	// Inputs with three or more decimals are first quantised to cents (HALF_UP)
	// before the 10-cent rounding kicks in. A raw 2.745 (10% of 27.45) becomes
	// 2.75 in cents → then rounds up to 2.80.
	got := RoundToNearest10Cents(decimal.RequireFromString("2.745"))
	want := decimal.RequireFromString("2.80")
	if !got.Equal(want) {
		t.Errorf("RoundToNearest10Cents(2.745) = %s, want %s", got.String(), want.String())
	}
}
