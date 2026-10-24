package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/modules/hubrise/domain"
	pkghubrise "tsb-service/pkg/hubrise"
	"tsb-service/pkg/hubrise/hubrisetest"
)

var errBoom = errors.New("boom")

// injector says which repository calls fail: by method name, with an optional number of calls that
// succeed first.
type injector struct {
	mu    sync.Mutex
	fail  map[string]error
	after map[string]int
}

func newInjector() *injector { return &injector{fail: map[string]error{}, after: map[string]int{}} }

func (f *injector) on(method string, after ...int) *injector {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail[method] = errBoom
	if len(after) > 0 {
		f.after[method] = after[0]
	}
	return f
}

func (f *injector) onWith(method string, err error) *injector {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail[method] = err
	return f
}

func (f *injector) hit(method string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	err := f.fail[method]
	if err == nil {
		return nil
	}
	if f.after[method] > 0 {
		f.after[method]--
		return nil
	}
	return err
}

type faultyOrders struct {
	domain.OrderLinkRepository
	f *injector
}

func (o faultyOrders) Get(ctx context.Context, id uuid.UUID) (*domain.OrderLink, error) {
	if err := o.f.hit("Get"); err != nil {
		return nil, err
	}
	return o.OrderLinkRepository.Get(ctx, id)
}

func (o faultyOrders) FindByHubriseID(ctx context.Context, id string) (*domain.OrderLink, error) {
	if err := o.f.hit("FindByHubriseID"); err != nil {
		return nil, err
	}
	return o.OrderLinkRepository.FindByHubriseID(ctx, id)
}

func (o faultyOrders) MarkSent(ctx context.Context, id uuid.UUID, h, s string, at time.Time) error {
	if err := o.f.hit("MarkSent"); err != nil {
		return err
	}
	return o.OrderLinkRepository.MarkSent(ctx, id, h, s, at)
}

func (o faultyOrders) RecordRemoteStatus(ctx context.Context, id uuid.UUID, s string, at *time.Time) error {
	if err := o.f.hit("RecordRemoteStatus"); err != nil {
		return err
	}
	return o.OrderLinkRepository.RecordRemoteStatus(ctx, id, s, at)
}

func (o faultyOrders) ClearAttention(ctx context.Context, id uuid.UUID, codes ...string) error {
	if err := o.f.hit("ClearAttention"); err != nil {
		return err
	}
	return o.OrderLinkRepository.ClearAttention(ctx, id, codes...)
}

func (o faultyOrders) SetNeedsAttention(ctx context.Context, id uuid.UUID, code string) error {
	if err := o.f.hit("SetNeedsAttention"); err != nil {
		return err
	}
	return o.OrderLinkRepository.SetNeedsAttention(ctx, id, code)
}

func (o faultyOrders) FlagAttention(ctx context.Context, id uuid.UUID, code string) error {
	if err := o.f.hit("FlagAttention"); err != nil {
		return err
	}
	return o.OrderLinkRepository.FlagAttention(ctx, id, code)
}

func (o faultyOrders) HasUnsettledCreate(ctx context.Context) (bool, error) {
	if err := o.f.hit("HasUnsettledCreate"); err != nil {
		return false, err
	}
	return o.OrderLinkRepository.HasUnsettledCreate(ctx)
}

func (o faultyOrders) HasQueuedStatus(ctx context.Context, id uuid.UUID) (bool, error) {
	if err := o.f.hit("HasQueuedStatus"); err != nil {
		return false, err
	}
	return o.OrderLinkRepository.HasQueuedStatus(ctx, id)
}

func (o faultyOrders) RecordRemoteConfirmedTime(ctx context.Context, id uuid.UUID, t time.Time) error {
	if err := o.f.hit("RecordRemoteConfirmedTime"); err != nil {
		return err
	}
	return o.OrderLinkRepository.RecordRemoteConfirmedTime(ctx, id, t)
}

type faultySource struct {
	domain.OrderSource
	f *injector
}

func (s faultySource) State(ctx context.Context, id uuid.UUID) (*domain.OrderState, error) {
	if err := s.f.hit("State"); err != nil {
		return nil, err
	}
	return s.OrderSource.State(ctx, id)
}

type faultyLeases struct {
	domain.LeaseRepository
	f *injector
}

func (l faultyLeases) Acquire(ctx context.Context, name, holder string, now time.Time, ttl time.Duration) (bool, error) {
	if err := l.f.hit("Acquire"); err != nil {
		return false, err
	}
	return l.LeaseRepository.Acquire(ctx, name, holder, now, ttl)
}

func (l faultyLeases) Release(ctx context.Context, name, holder string) error {
	if err := l.f.hit("Release"); err != nil {
		return err
	}
	return l.LeaseRepository.Release(ctx, name, holder)
}

type faultyForeign struct {
	domain.ForeignOrderRepository
	f *injector
}

func (r faultyForeign) IsForeign(ctx context.Context, id string) (bool, error) {
	if err := r.f.hit("IsForeign"); err != nil {
		return false, err
	}
	return r.ForeignOrderRepository.IsForeign(ctx, id)
}

type faultyEvents struct {
	domain.EventRepository
	f *injector
}

func (r faultyEvents) Claim(ctx context.Context, now time.Time, lease time.Duration, limit int, types ...string) ([]domain.Event, error) {
	if err := r.f.hit("Claim"); err != nil {
		return nil, err
	}
	return r.EventRepository.Claim(ctx, now, lease, limit, types...)
}

func (r faultyEvents) MarkDone(ctx context.Context, id string, now time.Time) error {
	if err := r.f.hit("MarkDone"); err != nil {
		return err
	}
	return r.EventRepository.MarkDone(ctx, id, now)
}

func (r faultyEvents) Insert(ctx context.Context, e domain.Event) (bool, error) {
	if err := r.f.hit("Insert"); err != nil {
		return false, err
	}
	return r.EventRepository.Insert(ctx, e)
}

type faultyConns struct {
	domain.ConnectionRepository
	f *injector
}

func (c faultyConns) TouchLastEvent(ctx context.Context, at time.Time) error {
	if err := c.f.hit("TouchLastEvent"); err != nil {
		return err
	}
	return c.ConnectionRepository.TouchLastEvent(ctx, at)
}

func (c faultyConns) RecordAuthError(ctx context.Context, at time.Time, msg string) error {
	if err := c.f.hit("RecordAuthError"); err != nil {
		return err
	}
	return c.ConnectionRepository.RecordAuthError(ctx, at, msg)
}

// built is a status sync and a processor wired to the real repositories through fault injectors.
type built struct {
	*syncEnv
	f    *injector
	sync *application.StatusSync
	proc *application.EventProcessor
}

func (s *syncEnv) withFaults(cfg application.EventProcessorConfig) *built {
	f := newInjector()
	orders := faultyOrders{s.mod.Orders, f}
	status := application.NewStatusSync(application.StatusSyncDeps{
		Connections: s.mod.Connections, Orders: orders, Source: faultySource{s.mod.Source, f},
		Leases: faultyLeases{s.mod.Leases, f}, Transitioner: s.tr, Clock: s.clock, Holder: "faulty",
	})
	proc := application.NewEventProcessor(application.EventProcessorDeps{
		Connections: s.mod.Connections, Orders: orders, Events: faultyEvents{s.mod.Events, f},
		Foreign: faultyForeign{s.mod.Foreign, f}, Clients: s.push.Clients, Sync: status, Clock: s.clock, Config: cfg,
	})
	return &built{syncEnv: s, f: f, sync: status, proc: proc}
}

func (b *built) processOnce() int {
	b.t.Helper()
	n, err := b.proc.ProcessOnce(b.t.Context())
	require.NoError(b.t, err)
	return n
}

// ---- the status sync when the database fails ----

func TestTheStatusSyncReportsEveryDatabaseFailure(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{})
	delivery := s.pushed(orderSpec{orderType: "DELIVERY"})
	paid := s.pushed(orderSpec{online: true, paid: true, total: "12.80"})
	remote := func(order uuid.UUID, status pkghubrise.OrderStatus) *pkghubrise.Order {
		o := s.remote2(s.remote(order).ID)
		o.Status = status
		return o
	}

	for _, tc := range []struct {
		name   string
		method string
		order  uuid.UUID
		status pkghubrise.OrderStatus
		after  int
		setup  func()
	}{
		{"link lookup", "Get", id, pkghubrise.StatusAccepted, 0, nil},
		{"lease", "Acquire", id, pkghubrise.StatusAccepted, 0, nil},
		{"queued status check", "HasQueuedStatus", id, pkghubrise.StatusAccepted, 0, nil},
		{"record remote status", "RecordRemoteStatus", id, pkghubrise.StatusAccepted, 0, nil},
		{"clear flag", "ClearAttention", id, pkghubrise.StatusAccepted, 0, func() {
			s.exec(`UPDATE hubrise_orders SET needs_attention = 'NOT_ACCEPTED' WHERE order_id = $1`, id)
		}},
		{"order state", "State", id, pkghubrise.StatusAccepted, 0, nil},
		{"delivery failed flag", "SetNeedsAttention", delivery, pkghubrise.StatusDeliveryFailed, 0, nil},
		{"paid cancel flag", "SetNeedsAttention", paid, pkghubrise.StatusCancelled, 0, nil},
		{"conflict flag", "FlagAttention", id, pkghubrise.StatusInPreparation, 0, func() {
			s.exec(`UPDATE orders SET order_status = 'DELIVERED' WHERE id = $1`, id)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setup != nil {
				tc.setup()
			}
			b := s.withFaults(application.EventProcessorConfig{})
			b.f.on(tc.method, tc.after)
			err := b.sync.ApplyRemote(t.Context(), tc.order, remote(tc.order, tc.status))
			require.ErrorIs(t, err, errBoom)
		})
	}

	t.Run("record the remote confirmed time", func(t *testing.T) {
		timed := s.pushed(orderSpec{})
		o := remote(timed, pkghubrise.StatusReceived)
		ct := s.clock.Now().Add(20 * time.Minute)
		o.ConfirmedTime = &ct
		b := s.withFaults(application.EventProcessorConfig{})
		b.f.on("RecordRemoteConfirmedTime")
		require.ErrorIs(t, b.sync.ApplyRemote(t.Context(), timed, o), errBoom)
	})
	t.Run("an unknown order is nothing to do", func(t *testing.T) {
		b := s.withFaults(application.EventProcessorConfig{})
		require.NoError(t, b.sync.ApplyRemote(t.Context(), uuid.New(), remote(id, pkghubrise.StatusAccepted)))
	})
	t.Run("a vanished order is nothing to do", func(t *testing.T) {
		b := s.withFaults(application.EventProcessorConfig{})
		b.f.onWith("State", domain.ErrNotFound)
		require.NoError(t, b.sync.ApplyRemote(t.Context(), id, remote(id, pkghubrise.StatusAccepted)))
	})
	t.Run("a lease that cannot be released is only logged", func(t *testing.T) {
		b := s.withFaults(application.EventProcessorConfig{})
		b.f.on("Release")
		require.NoError(t, b.sync.ApplyRemote(t.Context(), id, remote(id, pkghubrise.StatusReceived)))
	})
	t.Run("a flag that cannot be set after a refused refund is only logged", func(t *testing.T) {
		s.exec(`UPDATE hubrise_connection SET auto_refund_on_cancel = TRUE`)
		b := s.withFaults(application.EventProcessorConfig{})
		b.f.on("SetNeedsAttention")
		s.tr.setErr(application.ErrNotRefundable)
		err := b.sync.ApplyRemote(t.Context(), paid, remote(paid, pkghubrise.StatusCancelled))
		require.ErrorIs(t, err, application.ErrNotRefundable)
	})
}

func TestTheMappingOfRemoteStatuses(t *testing.T) {
	for _, tc := range []struct {
		remote pkghubrise.OrderStatus
		kind   string
		want   string
	}{
		{pkghubrise.StatusNew, "PICKUP", ""},
		{pkghubrise.StatusReceived, "PICKUP", ""},
		{pkghubrise.StatusAccepted, "PICKUP", "CONFIRMED"},
		{pkghubrise.StatusInPreparation, "DELIVERY", "PREPARING"},
		{pkghubrise.StatusAwaitingCollection, "PICKUP", "AWAITING_PICK_UP"},
		{pkghubrise.StatusAwaitingShipment, "PICKUP", "AWAITING_PICK_UP"},
		{pkghubrise.StatusAwaitingCollection, "DELIVERY", ""},
		{pkghubrise.StatusInDelivery, "DELIVERY", "OUT_FOR_DELIVERY"},
		{pkghubrise.StatusCompleted, "PICKUP", "PICKED_UP"},
		{pkghubrise.StatusCompleted, "DELIVERY", "DELIVERED"},
		{pkghubrise.StatusCancelled, "PICKUP", "CANCELLED"},
		{pkghubrise.StatusRejected, "DELIVERY", "CANCELLED"},
		{pkghubrise.StatusDeliveryFailed, "DELIVERY", ""},
	} {
		assert.Equal(t, tc.want, application.LocalStatusFor(tc.remote, tc.kind), "%s %s", tc.remote, tc.kind)
	}
}

// ---- the processor when the database fails ----

func (s *syncEnv) rawEvent(id string, ev pkghubrise.Event) {
	s.t.Helper()
	ev.ID = id
	payload, err := json.Marshal(ev)
	require.NoError(s.t, err)
	s.exec(`INSERT INTO hubrise_events (id, resource_type, event_type, payload, received_at) VALUES ($1, 'order', 'update', $2::jsonb, $3)`,
		id, payload, s.clock.Now())
}

func (s *syncEnv) updateEvent(orderRemoteID string, status pkghubrise.OrderStatus) pkghubrise.Event {
	return pkghubrise.Event{
		ResourceType: "order", EventType: "update", LocationID: hubrisetest.LocationID, OrderID: orderRemoteID,
		CreatedAt: s.clock.Now(), NewState: json.RawMessage(`{"id":"` + orderRemoteID + `","status":"` + string(status) + `"}`),
	}
}

func TestTheProcessorRetriesWhenTheDatabaseFails(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{})
	remoteID := s.remote(id).ID

	for _, tc := range []struct{ name, method string }{
		{"the link lookup", "FindByHubriseID"},
		{"the apply", "RecordRemoteStatus"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s.clock.Advance(time.Minute)
			evID := "ev-" + tc.method
			s.rawEvent(evID, s.updateEvent(remoteID, pkghubrise.StatusAccepted))
			b := s.withFaults(application.EventProcessorConfig{})
			b.f.on(tc.method)
			b.processOnce()
			ev := s.event(evID)
			assert.Equal(t, domain.EventPending, ev.State)
			assert.Equal(t, 1, ev.Attempts)
			require.NotNil(t, ev.LastError)
			assert.Contains(t, *ev.LastError, "boom")
		})
	}
}

func TestTheProcessorRetriesWhenItCannotIdentifyAnOrder(t *testing.T) {
	// An order of ours HubRise knows but we never recorded: its link has no HubRise id and no create is queued.
	setup := func(t *testing.T) (*syncEnv, uuid.UUID, string) {
		s := newSyncEnv(t)
		id := s.order(orderSpec{})
		s.exec(`INSERT INTO hubrise_orders (order_id, push_state) VALUES ($1, 'pending')`, id)
		o, err := s.srv.NewClient(t).CreateOrder(t.Context(), &pkghubrise.Order{
			Status: pkghubrise.StatusNew, ServiceType: pkghubrise.ServiceCollection, PrivateRef: id.String(),
		})
		require.NoError(t, err)
		s.clock.Advance(time.Minute)
		s.rawEvent("ev-1", s.updateEvent(o.ID, pkghubrise.StatusAccepted))
		return s, id, o.ID
	}

	for _, tc := range []struct {
		name, method string
		after        int
	}{
		{"the foreign cache", "IsForeign", 0},
		{"the unsettled create check", "HasUnsettledCreate", 0},
		{"the local order lookup", "Get", 0},
		{"the adoption", "MarkSent", 0},
		{"the link read after the adoption", "Get", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := setup(t)
			b := s.withFaults(application.EventProcessorConfig{})
			b.f.on(tc.method, tc.after)
			b.processOnce()
			ev := s.event("ev-1")
			assert.Equal(t, domain.EventPending, ev.State)
			assert.Equal(t, 1, ev.Attempts)
		})
	}

	t.Run("without a fault the order is adopted and the event applied", func(t *testing.T) {
		s, id, remoteID := setup(t)
		b := s.withFaults(application.EventProcessorConfig{})
		b.processOnce()
		assert.Equal(t, domain.EventDone, s.event("ev-1").State)
		require.NotNil(t, s.link(id).HubriseOrderID)
		assert.Equal(t, remoteID, *s.link(id).HubriseOrderID)
	})
}

func TestTheProcessorOnlyLogsAnEventItCannotClose(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{})
	s.rawEvent("ev-1", s.updateEvent(s.remote(id).ID, pkghubrise.StatusAccepted))
	b := s.withFaults(application.EventProcessorConfig{})
	b.f.on("MarkDone")
	b.processOnce()
	assert.Equal(t, "CONFIRMED", s.localStatus(id))
}

func TestTheProcessorLoopSurvivesAFailingClaimAndKeepsTicking(t *testing.T) {
	s := newSyncEnv(t)
	b := s.withFaults(application.EventProcessorConfig{})
	var claims atomic.Int32
	b.f.on("Claim")
	proc := application.NewEventProcessor(application.EventProcessorDeps{
		Connections: s.mod.Connections, Orders: s.mod.Orders, Events: claimCounter{faultyEvents{s.mod.Events, b.f}, &claims},
		Foreign: s.mod.Foreign, Clients: s.push.Clients, Sync: b.sync, Clock: s.clock,
		Config: application.EventProcessorConfig{Tick: 10 * time.Millisecond},
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { proc.Run(ctx); close(done) }()
	require.Eventually(t, func() bool { return claims.Load() >= 3 }, 5*time.Second, 5*time.Millisecond, "it ticks on after a failure")
	cancel()
	<-done
}

type claimCounter struct {
	faultyEvents
	n *atomic.Int32
}

func (c claimCounter) Claim(ctx context.Context, now time.Time, lease time.Duration, limit int, types ...string) ([]domain.Event, error) {
	c.n.Add(1)
	return c.faultyEvents.Claim(ctx, now, lease, limit, types...)
}

func TestTheProcessorDrainsEveryFullBatchAndThenStops(t *testing.T) {
	s := newSyncEnv(t)
	s.sync = s.rebuildSync(application.EventProcessorConfig{BatchSize: 2, Tick: time.Hour})
	for i := range 5 { // foreign, ignored events: nothing to call
		s.rawEvent(uuid.NewString()+"-"+string(rune('a'+i)), s.updateEvent("", pkghubrise.StatusAccepted))
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { s.sync.Processor.Run(ctx); close(done) }()
	require.Eventually(t, func() bool { return s.count(`SELECT count(*) FROM hubrise_events WHERE state = 'pending'`) == 0 }, 5*time.Second, 10*time.Millisecond)
	cancel()
	<-done
	assert.Equal(t, 5, s.count(`SELECT count(*) FROM hubrise_events WHERE state = 'ignored'`))
}

func TestAnEventJustBeforeOurPatchIsReadAgain(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{})
	s.setStatus(id, "CONFIRMED")
	s.drainAll() // PATCH at the current time
	require.NoError(t, s.srv.SetOrderStatus(s.remote(id).ID, pkghubrise.StatusInPreparation))
	s.srv.ClearRequests()
	ev := s.updateEvent(s.remote(id).ID, pkghubrise.StatusInPreparation)
	ev.CreatedAt = s.clock.Now().Add(-2 * time.Second) // 2 s before our PATCH
	s.rawEvent("ev-1", ev)
	s.process()
	assert.Len(t, s.gets(), 1)
	assert.Equal(t, "PREPARING", s.localStatus(id), "what HubRise has now")
}

// ---- the poller when the database fails, and with an API that sends what the mock never does ----

func TestThePollerKeepsWhatItCouldNotStore(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{})
	s.register("")
	s.clock.Advance(time.Minute)
	s.eposQuiet(id, pkghubrise.StatusAccepted)
	f := newInjector().on("Insert")
	poller := application.NewCallbackPoller(application.CallbackPollerDeps{
		Connections: s.mod.Connections, Events: faultyEvents{s.mod.Events, f}, Leases: s.mod.Leases,
		Clients: s.push.Clients, Clock: s.clock,
	})
	require.ErrorIs(t, poller.PollOnce(t.Context()), errBoom)
	assert.Len(t, s.queued(), 1, "not acknowledged: it is listed again next time")
}

func TestThePollerReportsAFailedStampOfTheAuthError(t *testing.T) {
	s := newSyncEnv(t)
	s.srv.AddFault(hubrisetest.Fault{Method: http.MethodGet, Path: "/v1/callback/events", Status: http.StatusUnauthorized})
	f := newInjector().on("RecordAuthError")
	poller := application.NewCallbackPoller(application.CallbackPollerDeps{
		Connections: faultyConns{s.mod.Connections, f}, Events: s.mod.Events, Leases: s.mod.Leases,
		Clients: s.push.Clients, Clock: s.clock,
	})
	err := poller.PollOnce(t.Context())
	require.ErrorIs(t, err, errBoom)
}

func TestThePollerSkipsEventsOfKindsItNeverRegisteredFor(t *testing.T) {
	// A HubRise that lists a customer event next to an order one.
	var acked []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/callback/events":
			_, _ = w.Write([]byte(`[
				{"id":"c1","resource_type":"customer","event_type":"update","location_id":"` + hubrisetest.LocationID + `"},
				{"id":"o1","resource_type":"order","event_type":"update","location_id":"` + hubrisetest.LocationID + `","order_id":"x"}]`))
		case r.Method == http.MethodDelete:
			acked = append(acked, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(api.Close)
	client, err := pkghubrise.New(api.URL+"/v1", "tok", pkghubrise.WithLimiter(pkghubrise.NewLimiter(pkghubrise.Limits{})))
	require.NoError(t, err)

	s := newSyncEnv(t)
	poller := application.NewCallbackPoller(application.CallbackPollerDeps{
		Connections: s.mod.Connections, Events: s.mod.Events, Leases: s.mod.Leases,
		Clients: fixedClients{c: client}, Clock: s.clock, Wake: []func(){func() {}},
	})
	require.NoError(t, poller.PollOnce(t.Context()))
	assert.Equal(t, 1, s.count(`SELECT count(*) FROM hubrise_events`), "only the order event is kept")
	assert.Len(t, acked, 2, "both are acknowledged")
}

func TestThePollerTicksUntilItsContextEnds(t *testing.T) {
	s := newSyncEnv(t)
	var polls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		polls.Add(1)
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(api.Close)
	client, err := pkghubrise.New(api.URL+"/v1", "tok", pkghubrise.WithLimiter(pkghubrise.NewLimiter(pkghubrise.Limits{})))
	require.NoError(t, err)
	poller := application.NewCallbackPoller(application.CallbackPollerDeps{
		Connections: s.mod.Connections, Events: s.mod.Events, Leases: s.mod.Leases,
		Clients: fixedClients{c: client}, Clock: realClock{}, Config: application.CallbackPollerConfig{Interval: 20 * time.Millisecond},
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { poller.Run(ctx); close(done) }()
	require.Eventually(t, func() bool { return polls.Load() >= 1 }, 5*time.Second, 5*time.Millisecond)
	time.Sleep(60 * time.Millisecond)
	cancel()
	<-done
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func TestTheReceiverStillAcceptsAnEventWhenItCannotStampTheLastEvent(t *testing.T) {
	s := newSyncEnv(t)
	f := newInjector().on("TouchLastEvent")
	receiver := application.NewCallbackReceiver(hubrisetest.ClientSecret, faultyConns{s.mod.Connections, f}, s.mod.Events, s.clock)
	ev := s.updateEvent("x", pkghubrise.StatusAccepted)
	ev.ID = "e-stamp"
	body, _ := json.Marshal(ev)
	outcome, err := receiver.Receive(t.Context(), body, pkghubrise.Sign(hubrisetest.ClientSecret, body))
	require.NoError(t, err)
	assert.Equal(t, application.EventStored, outcome)
	outcome, err = receiver.Receive(t.Context(), body, pkghubrise.Sign(hubrisetest.ClientSecret, body))
	require.NoError(t, err)
	assert.Equal(t, application.EventDuplicate, outcome)
}
