package resolver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"tsb-service/internal/api/graphql/apperr"
	addressDomain "tsb-service/internal/modules/address/domain"
	couponDomain "tsb-service/internal/modules/coupon/domain"
	orderDomain "tsb-service/internal/modules/order/domain"
	productDomain "tsb-service/internal/modules/product/domain"
	restaurantDomain "tsb-service/internal/modules/restaurant/domain"
)

// ---- fakes ---------------------------------------------------------------------------------------

type fakeCatalog struct {
	fail     error
	products map[uuid.UUID]*productDomain.ProductOrderDetails
	choices  map[uuid.UUID]*productDomain.ProductChoice
	groups   map[uuid.UUID][]*productDomain.ProductChoiceGroup
}

func (f *fakeCatalog) GetProductsForPricing(_ context.Context, ids []string) ([]*productDomain.ProductOrderDetails, error) {
	if f.fail != nil {
		return nil, f.fail
	}
	var out []*productDomain.ProductOrderDetails
	for _, id := range ids {
		if p, ok := f.products[uuid.MustParse(id)]; ok {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakeCatalog) GetChoiceByID(_ context.Context, id uuid.UUID) (*productDomain.ProductChoice, error) {
	if c, ok := f.choices[id]; ok {
		return c, nil
	}
	return nil, sql.ErrNoRows
}

func (f *fakeCatalog) GetChoiceGroupsByProductID(_ context.Context, id uuid.UUID) ([]*productDomain.ProductChoiceGroup, error) {
	return f.groups[id], nil
}

type fakeAddresses map[string]*addressDomain.Address

// GetByPlaceID is the cache-only lookup: it answers from the same map (every fake address is "cached").
func (f fakeAddresses) GetByPlaceID(_ context.Context, placeID string) (*addressDomain.Address, error) {
	return f[placeID], nil
}

func (f fakeAddresses) Resolve(_ context.Context, placeID, _ string) (*addressDomain.Address, error) {
	if a, ok := f[placeID]; ok {
		return a, nil
	}
	return nil, errors.New("place not found")
}

// fakeCoupons answers ValidateCoupon like the real service: a known code gives a fixed amount or a
// percentage of the amount it is asked about (capped at that amount), with an optional minimum.
type fakeCoupons struct {
	calls   int
	failure error // returned as-is by every ValidateCoupon call, to simulate an outage
	coupons map[string]fakeCoupon
}

type fakeCoupon struct {
	id      uuid.UUID
	fixed   string
	percent string
	min     string
}

func (f *fakeCoupons) ValidateCoupon(_ context.Context, code string, amount decimal.Decimal, _ uuid.UUID) (*couponDomain.Coupon, decimal.Decimal, error) {
	f.calls++
	if f.failure != nil {
		return nil, decimal.Zero, f.failure
	}
	c, ok := f.coupons[code]
	if !ok {
		return nil, decimal.Zero, fmt.Errorf("invalid or expired coupon")
	}
	if c.min != "" && amount.LessThan(decimal.RequireFromString(c.min)) {
		return &couponDomain.Coupon{ID: c.id}, decimal.Zero, &couponDomain.MinOrderNotMetError{Required: decimal.RequireFromString(c.min)}
	}
	coupon := &couponDomain.Coupon{ID: c.id, DiscountType: couponDomain.DiscountTypeFixed, DiscountValue: decimal.Zero}
	if c.percent != "" {
		coupon.DiscountType = couponDomain.DiscountTypePercentage
		coupon.DiscountValue = decimal.RequireFromString(c.percent)
	} else {
		coupon.DiscountValue = decimal.RequireFromString(c.fixed)
	}
	return coupon, coupon.CalculateDiscount(amount), nil
}

type fakeOrders struct{ hasActiveCoupon bool }

func (f fakeOrders) HasActiveCouponOrder(context.Context, uuid.UUID) (bool, error) {
	return f.hasActiveCoupon, nil
}

type fakeRestaurant struct {
	dev    bool
	config *restaurantDomain.RestaurantConfig
}

func (f fakeRestaurant) IsDevMode() bool { return f.dev }
func (f fakeRestaurant) GetConfigWithOverrides(context.Context) (*restaurantDomain.RestaurantConfig, map[string]*restaurantDomain.ScheduleOverride, error) {
	return f.config, nil, nil
}

// ---- fixture -------------------------------------------------------------------------------------

type pricingFixture struct {
	t          *testing.T
	catalog    *fakeCatalog
	addresses  fakeAddresses
	coupons    *fakeCoupons
	orders     fakeOrders
	restaurant fakeRestaurant
	user       uuid.UUID
}

var (
	salmonID  = uuid.MustParse("00000000-0000-0000-0000-000000000001") // 12.50, discountable
	teaID     = uuid.MustParse("00000000-0000-0000-0000-000000000002") // 3.50, not discountable
	soldOutID = uuid.MustParse("00000000-0000-0000-0000-000000000003") // 9.00, is_available = false
	lunchID   = uuid.MustParse("00000000-0000-0000-0000-000000000004") // 11.00, lunch only
	bowlID    = uuid.MustParse("00000000-0000-0000-0000-000000000005") // 10.00 + broth (1 required per bowl)
	brothA    = uuid.MustParse("00000000-0000-0000-0000-0000000000a1") // +0.00
	brothB    = uuid.MustParse("00000000-0000-0000-0000-0000000000a2") // +1.50
	brothGrp  = uuid.MustParse("00000000-0000-0000-0000-0000000000b1")
	odd95ID   = uuid.MustParse("00000000-0000-0000-0000-000000000006") // 12.95, to get a basket that is not a multiple of 10 cents
)

func newPricingFixture(t *testing.T) *pricingFixture {
	product := func(id uuid.UUID, price string, discountable, lunchOnly, available bool) *productDomain.ProductOrderDetails {
		return &productDomain.ProductOrderDetails{
			ID: id, Name: "p-" + id.String()[34:], Price: decimal.RequireFromString(price),
			IsDiscountable: discountable, IsLunchOnly: lunchOnly, IsAvailable: available,
			VatCategory: productDomain.VatCategoryFood,
		}
	}
	f := &pricingFixture{
		t: t,
		catalog: &fakeCatalog{
			products: map[uuid.UUID]*productDomain.ProductOrderDetails{
				salmonID:  product(salmonID, "12.50", true, false, true),
				teaID:     product(teaID, "3.50", false, false, true),
				soldOutID: product(soldOutID, "9.00", true, false, false),
				lunchID:   product(lunchID, "11.00", false, true, true),
				bowlID:    product(bowlID, "10.00", true, false, true),
				odd95ID:   product(odd95ID, "12.95", true, false, true),
			},
			choices: map[uuid.UUID]*productDomain.ProductChoice{
				brothA: {ID: brothA, ProductID: bowlID, ChoiceGroupID: brothGrp, PriceModifier: decimal.Zero},
				brothB: {ID: brothB, ProductID: bowlID, ChoiceGroupID: brothGrp, PriceModifier: decimal.RequireFromString("1.50")},
			},
			groups: map[uuid.UUID][]*productDomain.ProductChoiceGroup{
				bowlID: {{ID: brothGrp, ProductID: bowlID, MinSelections: 1, MaxSelections: 1}},
			},
		},
		addresses: fakeAddresses{
			"near":     {PlaceID: "near", Postcode: "4000", Distance: 1500},
			"mid":      {PlaceID: "mid", Postcode: "4020", Distance: 3500},
			"far":      {PlaceID: "far", Postcode: "4020", Distance: 9500},
			"excluded": {PlaceID: "excluded", Postcode: "4610", Distance: 2000},
		},
		coupons: &fakeCoupons{coupons: map[string]fakeCoupon{
			"FIVE":    {id: uuid.New(), fixed: "5"},
			"TENPCT":  {id: uuid.New(), percent: "10"},
			"WHOLE":   {id: uuid.New(), percent: "100"},
			"MIN30":   {id: uuid.New(), fixed: "5", min: "30"},
			"BIGFIXD": {id: uuid.New(), fixed: "500"},
		}},
		restaurant: fakeRestaurant{dev: true},
		user:       uuid.New(),
	}
	return f
}

func (f *pricingFixture) pricer() *orderPricer {
	return &orderPricer{products: f.catalog, addresses: f.addresses, coupons: f.coupons, orders: f.orders, restaurant: f.restaurant}
}

func (f *pricingFixture) price(in pricingInput) *pricingResult {
	f.t.Helper()
	if in.OrderType == "" {
		in.OrderType = orderDomain.OrderTypePickUp
	}
	if in.UserID == nil {
		in.UserID = &f.user
	}
	res, err := f.pricer().price(f.t.Context(), in)
	if err != nil {
		f.t.Fatalf("price: unexpected server error: %v", err)
	}
	return res
}

func item(id uuid.UUID, qty int) pricingItem { return pricingItem{ProductID: id, Quantity: qty} }

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func codes(issues []*pricingIssue) []apperr.Code {
	out := make([]apperr.Code, len(issues))
	for i, issue := range issues {
		out[i] = issue.Code()
	}
	return out
}

func wantCodes(t *testing.T, got []*pricingIssue, want ...apperr.Code) {
	t.Helper()
	g := codes(got)
	if len(g) != len(want) {
		t.Fatalf("issue codes = %v, want %v", g, want)
	}
	for i := range want {
		if g[i] != want[i] {
			t.Fatalf("issue codes = %v, want %v", g, want)
		}
	}
}

func wantMoney(t *testing.T, name string, got decimal.Decimal, want string) {
	t.Helper()
	if !got.Equal(dec(want)) {
		t.Errorf("%s = %s, want %s", name, got, want)
	}
}

// ---- the totals ----------------------------------------------------------------------------------

func TestPricing_PickupCashAddsUpAndDiscountsTenPercent(t *testing.T) {
	f := newPricingFixture(t)
	// 2 × 12.50 + 3.50 = 28.50 ; 10 % of the discountable 25.00 = 2.50
	res := f.price(pricingInput{Items: []pricingItem{item(salmonID, 2), item(teaID, 1)}})
	wantCodes(t, res.Issues)
	wantMoney(t, "subtotal", res.Subtotal, "28.50")
	wantMoney(t, "pickupDiscount", res.PickupDiscount, "2.50")
	wantMoney(t, "deliveryFee", res.DeliveryFee, "0")
	wantMoney(t, "onlineFee", res.OnlineFee, "0")
	wantMoney(t, "total", res.Total, "26.00")
}

func TestPricing_NoPickupDiscountUnderTwentyEuros(t *testing.T) {
	f := newPricingFixture(t)
	res := f.price(pricingInput{Items: []pricingItem{item(salmonID, 1)}})
	wantMoney(t, "pickupDiscount", res.PickupDiscount, "0")
	wantMoney(t, "total", res.Total, "12.50")
}

func TestPricing_OnlineFeeIsAddedAfterTheDiscounts(t *testing.T) {
	f := newPricingFixture(t)
	res := f.price(pricingInput{IsOnlinePayment: true, Items: []pricingItem{item(salmonID, 2), item(teaID, 1)}})
	wantMoney(t, "onlineFee", res.OnlineFee, "0.30")
	// 28.50 − 2.50 + 0.30 = 26.30
	wantMoney(t, "total", res.Total, "26.30")
}

func TestPricing_SelectionsSurchargeIsNotMultipliedByQuantityTwice(t *testing.T) {
	f := newPricingFixture(t)
	// 2 bowls (10.00) with 1 broth A + 1 broth B (+1.50): 20.00 + 1.50
	res := f.price(pricingInput{Items: []pricingItem{{
		ProductID: bowlID, Quantity: 2,
		Selections: []pricingSelection{{GroupID: brothGrp, ChoiceID: brothA, Quantity: 1}, {GroupID: brothGrp, ChoiceID: brothB, Quantity: 1}},
	}}})
	wantCodes(t, res.Issues)
	wantMoney(t, "lineTotal", res.Lines[0].LineTotal, "21.50")
	// The line echoes today's modifiers, in canonical order.
	if len(res.Lines[0].Selections) != 2 || res.Lines[0].Selections[1].ChoiceID != brothB {
		t.Fatalf("selections echo = %+v", res.Lines[0].Selections)
	}
	wantMoney(t, "modifier B", res.Lines[0].Modifiers[brothB], "1.50")
}

func TestPricing_LegacyChoiceIsFoldedIntoSelections(t *testing.T) {
	f := newPricingFixture(t)
	choice := brothB
	res := f.price(pricingInput{Items: []pricingItem{{ProductID: bowlID, Quantity: 3, ChoiceID: &choice}}})
	wantCodes(t, res.Issues)
	// 3 bowls, the legacy choice applies to every unit: 30.00 + 3 × 1.50
	wantMoney(t, "lineTotal", res.Lines[0].LineTotal, "34.50")
	if got := res.Lines[0].Selections; len(got) != 1 || got[0].GroupID != brothGrp || got[0].Quantity != 3 {
		t.Fatalf("selections echo = %+v", got)
	}
}

// ---- line issues ---------------------------------------------------------------------------------

func TestPricing_LineIssues(t *testing.T) {
	f := newPricingFixture(t)
	missing := uuid.New()
	unknownChoice := uuid.New()
	res := f.price(pricingInput{Items: []pricingItem{
		item(salmonID, 1),  // 0 fine
		item(missing, 1),   // 1 deleted
		item(soldOutID, 1), // 2 sold out
		item(teaID, 100),   // 3 quantity
		{ProductID: bowlID, Quantity: 1, Selections: []pricingSelection{{GroupID: brothGrp, ChoiceID: unknownChoice, Quantity: 1}}}, // 4 unknown choice
		item(bowlID, 1), // 5 required group missing
	}})

	wantCodes(t, res.LineIssues(0))
	wantCodes(t, res.LineIssues(1), apperr.CodeProductNotFound)
	wantCodes(t, res.LineIssues(2), apperr.CodeProductUnavailable)
	wantCodes(t, res.LineIssues(3), apperr.CodeInvalidQuantity)
	wantCodes(t, res.LineIssues(4), apperr.CodeSelectionInvalid)
	wantCodes(t, res.LineIssues(5), apperr.CodeSelectionInvalid)

	// The product's current price travels with the issue; a missing product has none.
	if got := res.LineIssues(2)[0].CurrentPrice; got == nil || !got.Equal(dec("9.00")) {
		t.Errorf("sold-out currentPrice = %v, want 9.00", got)
	}
	if res.LineIssues(1)[0].CurrentPrice != nil {
		t.Error("a deleted product has no current price")
	}
	if res.Lines[1].ProductPrice != nil {
		t.Error("a deleted product has no productPrice")
	}

	// Only the good line is in the totals; the broken ones add nothing.
	wantMoney(t, "subtotal", res.Subtotal, "12.50")
	for _, i := range []int{1, 2, 3, 4, 5} {
		if res.Lines[i].Priced {
			t.Errorf("line %d must not be priced", i)
		}
		wantMoney(t, fmt.Sprintf("line %d total", i), res.Lines[i].LineTotal, "0")
	}
	// The issues carry the product id for the web to find the line again.
	if ext := res.LineIssues(1)[0].Err.Extensions(); ext["productId"] != missing.String() {
		t.Errorf("productId extension = %v", ext["productId"])
	}
}

func TestPricing_PriceChangedOnlyWhenTheClientTotalDiffers(t *testing.T) {
	f := newPricingFixture(t)
	stale, same := dec("11.00"), dec("12.50")
	res := f.price(pricingInput{Items: []pricingItem{
		{ProductID: salmonID, Quantity: 1, ExpectedLineTotal: &stale},
		{ProductID: salmonID, Quantity: 1, ExpectedLineTotal: &same},
	}})
	wantCodes(t, res.LineIssues(0), apperr.CodePriceChanged)
	wantCodes(t, res.LineIssues(1))
	if got := res.LineIssues(0)[0].CurrentPrice; got == nil || !got.Equal(dec("12.50")) {
		t.Errorf("currentPrice = %v, want 12.50", got)
	}
	// The line stays priced at today's price, so the quote total is today's total.
	wantMoney(t, "subtotal", res.Subtotal, "25.00")
}

func TestPricing_LunchOnlyProductNeedsALunchSlot(t *testing.T) {
	f := newPricingFixture(t)
	f.restaurant = fakeRestaurant{config: &restaurantDomain.RestaurantConfig{
		OrderingEnabled: true, OpeningHours: weeklyOpeningHours(t), PreparationMinutes: 30,
	}}
	// Wednesday 2026-05-13, 11:42 in Brussels: open, lunch service.
	now := atBrussels(t, "2026-05-13", "11:42")
	dinner := atBrussels(t, "2026-05-13", "19:00")
	lunch := atBrussels(t, "2026-05-13", "12:30")

	asap := f.price(pricingInput{Now: now, Items: []pricingItem{item(lunchID, 1)}})
	wantCodes(t, asap.Issues)
	wantCodes(t, asap.LineIssues(0))

	atLunch := f.price(pricingInput{Now: now, PreferredReadyTime: &lunch, Items: []pricingItem{item(lunchID, 1)}})
	wantCodes(t, atLunch.Issues)

	atDinner := f.price(pricingInput{Now: now, PreferredReadyTime: &dinner, Items: []pricingItem{item(lunchID, 1), item(teaID, 1)}})
	wantCodes(t, atDinner.LineIssues(0), apperr.CodeLunchSlotRequired)
	wantCodes(t, atDinner.LineIssues(1))
	// The product exists and is priced: the customer only has to pick another slot.
	wantMoney(t, "subtotal", atDinner.Subtotal, "14.50")
}

// ---- order issues --------------------------------------------------------------------------------

func TestPricing_OrderingGate(t *testing.T) {
	f := newPricingFixture(t)
	f.restaurant = fakeRestaurant{config: &restaurantDomain.RestaurantConfig{
		OrderingEnabled: false, OpeningHours: weeklyOpeningHours(t), PreparationMinutes: 30,
	}}
	now := atBrussels(t, "2026-05-13", "11:42")

	res := f.price(pricingInput{Now: now, Items: []pricingItem{item(salmonID, 1)}})
	wantCodes(t, res.Issues, apperr.CodeOrderingUnavailable)
	// A quote still prices the basket.
	wantMoney(t, "total", res.Total, "12.50")

	// The store-review bypass skips the gate (see CreateOrder).
	res = f.price(pricingInput{Now: now, SkipOrderingGate: true, Items: []pricingItem{item(salmonID, 1)}})
	wantCodes(t, res.Issues)
}

func TestPricing_ClosedShopNeedsASlot(t *testing.T) {
	f := newPricingFixture(t)
	f.restaurant = fakeRestaurant{config: &restaurantDomain.RestaurantConfig{
		OrderingEnabled: true, OpeningHours: weeklyOpeningHours(t), PreparationMinutes: 30,
	}}
	closedNow := atBrussels(t, "2026-05-13", "15:30") // between lunch and dinner
	res := f.price(pricingInput{Now: closedNow, Items: []pricingItem{item(salmonID, 1)}})
	wantCodes(t, res.Issues, apperr.CodeSlotRequired)
}

func TestPricing_EmptyAndOversizedBaskets(t *testing.T) {
	f := newPricingFixture(t)
	wantCodes(t, f.price(pricingInput{}).Issues, apperr.CodeOrderEmpty)

	many := make([]pricingItem, 51)
	for i := range many {
		many[i] = item(teaID, 1)
	}
	wantCodes(t, f.price(pricingInput{Items: many}).Issues, apperr.CodeOrderTooManyItems)
}

func TestPricing_DeliveryMinimumUsesTheGoodsBeforeTheFee(t *testing.T) {
	f := newPricingFixture(t)
	res := f.price(pricingInput{OrderType: orderDomain.OrderTypeDelivery, AddressPlaceID: new("mid"), Items: []pricingItem{item(salmonID, 1)}})
	wantCodes(t, res.Issues, apperr.CodeDeliveryMinimumNotMet)
	if res.Issues[0].Minimum == nil || *res.Issues[0].Minimum != "25" {
		t.Errorf("minimum = %v, want 25", res.Issues[0].Minimum)
	}
	if ext := res.Issues[0].Err.Extensions(); ext["minimum"] != "25" {
		t.Errorf("extensions minimum = %v", ext["minimum"])
	}
	// The quote keeps going: the fee is still shown.
	wantMoney(t, "deliveryFee", res.DeliveryFee, "1")
}

func TestPricing_DeliveryFeeAndAddressChecks(t *testing.T) {
	f := newPricingFixture(t)
	basket := []pricingItem{item(salmonID, 2)} // 25.00: exactly the minimum
	deliver := func(placeID *string) *pricingResult {
		return f.price(pricingInput{OrderType: orderDomain.OrderTypeDelivery, AddressPlaceID: placeID, Items: basket})
	}

	near := deliver(new("near"))
	wantCodes(t, near.Issues)
	wantMoney(t, "free delivery under 3 km", near.DeliveryFee, "0")
	wantMoney(t, "total", near.Total, "25.00")
	if near.Address == nil || *near.Address.Postcode != "4000" {
		t.Errorf("address snapshot = %+v", near.Address)
	}

	mid := deliver(new("mid"))
	wantCodes(t, mid.Issues)
	wantMoney(t, "fee at 3.5 km", mid.DeliveryFee, "1")
	wantMoney(t, "total with fee", mid.Total, "26.00")

	wantCodes(t, deliver(nil).Issues, apperr.CodeAddressRequired)
	wantCodes(t, deliver(new("")).Issues, apperr.CodeAddressRequired)
	wantCodes(t, deliver(new("nowhere")).Issues, apperr.CodeAddressUnresolvable)
	wantCodes(t, deliver(new("far")).Issues, apperr.CodeDeliveryOutOfZone)
	wantCodes(t, deliver(new("excluded")).Issues, apperr.CodeDeliveryAreaExcluded)
}

// ---- coupons -------------------------------------------------------------------------------------

func TestPricing_CouponIsValidatedAndSnappedWithoutReserving(t *testing.T) {
	f := newPricingFixture(t)
	// Pickup 2 × 12.50 + 3.50 = 28.50; pickup 2.50; 10 % coupon of 28.50 = 2.85 → 2.90
	res := f.price(pricingInput{CouponCode: new("TENPCT"), Items: []pricingItem{item(salmonID, 2), item(teaID, 1)}})
	wantCodes(t, res.Issues)
	if res.Coupon == nil || !res.Coupon.Valid || res.Coupon.Code != "TENPCT" || res.Coupon.ErrorCode != nil {
		t.Fatalf("coupon = %+v", res.Coupon)
	}
	wantMoney(t, "couponDiscount", res.CouponDiscount, "2.90")
	wantMoney(t, "total", res.Total, "23.10") // 28.50 − 2.50 − 2.90
	if res.CouponID == nil {
		t.Error("createOrder needs the coupon id to reserve it")
	}
}

func TestPricing_CouponFollowsTheBasket(t *testing.T) {
	f := newPricingFixture(t)
	small := f.price(pricingInput{CouponCode: new("TENPCT"), Items: []pricingItem{item(salmonID, 1)}})
	big := f.price(pricingInput{CouponCode: new("TENPCT"), Items: []pricingItem{item(salmonID, 4)}})
	wantMoney(t, "10 % of 12.50 = 1.25 → 1.30", small.CouponDiscount, "1.30")
	wantMoney(t, "10 % of 50.00", big.CouponDiscount, "5.00")
}

func TestPricing_CouponRefusals(t *testing.T) {
	f := newPricingFixture(t)
	basket := []pricingItem{item(salmonID, 1)}

	unknown := f.price(pricingInput{CouponCode: new("NOPE"), Items: basket})
	wantCodes(t, unknown.Issues, apperr.CodeCouponInvalid)
	if unknown.Coupon == nil || unknown.Coupon.Valid || unknown.Coupon.ErrorCode == nil || *unknown.Coupon.ErrorCode != "COUPON_INVALID" {
		t.Errorf("coupon = %+v", unknown.Coupon)
	}
	wantMoney(t, "no discount", unknown.CouponDiscount, "0")

	min := f.price(pricingInput{CouponCode: new("MIN30"), Items: basket})
	wantCodes(t, min.Issues, apperr.CodeCouponMinOrderNotMet)
	if min.Issues[0].Minimum == nil || *min.Issues[0].Minimum != "30" {
		t.Errorf("coupon minimum = %v", min.Issues[0].Minimum)
	}

	f.orders = fakeOrders{hasActiveCoupon: true}
	active := f.price(pricingInput{CouponCode: new("FIVE"), Items: basket})
	wantCodes(t, active.Issues, apperr.CodeCouponAlreadyActive)
	if active.Coupon.Valid {
		t.Error("a coupon blocked by another open order is not valid")
	}
}

// A database failure while checking the coupon is OUR fault: it must come back as a server error
// (COUPON_CHECK_FAILED), not as a "this coupon is invalid" issue the customer would act on.
func TestPricing_CouponInfrastructureFailureIsAServerFault(t *testing.T) {
	f := newPricingFixture(t)
	f.coupons.failure = &couponDomain.CheckFailedError{Err: errors.New("pq: connection refused")}
	res, err := f.pricer().price(t.Context(), pricingInput{
		OrderType: orderDomain.OrderTypePickUp, UserID: &f.user, CouponCode: new("FIVE"), Items: []pricingItem{item(salmonID, 1)},
	})
	if err == nil {
		t.Fatalf("expected a server error, got result %+v", res)
	}
	appErr, ok := apperr.From(err)
	if !ok || appErr.Code != apperr.CodeCouponCheckFailed {
		t.Fatalf("err = %v, want code COUPON_CHECK_FAILED", err)
	}
	if apperr.IsExpected(appErr.Code) {
		t.Error("COUPON_CHECK_FAILED must not be an expected code: it has to reach Sentry")
	}
}

func TestPricing_AnonymousQuoteDoesNotEvaluateTheCoupon(t *testing.T) {
	f := newPricingFixture(t)
	res, err := f.pricer().price(t.Context(), pricingInput{
		OrderType: orderDomain.OrderTypePickUp, CouponCode: new("FIVE"), Items: []pricingItem{item(salmonID, 1)},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantCodes(t, res.Issues) // not an issue: the customer has to sign in to order anyway
	if res.Coupon == nil || res.Coupon.Valid || res.Coupon.ErrorCode == nil || *res.Coupon.ErrorCode != "UNAUTHENTICATED" {
		t.Errorf("coupon = %+v", res.Coupon)
	}
	if f.coupons.calls != 0 {
		t.Errorf("an anonymous caller must not reach ValidateCoupon (it records failed attempts per user), got %d calls", f.coupons.calls)
	}
	wantMoney(t, "no discount", res.CouponDiscount, "0")
}

func TestPricing_NegativeTotalIsClampedAtZero(t *testing.T) {
	f := newPricingFixture(t)
	// 12.95 basket, 100 % coupon = 12.95 → snapped UP to 13.00: the stored total used to be −0.10
	// (cash) or 0.25 → 0.30 (online).
	basket := []pricingItem{item(odd95ID, 1)}

	cash := f.price(pricingInput{CouponCode: new("WHOLE"), Items: basket})
	wantCodes(t, cash.Issues)
	wantMoney(t, "couponDiscount", cash.CouponDiscount, "13.00")
	wantMoney(t, "cash total never negative", cash.Total, "0")

	online := f.price(pricingInput{IsOnlinePayment: true, CouponCode: new("WHOLE"), Items: basket})
	wantMoney(t, "online total is exactly the fee", online.Total, "0.30")

	// A fixed coupon far above the basket, stacked with the pickup discount on a 28.50 basket.
	huge := f.price(pricingInput{CouponCode: new("BIGFIXD"), Items: []pricingItem{item(salmonID, 2), item(teaID, 1)}})
	if huge.Total.Sign() < 0 {
		t.Errorf("total = %s, must never be negative", huge.Total)
	}
	wantMoney(t, "total", huge.Total, "0")
}

// ---- consistency with what createOrder stores ----------------------------------------------------

func TestPricing_TotalIsWhatTheOrderRepositoryStores(t *testing.T) {
	f := newPricingFixture(t)
	res := f.price(pricingInput{
		OrderType: orderDomain.OrderTypeDelivery, AddressPlaceID: new("mid"), IsOnlinePayment: true,
		CouponCode: new("FIVE"), Items: []pricingItem{item(salmonID, 2), item(teaID, 2)},
	})
	wantCodes(t, res.Issues)
	var items decimal.Decimal
	for _, raw := range res.RawItems {
		items = items.Add(raw.TotalPrice)
	}
	stored := orderDomain.OrderTotal(items, res.DeliveryFee, res.PickupDiscount, res.CouponDiscount, orderDomain.TransactionFee)
	wantMoney(t, "quote total == repository total", res.Total, stored.String())
	// 32.00 + 1.00 fee − 5.00 coupon + 0.30 online
	wantMoney(t, "total", res.Total, "28.30")
}

func TestPricing_FailFastStopsAtTheFirstProblemAndSkipsLaterLookups(t *testing.T) {
	f := newPricingFixture(t)
	in := pricingInput{
		OrderType: orderDomain.OrderTypeDelivery, CouponCode: new("FIVE"),
		Items: []pricingItem{item(uuid.New(), 1), item(soldOutID, 1)},
	}

	in.FailFast = true
	res := f.price(in)
	// createOrder's error is the first problem found, with its code intact.
	wantCodes(t, res.Issues, apperr.CodeProductNotFound)
	if err := res.FirstError(); err == nil || !errors.Is(err, res.Issues[0].Err) {
		t.Fatalf("FirstError = %v", err)
	}
	if appErr, ok := apperr.From(res.FirstError()); !ok || appErr.Code != apperr.CodeProductNotFound {
		t.Fatalf("FirstError must be the *apperr.Error, got %v", res.FirstError())
	}
	if f.coupons.calls != 0 {
		t.Errorf("fail-fast must not validate the coupon (it records failed attempts), got %d calls", f.coupons.calls)
	}

	// The quote reports everything instead.
	in.FailFast = false
	res = f.price(in)
	wantCodes(t, res.LineIssues(0), apperr.CodeProductNotFound)
	wantCodes(t, res.LineIssues(1), apperr.CodeProductUnavailable)
	wantCodes(t, res.OrderIssues(), apperr.CodeDeliveryMinimumNotMet, apperr.CodeAddressRequired)
}

func TestPricing_ServerFaultsAreErrorsNotIssues(t *testing.T) {
	f := newPricingFixture(t)
	f.catalog.fail = errors.New("db down")
	_, err := f.pricer().price(t.Context(), pricingInput{UserID: &f.user, Items: []pricingItem{item(teaID, 1)}})
	if err == nil {
		t.Fatal("a failed product lookup must be returned as an error, not reported as a customer issue")
	}
	if _, isApp := apperr.From(err); isApp {
		t.Errorf("a server fault must not carry a customer-facing code: %v", err)
	}
}
