package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/testhelpers"
	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/modules/hubrise/domain"
	pkghubrise "tsb-service/pkg/hubrise"
	"tsb-service/pkg/hubrise/hubrisetest"
)

// faults makes named repository methods fail, to reach the branches that handle a store that is
// down at the worst moment.
type faults struct {
	mu   sync.Mutex
	errs map[string]error
}

var errStore = errors.New("store down")

func newFaults(names ...string) *faults {
	f := &faults{errs: map[string]error{}}
	for _, n := range names {
		f.errs[n] = errStore
	}
	return f
}

func (f *faults) set(name string, err error) { f.mu.Lock(); f.errs[name] = err; f.mu.Unlock() }
func (f *faults) hit(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.errs[name]
}

type faultOutbox struct {
	domain.OutboxRepository
	f *faults
}

func (o faultOutbox) Enqueue(ctx context.Context, i domain.OutboxItem) (int64, bool, error) {
	if err := o.f.hit("Outbox.Enqueue"); err != nil {
		return 0, false, err
	}
	return o.OutboxRepository.Enqueue(ctx, i)
}

func (o faultOutbox) EnqueueIfNoPending(ctx context.Context, i domain.OutboxItem) (int64, bool, error) {
	if err := o.f.hit("Outbox.EnqueueIfNoPending"); err != nil {
		return 0, false, err
	}
	return o.OutboxRepository.EnqueueIfNoPending(ctx, i)
}

func (o faultOutbox) ScrubCustomerData(ctx context.Context, user uuid.UUID) (int64, error) {
	if err := o.f.hit("Outbox.ScrubCustomerData"); err != nil {
		return 0, err
	}
	return o.OutboxRepository.ScrubCustomerData(ctx, user)
}

func (o faultOutbox) SkipOpenByDedupeKey(ctx context.Context, k domain.OutboxKind, key, reason string) (int64, error) {
	if err := o.f.hit("Outbox.SkipOpenByDedupeKey"); err != nil {
		return 0, err
	}
	return o.OutboxRepository.SkipOpenByDedupeKey(ctx, k, key, reason)
}

func (o faultOutbox) Claim(ctx context.Context, now time.Time, lease time.Duration, limit int, kinds ...domain.OutboxKind) ([]domain.OutboxItem, error) {
	if err := o.f.hit("Outbox.Claim"); err != nil {
		return nil, err
	}
	return o.OutboxRepository.Claim(ctx, now, lease, limit, kinds...)
}

func (o faultOutbox) MarkSent(ctx context.Context, id int64, now time.Time) (bool, error) {
	if err := o.f.hit("Outbox.MarkSent"); err != nil {
		return false, err
	}
	return o.OutboxRepository.MarkSent(ctx, id, now)
}

func (o faultOutbox) MarkParked(ctx context.Context, id int64, now time.Time, reason string) (bool, error) {
	if err := o.f.hit("Outbox.MarkParked"); err != nil {
		return false, err
	}
	return o.OutboxRepository.MarkParked(ctx, id, now, reason)
}

func (o faultOutbox) MarkSkipped(ctx context.Context, id int64, reason string) (bool, error) {
	if err := o.f.hit("Outbox.MarkSkipped"); err != nil {
		return false, err
	}
	return o.OutboxRepository.MarkSkipped(ctx, id, reason)
}

func (o faultOutbox) Release(ctx context.Context, id int64, next time.Time, reason string, revert bool) (bool, error) {
	if err := o.f.hit("Outbox.Release"); err != nil {
		return false, err
	}
	return o.OutboxRepository.Release(ctx, id, next, reason, revert)
}

func (o faultOutbox) MaxPendingID(ctx context.Context, id uuid.UUID, k domain.OutboxKind) (int64, error) {
	if err := o.f.hit("Outbox.MaxPendingID"); err != nil {
		return 0, err
	}
	return o.OutboxRepository.MaxPendingID(ctx, id, k)
}

func (o faultOutbox) SkipPendingUpTo(ctx context.Context, id uuid.UUID, k domain.OutboxKind, upTo int64, reason string) (int64, error) {
	if err := o.f.hit("Outbox.SkipPendingUpTo"); err != nil {
		return 0, err
	}
	return o.OutboxRepository.SkipPendingUpTo(ctx, id, k, upTo, reason)
}

func (o faultOutbox) CountForOrder(ctx context.Context, id uuid.UUID, k domain.OutboxKind) (int, error) {
	if err := o.f.hit("Outbox.CountForOrder"); err != nil {
		return 0, err
	}
	return o.OutboxRepository.CountForOrder(ctx, id, k)
}

type faultLinks struct {
	domain.OrderLinkRepository
	f *faults
}

func (l faultLinks) Get(ctx context.Context, id uuid.UUID) (*domain.OrderLink, error) {
	if err := l.f.hit("Orders.Get"); err != nil {
		return nil, err
	}
	return l.OrderLinkRepository.Get(ctx, id)
}

func (l faultLinks) MarkSent(ctx context.Context, id uuid.UUID, remoteID, status string, at time.Time) error {
	if err := l.f.hit("Orders.MarkSent"); err != nil {
		return err
	}
	return l.OrderLinkRepository.MarkSent(ctx, id, remoteID, status, at)
}

func (l faultLinks) SetPushState(ctx context.Context, id uuid.UUID, s domain.PushState) error {
	if err := l.f.hit("Orders.SetPushState"); err != nil {
		return err
	}
	return l.OrderLinkRepository.SetPushState(ctx, id, s)
}

func (l faultLinks) SetRemoteTotal(ctx context.Context, id uuid.UUID, total decimal.Decimal) error {
	if err := l.f.hit("Orders.SetRemoteTotal"); err != nil {
		return err
	}
	return l.OrderLinkRepository.SetRemoteTotal(ctx, id, total)
}

func (l faultLinks) SetNeedsAttention(ctx context.Context, id uuid.UUID, code string) error {
	if err := l.f.hit("Orders.SetNeedsAttention"); err != nil {
		return err
	}
	return l.OrderLinkRepository.SetNeedsAttention(ctx, id, code)
}

func (l faultLinks) MarkNotAccepted(ctx context.Context, id uuid.UUID) error {
	if err := l.f.hit("Orders.MarkNotAccepted"); err != nil {
		return err
	}
	return l.OrderLinkRepository.MarkNotAccepted(ctx, id)
}

func (l faultLinks) MarkStuckAlerted(ctx context.Context, id uuid.UUID, at time.Time) error {
	if err := l.f.hit("Orders.MarkStuckAlerted"); err != nil {
		return err
	}
	return l.OrderLinkRepository.MarkStuckAlerted(ctx, id, at)
}

func (l faultLinks) FlagAttention(ctx context.Context, id uuid.UUID, code string) error {
	if err := l.f.hit("Orders.FlagAttention"); err != nil {
		return err
	}
	return l.OrderLinkRepository.FlagAttention(ctx, id, code)
}

func (l faultLinks) ClearAttention(ctx context.Context, id uuid.UUID, codes ...string) error {
	if err := l.f.hit("Orders.ClearAttention"); err != nil {
		return err
	}
	return l.OrderLinkRepository.ClearAttention(ctx, id, codes...)
}

func (l faultLinks) StampRemoteRead(ctx context.Context, id uuid.UUID, at time.Time) error {
	if err := l.f.hit("Orders.StampRemoteRead"); err != nil {
		return err
	}
	return l.OrderLinkRepository.StampRemoteRead(ctx, id, at)
}

func (l faultLinks) RecordLocalPatch(ctx context.Context, id uuid.UUID, status string, at time.Time) error {
	if err := l.f.hit("Orders.RecordLocalPatch"); err != nil {
		return err
	}
	return l.OrderLinkRepository.RecordLocalPatch(ctx, id, status, at)
}

func (l faultLinks) Recreate(ctx context.Context, id uuid.UUID, payload json.RawMessage, due time.Time) error {
	if err := l.f.hit("Orders.Recreate"); err != nil {
		return err
	}
	return l.OrderLinkRepository.Recreate(ctx, id, payload, due)
}

type faultCatalog struct {
	domain.CatalogLinkRepository
	f *faults
}

func (c faultCatalog) FindByLocal(ctx context.Context, t domain.EntityType, id uuid.UUID) (*domain.CatalogLink, error) {
	if err := c.f.hit("Catalog.FindByLocal." + string(t)); err != nil {
		return nil, err
	}
	return c.CatalogLinkRepository.FindByLocal(ctx, t, id)
}

type faultSource struct {
	domain.OrderSource
	f *faults
}

func (s faultSource) State(ctx context.Context, id uuid.UUID) (*domain.OrderState, error) {
	if err := s.f.hit("Source.State"); err != nil {
		return nil, err
	}
	return s.OrderSource.State(ctx, id)
}

func (s faultSource) Snapshot(ctx context.Context, id uuid.UUID) (*domain.OrderSnapshot, error) {
	if err := s.f.hit("Source.Snapshot"); err != nil {
		return nil, err
	}
	return s.OrderSource.Snapshot(ctx, id)
}

func (s faultSource) StuckNew(ctx context.Context, before time.Time, limit int) ([]domain.OpenLink, error) {
	if err := s.f.hit("Source.StuckNew"); err != nil {
		return nil, err
	}
	return s.OrderSource.StuckNew(ctx, before, limit)
}

// faultyWorker builds a worker whose stores can be made to fail.
func (e *env) faultyWorker(f *faults, clients ...clientProvider) *application.OutboxWorker {
	c := e.opts.Clients
	if len(clients) > 0 {
		c = clients[0]
	}
	return application.NewOutboxWorker(application.WorkerDeps{
		Connections: e.mod.Connections, Outbox: faultOutbox{e.mod.Outbox, f}, Orders: faultLinks{e.mod.Orders, f},
		Links: faultCatalog{e.mod.CatalogLinks, f}, Source: faultSource{e.mod.Source, f}, Clients: c, Clock: e.clock,
	})
}

func (e *env) faultyPusher(f *faults) *application.OrderPusher {
	return application.NewOrderPusher(application.PusherDeps{
		Connections: e.mod.Connections, Orders: faultLinks{e.mod.Orders, f}, Outbox: faultOutbox{e.mod.Outbox, f},
		Source: faultSource{e.mod.Source, f}, Clock: e.clock,
	})
}

func (e *env) faultyReconciler(f *faults, remote application.RemoteOrderHandler) *application.Reconciler {
	return application.NewReconciler(application.ReconcilerDeps{
		Connections: e.mod.Connections, Orders: faultLinks{e.mod.Orders, f}, Source: faultSource{e.mod.Source, f},
		Leases: e.mod.Leases, Events: e.mod.Events, Outbox: e.mod.Outbox, Foreign: e.mod.Foreign, OAuthStates: e.mod.OAuthStates,
		Clients: e.opts.Clients, Pusher: e.faultyPusher(f), Clock: e.clock, Remote: remote,
	})
}

func (e *env) state(id uuid.UUID, kind domain.OutboxKind) domain.OutboxState {
	e.t.Helper()
	rows := e.outboxRows(id, kind)
	require.NotEmpty(e.t, rows)
	return rows[len(rows)-1].State
}

// queued makes an order and queues its create with the healthy push.
func (e *env) queued(spec orderSpec) uuid.UUID {
	e.t.Helper()
	id := e.order(spec)
	e.ready(id)
	return id
}

func (e *env) drainWith(w *application.OutboxWorker) {
	e.t.Helper()
	_, err := w.DrainOnce(e.t.Context())
	require.NoError(e.t, err)
}

func TestWorkerReportsAStoreThatCannotBeClaimed(t *testing.T) {
	e := newEnv(t)
	w := e.faultyWorker(newFaults("Outbox.Claim"))
	_, err := w.DrainOnce(t.Context())
	require.ErrorIs(t, err, errStore)

	// Run logs it and keeps ticking until stopped.
	w = application.NewOutboxWorker(application.WorkerDeps{
		Connections: e.mod.Connections, Outbox: faultOutbox{e.mod.Outbox, newFaults("Outbox.Claim")}, Orders: e.mod.Orders,
		Links: e.mod.CatalogLinks, Source: e.mod.Source, Clients: e.opts.Clients, Clock: e.clock,
		Config: application.WorkerConfig{Tick: 10 * time.Millisecond},
	})
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Millisecond)
	defer cancel()
	w.Run(ctx)
}

func TestWorkerSurvivesBookkeepingFailures(t *testing.T) {
	linked := func(e *env) { e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON") }

	t.Run("a row that cannot be given back stays leased", func(t *testing.T) {
		e := newEnv(t)
		linked(e)
		id := e.queued(orderSpec{})
		e.srv.AddFault(hubrisetest.Fault{Method: http.MethodPost, Path: "/v1/location/orders", Status: 500, Times: 1})
		e.drainWith(e.faultyWorker(newFaults("Outbox.Release")))
		assert.Equal(t, domain.OutboxSending, e.state(id, domain.OutboxOrderCreate))
	})
	t.Run("a sent row that cannot be completed is recognised later", func(t *testing.T) {
		e := newEnv(t)
		linked(e)
		id := e.queued(orderSpec{})
		e.drainWith(e.faultyWorker(newFaults("Outbox.MarkSent")))
		assert.Len(t, e.posts(), 1)
		assert.Equal(t, domain.OutboxSending, e.state(id, domain.OutboxOrderCreate))
		assert.Equal(t, domain.PushSent, e.link(id).PushState, "the link knows the order was sent")

		// The lease runs out: the next claim adopts the order instead of posting it again.
		e.clock.Advance(6 * time.Minute)
		e.drainAll()
		assert.Len(t, e.posts(), 1)
		assert.Equal(t, domain.OutboxSent, e.state(id, domain.OutboxOrderCreate))
	})
	t.Run("a link that cannot be recorded leaves the row to its lease", func(t *testing.T) {
		e := newEnv(t)
		linked(e)
		id := e.queued(orderSpec{})
		e.drainWith(e.faultyWorker(newFaults("Orders.MarkSent")))
		assert.Equal(t, domain.OutboxSending, e.state(id, domain.OutboxOrderCreate))
		e.clock.Advance(6 * time.Minute)
		e.drainAll()
		assert.Len(t, e.posts(), 1, "the lost answer is found through private_ref")
		assert.Equal(t, domain.PushSent, e.link(id).PushState)
	})
	t.Run("the flags and the total are best effort", func(t *testing.T) {
		e := newEnv(t) // no ref: an unlinked item
		id := e.queued(orderSpec{})
		e.drainWith(e.faultyWorker(newFaults("Orders.ClearAttention", "Orders.FlagAttention", "Orders.SetRemoteTotal", "Outbox.SkipPendingUpTo")))
		assert.Equal(t, domain.OutboxSent, e.state(id, domain.OutboxOrderCreate))
		assert.Equal(t, domain.PushSent, e.link(id).PushState)
	})
	t.Run("a row without a link is skipped even when the store refuses", func(t *testing.T) {
		e := newEnv(t)
		id := e.order(orderSpec{})
		e.rawRow("order_create", &id, `{"v":1}`)
		e.drainWith(e.faultyWorker(newFaults("Outbox.MarkSkipped")))
		assert.Equal(t, domain.OutboxSending, e.state(id, domain.OutboxOrderCreate))
	})
	t.Run("an unreadable payload parks the row even when the bookkeeping fails", func(t *testing.T) {
		e := newEnv(t)
		linked(e)
		id := e.queued(orderSpec{})
		e.exec(`UPDATE hubrise_outbox SET payload = '{"v":9}' WHERE order_id = $1`, id)
		e.drainWith(e.faultyWorker(newFaults("Outbox.MarkParked", "Orders.SetPushState", "Orders.FlagAttention", "Outbox.SkipPendingUpTo")))
		assert.Empty(t, e.posts())
	})
	t.Run("a reactivated chain is marked pending again, if the store allows", func(t *testing.T) {
		e := newEnv(t)
		linked(e)
		id := e.queued(orderSpec{})
		e.exec(`UPDATE hubrise_orders SET push_state = 'parked' WHERE order_id = $1`, id)
		e.drainWith(e.faultyWorker(newFaults("Orders.SetPushState")))
		assert.Len(t, e.posts(), 1)
	})
	t.Run("a link row that already knows the HubRise order only completes the queue row", func(t *testing.T) {
		e := newEnv(t)
		linked(e)
		id := e.queued(orderSpec{})
		e.exec(`UPDATE hubrise_orders SET push_state = 'sent', hubrise_order_id = 'known' WHERE order_id = $1`, id)
		e.drainAll()
		assert.Empty(t, e.posts())
		assert.Equal(t, domain.OutboxSent, e.state(id, domain.OutboxOrderCreate))
	})
}

func TestWorkerGivesRowsBackWhenAReadFails(t *testing.T) {
	cases := map[string][]string{
		"the link":              {"Orders.Get"},
		"the covered status id": {"Outbox.MaxPendingID"},
		"the order state":       {"Source.State"},
		"the product link":      {"Catalog.FindByLocal.product"},
		"the choice link":       {"Catalog.FindByLocal.choice"},
	}
	for name, fail := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
			spec := orderSpec{}
			if name == "the choice link" {
				salmon := e.fx.SalmonSushi.ID
				group := testhelpers.SeedChoiceGroup(t, e.db, salmon, 0, 2, map[string]string{"fr": "Sauce"})
				choice := testhelpers.SeedChoice(t, e.db, salmon, group, "0.50", map[string]string{"fr": "Soja"})
				spec = orderSpec{lines: []lineSpec{{
					product: salmon, qty: 1, total: "13.00", selections: []selSpec{{group, choice, 1}},
				}}, total: "13.00"}
			}
			id := e.queued(spec)
			e.drainWith(e.faultyWorker(newFaults(fail...)))
			assert.Empty(t, e.posts())
			rows := e.outboxRows(id, domain.OutboxOrderCreate)
			require.Len(t, rows, 1)
			assert.Equal(t, domain.OutboxPending, rows[0].State, "to be retried")
			assert.Contains(t, *rows[0].LastError, errStore.Error())
		})
	}
}

func TestWorkerLooksTheProductUpOncePerRun(t *testing.T) {
	e := newEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	lines := []lineSpec{
		{product: e.fx.SalmonSushi.ID, qty: 1, total: "12.50"},
		{product: e.fx.SalmonSushi.ID, qty: 1, total: "12.50"},
	}
	id := e.queued(orderSpec{lines: lines, total: "25.00"})
	e.drainAll()
	require.Equal(t, domain.PushSent, e.link(id).PushState)
	assert.Len(t, e.remote(id).Items, 2)
}

func TestWorkerUsesTheConfiguredChannelName(t *testing.T) {
	e := newEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	e.exec(`UPDATE hubrise_connection SET channel_name = 'My Shop'`)
	id := e.queued(orderSpec{})
	e.drainAll()
	assert.Equal(t, "My Shop", e.remote(id).Channel)
}

func TestWorkerLookupFailureBeforeAResend(t *testing.T) {
	e := newEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	id := e.queued(orderSpec{})
	e.exec(`UPDATE hubrise_outbox SET attempts = 1 WHERE order_id = $1`, id) // the claim makes it 2
	e.srv.AddFault(hubrisetest.Fault{Method: http.MethodGet, Path: "/v1/location/orders", Status: 500, Times: 1})
	e.drain()
	assert.Empty(t, e.posts(), "the lookup failed: no blind second POST")
	assert.Equal(t, domain.OutboxPending, e.state(id, domain.OutboxOrderCreate))
}

// stubHubrise answers every order call with the given order JSON, to produce answers the mock never
// gives (a total HubRise could not compute, an unreadable one).
func stubHubrise(t *testing.T, e *env, orderJSON string) clientProvider {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(orderJSON))
	}))
	t.Cleanup(srv.Close)
	c, err := pkghubrise.New(srv.URL+"/v1", "tok")
	require.NoError(t, err)
	return fixedClients{c: c}
}

func TestWorkerHandlesTotalsItCannotCompare(t *testing.T) {
	for name, body := range map[string]string{
		"no total":          `{"id":"stub-1","status":"new"}`,
		"an unreadable one": `{"id":"stub-1","status":"new","total":"not money"}`,
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
			id := e.queued(orderSpec{})
			e.drainWith(e.faultyWorker(newFaults(), stubHubrise(t, e, body)))
			link := e.link(id)
			assert.Equal(t, domain.PushSent, link.PushState)
			assert.Nil(t, link.NeedsAttention, "nothing to compare, nothing to flag")
		})
	}
}

func TestWorkerStatusRowsSurviveStoreFailures(t *testing.T) {
	setup := func(t *testing.T) (*env, uuid.UUID) {
		e := newEnv(t)
		e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
		id := e.queued(orderSpec{})
		e.drainAll()
		e.setStatus(id, "CONFIRMED")
		return e, id
	}
	t.Run("the link cannot be read", func(t *testing.T) {
		e, id := setup(t)
		e.drainWith(e.faultyWorker(newFaults("Orders.Get")))
		assert.Equal(t, domain.OutboxPending, e.state(id, domain.OutboxOrderStatus))
		assert.Empty(t, e.patches())
	})
	t.Run("the patch cannot be recorded", func(t *testing.T) {
		e, id := setup(t)
		e.drainWith(e.faultyWorker(newFaults("Orders.RecordLocalPatch")))
		assert.Equal(t, domain.OutboxSent, e.state(id, domain.OutboxOrderStatus))
		assert.Len(t, e.patches(), 1)
	})
}

func TestWorkerRecoversAMissingOrderDespiteStoreFailures(t *testing.T) {
	long := errors.New(strings.Repeat("x", 700))
	setup := func(t *testing.T, hide bool) (*env, uuid.UUID) {
		e := newEnv(t)
		e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
		id := e.queued(orderSpec{})
		e.drainAll()
		if hide {
			hideRemote(t, e, id)
		} else {
			e.exec(`UPDATE hubrise_orders SET hubrise_order_id = 'stale' WHERE order_id = $1`, id)
		}
		e.setStatus(id, "CONFIRMED")
		return e, id
	}
	pending := func(t *testing.T, e *env, id uuid.UUID, wantErr string) {
		rows := e.outboxRows(id, domain.OutboxOrderStatus)
		require.Len(t, rows, 1)
		assert.Equal(t, domain.OutboxPending, rows[0].State)
		assert.Contains(t, *rows[0].LastError, wantErr)
		assert.LessOrEqual(t, len(*rows[0].LastError), 500, "the stored error is bounded")
	}
	t.Run("the adopted id cannot be recorded", func(t *testing.T) {
		e, id := setup(t, false)
		f := newFaults()
		f.set("Orders.MarkSent", long)
		e.drainWith(e.faultyWorker(f))
		pending(t, e, id, "xxxx")
	})
	t.Run("the creates cannot be counted", func(t *testing.T) {
		e, id := setup(t, true)
		e.drainWith(e.faultyWorker(newFaults("Outbox.CountForOrder")))
		pending(t, e, id, errStore.Error())
	})
	t.Run("the order cannot be read to be created again", func(t *testing.T) {
		e, id := setup(t, true)
		e.drainWith(e.faultyWorker(newFaults("Source.Snapshot")))
		pending(t, e, id, errStore.Error())
	})
	t.Run("the new create cannot be queued", func(t *testing.T) {
		e, id := setup(t, true)
		e.drainWith(e.faultyWorker(newFaults("Orders.Recreate")))
		pending(t, e, id, errStore.Error())
	})
	t.Run("the lookup itself fails", func(t *testing.T) {
		e, id := setup(t, true)
		e.srv.AddFault(hubrisetest.Fault{Method: http.MethodGet, Path: "/v1/location/orders", Status: 500, Times: 1})
		e.drain()
		pending(t, e, id, "500")
	})
}

func TestPusherReportsAStoreThatIsDown(t *testing.T) {
	e := newEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	id := e.sentOrder(orderSpec{})
	p := e.faultyPusher(newFaults("Outbox.Enqueue"))
	require.ErrorIs(t, p.OrderStatusChanged(t.Context(), id, "CONFIRMED", uuid.Nil), errStore)
}

func TestPusherWithoutAWakeFunctionStillQueues(t *testing.T) {
	e := newEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	id := e.order(orderSpec{})
	p := application.NewOrderPusher(application.PusherDeps{
		Connections: e.mod.Connections, Orders: e.mod.Orders, Outbox: e.mod.Outbox, Source: e.mod.Source, Clock: e.clock,
	})
	require.NoError(t, p.OrderReady(t.Context(), id))
	assert.Len(t, e.outboxRows(id, domain.OutboxOrderCreate), 1)
}

func TestReconcilerSurvivesStoreFailures(t *testing.T) {
	t.Run("a status it cannot queue", func(t *testing.T) {
		e := newEnv(t)
		e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
		id := e.sentOrder(orderSpec{})
		e.exec(`UPDATE orders SET order_status = 'PREPARING' WHERE id = $1`, id)
		r := e.faultyReconciler(newFaults("Outbox.Enqueue"), nil)
		require.Error(t, r.RunOnce(t.Context()))
	})
	for _, name := range []string{"Orders.StampRemoteRead", "Orders.SetRemoteTotal", "Orders.FlagAttention", "Orders.ClearAttention"} {
		t.Run("a read it cannot record: "+name, func(t *testing.T) {
			e := newEnv(t)
			e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
			id := e.sentOrder(orderSpec{})
			remote := e.remote(id)
			// A remote total that differs, and an old flag that the accepted status clears.
			_, err := e.srv.PatchOrderAs(hubrisetest.EPOSToken, remote.ID, pkghubrise.OrderPatch{
				Status: pkghubrise.StatusAccepted,
				Items:  []pkghubrise.OrderItem{{ID: remote.Items[0].ID, Deleted: true}},
			})
			require.NoError(t, err)
			e.exec(`UPDATE hubrise_orders SET needs_attention = 'NOT_ACCEPTED' WHERE order_id = $1`, id)
			r := e.faultyReconciler(newFaults(name), nil)
			require.Error(t, r.RunOnce(t.Context()))
		})
	}
	t.Run("a flag it cannot set", func(t *testing.T) {
		e := newEnv(t)
		e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
		e.sentOrder(orderSpec{})
		e.clock.Advance(6 * time.Minute)
		r := e.faultyReconciler(newFaults("Orders.MarkNotAccepted"), nil)
		require.Error(t, r.RunOnce(t.Context()))
	})
	t.Run("an alert it cannot record", func(t *testing.T) {
		e := newEnv(t)
		e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
		e.sentOrder(orderSpec{})
		e.clock.Advance(6 * time.Minute)
		r := e.faultyReconciler(newFaults("Orders.MarkStuckAlerted"), nil)
		require.Error(t, r.RunOnce(t.Context()))
	})
	t.Run("a stuck-order query that fails", func(t *testing.T) {
		e := newEnv(t)
		r := e.faultyReconciler(newFaults("Source.StuckNew"), nil)
		require.Error(t, r.RunOnce(t.Context()))
	})
}
