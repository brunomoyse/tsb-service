package application_test

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise"
	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/internal/modules/hubrise/interfaces"
	hubtest "tsb-service/internal/modules/hubrise/testhelpers"
	pkghubrise "tsb-service/pkg/hubrise"
	"tsb-service/pkg/hubrise/hubrisetest"
)

// transition is one call the fake transitioner received.
type transition struct {
	orderID uuid.UUID
	change  application.OrderChange
}

// fakeTransitioner stands in for the resolver: it records what HubRise asked for and, unless it is
// set to fail, writes the change to the order the way the order service would (the row and the
// history), without any of the notifications (those are tested end to end in the graphql package).
type fakeTransitioner struct {
	t  *testing.T
	e  *env
	mu sync.Mutex
	// err is returned for every call when set.
	err   error
	calls []transition
}

func (f *fakeTransitioner) Transition(ctx context.Context, id uuid.UUID, c application.OrderChange) error {
	f.mu.Lock()
	f.calls = append(f.calls, transition{id, c})
	err := f.err
	f.mu.Unlock()
	if err != nil {
		return err
	}
	if c.Status != "" {
		f.e.exec(`UPDATE orders SET order_status = $2 WHERE id = $1`, id, c.Status)
		f.e.exec(`INSERT INTO order_status_history (order_id, status) VALUES ($1, $2)`, id, c.Status)
	}
	if c.EstimatedReadyTime != nil {
		f.e.exec(`UPDATE orders SET estimated_ready_time = $2 WHERE id = $1`, id, *c.EstimatedReadyTime)
	}
	return nil
}

func (f *fakeTransitioner) all() []transition {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]transition(nil), f.calls...)
}

func (f *fakeTransitioner) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// syncEnv is an env with the inbound side built, the mock's callback pointing at a real HTTP
// handler, and the clocks of the mock and the shop shared.
type syncEnv struct {
	*env
	tr     *fakeTransitioner
	sync   *hubrise.Sync
	server *httptest.Server
	// failures is the limiter of the forged requests (nil: none).
	failures *countingLimiter
}

type countingLimiter struct {
	mu      sync.Mutex
	allowed int
	keys    []string
}

func (l *countingLimiter) AllowKey(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.keys = append(l.keys, key)
	return len(l.keys) <= l.allowed
}

// newSyncEnv builds the env, the inbound side and the callback endpoint, and registers the callback
// at the mock the way the connection setup will.
func newSyncEnv(t *testing.T, options ...envOption) *syncEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	e := newEnv(t, append([]envOption{
		withSharedClock(), withSeed(hubtest.WithLocation(hubrisetest.LocationID, hubrisetest.CatalogID)),
	}, options...)...)
	s := &syncEnv{env: e, failures: &countingLimiter{allowed: 30}}
	s.tr = &fakeTransitioner{t: t, e: e}
	s.sync = e.mod.NewSync(e.push, hubrise.SyncOptions{Transitioner: s.tr, Holder: "test"})

	router := gin.New()
	interfaces.NewCallbackHandler(s.sync.Receiver, s.failures).Register(router)
	s.server = httptest.NewServer(router)
	t.Cleanup(s.server.Close)
	s.register(s.server.URL + "/hubrise/callback")
	return s
}

// register sets the callback at the mock with the events the integration registers for.
func (s *syncEnv) register(url string) {
	s.t.Helper()
	_, err := s.srv.NewClient(s.t).SetCallback(s.t.Context(), pkghubrise.Callback{URL: url, Events: map[string][]string{
		"order": {"update"}, "catalog": {"create", "update", "delete"}, "inventory": {"update", "patch"},
	}})
	require.NoError(s.t, err)
}

// deliver posts the queued events to the callback endpoint, signed like HubRise signs them.
func (s *syncEnv) deliver() []hubrisetest.Delivery {
	s.t.Helper()
	return s.srv.Dispatch(s.t.Context(), hubrisetest.Dispatcher{Secret: hubrisetest.ClientSecret, Client: s.server.Client()})
}

// process runs the event processor on what is due.
func (s *syncEnv) process() int {
	s.t.Helper()
	n, err := s.sync.Processor.ProcessOnce(s.t.Context())
	require.NoError(s.t, err)
	return n
}

// epos changes the status of the HubRise order of a local order as the EPOS, delivers the event and
// processes it.
func (s *syncEnv) epos(id uuid.UUID, status pkghubrise.OrderStatus) {
	s.t.Helper()
	s.clock.Advance(10 * time.Second) // strictly newer than the previous event, and clear of any PATCH of ours
	s.eposQuiet(id, status)
	s.deliver()
	s.process()
}

func (s *syncEnv) eposQuiet(id uuid.UUID, status pkghubrise.OrderStatus) {
	s.t.Helper()
	require.NoError(s.t, s.srv.SetOrderStatus(s.remote(id).ID, status))
}

func (s *syncEnv) eventStates() map[string]int {
	s.t.Helper()
	out := map[string]int{}
	rows, err := s.db.QueryxContext(s.t.Context(), `SELECT state, count(*) FROM hubrise_events GROUP BY state`)
	require.NoError(s.t, err)
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var st string
		var n int
		require.NoError(s.t, rows.Scan(&st, &n))
		out[st] = n
	}
	return out
}

func (s *syncEnv) localStatus(id uuid.UUID) string {
	s.t.Helper()
	var st string
	require.NoError(s.t, s.db.GetContext(s.t.Context(), &st, `SELECT order_status FROM orders WHERE id = $1`, id))
	return st
}

func (s *syncEnv) gets() []hubrisetest.Request {
	return s.srv.RequestsTo("GET", "/v1/location/orders")
}

// pushed makes a pushed order: placed, queued and sent to the mock, with the links that make it
// complete (so nothing is flagged).
func (s *syncEnv) pushed(spec orderSpec) uuid.UUID {
	s.t.Helper()
	s.linkProduct(s.fx.SalmonSushi.ID, "SUSHI-SALMON")
	id := s.sentOrder(spec)
	s.srv.ClearRequests()
	return id
}

var _ = domain.PushSent
