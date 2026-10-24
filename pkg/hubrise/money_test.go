package hubrise

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseMoneyDocumentedFormats(t *testing.T) {
	cases := map[string]int64{
		"8.90 EUR":     890,
		"-0.05 EUR":    -5,
		"0.00 EUR":     0,
		"80000.00 EUR": 8_000_000,
		"12.5 EUR":     1250,
		"12 EUR":       1200,
		"007.10 EUR":   710,
		"9.80  EUR":    980,
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			got, err := Money(in).Cents()
			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}
}

// 0.29 * 100 is 28.999999999999996 in float64: money never goes through a float.
func TestMoneyFloatTrapsAreExact(t *testing.T) {
	for in, want := range map[string]int64{"0.29 EUR": 29, "1.15 EUR": 115, "4.35 EUR": 435, "0.57 EUR": 57, "8.20 EUR": 820} {
		got, err := Money(in).Cents()
		require.NoError(t, err)
		assert.Equal(t, want, got, in)
	}
	sum := decimal.Zero
	for range 10 {
		d, err := ParseMoney("0.10 EUR")
		require.NoError(t, err)
		sum = sum.Add(d)
	}
	assert.Equal(t, Money("1.00 EUR"), MoneyFromDecimal(sum), "ten times 0.10 is exactly 1.00")
}

func TestParseMoneyRejects(t *testing.T) {
	for _, bad := range []string{
		"", "EUR", "9.80", "abc EUR", "9.805 EUR", "9,80 EUR", "1.2.3 EUR", ".50 EUR", "9.80 EUR x",
		"+9.80 EUR", "- 9.80 EUR", "--9.80 EUR", "1e3 EUR", "0x10 EUR", "9. EUR", "NaN EUR", "9.80EUR", "- EUR",
	} {
		_, err := ParseMoney(bad)
		require.Error(t, err, "%q accepted", bad)
		assert.ErrorIs(t, err, ErrMoney, bad)
	}
	_, err := ParseMoney("9.80 USD")
	assert.ErrorIs(t, err, ErrCurrency)
	_, err = Money("nope").Cents()
	assert.Error(t, err)
}

func TestMoneyFormatsAndRoundTrips(t *testing.T) {
	assert.Equal(t, Money("12.50 EUR"), MoneyFromCents(1250))
	assert.Equal(t, Money("0.05 EUR"), MoneyFromCents(5))
	assert.Equal(t, Money("-0.05 EUR"), MoneyFromCents(-5))
	assert.Equal(t, Money("0.00 EUR"), MoneyFromCents(0))
	assert.Equal(t, "12.50 EUR", MoneyFromCents(1250).String())
	for _, c := range []int64{-12345, -1, 0, 1, 99, 100, 101, 987654} {
		got, err := MoneyFromCents(c).Cents()
		require.NoError(t, err)
		assert.Equal(t, c, got)
	}
	assert.Equal(t, Money("9.00 EUR"), MoneyFromDecimal(decimal.RequireFromString("9")))
	assert.Equal(t, Money("0.67 EUR"), MoneyFromDecimal(decimal.RequireFromString("0.665")), "rounded to the cent")
}

func TestQuantity(t *testing.T) {
	for in, want := range map[string]string{"2": "2", "2.0": "2", "1.00": "1", "0.5": "0.5", " 3 ": "3"} {
		d, err := ParseQuantity(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, d.String())
	}
	for _, bad := range []string{"", "x", "-1", "1e2", "+1", "1.", ".5", "1,5"} {
		_, err := ParseQuantity(bad)
		assert.ErrorIs(t, err, ErrMoney, bad)
	}
	assert.Equal(t, "3", FormatQuantity(3))
}

func TestStock(t *testing.T) {
	for in, want := range map[string]string{"0": "0", "2": "2", "2.5": "2.5", "2.500": "2.5", "0.001": "0.001"} {
		d, err := ParseStock(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, FormatStock(d))
	}
	for _, bad := range []string{"", "-1", "1.2345", "x", "1e3"} {
		_, err := ParseStock(bad)
		assert.ErrorIs(t, err, ErrMoney, bad)
	}
}

func TestTaxRateIsAPercent(t *testing.T) {
	for in, want := range map[string]string{"6": "6", "12.0": "12", "21.00": "21", "5.5": "5.5", "0": "0", "100": "100", "12.000": "12"} {
		d, err := ParseTaxRate(in)
		require.NoError(t, err, in)
		out, err := FormatTaxRate(d)
		require.NoError(t, err)
		assert.Equal(t, want, out)
	}
	for _, bad := range []string{"", "x", "-1", "100.01", "101", "0.06e2"} {
		_, err := ParseTaxRate(bad)
		assert.Error(t, err, bad)
	}
	// A ratio like 0.06 is a valid decimal but would mean 0.06 %: callers must send percents.
	d, err := ParseTaxRate("0.06")
	require.NoError(t, err)
	assert.True(t, d.LessThan(decimal.NewFromInt(1)))

	_, err = FormatTaxRate(decimal.NewFromInt(-1))
	assert.Error(t, err)
	_, err = FormatTaxRate(decimal.NewFromInt(101))
	assert.Error(t, err)
}
