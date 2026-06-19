package application

import (
	"testing"

	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/shopspring/decimal"
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
