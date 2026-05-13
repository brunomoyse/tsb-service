// Package money centralises monetary rounding rules applied to order totals.
//
// The restaurant's legacy POS rounds every customer-facing amount to 0,10 €
// (the last cent digit is always 0). We mirror that behaviour so the order
// total shown in the cart, charged via Mollie, printed on the receipt and
// stored in the database is identical across all surfaces.
package money

import "github.com/shopspring/decimal"

// RoundToNearest10Cents rounds d to the nearest 0,10 €. Inputs whose last
// cent digit is 0 stay unchanged. Digits 1–4 round down, digits 5–9 round up
// (so the 5-cent tie is always resolved in favour of the restaurant).
//
// Examples: 24.42 → 24.40, 24.45 → 24.50, 12.93 → 12.90, 12.95 → 13.00.
//
// The function is idempotent: applying it twice yields the same result.
func RoundToNearest10Cents(d decimal.Decimal) decimal.Decimal {
	cents := d.Mul(decimal.NewFromInt(100)).Round(0).IntPart()

	negative := cents < 0
	if negative {
		cents = -cents
	}

	last := cents % 10
	switch {
	case last == 0:
		// already on .x0
	case last <= 4:
		cents -= last
	default:
		cents += 10 - last
	}

	if negative {
		cents = -cents
	}

	return decimal.New(cents, -2)
}
