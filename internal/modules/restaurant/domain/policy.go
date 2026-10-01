package domain

import (
	"strings"

	"github.com/shopspring/decimal"

	"tsb-service/pkg/brand"
	"tsb-service/pkg/money"
)

// The ordering policy is the ONE place that defines the numbers and rules the order pricer
// enforces: delivery tiers, zone, minimum, pickup discount, online fee, rounding and slot rules.
// The pricer (resolver/order_pricing.go), the slot generator (slots.go), the ready-time
// validation and the public `RestaurantConfig.policy` GraphQL field all read it from here, so
// what the webshop displays is what the backend charges. Values are code defaults shared by
// every instance; the one per-instance switch is whether delivery is offered at all
// (RESTAURANT_DELIVERY_ENABLED, see pkg/brand).

const (
	// SlotIntervalMinutes is the granularity of ready-time slots: slots are generated on, and a
	// preferred ready time must be aligned to, this many minutes.
	SlotIntervalMinutes = 15
	// MinimumPreparationMinutes is the floor under the configured preparation time when a
	// preferred ready time is validated: max(preparationMinutes, this).
	MinimumPreparationMinutes = 15
	// DefaultPreparationMinutes is used by the slot generator when the configured preparation
	// time is not positive.
	DefaultPreparationMinutes = 30
)

// DeliveryFeeTier is one step of the delivery fee grid: addresses closer than UpToMeters (and not
// closer than the previous tier's bound) pay Fee.
type DeliveryFeeTier struct {
	UpToMeters int
	Fee        decimal.Decimal
}

// OrderingPolicy is the pricing and slot policy of the instance.
type OrderingPolicy struct {
	// DeliveryEnabled is false for a takeaway-only instance; delivery orders are then refused.
	DeliveryEnabled bool
	// DeliveryMinimum is the minimum basket (goods only, before the fee) for a delivery order.
	DeliveryMinimum decimal.Decimal
	// DeliveryMaxMeters is the delivery radius: addresses at or beyond it are out of zone.
	DeliveryMaxMeters int
	// DeliveryFeeTiers is the fee by distance, ascending by UpToMeters. The last tier's bound is
	// the radius.
	DeliveryFeeTiers []DeliveryFeeTier
	// ExcludedPostcodes are never delivered to, whatever the distance.
	ExcludedPostcodes []string
	// PickupDiscountRate is the share of the discountable lines' total taken off a pickup order
	// whose basket (goods + fee) reaches PickupDiscountMinimum. Which products are discountable is
	// a per-product flag (Product.isDiscountable).
	PickupDiscountRate    decimal.Decimal
	PickupDiscountMinimum decimal.Decimal
	// OnlinePaymentFee is added to the total of an online (Mollie) payment.
	OnlinePaymentFee decimal.Decimal
	// TotalRoundingStep is the step every customer-facing amount is rounded to.
	TotalRoundingStep decimal.Decimal
	// SlotIntervalMinutes and MinimumPreparationMinutes: see the constants above.
	SlotIntervalMinutes       int
	MinimumPreparationMinutes int
}

// DefaultOrderingPolicy is the policy of every instance, delivery switched on.
func DefaultOrderingPolicy() OrderingPolicy {
	tier := func(meters int, fee int64) DeliveryFeeTier {
		return DeliveryFeeTier{UpToMeters: meters, Fee: decimal.NewFromInt(fee)}
	}
	return OrderingPolicy{
		DeliveryEnabled:   true,
		DeliveryMinimum:   decimal.NewFromInt(25),
		DeliveryMaxMeters: 9000,
		DeliveryFeeTiers: []DeliveryFeeTier{
			tier(3000, 0), tier(4000, 1), tier(5000, 2), tier(6000, 3), tier(7000, 4), tier(8000, 5), tier(9000, 6),
		},
		ExcludedPostcodes:         []string{"4610"}, // Beyne-Heusay
		PickupDiscountRate:        decimal.New(10, -2),
		PickupDiscountMinimum:     decimal.NewFromInt(20),
		OnlinePaymentFee:          decimal.New(30, -2),
		TotalRoundingStep:         money.RoundingStep,
		SlotIntervalMinutes:       SlotIntervalMinutes,
		MinimumPreparationMinutes: MinimumPreparationMinutes,
	}
}

// CurrentPolicy is the policy of this instance: the defaults plus the per-instance delivery switch.
func CurrentPolicy() OrderingPolicy {
	p := DefaultOrderingPolicy()
	p.DeliveryEnabled = brand.Current().DeliveryEnabled
	return p
}

// DeliveryFee is the fee for an address distance (meters). ok is false when the distance is
// outside the delivery radius.
func (p OrderingPolicy) DeliveryFee(distanceMeters float64) (fee decimal.Decimal, ok bool) {
	if distanceMeters >= float64(p.DeliveryMaxMeters) {
		return decimal.Zero, false
	}
	for _, tier := range p.DeliveryFeeTiers {
		if distanceMeters < float64(tier.UpToMeters) {
			return tier.Fee, true
		}
	}
	return decimal.Zero, false
}

// IsPostcodeExcluded reports whether delivery to the postcode is refused regardless of distance.
func (p OrderingPolicy) IsPostcodeExcluded(postcode string) bool {
	postcode = strings.TrimSpace(postcode)
	for _, excluded := range p.ExcludedPostcodes {
		if excluded == postcode {
			return true
		}
	}
	return false
}
