package domain

import (
	"github.com/shopspring/decimal"

	"tsb-service/pkg/money"
)

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

// OrderTotal is the amount the customer is charged (orders.total_price, and what Mollie is asked
// for). It is the ONE place that adds an order up: the order repository stores it and the quote
// (quoteOrder) shows it, so the two can never drift.
//
//	total = round10( max(items + deliveryFee - takeawayDiscount - couponDiscount, 0) + transactionFee )
//
// The goods + delivery part is clamped at 0 BEFORE the online fee is added: a coupon that covers the
// whole basket leaves exactly the fee (or nothing, for cash), never a negative amount. Without the
// clamp, a coupon snapped up to 0,10 EUR on a basket that is not a multiple of 10 cents stored a
// total of -0,05 / -0,10 EUR (audit PR 2.1 finding). The result is a multiple of 0,10 EUR
// (pkg/money.RoundToNearest10Cents).
func OrderTotal(items, deliveryFee, takeawayDiscount, couponDiscount, transactionFee decimal.Decimal) decimal.Decimal {
	goodsAndDelivery := items.Add(deliveryFee).Sub(takeawayDiscount).Sub(couponDiscount)
	if goodsAndDelivery.Sign() < 0 {
		goodsAndDelivery = decimal.Zero
	}
	return money.RoundToNearest10Cents(goodsAndDelivery.Add(transactionFee))
}
