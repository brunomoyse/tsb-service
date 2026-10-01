package domain

import (
	"testing"

	"github.com/shopspring/decimal"

	"tsb-service/pkg/money"
)

func TestDefaultOrderingPolicy_Values(t *testing.T) {
	p := DefaultOrderingPolicy()

	if !p.DeliveryEnabled {
		t.Error("delivery must be enabled by default")
	}
	checks := map[string]struct{ got, want decimal.Decimal }{
		"deliveryMinimum":       {p.DeliveryMinimum, decimal.RequireFromString("25")},
		"pickupDiscountRate":    {p.PickupDiscountRate, decimal.RequireFromString("0.10")},
		"pickupDiscountMinimum": {p.PickupDiscountMinimum, decimal.RequireFromString("20")},
		"onlinePaymentFee":      {p.OnlinePaymentFee, decimal.RequireFromString("0.30")},
		"totalRoundingStep":     {p.TotalRoundingStep, decimal.RequireFromString("0.10")},
	}
	for name, c := range checks {
		if !c.got.Equal(c.want) {
			t.Errorf("%s = %s, want %s", name, c.got, c.want)
		}
	}
	if p.DeliveryMaxMeters != 9000 || p.SlotIntervalMinutes != 15 || p.MinimumPreparationMinutes != 15 {
		t.Errorf("max %d m, slot %d min, min prep %d min", p.DeliveryMaxMeters, p.SlotIntervalMinutes, p.MinimumPreparationMinutes)
	}
	if len(p.ExcludedPostcodes) != 1 || p.ExcludedPostcodes[0] != "4610" {
		t.Errorf("excludedPostcodes = %v", p.ExcludedPostcodes)
	}
}

func TestOrderingPolicy_TiersAreAscendingAndEndAtTheRadius(t *testing.T) {
	p := DefaultOrderingPolicy()
	if len(p.DeliveryFeeTiers) == 0 {
		t.Fatal("no delivery fee tiers")
	}
	prevBound, prevFee := 0, decimal.Zero
	for i, tier := range p.DeliveryFeeTiers {
		if tier.UpToMeters <= prevBound {
			t.Errorf("tier %d: upToMeters %d is not above %d", i, tier.UpToMeters, prevBound)
		}
		if tier.Fee.LessThan(prevFee) {
			t.Errorf("tier %d: fee %s drops below %s", i, tier.Fee, prevFee)
		}
		prevBound, prevFee = tier.UpToMeters, tier.Fee
	}
	if prevBound != p.DeliveryMaxMeters {
		t.Errorf("the last tier ends at %d m, the radius is %d m", prevBound, p.DeliveryMaxMeters)
	}
}

func TestOrderingPolicy_DeliveryFeeAtTheTierBoundaries(t *testing.T) {
	p := DefaultOrderingPolicy()
	cases := []struct {
		meters float64
		fee    string
		ok     bool
	}{
		{0, "0", true}, {2999.9, "0", true},
		{3000, "1", true}, {3999, "1", true},
		{4000, "2", true}, {5000, "3", true}, {6000, "4", true}, {7000, "5", true},
		{8000, "6", true}, {8999.9, "6", true},
		{9000, "0", false}, {12000, "0", false},
	}
	for _, c := range cases {
		fee, ok := p.DeliveryFee(c.meters)
		if ok != c.ok || !fee.Equal(decimal.RequireFromString(c.fee)) {
			t.Errorf("DeliveryFee(%v) = %s, %v; want %s, %v", c.meters, fee, ok, c.fee, c.ok)
		}
	}
}

func TestOrderingPolicy_ExcludedPostcodes(t *testing.T) {
	p := DefaultOrderingPolicy()
	if !p.IsPostcodeExcluded("4610") || !p.IsPostcodeExcluded(" 4610 ") {
		t.Error("4610 must be excluded (whitespace ignored)")
	}
	if p.IsPostcodeExcluded("4000") || p.IsPostcodeExcluded("") {
		t.Error("4000 / empty must not be excluded")
	}
}

// The rounding step the policy advertises is the one money.RoundToNearest10Cents applies.
func TestOrderingPolicy_RoundingStepMatchesMoney(t *testing.T) {
	step := DefaultOrderingPolicy().TotalRoundingStep
	for _, in := range []string{"24.42", "24.45", "12.93", "12.95", "0.04", "0.05"} {
		rounded := money.RoundToNearest10Cents(decimal.RequireFromString(in))
		if !rounded.Mod(step).IsZero() {
			t.Errorf("RoundToNearest10Cents(%s) = %s is not a multiple of %s", in, rounded, step)
		}
	}
}

// The slot generator and the policy share the interval and the preparation constants.
func TestOrderingPolicy_SlotRulesAreTheSlotGeneratorsRules(t *testing.T) {
	p := DefaultOrderingPolicy()
	if p.SlotIntervalMinutes != slotStepMinutes {
		t.Errorf("slot interval: policy %d, generator %d", p.SlotIntervalMinutes, slotStepMinutes)
	}
	if p.MinimumPreparationMinutes < 0 || p.MinimumPreparationMinutes > DefaultPreparationMinutes {
		t.Errorf("minimum preparation %d min must not exceed the default %d min", p.MinimumPreparationMinutes, DefaultPreparationMinutes)
	}
}
