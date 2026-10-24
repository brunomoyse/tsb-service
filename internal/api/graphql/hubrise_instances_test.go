package graphql_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/model"
	"tsb-service/internal/api/graphql/resolver"
	"tsb-service/internal/api/graphql/testhelpers"
	"tsb-service/internal/api/hubrisewire"
	hubtest "tsb-service/internal/modules/hubrise/testhelpers"
	"tsb-service/pkg/db"
	pkghubrise "tsb-service/pkg/hubrise"
	"tsb-service/pkg/hubrise/hubrisetest"
	"tsb-service/pkg/pubsub"
)

// Production runs two replicas on one database. Both run every worker and both observe every status
// change; HubRise calls whichever of them the load balancer picks. The claims, leases and unique keys
// of the database are what keep each order to one POST and each event to one transition.
func TestHubriseTwoReplicasOnOneDatabaseAndOneHubRise(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	srv := hubrisetest.New(t)

	// The second replica: its own services and resolver on the same database.
	broker := pubsub.NewBroker()
	t.Cleanup(broker.Shutdown)
	resB := env.wire(t, &db.DBPool{Customer: env.DB.DB, Admin: env.DB.DB}, broker)
	clientB := testhelpers.NewGraphQLTestClient(resB, testhelpers.TestJWTSecret)
	t.Cleanup(clientB.Close)
	replicaB := &TestContext{DB: env.DB, Resolver: resB, Client: clientB, Fixtures: env.Fixtures}
	replicas := []*TestContext{env.TestContext, replicaB}

	// The load balancer in front of them alternates, and the public URL is the one HubRise knows.
	gin.SetMode(gin.TestMode)
	routers := []*gin.Engine{gin.New(), gin.New()}
	var turn atomic.Uint64
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		routers[turn.Add(1)%2].ServeHTTP(w, r)
	}))
	t.Cleanup(front.Close)
	hubtest.SeedConnection(t, env.DB.DB, hubtest.WithToken(hubrisetest.Token),
		hubtest.WithLocation(hubrisetest.LocationID, hubrisetest.CatalogID), hubtest.WithPushOrdersSince(time.Now().Add(-time.Hour)),
		hubtest.WithFeatures(true, true, false, false, false))
	_, err := srv.NewClient(t).SetCallback(t.Context(), pkghubrise.Callback{
		URL: front.URL + "/api/v1/hubrise/callback", Events: map[string][]string{"order": {"update"}},
	})
	require.NoError(t, err)
	_, rtA := env.installHubrise(t, env.Resolver, srv, routers[0], front.URL+"/api/v1")
	_, rtB := env.installHubrise(t, resB, srv, routers[1], front.URL+"/api/v1")
	require.NotSame(t, rtA, rtB)
	runWorkers(t, rtA)
	runWorkers(t, rtB)

	// Orders arrive on both replicas.
	const orders = 12
	ids := make([]string, orders)
	for i := range ids {
		_, token := testhelpers.SeedCustomer(t, env.DB.DB, fmt.Sprintf("replica-%d", i))
		ids[i] = mustCreateOrder(t, replicas[i%2], token, "fr", map[string]any{
			"orderType": "PICKUP", "isOnlinePayment": false, "items": lines(env.Fixtures.SalmonSushi.ID, 1),
		}).ID
	}
	require.Eventually(t, func() bool { return len(srv.Orders()) == orders }, 20*time.Second, 20*time.Millisecond)
	time.Sleep(300 * time.Millisecond) // a duplicate would have arrived by now
	assert.Len(t, srv.RequestsTo(http.MethodPost, "/v1/location/orders"), orders, "exactly one POST per order")
	seen := map[string]bool{}
	for _, o := range srv.Orders() {
		assert.False(t, seen[o.PrivateRef], "order %s was created twice", o.PrivateRef)
		seen[o.PrivateRef] = true
	}
	for _, id := range ids {
		assert.True(t, seen[id], "order %s never arrived", id)
	}
	assert.Equal(t, orders, countRows(t, env.TestContext, `SELECT count(*) FROM hubrise_outbox WHERE kind = 'order_create' AND state = 'sent' AND attempts = 1`))

	// The EPOS accepts every order; HubRise delivers each event twice (a retry after a lost
	// answer), and the balancer spreads the deliveries over both replicas.
	for _, id := range ids {
		remote, ok := srv.OrderByPrivateRef(id)
		require.True(t, ok)
		require.NoError(t, srv.SetOrderStatus(remote.ID, pkghubrise.StatusAccepted))
	}
	for range 2 {
		for _, d := range srv.Dispatch(t.Context(), hubrisetest.Dispatcher{Secret: hubrisetest.ClientSecret, Client: front.Client(), Keep: true}) {
			require.Equal(t, http.StatusOK, d.Status)
		}
	}
	require.Eventually(t, func() bool {
		return countRows(t, env.TestContext, `SELECT count(*) FROM orders WHERE order_status = 'CONFIRMED'`) == orders
	}, 20*time.Second, 20*time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, orders, countRows(t, env.TestContext, `SELECT count(*) FROM hubrise_events`), "one stored event per HubRise event")
	for _, id := range ids {
		assert.Equal(t, []string{"PENDING", "CONFIRMED"}, env.historyOf(t, uuid.MustParse(id)), "one transition per event")
	}
	assert.Empty(t, srv.RequestsTo(http.MethodPatch, "/v1/location/orders"), "nothing is echoed back")

	// A dashboard change on either replica is one PATCH, however many replicas observe it.
	for i, tc := range replicas {
		resp := gqlAs(t, tc, adminToken(t, tc), "fr", updateOrderMutation, map[string]any{"id": ids[i], "input": map[string]any{"status": "PREPARING"}})
		require.Empty(t, resp.Errors, "%+v", resp.Errors)
	}
	require.Eventually(t, func() bool { return len(srv.RequestsTo(http.MethodPatch, "/v1/location/orders")) == 2 }, 20*time.Second, 20*time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	assert.Len(t, srv.RequestsTo(http.MethodPatch, "/v1/location/orders"), 2, "one PATCH per local change")
}

// The same two replicas publish the menu of the dashboard. The state of the publisher is one row in
// the database, so a burst of edits made on both replicas is one write, the minimum interval holds
// across them, a replica that died with the lease is replaced, and an edit made while a write is in
// flight is never lost.
func TestHubriseTwoReplicasPublishTheMenuOfTheDashboard(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	srv := hubrisetest.New(t)
	hubtest.SeedConnection(t, env.DB.DB, hubtest.WithToken(hubrisetest.Token),
		hubtest.WithLocation(hubrisetest.LocationID, hubrisetest.CatalogID), hubtest.WithPublishCatalog())
	require.NoError(t, srv.ReplaceCatalogRaw(json.RawMessage(`{}`)))

	broker := pubsub.NewBroker()
	t.Cleanup(broker.Shutdown)
	resB := env.wire(t, &db.DBPool{Customer: env.DB.DB, Admin: env.DB.DB}, broker)
	gin.SetMode(gin.TestMode)
	routers := []*gin.Engine{gin.New(), gin.New()}
	front := httptest.NewServer(routers[0])
	t.Cleanup(front.Close)
	_, rtA := env.installHubrise(t, env.Resolver, srv, routers[0], front.URL+"/api/v1")
	_, rtB := env.installHubrise(t, resB, srv, routers[1], front.URL+"/api/v1")
	r := &e2eRig{covEnv: env, t: t, srv: srv, rt: rtA, web: front, dashboard: e2eDashboard}
	var err error
	r.adminTok, err = testhelpers.GenerateTestAccessToken(env.Fixtures.AdminUser.ID.String(), true)
	require.NoError(t, err)
	runWorkers(t, rtA)
	runWorkers(t, rtB)

	admin := env.ctxFor(env.Fixtures.AdminUser.ID.String(), true, "en")
	salmon, tuna := env.Fixtures.SalmonSushi.ID, env.Fixtures.TunaSushi.ID
	price := func(res *resolver.Resolver, id uuid.UUID, p string) {
		_, err := res.Mutation().UpdateProduct(admin, id, model.UpdateProductInput{Price: &p})
		require.NoError(t, err)
	}
	row := func() (changeSeq int, dirty bool, holder *string, nextAfterStart float64) {
		var s struct {
			Seq    int     `db:"change_seq"`
			Dirty  bool    `db:"dirty"`
			Holder *string `db:"lease_holder"`
			Gap    float64 `db:"gap"`
		}
		require.NoError(t, env.DB.DB.GetContext(t.Context(), &s, `SELECT change_seq, dirty_since IS NOT NULL AS dirty, lease_holder,
			COALESCE(EXTRACT(EPOCH FROM next_attempt_at - last_started_at), 0)::float8 AS gap FROM hubrise_catalog_publish`))
		return s.Seq, s.Dirty, s.Holder, s.Gap
	}

	prev := r.pubPreview()
	require.True(t, r.pubPublish(false, prev.PlanHash).Published)
	r.pubSettled()
	require.Equal(t, 1, r.puts())

	t.Run("a burst of edits on both replicas is one write after the debounce", func(t *testing.T) {
		srv.ClearRequests()
		for i, p := range []string{"13.00", "13.10", "13.20", "13.30", "13.40"} {
			if i%2 == 0 {
				price(env.Resolver, salmon, p)
			} else {
				price(resB, tuna, p)
			}
		}
		seq, dirty, _, gap := row()
		assert.GreaterOrEqual(t, seq, 5, "five edits")
		assert.True(t, dirty)
		assert.GreaterOrEqual(t, gap, 29.0, "30 s after the start of the last run")
		r.quiet(300 * time.Millisecond) // both workers have had their ticks: nothing is due
		assert.Equal(t, 1, r.puts())

		r.pubDue()
		for _, rt := range []*hubrisewire.Runtime{rtA, rtB} {
			_, err := rt.Catalog.Publisher.Tick(t.Context())
			require.NoError(t, err)
		}
		r.pubSettled()
		assert.Equal(t, 2, r.puts(), "one write for the whole burst")
		assert.Len(t, srv.RequestsTo(http.MethodGet, "/v1/catalogs/"), 1)
		salmonSku, _ := skuOf(r.remoteCatalog(), "SUSHI-SALMON")
		tunaSku, _ := skuOf(r.remoteCatalog(), "SUSHI-TUNA")
		assert.Equal(t, "13.30 EUR", tunaSku["price"], "the last edit of the tuna, made on the other replica")
		assert.Equal(t, "13.40 EUR", salmonSku["price"], "the last edit of the salmon")
	})

	t.Run("a replica that died with the lease is replaced once the lease expired", func(t *testing.T) {
		price(env.Resolver, salmon, "14.00")
		r.pubExec(`UPDATE hubrise_catalog_publish SET lease_holder = 'dead-replica', leased_until = now() + interval '60 seconds',
			next_attempt_at = now() - interval '1 second'`)
		for _, rt := range []*hubrisewire.Runtime{rtA, rtB} {
			ran, err := rt.Catalog.Publisher.Tick(t.Context())
			require.NoError(t, err)
			assert.False(t, ran, "the lease of the other replica is respected")
		}
		assert.Equal(t, 2, r.puts())

		r.pubExec(`UPDATE hubrise_catalog_publish SET leased_until = now() - interval '1 second'`)
		require.Eventually(t, func() bool {
			_, err := rtB.Catalog.Publisher.Tick(t.Context())
			require.NoError(t, err)
			return r.puts() == 3
		}, 20*time.Second, 25*time.Millisecond)
		r.pubSettled()
		_, _, holder, _ := row()
		assert.Nil(t, holder)
	})

	t.Run("an edit made while the write is in flight is published by the next run", func(t *testing.T) {
		srv.AddFault(hubrisetest.Fault{Method: http.MethodPut, Path: "/v1/catalogs/", Times: 1, Delay: 600 * time.Millisecond})
		price(env.Resolver, salmon, "15.00")
		srv.ClearRequests()
		r.pubDue()
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, err := rtA.Catalog.Publisher.Tick(context.Background())
			assert.NoError(t, err)
		}()
		// The read comes after the snapshot of the menu and before the write.
		require.Eventually(t, func() bool { return r.gets() == 1 }, 10*time.Second, 2*time.Millisecond)
		price(resB, tuna, "15.50")
		<-done
		assert.Equal(t, 4, r.puts())
		_, dirty, _, _ := row()
		assert.True(t, dirty, "the edit of the run is not forgotten")
		tunaSku, _ := skuOf(r.remoteCatalog(), "SUSHI-TUNA")
		assert.NotEqual(t, "15.50 EUR", tunaSku["price"], "it was not part of that write")

		r.pubRuns(5)
		tunaSku, _ = skuOf(r.remoteCatalog(), "SUSHI-TUNA")
		assert.Equal(t, "15.50 EUR", tunaSku["price"])
		_, dirty, _, _ = row()
		assert.False(t, dirty)
	})
}
