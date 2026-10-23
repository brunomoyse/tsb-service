package graphql_test

import (
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

	"tsb-service/internal/api/graphql/testhelpers"
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
