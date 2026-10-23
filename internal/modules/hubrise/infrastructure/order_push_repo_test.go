package infrastructure_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/internal/modules/hubrise/infrastructure"
	hubtest "tsb-service/internal/modules/hubrise/testhelpers"
)

func TestOutboxKindFilterAndCreatedAt(t *testing.T) {
	e := newEnv(t)
	repo := infrastructure.NewOutboxRepository(e.db)
	hubtest.SeedConnection(t, e.db)
	ctx := t.Context()
	now := time.Now().Truncate(time.Microsecond)

	create := enqueue(t, repo, domain.OutboxOrderCreate, ptr(e.newOrder(t)), "", `{}`, now)
	inventory := enqueue(t, repo, domain.OutboxInventory, nil, "inv:A", `{}`, now)

	got, err := repo.Claim(ctx, now, time.Minute, 10, domain.OutboxOrderCreate, domain.OutboxOrderStatus)
	require.NoError(t, err)
	assert.Equal(t, []int64{create}, ids(got), "a worker never leases the kinds it does not handle")
	got, err = repo.Claim(ctx, now, time.Minute, 10)
	require.NoError(t, err)
	assert.Equal(t, []int64{inventory}, ids(got), "no filter claims the rest")

	// A created_at set by the caller is stored; without one the database's clock is used.
	order := e.newOrder(t)
	past := now.Add(-5 * time.Hour)
	item := domain.NewOutboxItem(domain.OutboxOrderStatus, &order, "k1", nil, now)
	item.CreatedAt = past
	id, _, err := repo.Enqueue(ctx, item)
	require.NoError(t, err)
	row, err := repo.Get(ctx, id)
	require.NoError(t, err)
	assert.WithinDuration(t, past, row.CreatedAt, time.Millisecond)
	id, _, err = repo.Enqueue(ctx, domain.NewOutboxItem(domain.OutboxOrderStatus, &order, "k2", nil, now))
	require.NoError(t, err)
	row, err = repo.Get(ctx, id)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now(), row.CreatedAt, time.Minute)
}

func TestOutboxOrderHelpers(t *testing.T) {
	e := newEnv(t)
	repo := infrastructure.NewOutboxRepository(e.db)
	ctx := t.Context()
	now := time.Now().Truncate(time.Microsecond)
	order, other := e.newOrder(t), e.newOrder(t)

	id, err := repo.MaxPendingID(ctx, order, domain.OutboxOrderStatus)
	require.NoError(t, err)
	assert.Zero(t, id, "nothing queued")

	s1 := enqueue(t, repo, domain.OutboxOrderStatus, &order, "a", `{}`, now)
	s2 := enqueue(t, repo, domain.OutboxOrderStatus, &order, "b", `{}`, now)
	s3 := enqueue(t, repo, domain.OutboxOrderStatus, &order, "c", `{}`, now)
	foreign := enqueue(t, repo, domain.OutboxOrderStatus, &other, "d", `{}`, now)
	create := enqueue(t, repo, domain.OutboxOrderCreate, &order, "", `{}`, now)

	id, err = repo.MaxPendingID(ctx, order, domain.OutboxOrderStatus)
	require.NoError(t, err)
	assert.Equal(t, s3, id)

	n, err := repo.SkipPendingUpTo(ctx, order, domain.OutboxOrderStatus, s2, "covered")
	require.NoError(t, err)
	assert.EqualValues(t, 2, n)
	for want, rowID := range map[domain.OutboxState]int64{
		domain.OutboxSkipped: s1, domain.OutboxPending: s3,
	} {
		assert.Equal(t, want, stateOf(t, repo, rowID))
	}
	assert.Equal(t, domain.OutboxPending, stateOf(t, repo, foreign), "another order is untouched")
	assert.Equal(t, domain.OutboxPending, stateOf(t, repo, create), "another kind is untouched")
	row, err := repo.Get(ctx, s2)
	require.NoError(t, err)
	assert.Equal(t, "covered", *row.LastError)

	c, err := repo.CountForOrder(ctx, order, domain.OutboxOrderStatus)
	require.NoError(t, err)
	assert.Equal(t, 3, c, "every state counts")
	c, err = repo.CountForOrder(ctx, order, domain.OutboxOrderCreate)
	require.NoError(t, err)
	assert.Equal(t, 1, c)
}

func TestOrderLinkAttentionFlags(t *testing.T) {
	e := newEnv(t)
	links := infrastructure.NewOrderLinkRepository(e.db)
	ctx := t.Context()
	order := e.newOrder(t)
	_, err := links.EnqueueCreate(ctx, order, json.RawMessage(`{}`), time.Now())
	require.NoError(t, err)

	attention := func() string {
		l, err := links.Get(ctx, order)
		require.NoError(t, err)
		if l.NeedsAttention == nil {
			return ""
		}
		return *l.NeedsAttention
	}

	require.NoError(t, links.FlagAttention(ctx, order, domain.AttentionUnlinkedItem))
	assert.Equal(t, domain.AttentionUnlinkedItem, attention())
	require.NoError(t, links.FlagAttention(ctx, order, domain.AttentionTotalMismatch))
	assert.Equal(t, domain.AttentionUnlinkedItem, attention(), "a flag already raised is never replaced")

	require.NoError(t, links.ClearAttention(ctx, order, domain.AttentionNotAccepted, domain.AttentionPushParked))
	assert.Equal(t, domain.AttentionUnlinkedItem, attention(), "only the named codes are cleared")
	require.NoError(t, links.ClearAttention(ctx, order, domain.AttentionNotAccepted, domain.AttentionUnlinkedItem))
	assert.Empty(t, attention())
	require.NoError(t, links.ClearAttention(ctx, order), "no code clears nothing")

	// Unknown orders are not an error for the best effort writers.
	require.NoError(t, links.FlagAttention(ctx, uuid.New(), domain.AttentionPushParked))
	require.NoError(t, links.ClearAttention(ctx, uuid.New(), domain.AttentionPushParked))
}

func TestOrderLinkStampRemoteRead(t *testing.T) {
	e := newEnv(t)
	links := infrastructure.NewOrderLinkRepository(e.db)
	ctx := t.Context()
	order := e.newOrder(t)
	_, err := links.EnqueueCreate(ctx, order, json.RawMessage(`{}`), time.Now())
	require.NoError(t, err)

	at := time.Now().Add(-time.Minute).Truncate(time.Microsecond)
	require.NoError(t, links.StampRemoteRead(ctx, order, at))
	l, err := links.Get(ctx, order)
	require.NoError(t, err)
	assert.Nil(t, l.LastRemoteEventAt, "our own clock never becomes the time of remote data")
	var read *time.Time
	require.NoError(t, e.db.GetContext(ctx, &read, `SELECT last_remote_read_at FROM hubrise_orders WHERE order_id = $1`, order))
	require.NotNil(t, read)
	assert.WithinDuration(t, at, *read, time.Millisecond)

	assert.ErrorIs(t, links.StampRemoteRead(ctx, uuid.New(), at), domain.ErrNotFound)
}

func TestOrderLinkRecreate(t *testing.T) {
	e := newEnv(t)
	links := infrastructure.NewOrderLinkRepository(e.db)
	outbox := infrastructure.NewOutboxRepository(e.db)
	ctx := t.Context()
	now := time.Now().Truncate(time.Microsecond)
	order := e.newOrder(t)

	_, err := links.EnqueueCreate(ctx, order, json.RawMessage(`{"v":1}`), now)
	require.NoError(t, err)
	require.NoError(t, links.MarkSent(ctx, order, "lost-1", "accepted", now))
	require.NoError(t, links.SetRemoteTotal(ctx, order, decimal.RequireFromString("12.5")))
	_, err = e.db.ExecContext(ctx, `UPDATE hubrise_outbox SET state = 'sent' WHERE order_id = $1 AND kind = 'order_create'`, order)
	require.NoError(t, err)
	pendingStatus := enqueue(t, outbox, domain.OutboxOrderStatus, &order, "s1", `{}`, now)
	sendingStatus := enqueue(t, outbox, domain.OutboxOrderStatus, &order, "s2", `{}`, now)
	_, err = e.db.ExecContext(ctx, `UPDATE hubrise_outbox SET state = 'sending' WHERE id = $1`, sendingStatus)
	require.NoError(t, err)
	sentStatus := enqueue(t, outbox, domain.OutboxOrderStatus, &order, "s3", `{}`, now)
	_, err = e.db.ExecContext(ctx, `UPDATE hubrise_outbox SET state = 'sent' WHERE id = $1`, sentStatus)
	require.NoError(t, err)

	later := now.Add(time.Minute)
	require.NoError(t, links.Recreate(ctx, order, nil, later))

	l, err := links.Get(ctx, order)
	require.NoError(t, err)
	assert.Equal(t, domain.PushPending, l.PushState)
	assert.Nil(t, l.HubriseOrderID)
	assert.Nil(t, l.LastRemoteStatus)
	assert.Nil(t, l.RemoteTotal)
	assert.Nil(t, l.PushedAt)

	assert.Equal(t, domain.OutboxSkipped, stateOf(t, outbox, pendingStatus))
	assert.Equal(t, domain.OutboxSkipped, stateOf(t, outbox, sendingStatus))
	assert.Equal(t, domain.OutboxSent, stateOf(t, outbox, sentStatus), "history is kept")

	creates, err := outbox.CountForOrder(ctx, order, domain.OutboxOrderCreate)
	require.NoError(t, err)
	assert.Equal(t, 2, creates)
	pending := domain.OutboxPending
	rows, err := outbox.List(ctx, &pending, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, domain.OutboxOrderCreate, rows[0].Kind)
	assert.JSONEq(t, `{}`, string(rows[0].Payload), "an empty payload is stored as {}")
	assert.WithinDuration(t, later, rows[0].NextAttemptAt, time.Millisecond)
	assert.WithinDuration(t, later, rows[0].CreatedAt, time.Millisecond, "the age of the new create starts now")
}

func TestOrderLinkRecreateFailures(t *testing.T) {
	e := newEnv(t)
	links := infrastructure.NewOrderLinkRepository(e.db)
	outbox := infrastructure.NewOutboxRepository(e.db)
	ctx := t.Context()
	now := time.Now()

	assert.ErrorIs(t, links.Recreate(ctx, uuid.New(), json.RawMessage(`{}`), now), domain.ErrNotFound)

	// A failure after the first statement rolls everything back.
	order := e.newOrder(t)
	_, err := links.EnqueueCreate(ctx, order, json.RawMessage(`{}`), now)
	require.NoError(t, err)
	status := enqueue(t, outbox, domain.OutboxOrderStatus, &order, "s", `{}`, now)
	_, err = e.db.ExecContext(ctx, `CREATE UNIQUE INDEX no_second_create ON hubrise_outbox (order_id) WHERE kind = 'order_create'`)
	require.NoError(t, err)
	require.Error(t, links.Recreate(ctx, order, json.RawMessage(`{}`), now))
	assert.Equal(t, domain.OutboxPending, stateOf(t, outbox, status), "the status row was not skipped")
	l, err := links.Get(ctx, order)
	require.NoError(t, err)
	assert.Equal(t, domain.PushPending, l.PushState)
}

func TestOrderLinkRecreateReportsEachStepThatFails(t *testing.T) {
	for name, rename := range map[string]string{
		"skipping the status rows": `ALTER TABLE hubrise_outbox RENAME TO hubrise_outbox_gone`,
		"resetting the link":       `ALTER TABLE hubrise_orders RENAME TO hubrise_orders_gone`,
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			links := infrastructure.NewOrderLinkRepository(e.db)
			order := e.newOrder(t)
			_, err := links.EnqueueCreate(t.Context(), order, json.RawMessage(`{}`), time.Now())
			require.NoError(t, err)
			_, err = e.db.ExecContext(t.Context(), rename)
			require.NoError(t, err)
			require.Error(t, links.Recreate(t.Context(), order, json.RawMessage(`{}`), time.Now()))
		})
	}
}
