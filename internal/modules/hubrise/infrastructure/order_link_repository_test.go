package infrastructure_test

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/testhelpers"
	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/internal/modules/hubrise/infrastructure"
)

func TestOrderLinkEnqueueCreateIsIdempotent(t *testing.T) {
	e := newEnv(t)
	links := infrastructure.NewOrderLinkRepository(e.db)
	outbox := infrastructure.NewOutboxRepository(e.db)
	ctx := t.Context()
	now := time.Now().Truncate(time.Microsecond)
	order := e.newOrder(t)

	// A Mollie webhook redelivered three times enqueues exactly one push.
	enqueued := 0
	for range 3 {
		ok, err := links.EnqueueCreate(ctx, order, json.RawMessage(`{"items":[]}`), now)
		require.NoError(t, err)
		if ok {
			enqueued++
		}
	}
	assert.Equal(t, 1, enqueued)

	rows, err := outbox.List(ctx, nil, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, domain.OutboxOrderCreate, rows[0].Kind)
	assert.Equal(t, order, *rows[0].OrderID)
	assert.JSONEq(t, `{"items":[]}`, string(rows[0].Payload))

	link, err := links.Get(ctx, order)
	require.NoError(t, err)
	assert.Equal(t, domain.PushPending, link.PushState)
	assert.Nil(t, link.HubriseOrderID)
	assert.Nil(t, link.NeedsAttention)
}

func TestOrderLinkEnqueueCreateConcurrent(t *testing.T) {
	e := newEnv(t)
	links := infrastructure.NewOrderLinkRepository(e.db)
	order := e.newOrder(t)

	var mu sync.Mutex
	won := 0
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			ok, err := links.EnqueueCreate(t.Context(), order, nil, time.Now())
			if err == nil && ok {
				mu.Lock()
				won++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	assert.Equal(t, 1, won)
	var n int
	require.NoError(t, e.db.GetContext(t.Context(), &n, `SELECT count(*) FROM hubrise_outbox WHERE order_id = $1`, order))
	assert.Equal(t, 1, n, "one outbox row, however many callers raced")
}

func TestOrderLinkEnqueueCreateFailures(t *testing.T) {
	ctx := t.Context()
	now := time.Now()

	t.Run("a nil payload is stored as {}", func(t *testing.T) {
		e := newEnv(t)
		links := infrastructure.NewOrderLinkRepository(e.db)
		order := e.newOrder(t)
		ok, err := links.EnqueueCreate(ctx, order, nil, now)
		require.NoError(t, err)
		assert.True(t, ok)
		var payload string
		require.NoError(t, e.db.GetContext(ctx, &payload, `SELECT payload::text FROM hubrise_outbox WHERE order_id = $1`, order))
		assert.JSONEq(t, `{}`, payload)
	})

	t.Run("unknown order", func(t *testing.T) {
		e := newEnv(t)
		links := infrastructure.NewOrderLinkRepository(e.db)
		_, err := links.EnqueueCreate(ctx, uuid.New(), nil, now)
		assert.Error(t, err)
	})

	t.Run("outbox insert fails: the link is rolled back too", func(t *testing.T) {
		e := newEnv(t)
		links := infrastructure.NewOrderLinkRepository(e.db)
		order := e.newOrder(t)
		testhelpers.FailTrigger(t, e.db, "hubrise_outbox", "BEFORE", "INSERT", false)
		_, err := links.EnqueueCreate(ctx, order, nil, now)
		require.Error(t, err)
		var n int
		require.NoError(t, e.db.GetContext(ctx, &n, `SELECT count(*) FROM hubrise_orders`))
		assert.Zero(t, n, "neither or both")
	})

	t.Run("commit fails", func(t *testing.T) {
		e := newEnv(t)
		links := infrastructure.NewOrderLinkRepository(e.db)
		order := e.newOrder(t)
		testhelpers.FailCommitTrigger(t, e.db, "hubrise_outbox", "INSERT")
		_, err := links.EnqueueCreate(ctx, order, nil, now)
		require.Error(t, err)
		var n int
		require.NoError(t, e.db.GetContext(ctx, &n, `SELECT count(*) FROM hubrise_orders`))
		assert.Zero(t, n)
	})
}

func TestOrderLinkLifecycle(t *testing.T) {
	e := newEnv(t)
	links := infrastructure.NewOrderLinkRepository(e.db)
	ctx := t.Context()
	now := time.Now().Truncate(time.Microsecond)
	order := e.newOrder(t)

	_, err := links.Get(ctx, order)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	_, err = links.FindByHubriseID(ctx, "ho-1")
	assert.ErrorIs(t, err, domain.ErrNotFound)
	for name, err := range map[string]error{
		"mark sent":    links.MarkSent(ctx, order, "ho-1", "new", now),
		"push state":   links.SetPushState(ctx, order, domain.PushParked),
		"reset":        links.ResetToPending(ctx, order),
		"remote":       links.RecordRemoteStatus(ctx, order, "accepted", &now),
		"local patch":  links.RecordLocalPatch(ctx, order, "accepted", now),
		"remote total": links.SetRemoteTotal(ctx, order, decimal.NewFromInt(1)),
		"attention":    links.SetNeedsAttention(ctx, order, "X"),
		"not accepted": links.MarkNotAccepted(ctx, order),
		"alerted":      links.MarkStuckAlerted(ctx, order, now),
	} {
		assert.ErrorIs(t, err, domain.ErrNotFound, name)
	}

	_, err = links.EnqueueCreate(ctx, order, nil, now)
	require.NoError(t, err)

	require.NoError(t, links.MarkSent(ctx, order, "ho-1", "new", now))
	got, err := links.Get(ctx, order)
	require.NoError(t, err)
	assert.Equal(t, domain.PushSent, got.PushState)
	assert.Equal(t, "ho-1", *got.HubriseOrderID)
	assert.Equal(t, "new", *got.LastRemoteStatus)
	assert.WithinDuration(t, now, *got.PushedAt, time.Millisecond)
	byHubrise, err := links.FindByHubriseID(ctx, "ho-1")
	require.NoError(t, err)
	assert.Equal(t, order, byHubrise.OrderID)

	// A HubRise id belongs to one order.
	other := e.newOrder(t)
	_, err = links.EnqueueCreate(ctx, other, nil, now)
	require.NoError(t, err)
	assert.Error(t, links.MarkSent(ctx, other, "ho-1", "new", now))

	// Remote status keeps the previous event time unless one is given; local patch stamps its own clock.
	eventAt := now.Add(-time.Minute)
	require.NoError(t, links.RecordRemoteStatus(ctx, order, "accepted", &eventAt))
	require.NoError(t, links.RecordRemoteStatus(ctx, order, "in_preparation", nil))
	got, _ = links.Get(ctx, order)
	assert.Equal(t, "in_preparation", *got.LastRemoteStatus)
	assert.WithinDuration(t, eventAt, *got.LastRemoteEventAt, time.Millisecond, "a nil event time keeps the last one")
	assert.Nil(t, got.LastLocalPatchAt)

	require.NoError(t, links.RecordLocalPatch(ctx, order, "awaiting_collection", now))
	got, _ = links.Get(ctx, order)
	assert.Equal(t, "awaiting_collection", *got.LastRemoteStatus)
	assert.WithinDuration(t, now, *got.LastLocalPatchAt, time.Millisecond)
	assert.WithinDuration(t, eventAt, *got.LastRemoteEventAt, time.Millisecond, "a local patch is not a remote event")

	require.NoError(t, links.SetRemoteTotal(ctx, order, decimal.RequireFromString("12.30")))
	got, _ = links.Get(ctx, order)
	assert.Equal(t, "12.3", got.RemoteTotal.String())

	n, _ := links.CountNeedsAttention(ctx)
	assert.Zero(t, n)
	require.NoError(t, links.SetNeedsAttention(ctx, order, "TOTAL_MISMATCH"))
	n, _ = links.CountNeedsAttention(ctx)
	assert.Equal(t, 1, n)
	got, _ = links.Get(ctx, order)
	assert.Equal(t, "TOTAL_MISMATCH", *got.NeedsAttention)
	require.NoError(t, links.SetNeedsAttention(ctx, order, ""))
	n, _ = links.CountNeedsAttention(ctx)
	assert.Zero(t, n)

	// The stuck flag marks an unflagged order and keeps a more specific diagnosis; the alert is
	// recorded on its own, once it went out.
	require.NoError(t, links.MarkNotAccepted(ctx, order))
	var alerted *time.Time
	require.NoError(t, e.db.GetContext(ctx, &alerted, `SELECT stuck_alerted_at FROM hubrise_orders WHERE order_id = $1`, order))
	assert.Nil(t, alerted, "flagging does not record an alert")
	require.NoError(t, links.MarkStuckAlerted(ctx, order, now))
	require.NoError(t, e.db.GetContext(ctx, &alerted, `SELECT stuck_alerted_at FROM hubrise_orders WHERE order_id = $1`, order))
	require.NotNil(t, alerted)
	got, _ = links.Get(ctx, order)
	assert.Equal(t, domain.AttentionNotAccepted, *got.NeedsAttention)
	require.NoError(t, links.SetNeedsAttention(ctx, order, "TOTAL_MISMATCH"))
	require.NoError(t, links.MarkNotAccepted(ctx, order))
	got, _ = links.Get(ctx, order)
	assert.Equal(t, "TOTAL_MISMATCH", *got.NeedsAttention)
	require.NoError(t, links.SetNeedsAttention(ctx, order, ""))

	require.NoError(t, links.SetPushState(ctx, order, domain.PushParked))
	got, _ = links.Get(ctx, order)
	assert.Equal(t, domain.PushParked, got.PushState)
	assert.Error(t, links.SetPushState(ctx, order, "weird"), "the table checks the state")

	// HubRise does not know the order: back to pending, the id is forgotten.
	require.NoError(t, links.ResetToPending(ctx, order))
	got, _ = links.Get(ctx, order)
	assert.Equal(t, domain.PushPending, got.PushState)
	assert.Nil(t, got.HubriseOrderID)
	assert.Nil(t, got.PushedAt)
	assert.Nil(t, got.LastRemoteStatus)
	_, err = links.FindByHubriseID(ctx, "ho-1")
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestOrderLinkCascadesWithOrder(t *testing.T) {
	e := newEnv(t)
	links := infrastructure.NewOrderLinkRepository(e.db)
	order := e.newOrder(t)
	_, err := links.EnqueueCreate(t.Context(), order, nil, time.Now())
	require.NoError(t, err)

	_, err = e.db.ExecContext(t.Context(), `DELETE FROM orders WHERE id = $1`, order)
	require.NoError(t, err)
	var n int
	require.NoError(t, e.db.GetContext(t.Context(), &n, `SELECT (SELECT count(*) FROM hubrise_orders) + (SELECT count(*) FROM hubrise_outbox)`))
	assert.Zero(t, n, "deleting an order removes its HubRise link and queued rows")
}
