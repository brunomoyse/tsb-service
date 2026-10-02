package interfaces

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestInvoiceLineAmounts(t *testing.T) {
	d := decimal.RequireFromString

	t.Run("keeps quantity and unit price when they multiply back exactly", func(t *testing.T) {
		name, qty, unit := invoiceLineAmounts("Bowl", 2, d("13.50"), d("27.00"))
		if name != "Bowl" || qty != 2 || !unit.Equal(decimal.RequireFromString("13.50")) {
			t.Fatalf("got %q %d %s", name, qty, unit)
		}
	})

	t.Run("prints quantity 1 at the line total when the rounded unit price does not multiply back", func(t *testing.T) {
		// 10.00 × 3 + one 0.50 surcharge = 30.50; unit_price 10.17 × 3 = 30.51.
		name, qty, unit := invoiceLineAmounts("Bowl", 3, d("10.17"), d("30.50"))
		if name != "3 × Bowl" || qty != 1 || !unit.Equal(decimal.RequireFromString("30.50")) {
			t.Fatalf("got %q %d %s", name, qty, unit)
		}
		if !unit.Mul(decimal.NewFromInt(qty)).Equal(decimal.RequireFromString("30.50")) {
			t.Fatal("printed unit price × quantity must equal the line total")
		}
	})
}
