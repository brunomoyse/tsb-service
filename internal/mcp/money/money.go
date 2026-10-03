// Package money converts between integer cents (the MCP contract) and the
// decimal strings tsb-service uses for every amount ("12.5", "12.50").
package money

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
)

// Currency is the only currency tsb-service handles.
const Currency = "EUR"

var hundred = decimal.NewFromInt(100)

// ToCents parses an upstream decimal string into integer cents. It rejects
// values with more than two decimals instead of rounding them silently.
func ToCents(s string) (int64, error) {
	d, err := decimal.NewFromString(s)
	if err != nil {
		return 0, fmt.Errorf("invalid amount %q: %w", s, err)
	}
	c := d.Mul(hundred)
	if !c.Equal(c.Truncate(0)) {
		return 0, fmt.Errorf("amount %q has more than two decimals", s)
	}
	return c.IntPart(), nil
}

// MustCents is ToCents for values already validated upstream. A malformed
// value maps to 0 so a read never fails on one bad row.
func MustCents(s string) int64 {
	c, err := ToCents(s)
	if err != nil {
		return 0
	}
	return c
}

// FromCents formats cents as the decimal string tsb-service accepts ("12.50").
func FromCents(cents int64) string {
	return decimal.New(cents, -2).StringFixed(2)
}

// Format renders cents for a human summary, e.g. "12.50 EUR".
func Format(cents int64) string {
	return FromCents(cents) + " " + Currency
}

// ErrOutOfBounds is returned by CheckPriceChange.
var ErrOutOfBounds = errors.New("price out of bounds")

// CheckPriceChange enforces the price guardrail: strictly positive, and within
// ±maxPct percent of the current price. The error message states the allowed
// range so the agent can relay it to the owner.
func CheckPriceChange(currentCents, newCents int64, maxPct int) error {
	if newCents <= 0 {
		return fmt.Errorf("%w: the new price must be greater than 0", ErrOutOfBounds)
	}
	if currentCents <= 0 {
		return nil
	}
	minAllowed, maxAllowed := Bounds(currentCents, maxPct)
	if newCents < minAllowed || newCents > maxAllowed {
		return fmt.Errorf("%w: a price change is limited to ±%d%% of the current price %s, so the new price must be between %s and %s",
			ErrOutOfBounds, maxPct, Format(currentCents), Format(minAllowed), Format(maxAllowed))
	}
	return nil
}

// Bounds returns the inclusive [min, max] cents allowed around current.
// The allowed delta is truncated to whole cents, so both bounds stay within
// the percentage.
func Bounds(currentCents int64, maxPct int) (int64, int64) {
	delta := currentCents * int64(maxPct) / 100
	lo, hi := currentCents-delta, currentCents+delta
	if lo < 1 {
		lo = 1
	}
	return lo, hi
}
