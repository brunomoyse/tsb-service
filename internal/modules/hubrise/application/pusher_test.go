package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/internal/modules/hubrise/infrastructure"
	hubtest "tsb-service/internal/modules/hubrise/testhelpers"
)

func TestOrderReadyIsIdempotent(t *testing.T) {
	e := newEnv(t)
	id := e.order(orderSpec{})
	for range 3 { // a Mollie webhook delivered three times
		e.ready(id)
	}
	assert.Len(t, e.outboxRows(id, domain.OutboxOrderCreate), 1)
	assert.Equal(t, 1, e.count(`SELECT count(*) FROM hubrise_orders WHERE order_id = $1`, id))
}

func TestOrderReadyConcurrentCallsQueueOnce(t *testing.T) {
	e := newEnv(t)
	id := e.order(orderSpec{})
	done := make(chan struct{})
	for range 6 {
		go func() {
			defer func() { done <- struct{}{} }()
			assert.NoError(t, e.push.Pusher.OrderReady(t.Context(), id))
		}()
	}
	for range 6 {
		<-done
	}
	assert.Len(t, e.outboxRows(id, domain.OutboxOrderCreate), 1)
}

func TestOrderReadyIgnoresWhatMustNeverBePushed(t *testing.T) {
	e := newEnv(t)
	for name, spec := range map[string]orderSpec{
		"a test order":      {test: true},
		"a cancelled order": {status: "CANCELLED"},
		"a failed order":    {status: "FAILED"},
		"a delivered order": {status: "DELIVERED"},
	} {
		t.Run(name, func(t *testing.T) {
			e.ready(e.order(spec))
			assert.Zero(t, e.count(`SELECT count(*) FROM hubrise_orders`))
		})
	}
}

func TestOrderReadyReportsMissingOrdersAndStoreFailures(t *testing.T) {
	e := newEnv(t)
	require.Error(t, e.push.Pusher.OrderReady(t.Context(), uuid.New()), "an order that does not exist cannot be snapshotted")

	closed := e.td.ClosedConnection(t)
	broken := application.NewOrderPusher(application.PusherDeps{
		Connections: infrastructure.NewConnectionRepository(closed), Orders: e.mod.Orders, Outbox: e.mod.Outbox,
		Source: e.mod.Source, Clock: e.clock,
	})
	require.Error(t, broken.OrderReady(t.Context(), uuid.New()), "the connection cannot be read")
	require.Error(t, broken.OrderStatusChanged(t.Context(), uuid.New(), "CONFIRMED", uuid.New()))

	linksDown := application.NewOrderPusher(application.PusherDeps{
		Connections: e.mod.Connections, Orders: infrastructure.NewOrderLinkRepository(closed), Outbox: e.mod.Outbox,
		Source: e.mod.Source, Clock: e.clock,
	})
	require.Error(t, linksDown.OrderReady(t.Context(), e.order(orderSpec{})), "the link lookup fails")
	require.Error(t, linksDown.OrderStatusChanged(t.Context(), uuid.New(), "CONFIRMED", uuid.New()))
}

func TestOrderReadyDoesNotFailWhenTheEnqueueDoes(t *testing.T) {
	e := newEnv(t)
	id := e.order(orderSpec{})
	closed := e.td.ClosedConnection(t)
	p := application.NewOrderPusher(application.PusherDeps{
		Connections: e.mod.Connections, Orders: &failingEnqueue{OrderLinkRepository: infrastructure.NewOrderLinkRepository(e.db)},
		Outbox: infrastructure.NewOutboxRepository(closed), Source: e.mod.Source, Clock: e.clock,
	})
	require.Error(t, p.OrderReady(t.Context(), id), "the failure is returned for the hook helper to log")
	assert.Zero(t, e.count(`SELECT count(*) FROM hubrise_orders`))
}

type failingEnqueue struct{ domain.OrderLinkRepository }

func (failingEnqueue) EnqueueCreate(context.Context, uuid.UUID, json.RawMessage, time.Time) (bool, error) {
	return false, errors.New("boom")
}

func TestStatusChangesAreQueuedPerTransition(t *testing.T) {
	e := newEnv(t)
	id := e.order(orderSpec{})
	e.ready(id)
	e.setStatus(id, "CONFIRMED")
	e.setStatus(id, "CONFIRMED") // the same target twice is still two transitions for the observer
	rows := e.outboxRows(id, domain.OutboxOrderStatus)
	require.Len(t, rows, 2, "a pending create does not stop the status rows: they wait behind it")
	assert.NotEqual(t, *rows[0].DedupeKey, *rows[1].DedupeKey)
	assert.Contains(t, string(rows[0].Payload), "CONFIRMED")
}

func TestStatusChangesWithoutAHistoryRowStillGetUniqueKeys(t *testing.T) {
	e := newEnv(t)
	id := e.order(orderSpec{})
	e.ready(id)
	for range 2 {
		require.NoError(t, e.push.Pusher.OrderStatusChanged(t.Context(), id, "CONFIRMED", uuid.Nil))
	}
	rows := e.outboxRows(id, domain.OutboxOrderStatus)
	require.Len(t, rows, 2)
	assert.NotEqual(t, *rows[0].DedupeKey, *rows[1].DedupeKey)
}

func TestStatusChangesAreIgnoredWhenTheyMustNotBePushed(t *testing.T) {
	e := newEnv(t)
	// PENDING and FAILED are never pushed.
	id := e.order(orderSpec{})
	e.ready(id)
	require.NoError(t, e.push.Pusher.OrderStatusChanged(t.Context(), id, "PENDING", uuid.New()))
	require.NoError(t, e.push.Pusher.OrderStatusChanged(t.Context(), id, "FAILED", uuid.New()))
	assert.Empty(t, e.outboxRows(id, domain.OutboxOrderStatus))

	// An order that was never pushed has no link: nothing is queued.
	other := e.order(orderSpec{test: true})
	e.ready(other)
	require.NoError(t, e.push.Pusher.OrderStatusChanged(t.Context(), other, "CONFIRMED", uuid.New()))
	assert.Empty(t, e.outboxRows(other, domain.OutboxOrderStatus))

	// A parked or skipped push queues nothing either.
	for _, state := range []domain.PushState{domain.PushParked, domain.PushSkipped} {
		require.NoError(t, e.mod.Orders.SetPushState(t.Context(), id, state))
		require.NoError(t, e.push.Pusher.OrderStatusChanged(t.Context(), id, "CONFIRMED", uuid.New()))
		assert.Empty(t, e.outboxRows(id, domain.OutboxOrderStatus), string(state))
	}

	// The status toggle off.
	_, err := e.mod.Connections.UpdateSettings(t.Context(), domain.SettingsPatch{SyncStatus: ptr(false)})
	require.NoError(t, err)
	require.NoError(t, e.mod.Orders.SetPushState(t.Context(), id, domain.PushSent))
	require.NoError(t, e.push.Pusher.OrderStatusChanged(t.Context(), id, "CONFIRMED", uuid.New()))
	assert.Empty(t, e.outboxRows(id, domain.OutboxOrderStatus))
}

func TestASettingsChangeIsSeenByTheHooksAtOnce(t *testing.T) {
	e := newEnv(t)
	first := e.order(orderSpec{})
	e.ready(first)
	assert.Len(t, e.outboxRows(first, domain.OutboxOrderCreate), 1)

	// No row is queued after the integration was switched off, not even within a cache window.
	_, err := e.mod.Connections.UpdateSettings(t.Context(), domain.SettingsPatch{Enabled: ptr(false)})
	require.NoError(t, err)
	second := e.order(orderSpec{})
	e.ready(second)
	assert.Empty(t, e.outboxRows(second, domain.OutboxOrderCreate))
	assert.Zero(t, e.count(`SELECT count(*) FROM hubrise_orders WHERE order_id = '`+second.String()+`'`))

	_, err = e.mod.Connections.UpdateSettings(t.Context(), domain.SettingsPatch{Enabled: ptr(true), PushOrders: ptr(false)})
	require.NoError(t, err)
	third := e.order(orderSpec{})
	e.ready(third)
	assert.Empty(t, e.outboxRows(third, domain.OutboxOrderCreate), "the push_orders toggle bites at once too")

	_, err = e.mod.Connections.UpdateSettings(t.Context(), domain.SettingsPatch{PushOrders: ptr(true)})
	require.NoError(t, err)
	fourth := e.order(orderSpec{})
	e.ready(fourth)
	assert.Len(t, e.outboxRows(fourth, domain.OutboxOrderCreate), 1)
}

func TestTheWakeFunctionIsCalledWhenARowIsQueued(t *testing.T) {
	e := newEnv(t)
	woken := 0
	e.push.Pusher.SetWake(func() { woken++ })
	id := e.order(orderSpec{})
	e.ready(id)
	e.ready(id) // not queued again: no wake-up
	e.setStatus(id, "CONFIRMED")
	assert.Equal(t, 2, woken)
	e.push.Pusher.SetWake(nil) // a nil function is replaced by a no-op
	e.setStatus(id, "PREPARING")
}

func TestPooledClients(t *testing.T) {
	e := newEnv(t, withSeed(hubtest.WithToken(testTokenForMock())))
	conn, err := e.mod.Connections.Get(t.Context())
	require.NoError(t, err)

	limiter := application.NewPooledClients(e.mod.Codec, e.srv.APIBaseURL(), e.srv.HTTPClient(), nil)
	c1, err := limiter.Client(t.Context(), conn)
	require.NoError(t, err)
	c2, err := limiter.Client(t.Context(), conn)
	require.NoError(t, err)
	assert.Same(t, c1, c2, "the client is kept while the token does not change")
	loc, err := c1.GetLocation(t.Context())
	require.NoError(t, err)
	assert.NotEmpty(t, loc.ID, "the stored token opens the mock")

	// A new token builds a new client.
	hubtest.SeedConnection(t, e.db, hubtest.WithToken(testTokenForMock()))
	other, err := e.mod.Connections.Get(t.Context())
	require.NoError(t, err)
	c3, err := limiter.Client(t.Context(), other)
	require.NoError(t, err)
	assert.NotSame(t, c1, c3)

	// No token, a bad base URL, a token that cannot be opened.
	_, err = limiter.Client(t.Context(), &domain.Connection{})
	assert.ErrorIs(t, err, domain.ErrNotConnected)
	badURL := application.NewPooledClients(e.mod.Codec, "not a url", nil, nil)
	_, err = badURL.Client(t.Context(), conn)
	assert.Error(t, err)
	garbage := &domain.Connection{AccessTokenEnc: []byte("garbage")}
	_, err = limiter.Client(t.Context(), garbage)
	assert.Error(t, err)
}

func testTokenForMock() string { return "tsb-access-token" }
