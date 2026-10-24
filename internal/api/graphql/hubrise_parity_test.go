package graphql_test

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/model"
	"tsb-service/internal/api/graphql/resolver"
	hubtest "tsb-service/internal/modules/hubrise/testhelpers"
	orderDomain "tsb-service/internal/modules/order/domain"
	productDomain "tsb-service/internal/modules/product/domain"
	"tsb-service/pkg/hubrise/hubrisetest"
)

// hubriseTables are the tables the shop's activity could fill. The job leases are left out: they
// are the bookkeeping of the running workers (one row per singleton job), not a consequence of
// anything the restaurant does.
var hubriseTables = []string{
	"hubrise_connection", "hubrise_oauth_states", "hubrise_catalog_links", "hubrise_orders",
	"hubrise_outbox", "hubrise_events", "hubrise_foreign_orders", "hubrise_customers", "hubrise_published_items",
}

// couplingSpy is the one thing the shop knows of the integration: hooks, a status observer and a
// profile observer. It records every call.
type couplingSpy struct {
	resolver.NoopHubriseHooks
	mu    sync.Mutex
	calls []string
}

func (s *couplingSpy) note(call string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, call)
}

func (s *couplingSpy) all() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func (s *couplingSpy) OrderReady(context.Context, uuid.UUID) error {
	s.note("OrderReady")
	return nil
}

func (s *couplingSpy) OrderStatusChanged(_ context.Context, _ uuid.UUID, st orderDomain.OrderStatus, _ uuid.UUID) error {
	s.note("OrderStatusChanged:" + string(st))
	return nil
}

func (s *couplingSpy) AvailabilityChanged(_ context.Context, _ uuid.UUID, available bool) error {
	if available {
		s.note("AvailabilityChanged:available")
	} else {
		s.note("AvailabilityChanged:sold out")
	}
	return nil
}

func (s *couplingSpy) ProductGuard(context.Context, *productDomain.Product, *productDomain.Product) error {
	s.note("ProductGuard")
	return nil
}

func (s *couplingSpy) ChoiceGroupGuard(context.Context, *productDomain.ProductChoiceGroup, *productDomain.ProductChoiceGroup) error {
	s.note("ChoiceGroupGuard")
	return nil
}

func (s *couplingSpy) ProfileChanged(context.Context, uuid.UUID) error {
	s.note("ProfileChanged")
	return nil
}

func (s *couplingSpy) AccountDeleted(context.Context, uuid.UUID) error {
	s.note("AccountDeleted")
	return nil
}

// The shop talks to the integration through the hooks and the two observers and through nothing
// else: a scripted day of the restaurant produces exactly the calls below, each at the moment it
// belongs to. (tests in the hubrise module check that no other package imports it.)
func TestTheHooksAreTheOnlyCouplingBetweenTheShopAndTheIntegration(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	spy := &couplingSpy{}
	env.Resolver.Hubrise = spy
	env.Resolver.OrderService.SetStatusObserver(spy)
	env.Resolver.UserService.SetProfileObserver(spy)
	admin := env.ctxFor(env.Fixtures.AdminUser.ID.String(), true, "en")
	salmon := env.Fixtures.SalmonSushi.ID

	c := env.newPushCustomer(t, "coupling", false)
	order := env.placeOrder(t, c, "fr")
	assert.Equal(t, []string{"OrderReady"}, spy.all(), "a cash order is ready as soon as it is placed")

	env.mustUpdateOrder(t, order.ID, map[string]any{"status": "CONFIRMED", "estimatedReadyTime": inMinutes(20)})
	env.mustUpdateOrder(t, order.ID, map[string]any{"status": "CONFIRMED"})
	assert.Equal(t, []string{"OrderReady", "OrderStatusChanged:CONFIRMED"}, spy.all(), "once per real change")

	_, err := env.Resolver.Mutation().UpdateProduct(admin, salmon, model.UpdateProductInput{Price: new("13.00")})
	require.NoError(t, err)
	_, err = env.Resolver.Mutation().UpdateProduct(admin, salmon, model.UpdateProductInput{IsAvailable: new(false)})
	require.NoError(t, err)
	_, err = env.Resolver.Mutation().CreateProductChoiceGroup(admin, model.CreateProductChoiceGroupInput{
		ProductID: salmon, MinSelections: 0, MaxSelections: 1,
		Translations: []*model.ChoiceTranslationInput{{Locale: "fr", Name: "Sauce"}}})
	require.NoError(t, err)

	_, err = env.Resolver.UserService.UpdateMe(t.Context(), c.id.String(), new("Anna"), nil, nil, nil, nil, nil, nil)
	require.NoError(t, err)

	assert.Equal(t, []string{
		"OrderReady", "OrderStatusChanged:CONFIRMED",
		"ProductGuard", "ProductGuard", "AvailabilityChanged:sold out",
		"ChoiceGroupGuard",
		"ProfileChanged",
	}, spy.all())
}

// With the integration installed but switched off the shop is the standalone shop: no call to HubRise,
// no row in any of its tables, whatever the restaurant does, with all workers running.
func TestAnInstalledButDisabledIntegrationDoesNothing(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	srv := hubrisetest.New(t)
	hubtest.SeedConnection(t, env.DB.DB, hubtest.WithToken(hubrisetest.Token), hubtest.WithEnabled(false),
		hubtest.WithLocation(hubrisetest.LocationID, hubrisetest.CatalogID), hubtest.WithPushOrdersSince(time.Now().Add(-time.Hour)),
		hubtest.WithFeatures(true, true, true, true, true))
	r := env.withHubriseInstalled(t, srv)
	r.run()
	require.NotNil(t, env.Resolver.Hubrise, "the hooks are in place")
	admin := env.ctxFor(env.Fixtures.AdminUser.ID.String(), true, "en")
	salmon := env.Fixtures.SalmonSushi.ID

	rows := func() map[string]int {
		out := map[string]int{}
		for _, table := range hubriseTables {
			out[table] = countRows(t, env.TestContext, `SELECT count(*) FROM `+table)
		}
		return out
	}
	before := rows()
	require.Equal(t, 1, before["hubrise_connection"])

	// The restaurant carries on: orders (cash and online, paid by webhook), status changes, product
	// edits (a price, a sold-out toggle, an option), a profile edit.
	c := env.newPushCustomer(t, "disabled", false)
	order := env.placeOrder(t, c, "fr")
	env.mustUpdateOrder(t, order.ID, map[string]any{"status": "CONFIRMED", "estimatedReadyTime": inMinutes(20)})
	online := env.placeOnlineOrder(t, env.newPushCustomer(t, "disabled-online", false))
	r.payOnline(online)
	env.mustUpdateOrder(t, order.ID, map[string]any{"status": "PICKED_UP"})

	_, err := env.Resolver.Mutation().UpdateProduct(admin, salmon, model.UpdateProductInput{Price: new("13.00")})
	require.NoError(t, err, "nothing is locked")
	for _, avail := range []bool{false, true} {
		_, err = env.Resolver.Mutation().UpdateProduct(admin, salmon, model.UpdateProductInput{IsAvailable: &avail})
		require.NoError(t, err)
	}
	_, err = env.Resolver.Mutation().CreateProductChoiceGroup(admin, model.CreateProductChoiceGroupInput{
		ProductID: salmon, MinSelections: 0, MaxSelections: 1,
		Translations: []*model.ChoiceTranslationInput{{Locale: "fr", Name: "Sauce"}}})
	require.NoError(t, err)
	_, err = env.Resolver.UserService.UpdateMe(t.Context(), c.id.String(), new("Anna"), new("Smith"), nil, new("+32470000000"), nil, nil, nil)
	require.NoError(t, err)

	time.Sleep(300 * time.Millisecond) // the workers have had their chances
	assert.Empty(t, srv.Requests(), "zero HTTP calls to HubRise")
	assert.Equal(t, before, rows(), "and zero new rows")
}

// A connection that publishes the menu but is switched off queues the edits (nothing is sent while it
// is off), and one that neither imports nor publishes, or no connection at all, leaves the publisher
// alone: no write to HubRise, and the standalone shop does not even read.
func TestMenuEditsOutsideAnActivePublishConnectionSendNothing(t *testing.T) {
	cases := []struct {
		name   string
		seed   []hubtest.SeedOption
		queued bool
		silent bool
	}{
		{"publishing but switched off", []hubtest.SeedOption{hubtest.WithPublishCatalog(), hubtest.WithEnabled(false)}, true, true},
		{"enabled, no catalog direction", []hubtest.SeedOption{hubtest.WithFeatures(true, true, true, false, false)}, false, false},
		{"not connected", nil, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := setupCovEnv(t, covOptions{})
			srv := hubrisetest.New(t)
			newFileService(t)
			if tc.seed != nil {
				hubtest.SeedConnection(t, env.DB.DB, append([]hubtest.SeedOption{hubtest.WithToken(hubrisetest.Token),
					hubtest.WithLocation(hubrisetest.LocationID, hubrisetest.CatalogID)}, tc.seed...)...)
			}
			r := env.withHubriseInstalled(t, srv)
			r.run()

			r.editProduct(env.Fixtures.SalmonSushi.ID, `{ price: "13.00" }`)
			r.createProduct("PARITY", "Parity", "4.00")
			require.NoError(t, r.rt.Catalog.Publisher.Scan(t.Context()))
			r.pubDue()
			ran, err := r.rt.Catalog.Publisher.Tick(t.Context())
			require.NoError(t, err)
			assert.False(t, ran, "the claim needs an enabled, confirmed publishing connection")
			time.Sleep(200 * time.Millisecond)

			assert.Empty(t, srv.RequestsTo(http.MethodPut, "/v1/catalogs/"), "nothing is written to HubRise")
			if tc.silent {
				assert.Empty(t, srv.Requests(), "zero HTTP calls")
			}
			queued := countRows(t, env.TestContext, `SELECT count(*) FROM hubrise_catalog_publish WHERE dirty_since IS NOT NULL`)
			if tc.queued {
				assert.Equal(t, 1, queued, "the edits wait for the connection to come back")
			} else {
				assert.Zero(t, queued, "the publisher was not told")
				assert.Zero(t, countRows(t, env.TestContext, `SELECT count(*) FROM hubrise_catalog_publish WHERE change_seq <> 0`))
			}
			assert.Zero(t, countRows(t, env.TestContext, `SELECT count(*) FROM hubrise_published_items`))
		})
	}
}
