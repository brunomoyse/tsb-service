package hubrise

import (
	"errors"
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
)

// Currency is the only currency the integration handles (a Belgian restaurant).
const Currency = "EUR"

// ErrMoney is returned (wrapped) for a value that is not a HubRise amount, quantity, stock or tax
// rate.
var ErrMoney = errors.New("hubrise: malformed number")

// ErrCurrency is returned (wrapped) for an amount in a currency other than EUR.
var ErrCurrency = errors.New("hubrise: currency is not EUR")

// Money is a HubRise monetary value on the wire: an amount with at most two decimals, a space and
// the currency ("8.90 EUR", "-0.05 EUR"). It is kept as the wire string so that a payload decodes
// and re-encodes untouched; Decimal and Cents parse it, MoneyFromDecimal and MoneyFromCents build
// it. Floating point is never involved: the traps of float arithmetic (0.29 * 100) cannot occur.
type Money string

// String returns the wire form.
func (m Money) String() string { return string(m) }

// Decimal parses the amount.
func (m Money) Decimal() (decimal.Decimal, error) { return ParseMoney(string(m)) }

// Cents parses the amount as a whole number of euro cents.
func (m Money) Cents() (int64, error) {
	d, err := m.Decimal()
	if err != nil {
		return 0, err
	}
	return d.Shift(2).IntPart(), nil
}

// MoneyFromDecimal formats d with exactly two decimals (rounded half away from zero), "12.50 EUR".
func MoneyFromDecimal(d decimal.Decimal) Money {
	return Money(d.StringFixed(2) + " " + Currency)
}

// MoneyFromCents formats a whole number of cents, 1250 -> "12.50 EUR".
func MoneyFromCents(cents int64) Money {
	return MoneyFromDecimal(decimal.New(cents, -2))
}

// ParseMoney parses "12.50 EUR". The amount has an optional leading minus, digits, and at most two
// decimals ("12.5 EUR" and "12 EUR" are accepted); exponents, a plus sign, a decimal comma and a
// missing integer part are not. A currency other than EUR is ErrCurrency.
func ParseMoney(s string) (decimal.Decimal, error) {
	parts := strings.Fields(s)
	if len(parts) != 2 {
		return decimal.Zero, fmt.Errorf("%w: %q", ErrMoney, s)
	}
	if parts[1] != Currency {
		return decimal.Zero, fmt.Errorf("%w: %q", ErrCurrency, s)
	}
	d, ok := parseDecimal(parts[0], 2, true)
	if !ok {
		return decimal.Zero, fmt.Errorf("%w: %q", ErrMoney, s)
	}
	return d, nil
}

// ParseQuantity parses an order item quantity, a non-negative decimal string ("2", "1.0", "0.5").
func ParseQuantity(s string) (decimal.Decimal, error) {
	d, ok := parseDecimal(strings.TrimSpace(s), -1, false)
	if !ok {
		return decimal.Zero, fmt.Errorf("%w: quantity %q", ErrMoney, s)
	}
	return d, nil
}

// FormatQuantity formats a whole number of units as a HubRise quantity string.
func FormatQuantity(n int) string { return decimal.NewFromInt(int64(n)).String() }

// ParseStock parses an inventory stock: a non-negative decimal with at most three decimals.
func ParseStock(s string) (decimal.Decimal, error) {
	d, ok := parseDecimal(strings.TrimSpace(s), 3, false)
	if !ok {
		return decimal.Zero, fmt.Errorf("%w: stock %q", ErrMoney, s)
	}
	return d, nil
}

// FormatStock formats a stock value, without trailing zeros ("0", "2.5").
func FormatStock(d decimal.Decimal) string { return d.String() }

// ParseTaxRate parses a tax rate, a percent decimal between 0 and 100 ("6", "12.0", "21.00").
func ParseTaxRate(s string) (decimal.Decimal, error) {
	d, ok := parseDecimal(strings.TrimSpace(s), -1, false)
	if !ok || d.GreaterThan(decimal.NewFromInt(100)) {
		return decimal.Zero, fmt.Errorf("%w: tax rate %q (a percent between 0 and 100)", ErrMoney, s)
	}
	return d, nil
}

// FormatTaxRate formats a percent between 0 and 100 the way HubRise expects it: "6" for 6 %, not a
// ratio like 0.06. A value outside 0..100 is an error.
func FormatTaxRate(d decimal.Decimal) (string, error) {
	if d.IsNegative() || d.GreaterThan(decimal.NewFromInt(100)) {
		return "", fmt.Errorf("%w: tax rate %s (a percent between 0 and 100)", ErrMoney, d)
	}
	return d.String(), nil
}

// parseDecimal parses a plain decimal: digits with an optional single point and, when allowNeg, an
// optional leading minus. maxFrac < 0 means any number of decimals. shopspring/decimal accepts
// exponents and a plus sign, so the shape is checked here first.
func parseDecimal(s string, maxFrac int, allowNeg bool) (decimal.Decimal, bool) {
	digits := s
	if allowNeg {
		digits = strings.TrimPrefix(s, "-")
	}
	whole, frac, _ := strings.Cut(digits, ".")
	if whole == "" || !allDigits(whole) || !allDigits(frac) {
		return decimal.Zero, false
	}
	if strings.Contains(digits, ".") && frac == "" {
		return decimal.Zero, false
	}
	if maxFrac >= 0 && len(frac) > maxFrac {
		return decimal.Zero, false
	}
	d, err := decimal.NewFromString(s)
	return d, err == nil
}

func allDigits(s string) bool {
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
