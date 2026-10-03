package resolver

import (
	"context"
	"strings"
	"testing"

	"tsb-service/internal/api/graphql/apperr"
	addressDomain "tsb-service/internal/modules/address/domain"
	orderDomain "tsb-service/internal/modules/order/domain"
	"tsb-service/internal/shared/middleware"
	"tsb-service/pkg/utils"
)

// spyAddresses tells the cache-only lookup apart from the full Resolve (which asks Google on a miss).
type spyAddresses struct {
	cached       map[string]*addressDomain.Address
	resolveCalls int
	cacheCalls   int
}

func (s *spyAddresses) GetByPlaceID(_ context.Context, placeID string) (*addressDomain.Address, error) {
	s.cacheCalls++
	return s.cached[placeID], nil
}

func (s *spyAddresses) Resolve(_ context.Context, placeID, _ string) (*addressDomain.Address, error) {
	s.resolveCalls++
	return &addressDomain.Address{PlaceID: placeID, Postcode: "4000", Distance: 1500}, nil
}

func deliveryQuote(f *pricingFixture, spy *spyAddresses, placeID string, signedIn bool) *pricingResult {
	f.t.Helper()
	p := f.pricer()
	p.addresses = spy
	in := pricingInput{
		OrderType: orderDomain.OrderTypeDelivery, AddressPlaceID: new(placeID),
		Items: []pricingItem{item(salmonID, 3)},
	}
	if signedIn {
		in.UserID = &f.user
	}
	res, err := p.price(f.t.Context(), in)
	if err != nil {
		f.t.Fatalf("price: %v", err)
	}
	return res
}

func TestPricing_AnonymousQuoteNeverAsksGoogleForTheAddress(t *testing.T) {
	f := newPricingFixture(t)
	spy := &spyAddresses{cached: map[string]*addressDomain.Address{"known": {PlaceID: "known", Postcode: "4000", Distance: 1500}}}

	hit := deliveryQuote(f, spy, "known", false)
	wantCodes(t, hit.Issues)
	wantMoney(t, "free delivery under 3 km", hit.DeliveryFee, "0")

	miss := deliveryQuote(f, spy, "never-resolved", false)
	wantCodes(t, miss.Issues, apperr.CodeAddressUnresolvable)

	if spy.resolveCalls != 0 {
		t.Errorf("an anonymous quote called the full Resolve %d times (Google Place Details / Routes)", spy.resolveCalls)
	}
}

func TestPricing_SignedInQuoteStillResolvesOnACacheMiss(t *testing.T) {
	f := newPricingFixture(t)
	spy := &spyAddresses{cached: map[string]*addressDomain.Address{}}
	res := deliveryQuote(f, spy, "fresh", true)
	wantCodes(t, res.Issues)
	if spy.resolveCalls != 1 {
		t.Errorf("resolveCalls = %d, want 1 (createOrder relies on this path)", spy.resolveCalls)
	}
}

func TestAllowPublicQuery(t *testing.T) {
	limiter := middleware.NewRateLimiter(0.0001, 2) // burst of 2, effectively no refill during the test
	t.Cleanup(limiter.Stop)
	r := &Resolver{PublicQueryLimiter: limiter}
	ctx := utils.SetClientIP(t.Context(), "203.0.113.7")

	for i := range 2 {
		if err := r.allowPublicQuery(ctx, "quoteOrder"); err != nil {
			t.Fatalf("request %d refused: %v", i+1, err)
		}
	}
	err := r.allowPublicQuery(ctx, "quoteOrder")
	if appErr, ok := apperr.From(err); !ok || appErr.Code != apperr.CodeRateLimited {
		t.Fatalf("third request: err = %v, want RATE_LIMITED", err)
	}

	// Another query and another IP have their own bucket.
	if err := r.allowPublicQuery(ctx, "resolveAddress"); err != nil {
		t.Errorf("resolveAddress shares the quoteOrder bucket: %v", err)
	}
	if err := r.allowPublicQuery(utils.SetClientIP(t.Context(), "203.0.113.8"), "quoteOrder"); err != nil {
		t.Errorf("another IP is throttled: %v", err)
	}

	// No limiter, or no IP (not an HTTP request): never limited.
	if err := (&Resolver{}).allowPublicQuery(ctx, "quoteOrder"); err != nil {
		t.Errorf("nil limiter: %v", err)
	}
	for range 5 {
		if err := r.allowPublicQuery(t.Context(), "quoteOrder"); err != nil {
			t.Fatalf("a call without a client IP was limited: %v", err)
		}
	}
}

// countingAddresses counts the billed Autocomplete calls.
type countingAddresses struct {
	spyAddresses
	autocompleteCalls int
}

func (c *countingAddresses) Autocomplete(_ context.Context, _, _ string) ([]addressDomain.Suggestion, error) {
	c.autocompleteCalls++
	return []addressDomain.Suggestion{{PlaceID: "p"}}, nil
}

func TestAutocompleteAddressesIsThrottledAndBounded(t *testing.T) {
	limiter := middleware.NewRateLimiter(0.0001, 3)
	t.Cleanup(limiter.Stop)
	addrs := &countingAddresses{}
	q := &queryResolver{&Resolver{PublicQueryLimiter: limiter, AddressService: addrs}}
	ctx := utils.SetClientIP(t.Context(), "203.0.113.9")

	for _, input := range []string{"", "  ab ", strings.Repeat("a", 201)} {
		_, err := q.AutocompleteAddresses(ctx, input, "s")
		if appErr, ok := apperr.From(err); !ok || appErr.Code != apperr.CodeUserError {
			t.Errorf("input %q: err = %v, want USER_ERROR", input, err)
		}
	}
	if addrs.autocompleteCalls != 0 {
		t.Fatalf("rejected inputs reached Google %d times", addrs.autocompleteCalls)
	}

	for i := range 3 {
		if _, err := q.AutocompleteAddresses(ctx, "rue de la paix 1", "s"); err != nil {
			t.Fatalf("request %d refused: %v", i+1, err)
		}
	}
	_, err := q.AutocompleteAddresses(ctx, "rue de la paix 1", "s")
	if appErr, ok := apperr.From(err); !ok || appErr.Code != apperr.CodeRateLimited {
		t.Fatalf("fourth request: err = %v, want RATE_LIMITED", err)
	}
	if addrs.autocompleteCalls != 3 {
		t.Errorf("autocompleteCalls = %d, want 3", addrs.autocompleteCalls)
	}
}
