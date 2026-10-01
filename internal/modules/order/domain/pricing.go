package domain

import "github.com/shopspring/decimal"

// PricedSelection is one selected product choice with its price modifier, as
// used by PriceLine. Quantity is the TOTAL number of times the choice is
// selected on the line (not per unit of the line).
type PricedSelection struct {
	Modifier decimal.Decimal
	Quantity int
}

// PriceLine prices one order line.
//
// Choice selection quantities already scale with the line quantity: the group
// min/max are multiplied by the line quantity, so 2 bowls need 2 broths and the
// selection quantities describe the whole line. The option surcharge is
// therefore NOT multiplied by the line quantity again:
//
//	lineTotal = base × qty + Σ(max(modifier, 0) × selectionQty)
//
// (Before audit finding M2 the surcharge was multiplied by qty twice, so 2
// bowls with a 1.50 broth were charged 6.00 of surcharge instead of 3.00.)
//
// Negative modifiers are clamped to 0, as before: a choice can never discount.
//
// Stored unit price: order_product.unit_price is numeric(10,2) and, with the
// formula above, lineTotal/qty is not always a whole number of cents (3 lines
// with one 0.50 surcharge: 30.50/3). We store lineTotal/qty rounded to cents.
// total_price is the authority: the order total, VAT, invoices, emails, the
// dashboard and Mollie all use total_price, never unit_price × quantity (the
// Mollie line builder falls back to quantity 1 when the two do not multiply
// back exactly). unit_price is a display-only "average per unit" figure.
func PriceLine(base decimal.Decimal, qty int64, selections []PricedSelection) (lineTotal, unitPrice decimal.Decimal) {
	lineTotal = base.Mul(decimal.NewFromInt(qty))
	for _, s := range selections {
		modifier := s.Modifier
		if modifier.Sign() < 0 {
			modifier = decimal.Zero
		}
		lineTotal = lineTotal.Add(modifier.Mul(decimal.NewFromInt(int64(s.Quantity))))
	}
	if qty <= 0 {
		return lineTotal, base
	}
	unitPrice = lineTotal.DivRound(decimal.NewFromInt(qty), 2)
	return lineTotal, unitPrice
}
