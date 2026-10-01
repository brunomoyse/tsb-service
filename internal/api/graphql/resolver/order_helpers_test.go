package resolver

import (
	"testing"

	"github.com/shopspring/decimal"

	orderDomain "tsb-service/internal/modules/order/domain"
)

// The legacy single `choiceId` input must price like the equivalent
// selections:[{choiceId, quantity: lineQty}] input.
func TestLegacyChoiceQuantity_PricesLikeScaledSelection(t *testing.T) {
	base := decimal.RequireFromString("8.00")
	modifier := decimal.RequireFromString("0.50")
	for _, qty := range []int64{1, 2, 3, 10} {
		legacy := []orderDomain.PricedSelection{{Modifier: modifier, Quantity: legacyChoiceQuantity(qty)}}
		scaled := []orderDomain.PricedSelection{{Modifier: modifier, Quantity: int(qty)}}
		lt1, u1 := orderDomain.PriceLine(base, qty, legacy)
		lt2, u2 := orderDomain.PriceLine(base, qty, scaled)
		if !lt1.Equal(lt2) || !u1.Equal(u2) {
			t.Errorf("qty %d: legacy (%s, %s) != selections (%s, %s)", qty, lt1, u1, lt2, u2)
		}
		want := base.Mul(decimal.NewFromInt(qty)).Add(modifier.Mul(decimal.NewFromInt(qty)))
		if !lt1.Equal(want) {
			t.Errorf("qty %d: lineTotal = %s, want %s", qty, lt1, want)
		}
	}
}
