package resolver

// The pricer reads the same ordering policy the public RestaurantConfig.policy field serves
// (restaurantDomain.CurrentPolicy). These tests price baskets at the policy's own boundaries, so a
// number changed in the policy moves the pricer and the field together, and a number hard-coded
// back into the pricer fails here.

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"tsb-service/internal/api/graphql/apperr"
	addressDomain "tsb-service/internal/modules/address/domain"
	orderDomain "tsb-service/internal/modules/order/domain"
	productDomain "tsb-service/internal/modules/product/domain"
	restaurantDomain "tsb-service/internal/modules/restaurant/domain"
	"tsb-service/pkg/brand"
)

func addressAt(placeID, postcode string, meters float64) *addressDomain.Address {
	return &addressDomain.Address{PlaceID: placeID, Postcode: postcode, Distance: meters}
}

func deliveryAt(f *pricingFixture, placeID string, items ...pricingItem) *pricingResult {
	return f.price(pricingInput{OrderType: orderDomain.OrderTypeDelivery, AddressPlaceID: strp(placeID), Items: items})
}

func TestPolicy_DeliveryFeeTiersDriveThePricer(t *testing.T) {
	policy := restaurantDomain.CurrentPolicy()
	f := newPricingFixture(t)
	basket := item(salmonID, 2) // 25.00, the minimum

	lower := 0
	for _, tier := range policy.DeliveryFeeTiers {
		// Both ends of the tier: its first meter and the last one before the next tier.
		for _, meters := range []int{lower, tier.UpToMeters - 1} {
			id := fmt.Sprintf("d%d", meters)
			f.addresses[id] = addressAt(id, "4020", float64(meters))
			res := deliveryAt(f, id, basket)
			wantCodes(t, res.Issues)
			if !res.DeliveryFee.Equal(tier.Fee) {
				t.Errorf("at %d m the pricer charges %s, the policy tier says %s", meters, res.DeliveryFee, tier.Fee)
			}
		}
		lower = tier.UpToMeters
	}

	// The radius itself is out of zone.
	f.addresses["edge"] = addressAt("edge", "4020", float64(policy.DeliveryMaxMeters))
	wantCodes(t, deliveryAt(f, "edge", basket).Issues, apperr.CodeDeliveryOutOfZone)
	f.addresses["inside"] = addressAt("inside", "4020", float64(policy.DeliveryMaxMeters-1))
	wantCodes(t, deliveryAt(f, "inside", basket).Issues)
}

func TestPolicy_ExcludedPostcodesDriveThePricer(t *testing.T) {
	policy := restaurantDomain.CurrentPolicy()
	f := newPricingFixture(t)
	for _, postcode := range policy.ExcludedPostcodes {
		id := "excl" + postcode
		f.addresses[id] = addressAt(id, postcode, 1000)
		wantCodes(t, deliveryAt(f, id, item(salmonID, 2)).Issues, apperr.CodeDeliveryAreaExcluded)
	}
}

// productAt registers a product priced exactly price (discountable) and returns its id.
func productAt(f *pricingFixture, price decimal.Decimal) uuid.UUID {
	id := uuid.New()
	f.catalog.products[id] = &productDomain.ProductOrderDetails{
		ID: id, Name: "boundary", Price: price, IsDiscountable: true, IsAvailable: true,
		VatCategory: productDomain.VatCategoryFood,
	}
	return id
}

func TestPolicy_DeliveryMinimumDrivesThePricer(t *testing.T) {
	policy := restaurantDomain.CurrentPolicy()
	f := newPricingFixture(t)
	f.addresses["near"] = addressAt("near", "4000", 1500)

	atMinimum := productAt(f, policy.DeliveryMinimum)
	wantCodes(t, deliveryAt(f, "near", item(atMinimum, 1)).Issues)

	underMinimum := productAt(f, policy.DeliveryMinimum.Sub(decimal.New(1, -2)))
	res := deliveryAt(f, "near", item(underMinimum, 1))
	wantCodes(t, res.Issues, apperr.CodeDeliveryMinimumNotMet)
	if res.Issues[0].Minimum == nil || *res.Issues[0].Minimum != policy.DeliveryMinimum.String() {
		t.Errorf("issue minimum = %v, policy says %s", res.Issues[0].Minimum, policy.DeliveryMinimum)
	}
}

func TestPolicy_PickupDiscountDrivesThePricer(t *testing.T) {
	policy := restaurantDomain.CurrentPolicy()
	f := newPricingFixture(t)

	under := productAt(f, policy.PickupDiscountMinimum.Sub(decimal.New(1, -2)))
	wantMoney(t, "discount just under the minimum", f.price(pricingInput{Items: []pricingItem{item(under, 1)}}).PickupDiscount, "0")

	// A basket that is a multiple of the rounding step, so the discount is exactly rate × goods.
	at := productAt(f, policy.PickupDiscountMinimum)
	res := f.price(pricingInput{Items: []pricingItem{item(at, 1)}})
	want := policy.PickupDiscountMinimum.Mul(policy.PickupDiscountRate)
	if !res.PickupDiscount.Equal(want) {
		t.Errorf("discount at the minimum = %s, policy rate gives %s", res.PickupDiscount, want)
	}
}

func TestPolicy_OnlineFeeDrivesThePricer(t *testing.T) {
	policy := restaurantDomain.CurrentPolicy()
	f := newPricingFixture(t)
	online := f.price(pricingInput{IsOnlinePayment: true, Items: []pricingItem{item(teaID, 1)}})
	if !online.OnlineFee.Equal(policy.OnlinePaymentFee) {
		t.Errorf("online fee = %s, policy says %s", online.OnlineFee, policy.OnlinePaymentFee)
	}
	// What the order repository stores comes from the same policy.
	if !orderDomain.TransactionFee.Equal(policy.OnlinePaymentFee) {
		t.Errorf("orderDomain.TransactionFee = %s, policy says %s", orderDomain.TransactionFee, policy.OnlinePaymentFee)
	}
}

func TestPolicy_DeliveryDisabledRefusesDelivery(t *testing.T) {
	// Cleanups run last-in first-out: t.Setenv restores the variable, then the brand is reloaded.
	t.Cleanup(func() { brand.Load() })
	t.Setenv("RESTAURANT_DELIVERY_ENABLED", "false")
	brand.Load()

	f := newPricingFixture(t)
	f.addresses["near"] = addressAt("near", "4000", 1500)
	res := deliveryAt(f, "near", item(salmonID, 2))
	wantCodes(t, res.Issues, apperr.CodeDeliveryUnavailable)

	// Pickup is untouched.
	wantCodes(t, f.price(pricingInput{Items: []pricingItem{item(salmonID, 2)}}).Issues)
	if restaurantDomain.CurrentPolicy().DeliveryEnabled {
		t.Error("policy still advertises delivery")
	}
}
