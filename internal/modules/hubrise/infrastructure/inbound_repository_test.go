package infrastructure_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/internal/modules/hubrise/infrastructure"
)

func TestEventClaimCanBeLimitedToResourceTypes(t *testing.T) {
	e := newEnv(t)
	repo := infrastructure.NewEventRepository(e.db)
	now := time.Now().Truncate(time.Microsecond)
	for i, resource := range []string{"order", "catalog", "inventory", "order"} {
		ok, err := repo.Insert(t.Context(), event(string(rune('a'+i)), resource, now.Add(time.Duration(i)*time.Second)))
		require.NoError(t, err)
		require.True(t, ok)
	}
	later := now.Add(time.Minute)

	got, err := repo.Claim(t.Context(), later, time.Minute, 10, "order")
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "d"}, eventIDs(got))

	got, err = repo.Claim(t.Context(), later, time.Minute, 10, "catalog", "inventory")
	require.NoError(t, err)
	assert.Equal(t, []string{"b", "c"}, eventIDs(got))

	got, err = repo.Claim(t.Context(), later.Add(2*time.Minute), time.Minute, 10)
	require.NoError(t, err)
	assert.Len(t, got, 4, "no filter: every resource")
}

func TestRemoteEventTimeNeverGoesBackwards(t *testing.T) {
	e := newEnv(t)
	links := infrastructure.NewOrderLinkRepository(e.db)
	order := e.newOrder(t)
	_, err := links.EnqueueCreate(t.Context(), order, nil, time.Now())
	require.NoError(t, err)
	noon := time.Now().Truncate(time.Microsecond)

	require.NoError(t, links.RecordRemoteStatus(t.Context(), order, "accepted", &noon))
	earlier := noon.Add(-time.Hour)
	require.NoError(t, links.RecordRemoteStatus(t.Context(), order, "in_preparation", &earlier))
	got, err := links.Get(t.Context(), order)
	require.NoError(t, err)
	assert.Equal(t, "in_preparation", *got.LastRemoteStatus, "the status is the last one seen")
	assert.WithinDuration(t, noon, *got.LastRemoteEventAt, time.Millisecond, "a late event does not rewind the clock of remote data")

	later := noon.Add(time.Minute)
	require.NoError(t, links.RecordRemoteStatus(t.Context(), order, "completed", &later))
	got, _ = links.Get(t.Context(), order)
	assert.WithinDuration(t, later, *got.LastRemoteEventAt, time.Millisecond)
}

func TestHasUnsettledCreate(t *testing.T) {
	e := newEnv(t)
	links := infrastructure.NewOrderLinkRepository(e.db)
	ctx := t.Context()

	busy, err := links.HasUnsettledCreate(ctx)
	require.NoError(t, err)
	assert.False(t, busy, "nothing queued")

	order := e.newOrder(t)
	now := time.Now()
	_, err = links.EnqueueCreate(ctx, order, nil, now)
	require.NoError(t, err)
	busy, err = links.HasUnsettledCreate(ctx)
	require.NoError(t, err)
	assert.True(t, busy, "a create is waiting")

	_, err = e.db.ExecContext(ctx, `UPDATE hubrise_outbox SET state = 'sending', attempts = 1`)
	require.NoError(t, err)
	busy, _ = links.HasUnsettledCreate(ctx)
	assert.True(t, busy, "and while it is being sent")

	// The answer is lost and the row backs off: still unsettled.
	_, err = e.db.ExecContext(ctx, `UPDATE hubrise_outbox SET state = 'pending', next_attempt_at = now() + interval '1 minute'`)
	require.NoError(t, err)
	busy, _ = links.HasUnsettledCreate(ctx)
	assert.True(t, busy)

	// Parked: a human has it, nothing is in flight.
	_, err = e.db.ExecContext(ctx, `UPDATE hubrise_outbox SET state = 'parked'`)
	require.NoError(t, err)
	busy, _ = links.HasUnsettledCreate(ctx)
	assert.False(t, busy)

	_, err = e.db.ExecContext(ctx, `UPDATE hubrise_outbox SET state = 'pending'`)
	require.NoError(t, err)
	require.NoError(t, links.MarkSent(ctx, order, "ho-1", "new", now))
	busy, _ = links.HasUnsettledCreate(ctx)
	assert.False(t, busy, "the id is known")
}

func TestHasQueuedStatus(t *testing.T) {
	e := newEnv(t)
	links := infrastructure.NewOrderLinkRepository(e.db)
	ctx := t.Context()
	order := e.newOrder(t)
	now := time.Now()
	_, err := links.EnqueueCreate(ctx, order, nil, now)
	require.NoError(t, err)

	queued, err := links.HasQueuedStatus(ctx, order)
	require.NoError(t, err)
	assert.False(t, queued, "a create is not a status")

	_, err = e.db.ExecContext(ctx, `INSERT INTO hubrise_outbox (kind, order_id, payload, state) VALUES ('order_status', $1, '{}', 'pending')`, order)
	require.NoError(t, err)
	queued, _ = links.HasQueuedStatus(ctx, order)
	assert.True(t, queued)

	_, err = e.db.ExecContext(ctx, `UPDATE hubrise_outbox SET state = 'sending' WHERE kind = 'order_status'`)
	require.NoError(t, err)
	queued, _ = links.HasQueuedStatus(ctx, order)
	assert.True(t, queued, "and while it is being sent")

	for _, state := range []string{"sent", "parked", "skipped"} {
		_, err = e.db.ExecContext(ctx, `UPDATE hubrise_outbox SET state = $1 WHERE kind = 'order_status'`, state)
		require.NoError(t, err)
		queued, _ = links.HasQueuedStatus(ctx, order)
		assert.False(t, queued, state)
	}
	queued, _ = links.HasQueuedStatus(ctx, uuid.New())
	assert.False(t, queued, "another order")
}

func TestRecordRemoteConfirmedTime(t *testing.T) {
	e := newEnv(t)
	links := infrastructure.NewOrderLinkRepository(e.db)
	ctx := t.Context()
	order := e.newOrder(t)
	now := time.Now()
	_, err := links.EnqueueCreate(ctx, order, nil, now)
	require.NoError(t, err)

	got, _ := links.Get(ctx, order)
	assert.Nil(t, got.LastRemoteConfirmedTime)

	at := now.Add(20 * time.Minute)
	require.NoError(t, links.RecordRemoteConfirmedTime(ctx, order, at))
	got, _ = links.Get(ctx, order)
	require.NotNil(t, got.LastRemoteConfirmedTime)
	assert.WithinDuration(t, at, *got.LastRemoteConfirmedTime, time.Millisecond)

	// A new HubRise order forgets it.
	require.NoError(t, links.MarkSent(ctx, order, "ho-1", "new", now))
	require.NoError(t, links.ResetToPending(ctx, order))
	got, _ = links.Get(ctx, order)
	assert.Nil(t, got.LastRemoteConfirmedTime)
	require.NoError(t, links.RecordRemoteConfirmedTime(ctx, order, at))
	require.NoError(t, links.Recreate(ctx, order, nil, now))
	got, _ = links.Get(ctx, order)
	assert.Nil(t, got.LastRemoteConfirmedTime)

	assert.ErrorIs(t, links.RecordRemoteConfirmedTime(ctx, uuid.New(), at), domain.ErrNotFound)
}

func TestSourceStateCarriesWhatTheStatusSyncNeeds(t *testing.T) {
	e := newEnv(t)
	src := infrastructure.NewOrderSource(e.db)
	cash := e.sourceOrder(t, sourceOrder{})
	open := e.sourceOrder(t, sourceOrder{online: true})
	paid := e.sourceOrder(t, sourceOrder{online: true, paid: true})
	ready := time.Now().Add(20 * time.Minute).Truncate(time.Second)
	_, err := e.db.ExecContext(t.Context(), `UPDATE orders SET estimated_ready_time = $2 WHERE id = $1`, paid, ready)
	require.NoError(t, err)

	for name, tc := range map[string]struct {
		id   uuid.UUID
		paid bool
	}{"cash": {cash, false}, "online, not paid": {open, false}, "online, paid": {paid, true}} {
		st, err := src.State(t.Context(), tc.id)
		require.NoError(t, err, name)
		assert.Equal(t, tc.paid, st.OnlinePaid, name)
		assert.Equal(t, "PICKUP", st.Type)
	}
	st, err := src.State(t.Context(), paid)
	require.NoError(t, err)
	require.NotNil(t, st.EstimatedReadyTime)
	assert.True(t, ready.Equal(*st.EstimatedReadyTime))
	st, _ = src.State(t.Context(), cash)
	assert.Nil(t, st.EstimatedReadyTime)
}

func TestStatusMismatchesIgnoresADeliveryReadyForCollection(t *testing.T) {
	e := newEnv(t)
	src := infrastructure.NewOrderSource(e.db)
	pairs := []domain.StatusPair{{Local: "PREPARING", Remote: "in_preparation"}}
	delivery := e.sourceOrder(t, sourceOrder{status: "PREPARING"})
	e.sentLink(t, delivery, "awaiting_collection", time.Minute)
	pickup := e.sourceOrder(t, sourceOrder{status: "PREPARING"})
	e.sentLink(t, pickup, "awaiting_collection", time.Minute)
	_, err := e.db.ExecContext(t.Context(), `UPDATE orders SET order_type = 'DELIVERY' WHERE id = $1`, delivery)
	require.NoError(t, err)

	got, err := src.StatusMismatches(t.Context(), pairs, 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, pickup, got[0].OrderID, "the delivery has no local equivalent of that status, so it is in sync")
}
