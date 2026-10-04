package resolver

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/apperr"
	addressDomain "tsb-service/internal/modules/address/domain"
	orderDomain "tsb-service/internal/modules/order/domain"
	productDomain "tsb-service/internal/modules/product/domain"
	restaurantDomain "tsb-service/internal/modules/restaurant/domain"
	"tsb-service/pkg/brand"
	"tsb-service/pkg/utils"
)

// What the pricer does when one of its collaborators fails or answers something unexpected: a
// failed lookup is a server fault (an error), never a silently wrong price.

var errLookup = errors.New("lookup failed")

// shakyCatalog is the fixture catalog with chosen lookups misbehaving.
type shakyCatalog struct {
	*fakeCatalog
	choiceErr, groupsErr error
	nilChoice            bool
	choiceCalls          int
}

func (c *shakyCatalog) GetChoiceByID(ctx context.Context, id uuid.UUID) (*productDomain.ProductChoice, error) {
	c.choiceCalls++
	if c.choiceErr != nil {
		return nil, c.choiceErr
	}
	if c.nilChoice {
		return nil, nil
	}
	return c.fakeCatalog.GetChoiceByID(ctx, id)
}

func (c *shakyCatalog) GetChoiceGroupsByProductID(ctx context.Context, id uuid.UUID) ([]*productDomain.ProductChoiceGroup, error) {
	if c.groupsErr != nil {
		return nil, c.groupsErr
	}
	return c.fakeCatalog.GetChoiceGroupsByProductID(ctx, id)
}

type shakyAddresses struct{ cacheErr error }

func (shakyAddresses) Resolve(context.Context, string, string) (*addressDomain.Address, error) {
	return nil, errLookup
}

func (s shakyAddresses) GetByPlaceID(context.Context, string) (*addressDomain.Address, error) {
	return nil, s.cacheErr
}

type shakyOrders struct{ err error }

func (s shakyOrders) HasActiveCouponOrder(context.Context, uuid.UUID) (bool, error) {
	return false, s.err
}

type shakyRestaurant struct {
	err     error
	enabled bool
}

func (shakyRestaurant) IsDevMode() bool { return false }
func (s shakyRestaurant) GetConfigWithOverrides(context.Context) (*restaurantDomain.RestaurantConfig, map[string]*restaurantDomain.ScheduleOverride, error) {
	if s.err != nil {
		return nil, nil, s.err
	}
	return &restaurantDomain.RestaurantConfig{OrderingEnabled: s.enabled, OpeningHours: []byte(`{}`)}, nil, nil
}

func (f *pricingFixture) priceWith(mutate func(p *orderPricer), in pricingInput) (*pricingResult, error) {
	f.t.Helper()
	p := f.pricer()
	mutate(p)
	if in.OrderType == "" {
		in.OrderType = orderDomain.OrderTypePickUp
	}
	if in.UserID == nil {
		in.UserID = &f.user
	}
	return p.price(f.t.Context(), in)
}

func TestPricingServerFaults(t *testing.T) {
	bowlWith := func(choice uuid.UUID) pricingItem {
		return pricingItem{ProductID: bowlID, Quantity: 1, Selections: []pricingSelection{{GroupID: brothGrp, ChoiceID: choice, Quantity: 1}}}
	}

	t.Run("the restaurant's configuration cannot be loaded", func(t *testing.T) {
		f := newPricingFixture(t)
		_, err := f.priceWith(func(p *orderPricer) { p.restaurant = shakyRestaurant{err: errLookup} }, pricingInput{Items: []pricingItem{item(teaID, 1)}})
		require.ErrorIs(t, err, errLookup)
		assert.ErrorContains(t, err, "failed to load restaurant config")
	})

	t.Run("ordering switched off is an issue of the basket, not a fault", func(t *testing.T) {
		f := newPricingFixture(t)
		res, err := f.priceWith(func(p *orderPricer) { p.restaurant = shakyRestaurant{enabled: false} }, pricingInput{Items: []pricingItem{item(teaID, 1)}})
		require.NoError(t, err)
		wantCodes(t, res.Issues, apperr.CodeOrderingUnavailable)
	})

	t.Run("a choice lookup that fails for a reason other than not-found is a fault", func(t *testing.T) {
		f := newPricingFixture(t)
		_, err := f.priceWith(func(p *orderPricer) { p.products = &shakyCatalog{fakeCatalog: f.catalog, choiceErr: errLookup} }, pricingInput{Items: []pricingItem{bowlWith(brothA)}})
		require.ErrorIs(t, err, errLookup)
	})

	t.Run("a choice the catalog answers with nothing is an invalid selection", func(t *testing.T) {
		f := newPricingFixture(t)
		res, err := f.priceWith(func(p *orderPricer) { p.products = &shakyCatalog{fakeCatalog: f.catalog, nilChoice: true} }, pricingInput{Items: []pricingItem{bowlWith(brothA)}})
		require.NoError(t, err)
		wantCodes(t, res.Issues, apperr.CodeSelectionInvalid)
	})

	t.Run("a choice group lookup that fails is a fault", func(t *testing.T) {
		f := newPricingFixture(t)
		_, err := f.priceWith(func(p *orderPricer) { p.products = &shakyCatalog{fakeCatalog: f.catalog, groupsErr: errLookup} }, pricingInput{Items: []pricingItem{bowlWith(brothA)}})
		require.ErrorIs(t, err, errLookup)
		assert.ErrorContains(t, err, "failed to load choice groups")
	})

	t.Run("a choice used on several lines is loaded once", func(t *testing.T) {
		f := newPricingFixture(t)
		catalog := &shakyCatalog{fakeCatalog: f.catalog}
		res, err := f.priceWith(func(p *orderPricer) { p.products = catalog }, pricingInput{Items: []pricingItem{bowlWith(brothB), bowlWith(brothB), bowlWith(brothB)}})
		require.NoError(t, err)
		wantCodes(t, res.Issues)
		assert.Equal(t, 1, catalog.choiceCalls)
		wantMoney(t, "subtotal", res.Subtotal, "34.50")
	})

	t.Run("a product with a negative price is refused, and a negative modifier is never a discount", func(t *testing.T) {
		f := newPricingFixture(t)
		broken := uuid.MustParse("00000000-0000-0000-0000-000000000099")
		f.catalog.products[broken] = &productDomain.ProductOrderDetails{ID: broken, Name: "broken", Price: decimal.RequireFromString("-1"), IsAvailable: true, VatCategory: productDomain.VatCategoryFood}
		res := f.price(pricingInput{Items: []pricingItem{item(broken, 1)}})
		wantCodes(t, res.Issues, apperr.CodeInvalidPrice)
		wantMoney(t, "nothing is added to the basket", res.Subtotal, "0")

		negative := uuid.MustParse("00000000-0000-0000-0000-0000000000a9")
		f.catalog.choices[negative] = &productDomain.ProductChoice{ID: negative, ProductID: bowlID, ChoiceGroupID: brothGrp, PriceModifier: decimal.RequireFromString("-3")}
		res = f.price(pricingInput{Items: []pricingItem{bowlWith(negative)}})
		wantCodes(t, res.Issues)
		wantMoney(t, "the modifier counts as zero", res.Subtotal, "10.00")
	})

	t.Run("an anonymous quote whose address cache cannot be read is reported as an unresolvable address", func(t *testing.T) {
		f := newPricingFixture(t)
		p := f.pricer()
		p.addresses = shakyAddresses{cacheErr: errLookup}
		res, err := p.price(t.Context(), pricingInput{
			OrderType: orderDomain.OrderTypeDelivery, AddressPlaceID: new("near"), Items: []pricingItem{item(salmonID, 3)}})
		require.NoError(t, err)
		wantCodes(t, res.Issues, apperr.CodeAddressUnresolvable)

		res, err = f.pricer().price(t.Context(), pricingInput{
			OrderType: orderDomain.OrderTypeDelivery, AddressPlaceID: new("unknown"), Items: []pricingItem{item(salmonID, 3)}})
		require.NoError(t, err)
		wantCodes(t, res.Issues, apperr.CodeAddressUnresolvable)
	})

	t.Run("an anonymous caller is detected by the missing user, not by a zero uuid", func(t *testing.T) {
		f := newPricingFixture(t)
		p := f.pricer()
		res, err := p.price(t.Context(), pricingInput{OrderType: orderDomain.OrderTypePickUp, Items: []pricingItem{item(salmonID, 2)}, CouponCode: new("FIVE")})
		require.NoError(t, err)
		require.NotNil(t, res.Coupon)
		assert.False(t, res.Coupon.Valid)
		assert.Equal(t, "UNAUTHENTICATED", *res.Coupon.ErrorCode)
	})

	t.Run("the check for another active coupon order fails", func(t *testing.T) {
		f := newPricingFixture(t)
		f.coupons.coupons["PLAIN"] = fakeCoupon{id: uuid.New(), fixed: "1"}
		_, err := f.priceWith(func(p *orderPricer) { p.orders = shakyOrders{err: errLookup} }, pricingInput{Items: []pricingItem{item(salmonID, 2)}, CouponCode: new("PLAIN")})
		require.ErrorIs(t, err, errLookup)
		assert.ErrorContains(t, err, "failed to check active coupon orders")
	})

	t.Run("a coupon refusal that is not a minimum or a limit is the generic one", func(t *testing.T) {
		f := newPricingFixture(t)
		res := f.price(pricingInput{Items: []pricingItem{item(salmonID, 2)}, CouponCode: new("NOPE")})
		wantCodes(t, res.Issues, apperr.CodeCouponInvalid)
	})

	t.Run("fail-fast stops at delivery being unavailable", func(t *testing.T) {
		t.Cleanup(func() { brand.Load() })
		t.Setenv("RESTAURANT_DELIVERY_ENABLED", "false")
		brand.Load()
		f := newPricingFixture(t)
		res, err := f.priceWith(func(*orderPricer) {}, pricingInput{
			OrderType: orderDomain.OrderTypeDelivery, AddressPlaceID: new("near"), Items: []pricingItem{item(salmonID, 4)}, FailFast: true})
		require.NoError(t, err)
		wantCodes(t, res.Issues, apperr.CodeDeliveryUnavailable)
	})
}

func TestQuoteCallerID(t *testing.T) {
	id := uuid.New()
	with := func(raw string, exp time.Time) context.Context {
		ctx := utils.SetUserID(t.Context(), raw)
		if !exp.IsZero() {
			ctx = utils.SetTokenExpiry(ctx, exp)
		}
		return ctx
	}
	got := quoteCallerID(with(id.String(), time.Time{}))
	require.NotNil(t, got)
	assert.Equal(t, id, *got)
	assert.NotNil(t, quoteCallerID(with(id.String(), time.Now().Add(time.Hour))))

	assert.Nil(t, quoteCallerID(t.Context()), "anonymous")
	assert.Nil(t, quoteCallerID(with(id.String(), time.Now().Add(-time.Second))), "an expired token is anonymous")
	assert.Nil(t, quoteCallerID(with("not-a-uuid", time.Time{})), "a caller that is not a uuid is anonymous")
}
