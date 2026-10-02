package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"tsb-service/pkg/money"
	"tsb-service/pkg/timezone"
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

// Every slot the generator produces is one the policy accepts: on a SlotIntervalMinutes boundary,
// with no seconds. (createOrder rejects anything else as SLOT_MISALIGNED.)
func TestOrderingPolicy_GeneratedSlotsAreAligned(t *testing.T) {
	p := DefaultOrderingPolicy()
	cfg := configWith(t, weeklyHours(t), 20)

	// A 20 min preparation time and off-grid "now" values: the first slot of each service has to be rounded up.
	var checked int
	for _, now := range []time.Time{at(t, "2026-04-22", "10:07"), at(t, "2026-04-23", "11:38"), at(t, "2026-04-24", "17:01")} {
		for _, s := range append(cfg.AvailableSlotsToday(now, nil), cfg.ReviewSlotsToday(now)...) {
			local := timezone.In(s.Value)
			if local.Minute()%p.SlotIntervalMinutes != 0 || local.Second() != 0 || local.Nanosecond() != 0 {
				t.Errorf("slot %s is not aligned to %d minutes", s.Label, p.SlotIntervalMinutes)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no slot was generated, the alignment check proved nothing")
	}
	if p.MinimumPreparationMinutes < 0 || p.MinimumPreparationMinutes > DefaultPreparationMinutes {
		t.Errorf("minimum preparation %d min must not exceed the default %d min", p.MinimumPreparationMinutes, DefaultPreparationMinutes)
	}
}
