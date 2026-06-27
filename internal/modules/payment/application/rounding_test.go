package application

import (
	"testing"

	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/shopspring/decimal"

	"tsb-service/pkg/money"
)

func line(value string) mollie.PaymentLines {
	return mollie.PaymentLines{TotalAmount: &mollie.Amount{Value: value, Currency: "EUR"}}
}

func TestRoundingCorrectionLine(t *testing.T) {
	t.Run("no correction when lines already sum to total", func(t *testing.T) {
		lines := []mollie.PaymentLines{line("20.00"), line("5.00"), line("-5.00")}
		corr, err := roundingCorrectionLine(decimal.RequireFromString("20.00"), lines)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if corr != nil {
			t.Fatalf("expected no correction, got %+v", corr)
		}
	})

	t.Run("positive correction when total exceeds line sum", func(t *testing.T) {
		// lines sum to 19.95, total snapped to 20.00 → +0.05 surcharge.
		lines := []mollie.PaymentLines{line("24.95"), line("-5.00")}
		corr, err := roundingCorrectionLine(decimal.RequireFromString("20.00"), lines)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if corr == nil {
			t.Fatal("expected a correction line")
		}
		if corr.Type != mollie.SurchargeLine {
			t.Fatalf("expected SurchargeLine, got %v", corr.Type)
		}
		if corr.TotalAmount.Value != "0.05" {
			t.Fatalf("expected 0.05, got %s", corr.TotalAmount.Value)
		}
	})

	t.Run("negative correction when line sum exceeds total", func(t *testing.T) {
		// lines sum to 20.05, total snapped to 20.00 → -0.05 discount.
		lines := []mollie.PaymentLines{line("25.05"), line("-5.00")}
		corr, err := roundingCorrectionLine(decimal.RequireFromString("20.00"), lines)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if corr == nil {
			t.Fatal("expected a correction line")
		}
		if corr.Type != mollie.DiscountProductLine {
			t.Fatalf("expected DiscountProductLine, got %v", corr.Type)
		}
		if corr.TotalAmount.Value != "-0.05" {
			t.Fatalf("expected -0.05, got %s", corr.TotalAmount.Value)
		}
	})

	t.Run("correction makes lines sum exactly to total", func(t *testing.T) {
		lines := []mollie.PaymentLines{line("24.93"), line("-5.00")}
		total := decimal.RequireFromString("20.00")
		corr, err := roundingCorrectionLine(total, lines)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if corr != nil {
			lines = append(lines, *corr)
		}
		sum := decimal.Zero
		for _, l := range lines {
			sum = sum.Add(decimal.RequireFromString(l.TotalAmount.Value))
		}
		if !sum.Equal(total) {
			t.Fatalf("lines sum %s != total %s", sum, total)
		}
	})
}

// TestRoundingCorrectionLineNearFullDiscount guards the heavy-discount path:
// when coupon + takeaway discounts drive the order total close to zero, the
// correction line must still (a) make the lines sum exactly to the charged
// total and (b) stay within a single rounding unit (0.10 €). These mirror the
// line set CreatePayment builds: raw product totals, snapped takeaway/coupon
// discounts (negative), and a transaction surcharge. The order total is snapped
// to 10 cents the same way OrderRepository.Save computes TotalPrice.
func TestRoundingCorrectionLineNearFullDiscount(t *testing.T) {
	tenCents := decimal.RequireFromString("0.10")

	cases := []struct {
		name     string
		products []string // raw product line totals
		takeaway string   // snapped takeaway discount (positive magnitude)
		coupon   string   // snapped coupon discount (positive magnitude)
		txFee    string   // transaction surcharge
	}{
		{
			name:     "coupon ~= subtotal, total lands on a clean dime",
			products: []string{"25.00"},
			takeaway: "0.00",
			coupon:   "24.90",
			txFee:    "0.00",
		},
		{
			name:     "combined discounts ~= subtotal with odd cents",
			products: []string{"30.00"},
			takeaway: "3.00",
			coupon:   "26.93",
			txFee:    "0.35",
		},
		{
			name:     "near-100% across multiple .x5 product lines",
			products: []string{"12.95", "17.05", "0.05"},
			takeaway: "0.00",
			coupon:   "29.90",
			txFee:    "0.40",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var lines []mollie.PaymentLines
			rawSum := decimal.Zero
			for _, p := range tc.products {
				lines = append(lines, line(p))
				rawSum = rawSum.Add(decimal.RequireFromString(p))
			}
			takeaway := decimal.RequireFromString(tc.takeaway)
			coupon := decimal.RequireFromString(tc.coupon)
			txFee := decimal.RequireFromString(tc.txFee)
			if takeaway.IsPositive() {
				lines = append(lines, line(takeaway.Neg().StringFixed(2)))
			}
			if coupon.IsPositive() {
				lines = append(lines, line(coupon.Neg().StringFixed(2)))
			}
			if txFee.IsPositive() {
				lines = append(lines, line(txFee.StringFixed(2)))
			}

			// TotalPrice as Save computes it: snap the raw composite to 10 cents.
			total := money.RoundToNearest10Cents(rawSum.Sub(takeaway).Sub(coupon).Add(txFee))

			corr, err := roundingCorrectionLine(total, lines)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if corr != nil {
				// The correction must never exceed one rounding unit; a larger
				// gap would mean a pricing/rounding bug, not a rounding artifact.
				mag := decimal.RequireFromString(corr.TotalAmount.Value).Abs()
				if mag.GreaterThan(tenCents) {
					t.Fatalf("correction %s exceeds one rounding unit (0.10)", corr.TotalAmount.Value)
				}
				lines = append(lines, *corr)
			}

			sum := decimal.Zero
			for _, l := range lines {
				sum = sum.Add(decimal.RequireFromString(l.TotalAmount.Value))
			}
			if !sum.Equal(total) {
				t.Fatalf("lines sum %s != charged total %s", sum, total)
			}
			if total.IsNegative() {
				t.Fatalf("charged total went negative: %s", total)
			}
		})
	}
}
