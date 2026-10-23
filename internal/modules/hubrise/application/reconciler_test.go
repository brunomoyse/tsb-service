package application_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise"
	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/modules/hubrise/domain"
	hubtest "tsb-service/internal/modules/hubrise/testhelpers"
	pkghubrise "tsb-service/pkg/hubrise"
	"tsb-service/pkg/hubrise/hubrisetest"
)

// sentOrder pushes an order to the mock and returns its id.
func (e *env) sentOrder(spec orderSpec) uuid.UUID {
	e.t.Helper()
	id := e.order(spec)
	e.ready(id)
	e.drainAll()
	require.Equal(e.t, domain.PushSent, e.link(id).PushState)
	return id
}

func TestReconcilerQueuesTheOpenOrdersTheHookMissed(t *testing.T) {
	e := newEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	cash := e.order(orderSpec{})
	paid := e.order(orderSpec{online: true, paid: true, total: "12.80"})
	unpaid := e.order(orderSpec{online: true, total: "12.80"})
	test := e.order(orderSpec{test: true})
	cancelled := e.order(orderSpec{status: "CANCELLED"})
	failed := e.order(orderSpec{status: "FAILED"})
	delivered := e.order(orderSpec{status: "DELIVERED"})
	old := e.order(orderSpec{createdAgo: 3 * time.Hour})
	known := e.order(orderSpec{})
	e.ready(known) // the hook did run for this one

	require.NoError(t, e.push.Reconciler.RunOnce(t.Context()))
	e.drainAll()

	for name, id := range map[string]uuid.UUID{"cash": cash, "paid online": paid, "already queued": known} {
		_, ok := e.srv.OrderByPrivateRef(id.String())
		assert.True(t, ok, name)
	}
	for name, id := range map[string]uuid.UUID{
		"unpaid online": unpaid, "test": test, "cancelled": cancelled, "failed": failed, "delivered": delivered,
		"created before push_orders_since less the grace": old,
	} {
		_, ok := e.srv.OrderByPrivateRef(id.String())
		assert.False(t, ok, name)
		assert.Zero(t, e.count(`SELECT count(*) FROM hubrise_orders WHERE order_id = $1`, id), name)
	}
	assert.Len(t, e.posts(), 3, "the known order is not sent twice")

	// A second run finds nothing more to do.
	e.srv.ClearRequests()
	require.NoError(t, e.push.Reconciler.RunOnce(t.Context()))
	e.drainAll()
	assert.Empty(t, e.posts())
}

func TestReconcilerNeverReplaysHistoryWithoutAStamp(t *testing.T) {
	e := newEnv(t)
	_, err := e.db.ExecContext(t.Context(), `UPDATE hubrise_connection SET push_orders_since = NULL`)
	require.NoError(t, err)
	e.order(orderSpec{})
	require.NoError(t, e.push.Reconciler.RunOnce(t.Context()))
	assert.Zero(t, e.count(`SELECT count(*) FROM hubrise_orders`))
}

func TestReconcilerQueuesAMissingStatusChange(t *testing.T) {
	e := newEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	id := e.sentOrder(orderSpec{})
	// A writer that bypassed the observer.
	e.exec(`UPDATE orders SET order_status = 'PREPARING' WHERE id = $1`, id)

	require.NoError(t, e.push.Reconciler.RunOnce(t.Context()))
	e.drainAll()
	assert.Equal(t, pkghubrise.StatusInPreparation, e.remote(id).Status)
	assert.Equal(t, "in_preparation", *e.link(id).LastRemoteStatus)

	e.srv.ClearRequests()
	require.NoError(t, e.push.Reconciler.RunOnce(t.Context()))
	e.drainAll()
	assert.Empty(t, e.patches(), "in sync: nothing to do")
}

func TestReconcilerLeavesStatusMismatchesItMustNotTouch(t *testing.T) {
	e := newEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")

	t.Run("a status already queued", func(t *testing.T) {
		id := e.sentOrder(orderSpec{})
		e.setStatus(id, "CONFIRMED") // queued, not sent
		before := len(e.outboxRows(id, domain.OutboxOrderStatus))
		require.NoError(t, e.push.Reconciler.RunOnce(t.Context()))
		assert.Len(t, e.outboxRows(id, domain.OutboxOrderStatus), before)
	})
	t.Run("a parked status waits for a human", func(t *testing.T) {
		id := e.sentOrder(orderSpec{})
		e.setStatus(id, "CONFIRMED")
		e.exec(`UPDATE hubrise_outbox SET state = 'parked' WHERE order_id = $1`, id)
		require.NoError(t, e.push.Reconciler.RunOnce(t.Context()))
		assert.Len(t, e.outboxRows(id, domain.OutboxOrderStatus), 1)
	})
	t.Run("an order that is final at HubRise is not reopened", func(t *testing.T) {
		id := e.sentOrder(orderSpec{})
		e.exec(`UPDATE hubrise_orders SET last_remote_status = 'cancelled' WHERE order_id = $1`, id)
		e.exec(`UPDATE orders SET order_status = 'PREPARING' WHERE id = $1`, id)
		require.NoError(t, e.push.Reconciler.RunOnce(t.Context()))
		assert.Empty(t, e.outboxRows(id, domain.OutboxOrderStatus))
	})
	t.Run("a pending local order has no remote equivalent", func(t *testing.T) {
		id := e.sentOrder(orderSpec{})
		require.NoError(t, e.push.Reconciler.RunOnce(t.Context()))
		assert.Empty(t, e.outboxRows(id, domain.OutboxOrderStatus))
	})
}

type remoteHandler struct {
	seen []pkghubrise.Order
	err  error
	// record, when set, stores the remote status like the status sync does.
	record domain.OrderLinkRepository
}

func (h *remoteHandler) ApplyRemote(ctx context.Context, id uuid.UUID, remote *pkghubrise.Order) error {
	h.seen = append(h.seen, *remote)
	if h.record != nil {
		if err := h.record.RecordRemoteStatus(ctx, id, string(remote.Status), nil); err != nil {
			return err
		}
	}
	return h.err
}

func (e *env) getsOf() map[string]int {
	got := map[string]int{}
	for _, r := range e.srv.RequestsTo(http.MethodGet, "/v1/location/orders/") {
		got[r.Path]++
	}
	return got
}

func TestReconcilerRefreshesAtMostFiveOpenOrdersPerRunOldestFirst(t *testing.T) {
	e := newEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	var ids []uuid.UUID
	for range 7 {
		ids = append(ids, e.sentOrder(orderSpec{}))
	}
	e.srv.ClearRequests()

	require.NoError(t, e.push.Reconciler.RunOnce(t.Context()))
	first := e.getsOf()
	assert.Len(t, first, 5, "five different orders")

	e.clock.Advance(3 * time.Minute)
	e.srv.ClearRequests()
	require.NoError(t, e.push.Reconciler.RunOnce(t.Context()))
	second := e.getsOf()
	assert.Len(t, second, 5)
	covered := map[string]bool{}
	for p := range first {
		covered[p] = true
	}
	for p := range second {
		covered[p] = true
	}
	assert.Len(t, covered, 7, "the orders not read yet come first, then the ones read longest ago")
	for _, id := range ids {
		assert.Equal(t, 1, e.count(`SELECT count(*) FROM hubrise_orders WHERE order_id = $1 AND last_remote_read_at IS NOT NULL`, id), "stamped with the time of the read")
		assert.Nil(t, e.link(id).LastRemoteEventAt, "our clock is never the time of remote data")
	}
}

func TestReconcilerRefreshChecksTheRemoteTotalAndHandsOverAChangedStatus(t *testing.T) {
	handler := &remoteHandler{}
	e := newEnv(t, withPush(hubrise.PushOptions{Remote: handler}))
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	id := e.sentOrder(orderSpec{})
	remote := e.remote(id)

	// The EPOS accepts the order and deletes its only item.
	_, err := e.srv.PatchOrderAs(hubrisetest.EPOSToken, remote.ID, pkghubrise.OrderPatch{
		Status: pkghubrise.StatusAccepted,
		Items:  []pkghubrise.OrderItem{{ID: remote.Items[0].ID, Deleted: true}},
	})
	require.NoError(t, err)

	require.NoError(t, e.push.Reconciler.RunOnce(t.Context()))
	link := e.link(id)
	require.NotNil(t, link.NeedsAttention)
	assert.Equal(t, domain.AttentionTotalMismatch, *link.NeedsAttention)
	assert.Equal(t, "0", link.RemoteTotal.String())
	require.Len(t, handler.seen, 1, "the status sync gets the order HubRise now has")
	assert.Equal(t, pkghubrise.StatusAccepted, handler.seen[0].Status)
	assert.Equal(t, "new", *link.LastRemoteStatus, "recording the status is the status sync's job")

	// A failing handler is reported but does not stop the run.
	handler.err = errors.New("cannot apply")
	require.Error(t, e.push.Reconciler.RunOnce(t.Context()))
}

func TestReconcilerRefreshWithoutAHandlerOnlyStampsTheRead(t *testing.T) {
	e := newEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	id := e.sentOrder(orderSpec{})
	require.NoError(t, e.srv.SetOrderStatus(e.remote(id).ID, pkghubrise.StatusAccepted))
	require.NoError(t, e.push.Reconciler.RunOnce(t.Context()))
	link := e.link(id)
	assert.Equal(t, "new", *link.LastRemoteStatus)
	assert.Nil(t, link.LastRemoteEventAt, "our clock is never the time of remote data")
	assert.Equal(t, 1, e.count(`SELECT count(*) FROM hubrise_orders WHERE order_id = $1 AND last_remote_read_at IS NOT NULL`, id))
	assert.Nil(t, link.NeedsAttention)
}

func TestReconcilerRefreshFailures(t *testing.T) {
	t.Run("an order HubRise no longer knows is flagged", func(t *testing.T) {
		e := newEnv(t)
		e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
		id := e.sentOrder(orderSpec{})
		hideRemote(t, e, id)
		require.NoError(t, e.push.Reconciler.RunOnce(t.Context()))
		assert.Equal(t, domain.AttentionRemoteMissing, *e.link(id).NeedsAttention)
	})
	t.Run("a refused connection pauses the pipeline", func(t *testing.T) {
		e := newEnv(t)
		e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
		first := e.sentOrder(orderSpec{})
		e.sentOrder(orderSpec{})
		e.srv.ClearRequests()
		e.srv.AddFault(hubrisetest.Fault{Method: http.MethodGet, Path: "/v1/location/orders/", Status: 401})
		require.NoError(t, e.push.Reconciler.RunOnce(t.Context()))
		conn, err := e.mod.Connections.Get(t.Context())
		require.NoError(t, err)
		assert.NotNil(t, conn.AuthErrorAt)
		assert.Len(t, e.getsOf(), 1, "the second read was not attempted")
		_ = first
	})
	t.Run("another error is reported and the other orders are still read", func(t *testing.T) {
		e := newEnv(t)
		e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
		e.sentOrder(orderSpec{})
		e.sentOrder(orderSpec{})
		e.srv.ClearRequests()
		e.srv.AddFault(hubrisetest.Fault{Method: http.MethodGet, Path: "/v1/location/orders/", Status: 500, Times: 1})
		require.Error(t, e.push.Reconciler.RunOnce(t.Context()))
		assert.Len(t, e.srv.RequestsTo(http.MethodGet, "/v1/location/orders/"), 2)
	})
	t.Run("a client that cannot be built is reported", func(t *testing.T) {
		e := newEnv(t, withClients(func(*env) clientProvider { return brokenClients{} }))
		e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
		id := e.order(orderSpec{})
		e.exec(`INSERT INTO hubrise_orders (order_id, push_state, hubrise_order_id, last_remote_status) VALUES ($1, 'sent', 'x1', 'new')`, id)
		require.Error(t, e.push.Reconciler.RunOnce(t.Context()))
	})
}

func TestReconcilerReadsAreBackgroundCalls(t *testing.T) {
	limiter := pkghubrise.NewLimiter(pkghubrise.Limits{PerHour: 10, BackgroundShare: 0.6})
	e := newEnv(t, withClients(func(e *env) clientProvider {
		return fixedClients{c: e.srv.NewClient(t, pkghubrise.WithLimiter(limiter))}
	}))
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	id := e.sentOrder(orderSpec{}) // 1 critical call
	for range 5 {                  // 6 of the 10 hourly calls are used: the background share is spent
		_, err := e.srv.NewClient(t, pkghubrise.WithLimiter(limiter)).GetLocation(t.Context())
		require.NoError(t, err)
	}
	e.srv.ClearRequests()
	err := e.push.Reconciler.RunOnce(t.Context())
	require.Error(t, err, "the background read is refused by the limiter")
	assert.Empty(t, e.srv.RequestsTo(http.MethodGet, "/v1/location/orders/"), "and never sent")

	// A critical call still goes through.
	e.setStatus(id, "CONFIRMED")
	e.drainAll()
	assert.Equal(t, pkghubrise.StatusAccepted, e.remote(id).Status)
}

func TestReconcilerFlagsOrdersStuckInNew(t *testing.T) {
	alerts := &alertRecorder{}
	var closedFrom time.Time // the ordering hours end here; zero means they never do
	handler := &remoteHandler{}
	e := newEnv(t, withPush(hubrise.PushOptions{
		Alerter:  alerts,
		Schedule: scheduleFunc(func(at time.Time) bool { return closedFrom.IsZero() || at.Before(closedFrom) }),
		Remote:   handler,
	}))
	handler.record = e.mod.Orders
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	pushedAt := e.clock.Now()
	stuck := e.sentOrder(orderSpec{})
	unlinked := e.sentOrder(orderSpec{lines: []lineSpec{{product: e.fx.TunaSushi.ID, qty: 1, total: "14.00"}}, total: "14.00"})
	require.Equal(t, domain.AttentionUnlinkedItem, *e.link(unlinked).NeedsAttention)

	require.NoError(t, e.push.Reconciler.RunOnce(t.Context()))
	assert.Nil(t, e.link(stuck).NeedsAttention, "not before 5 minutes")

	e.clock.Advance(6 * time.Minute)
	closedFrom = pushedAt // the orders were pushed when ordering was closed
	require.NoError(t, e.push.Reconciler.RunOnce(t.Context()))
	assert.Nil(t, e.link(stuck).NeedsAttention, "an order nobody could accept outside the ordering hours is no alert")
	assert.Empty(t, alerts.subjects)

	// The ordering hours covered the push and ended since: the order is still waiting for somebody.
	closedFrom = pushedAt.Add(time.Minute)
	require.NoError(t, e.push.Reconciler.RunOnce(t.Context()))
	assert.Equal(t, domain.AttentionNotAccepted, *e.link(stuck).NeedsAttention)
	assert.Equal(t, domain.AttentionUnlinkedItem, *e.link(unlinked).NeedsAttention, "the more specific diagnosis is kept")
	require.Len(t, alerts.subjects, 2)
	assert.Contains(t, alerts.subjects[0], "not accepted")

	require.NoError(t, e.push.Reconciler.RunOnce(t.Context()))
	assert.Len(t, alerts.subjects, 2, "one alert per order")

	// Once HubRise moves the order on, the flag is lifted by the next read.
	require.NoError(t, e.srv.SetOrderStatus(e.remote(stuck).ID, pkghubrise.StatusAccepted))
	e.clock.Advance(time.Minute)
	for range 3 {
		require.NoError(t, e.push.Reconciler.RunOnce(t.Context()))
		e.clock.Advance(time.Minute)
	}
	assert.Nil(t, e.link(stuck).NeedsAttention)
}

// flakyAlerter fails until it is told to work.
type flakyAlerter struct {
	failing bool
	sent    []string
}

func (a *flakyAlerter) Alert(_ context.Context, subject, _ string) error {
	if a.failing {
		return errors.New("smtp down")
	}
	a.sent = append(a.sent, subject)
	return nil
}

func TestReconcilerRetriesAnAlertThatCouldNotBeSent(t *testing.T) {
	alerts := &flakyAlerter{failing: true}
	e := newEnv(t, withPush(hubrise.PushOptions{Alerter: alerts}))
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	id := e.sentOrder(orderSpec{})
	e.clock.Advance(6 * time.Minute)

	require.Error(t, e.push.Reconciler.RunOnce(t.Context()), "the mail provider is down")
	assert.Equal(t, domain.AttentionNotAccepted, *e.link(id).NeedsAttention, "the order is flagged anyway")
	assert.Zero(t, e.count(`SELECT count(*) FROM hubrise_orders WHERE order_id = $1 AND stuck_alerted_at IS NOT NULL`, id), "the alert is not recorded")

	alerts.failing = false
	require.NoError(t, e.push.Reconciler.RunOnce(t.Context()))
	require.Len(t, alerts.sent, 1, "the next run alerts")
	assert.Equal(t, 1, e.count(`SELECT count(*) FROM hubrise_orders WHERE order_id = $1 AND stuck_alerted_at IS NOT NULL`, id))

	require.NoError(t, e.push.Reconciler.RunOnce(t.Context()))
	assert.Len(t, alerts.sent, 1, "and only once")
}

func TestReconcilerDoesNotAlertWhileTheStatusIsNotFollowed(t *testing.T) {
	alerts := &alertRecorder{}
	handler := &remoteHandler{}
	e := newEnv(t, withPush(hubrise.PushOptions{Alerter: alerts, Remote: handler}))
	handler.record = e.mod.Orders
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	id := e.sentOrder(orderSpec{})
	// The EPOS accepted it at once, but with the status sync off nothing reads that back: the order
	// stays "new" for the shop and must not be reported as stuck.
	e.exec(`UPDATE hubrise_connection SET sync_status = false`)
	e.clock.Advance(10 * time.Minute)

	require.NoError(t, e.push.Reconciler.RunOnce(t.Context()))
	assert.Nil(t, e.link(id).NeedsAttention)
	assert.Empty(t, alerts.subjects)

	e.exec(`UPDATE hubrise_connection SET sync_status = true`)
	require.NoError(t, e.srv.SetOrderStatus(e.remote(id).ID, pkghubrise.StatusAccepted))
	require.NoError(t, e.push.Reconciler.RunOnce(t.Context()))
	assert.Empty(t, alerts.subjects, "followed again, the accepted order is no longer new")
}

// noPushTimeSource reports the stuck orders without their push time.
type noPushTimeSource struct{ domain.OrderSource }

func (s noPushTimeSource) StuckNew(ctx context.Context, before time.Time, limit int) ([]domain.OpenLink, error) {
	links, err := s.OrderSource.StuckNew(ctx, before, limit)
	for i := range links {
		links[i].PushedAt = nil
	}
	return links, err
}

func TestReconcilerJudgesAnOrderWithoutAPushTimeByTheCurrentHours(t *testing.T) {
	e := newEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	id := e.sentOrder(orderSpec{})
	e.clock.Advance(6 * time.Minute)
	var asked []time.Time
	r := application.NewReconciler(application.ReconcilerDeps{
		Connections: e.mod.Connections, Orders: e.mod.Orders, Source: noPushTimeSource{e.mod.Source},
		Leases: e.mod.Leases, Events: e.mod.Events, Outbox: e.mod.Outbox, Foreign: e.mod.Foreign, OAuthStates: e.mod.OAuthStates,
		Clients: e.opts.Clients, Pusher: e.push.Pusher, Clock: e.clock,
		Schedule: scheduleFunc(func(at time.Time) bool { asked = append(asked, at); return true }),
	})
	require.NoError(t, r.RunOnce(t.Context()))
	assert.Equal(t, []time.Time{e.clock.Now()}, asked)
	assert.Equal(t, domain.AttentionNotAccepted, *e.link(id).NeedsAttention)
}

func TestReconcilerFlagsWithoutAnAlerterOrASchedule(t *testing.T) {
	e := newEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	id := e.sentOrder(orderSpec{})
	e.clock.Advance(6 * time.Minute)
	require.NoError(t, e.push.Reconciler.RunOnce(t.Context()))
	assert.Equal(t, domain.AttentionNotAccepted, *e.link(id).NeedsAttention)
}

type failingAlerter struct{}

func (failingAlerter) Alert(context.Context, string, string) error { return errors.New("smtp down") }

type failingSchedule struct{}

func (failingSchedule) OrderingOpenAt(context.Context, time.Time) (bool, error) {
	return false, errors.New("no schedule")
}

func TestReconcilerReportsAlertAndScheduleFailures(t *testing.T) {
	e := newEnv(t, withPush(hubrise.PushOptions{Alerter: failingAlerter{}}))
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	e.sentOrder(orderSpec{})
	e.clock.Advance(6 * time.Minute)
	require.Error(t, e.push.Reconciler.RunOnce(t.Context()), "the alert could not be sent")

	e2 := newEnv(t, withPush(hubrise.PushOptions{Schedule: failingSchedule{}}))
	e2.linkProduct(e2.fx.SalmonSushi.ID, "SUSHI-SALMON")
	id := e2.sentOrder(orderSpec{})
	e2.clock.Advance(6 * time.Minute)
	require.Error(t, e2.push.Reconciler.RunOnce(t.Context()))
	assert.Nil(t, e2.link(id).NeedsAttention)
}

func TestReconcilerLeaseMakesItASingleton(t *testing.T) {
	e := newEnv(t)
	other := e.rebuild(hubrise.PushOptions{Reconciler: application.ReconcilerConfig{Holder: "replica-b"}})
	mine := e.rebuild(hubrise.PushOptions{Reconciler: application.ReconcilerConfig{Holder: "replica-a"}})

	ran, err := mine.Reconciler.Tick(t.Context())
	require.NoError(t, err)
	assert.True(t, ran)
	ran, err = other.Reconciler.Tick(t.Context())
	require.NoError(t, err)
	assert.False(t, ran, "the other replica waits")
	ran, err = mine.Reconciler.Tick(t.Context())
	require.NoError(t, err)
	assert.True(t, ran, "the holder renews its own lease")

	e.clock.Advance(3 * time.Minute)
	ran, err = other.Reconciler.Tick(t.Context())
	require.NoError(t, err)
	assert.True(t, ran, "after the lease expired the other replica takes over")
}

func TestReconcilerRunTicksUntilCanceled(t *testing.T) {
	e := newEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	r := e.rebuild(hubrise.PushOptions{Reconciler: application.ReconcilerConfig{Interval: 50 * time.Millisecond}})
	id := e.order(orderSpec{})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { r.Reconciler.Run(ctx); close(done) }()
	require.Eventually(t, func() bool {
		return e.count(`SELECT count(*) FROM hubrise_orders WHERE order_id = $1`, id) == 1
	}, 5*time.Second, 20*time.Millisecond)
	cancel()
	<-done
}

func TestReconcilerRunReportsFailuresAndKeepsGoing(t *testing.T) {
	e := newEnv(t)
	closed := e.td.ClosedConnection(t)
	broken := application.NewReconciler(application.ReconcilerDeps{
		Connections: e.mod.Connections, Orders: e.mod.Orders, Source: e.mod.Source,
		Leases: hubtestLeases{err: errors.New("db down")}, Events: e.mod.Events, Outbox: e.mod.Outbox, Foreign: e.mod.Foreign,
		OAuthStates: e.mod.OAuthStates, Clients: fixedClients{}, Pusher: e.push.Pusher, Clock: e.clock,
		Config: application.ReconcilerConfig{Interval: 20 * time.Millisecond},
	})
	_ = closed
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	broken.Run(ctx) // the lease errors are logged, the loop ends with the context
}

type hubtestLeases struct{ err error }

func (l hubtestLeases) Acquire(context.Context, string, string, time.Time, time.Duration) (bool, error) {
	return false, l.err
}
func (hubtestLeases) Release(context.Context, string, string) error { return nil }

func TestReconcilerPurgesOldFinishedWork(t *testing.T) {
	e := newEnv(t)
	old := e.clock.Now().Add(-10 * 24 * time.Hour)
	e.exec(`INSERT INTO hubrise_events (id, resource_type, event_type, state, received_at) VALUES ('old-done', 'order', 'update', 'done', $1)`, old)
	e.exec(`INSERT INTO hubrise_events (id, resource_type, event_type, state, received_at) VALUES ('old-pending', 'order', 'update', 'pending', $1)`, old)
	e.exec(`INSERT INTO hubrise_foreign_orders (hubrise_order_id, seen_at) VALUES ('foreign-old', $1), ('foreign-new', $2)`, old, e.clock.Now())
	e.exec(`INSERT INTO hubrise_oauth_states (state, admin_user_id, expires_at) VALUES ('s1', $1, $2)`, e.fx.AdminUser.ID, e.clock.Now().Add(-time.Hour))

	require.NoError(t, e.push.Reconciler.RunOnce(t.Context()))
	assert.Zero(t, e.count(`SELECT count(*) FROM hubrise_events WHERE id = 'old-done'`))
	assert.Equal(t, 1, e.count(`SELECT count(*) FROM hubrise_events WHERE id = 'old-pending'`), "pending work is never purged")
	assert.Zero(t, e.count(`SELECT count(*) FROM hubrise_foreign_orders WHERE hubrise_order_id = 'foreign-old'`))
	assert.Equal(t, 1, e.count(`SELECT count(*) FROM hubrise_foreign_orders WHERE hubrise_order_id = 'foreign-new'`))
	assert.Zero(t, e.count(`SELECT count(*) FROM hubrise_oauth_states`))
}

func TestReconcilerAppliesTheRetentionOfTheOutbox(t *testing.T) {
	e := newEnv(t)
	old := e.clock.Now().Add(-10 * 24 * time.Hour)
	order := e.order(orderSpec{})
	e.exec(`INSERT INTO hubrise_outbox (kind, order_id, dedupe_key, state, sent_at, created_at) VALUES ('order_status', $1, 'old', 'sent', $2, $2)`, order, old)
	e.exec(`INSERT INTO hubrise_outbox (kind, order_id, dedupe_key, state, sent_at, created_at) VALUES ('order_status', $1, 'new', 'sent', $2, $2)`, order, e.clock.Now())
	e.exec(`INSERT INTO hubrise_outbox (kind, order_id, state, payload, sent_at, created_at) VALUES ('order_create', $1, 'sent', '{"customer":{"email":"a@b.c"},"customer_notes":"x"}', $2, $2)`, order, old)

	require.NoError(t, e.push.Reconciler.RunOnce(t.Context()))
	assert.Zero(t, e.count(`SELECT count(*) FROM hubrise_outbox WHERE dedupe_key = 'old'`), "old finished rows go")
	assert.Equal(t, 1, e.count(`SELECT count(*) FROM hubrise_outbox WHERE dedupe_key = 'new'`))
	assert.Equal(t, 1, e.count(`SELECT count(*) FROM hubrise_outbox WHERE kind = 'order_create' AND payload = '{}'`), "the create row stays, without the customer")
}

func TestReconcilerDoesNothingWhileTheIntegrationIsOff(t *testing.T) {
	for name, opts := range map[string][]hubtest.SeedOption{
		"disabled": {hubtest.WithEnabled(false)},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, withSeed(opts...))
			e.order(orderSpec{})
			require.NoError(t, e.push.Reconciler.RunOnce(t.Context()))
			assert.Empty(t, e.srv.Requests())
			assert.Zero(t, e.count(`SELECT count(*) FROM hubrise_orders`))
		})
	}
	t.Run("a revoked token", func(t *testing.T) {
		e := newEnv(t)
		require.NoError(t, e.mod.Connections.RecordAuthError(t.Context(), e.clock.Now(), "revoked"))
		e.order(orderSpec{})
		require.NoError(t, e.push.Reconciler.RunOnce(t.Context()))
		assert.Zero(t, e.count(`SELECT count(*) FROM hubrise_orders`))
	})
}

func TestReconcilerStepsRespectTheirToggles(t *testing.T) {
	e := newEnv(t, withSeed(hubtest.WithFeatures(false, false, true, false, true)))
	e.order(orderSpec{})
	require.NoError(t, e.push.Reconciler.RunOnce(t.Context()))
	assert.Zero(t, e.count(`SELECT count(*) FROM hubrise_orders`), "orders are not pushed while their toggle is off")
	assert.Empty(t, e.srv.Requests(), "and nothing is read for the status sync")
}

func TestReconcilerReportsAStoreThatIsDown(t *testing.T) {
	e := newEnv(t)
	closed := e.td.ClosedConnection(t)
	reconcilerOn := func(mutate func(*application.ReconcilerDeps)) *application.Reconciler {
		d := application.ReconcilerDeps{
			Connections: e.mod.Connections, Orders: e.mod.Orders, Source: e.mod.Source, Leases: e.mod.Leases,
			Events: e.mod.Events, Outbox: e.mod.Outbox, Foreign: e.mod.Foreign, OAuthStates: e.mod.OAuthStates,
			Clients: e.opts.Clients, Pusher: e.push.Pusher, Clock: e.clock,
		}
		mutate(&d)
		return application.NewReconciler(d)
	}
	t.Run("connection", func(t *testing.T) {
		r := reconcilerOn(func(d *application.ReconcilerDeps) { d.Connections = infraConnections(closed) })
		require.Error(t, r.RunOnce(t.Context()))
	})
	t.Run("order source", func(t *testing.T) {
		r := reconcilerOn(func(d *application.ReconcilerDeps) { d.Source = infraSource(closed) })
		require.Error(t, r.RunOnce(t.Context()))
	})
	t.Run("purges", func(t *testing.T) {
		r := reconcilerOn(func(d *application.ReconcilerDeps) {
			d.Events, d.Foreign, d.OAuthStates = infraEvents(closed), infraForeign(closed), infraStates(closed)
		})
		require.Error(t, r.RunOnce(t.Context()))
	})
	t.Run("the outbox retention", func(t *testing.T) {
		r := reconcilerOn(func(d *application.ReconcilerDeps) { d.Outbox = infraOutbox(closed) })
		require.Error(t, r.RunOnce(t.Context()))
	})
	t.Run("queueing a missed order", func(t *testing.T) {
		e.order(orderSpec{})
		// The pusher's link store is down: the order cannot be queued, and the run says so.
		r := reconcilerOn(func(d *application.ReconcilerDeps) {
			d.Pusher = application.NewOrderPusher(application.PusherDeps{
				Connections: e.mod.Connections, Orders: infraOrders(closed), Outbox: e.mod.Outbox, Source: e.mod.Source, Clock: e.clock,
			})
		})
		require.Error(t, r.RunOnce(t.Context()))
	})
}
