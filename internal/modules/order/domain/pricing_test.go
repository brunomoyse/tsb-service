package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// TestPriceLine pins the line-pricing formula
//
//	lineTotal = base × qty + Σ(max(modifier, 0) × selectionQty)
//
// The same cases are asserted by tsb-core (layers/engine/utils/pricing.test.mjs)
// and tsb-mobile so the three implementations cannot drift apart.
func TestPriceLine(t *testing.T) {
	tests := []struct {
		name          string
		base          string
		qty           int64
		selections    []PricedSelection
		wantLineTotal string
		wantUnitPrice string
	}{
		{
			name:          "no selections",
			base:          "12.50",
			qty:           3,
			wantLineTotal: "37.50",
			wantUnitPrice: "12.50",
		},
		{
			name:          "qty 1 with a paid choice",
			base:          "12.00",
			qty:           1,
			selections:    []PricedSelection{{Modifier: d("1.50"), Quantity: 1}},
			wantLineTotal: "13.50",
			wantUnitPrice: "13.50",
		},
		{
			// 2 bowls need 2 broths (selection qty scaled with the line qty):
			// 12 × 2 + 1.50 × 2, not (12 + 1.50 × 2) × 2.
			name:          "qty 2 with a paid choice scaled to 2",
			base:          "12.00",
			qty:           2,
			selections:    []PricedSelection{{Modifier: d("1.50"), Quantity: 2}},
			wantLineTotal: "27.00",
			wantUnitPrice: "13.50",
		},
		{
			// 3 bowls: free broths and two paid multi-select ingredients.
			// 10 × 3 + 0 × 3 + 0.50 × 4 + 1.20 × 2 = 34.40
			name: "qty 3 with mixed multi-select ingredients",
			base: "10.00",
			qty:  3,
			selections: []PricedSelection{
				{Modifier: d("0.00"), Quantity: 3},
				{Modifier: d("0.50"), Quantity: 4},
				{Modifier: d("1.20"), Quantity: 2},
			},
			wantLineTotal: "34.40",
			// 34.40 / 3 = 11.4666… -> 11.47 (display only; total_price is authoritative)
			wantUnitPrice: "11.47",
		},
		{
			name:          "negative modifier is clamped to 0",
			base:          "12.00",
			qty:           2,
			selections:    []PricedSelection{{Modifier: d("-2.00"), Quantity: 2}, {Modifier: d("1.00"), Quantity: 1}},
			wantLineTotal: "25.00",
			wantUnitPrice: "12.50",
		},
		{
			// Legacy `choiceId` is scaled to the line quantity (see
			// legacyChoiceQuantity in the resolver), so it prices exactly like
			// selections:[{choice, quantity: qty}].
			name:          "legacy single choice, qty 3",
			base:          "8.00",
			qty:           3,
			selections:    []PricedSelection{{Modifier: d("0.50"), Quantity: 3}},
			wantLineTotal: "25.50",
			wantUnitPrice: "8.50",
		},
		{
			name:          "unit price rounds to cents but total stays exact",
			base:          "10.00",
			qty:           3,
			selections:    []PricedSelection{{Modifier: d("0.50"), Quantity: 1}},
			wantLineTotal: "30.50",
			wantUnitPrice: "10.17",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			total, unit := PriceLine(d(tt.base), tt.qty, tt.selections)
			if !total.Equal(d(tt.wantLineTotal)) {
				t.Errorf("lineTotal = %s, want %s", total, tt.wantLineTotal)
			}
			if !unit.Equal(d(tt.wantUnitPrice)) {
				t.Errorf("unitPrice = %s, want %s", unit, tt.wantUnitPrice)
			}
		})
	}
}

// TestPriceLine_OrderTotalIsSumOfLineTotals guards the property the order total,
// VAT and Mollie rely on: the order total is the sum of exact line totals, so
// rounding of unit_price never leaks into it.
func TestPriceLine_OrderTotalIsSumOfLineTotals(t *testing.T) {
	a, _ := PriceLine(d("10.00"), 3, []PricedSelection{{Modifier: d("0.50"), Quantity: 1}})
	b, _ := PriceLine(d("12.00"), 2, []PricedSelection{{Modifier: d("1.50"), Quantity: 2}})
	if got := a.Add(b); !got.Equal(d("57.50")) {
		t.Errorf("sum = %s, want 57.50", got)
	}
}
