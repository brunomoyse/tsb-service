package infrastructure_test

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/internal/modules/hubrise/infrastructure"
	hubtest "tsb-service/internal/modules/hubrise/testhelpers"
)

func enqueue(t *testing.T, repo domain.OutboxRepository, kind domain.OutboxKind, orderID *uuid.UUID, dedupe string, payload string, due time.Time) int64 {
	t.Helper()
	id, _, err := repo.Enqueue(t.Context(), domain.NewOutboxItem(kind, orderID, dedupe, json.RawMessage(payload), due))
	require.NoError(t, err)
	return id
}

func ids(items []domain.OutboxItem) []int64 {
	out := make([]int64, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
}

func TestOutboxEnqueue(t *testing.T) {
	e := newEnv(t)
	repo := infrastructure.NewOutboxRepository(e.db)
	ctx := t.Context()
	now := time.Now().Truncate(time.Microsecond)
	order := e.newOrder(t)

	t.Run("a nil payload is stored as an empty object, not NULL", func(t *testing.T) {
		id, merged, err := repo.Enqueue(ctx, domain.OutboxItem{Kind: domain.OutboxOrderStatus, OrderID: &order, NextAttemptAt: now})
		require.NoError(t, err)
		assert.False(t, merged)
		got, err := repo.Get(ctx, id)
		require.NoError(t, err)
		assert.JSONEq(t, `{}`, string(got.Payload))
		assert.Equal(t, domain.OutboxPending, got.State)
		assert.Zero(t, got.Attempts)
		assert.Equal(t, order, *got.OrderID)
		assert.WithinDuration(t, now, got.NextAttemptAt, time.Millisecond)
	})

	t.Run("order_status rows are never collapsed", func(t *testing.T) {
		a := enqueue(t, repo, domain.OutboxOrderStatus, &order, "order:x:status:1", `{"status":"accepted"}`, now)
		b := enqueue(t, repo, domain.OutboxOrderStatus, &order, "order:x:status:1", `{"status":"accepted"}`, now)
		assert.NotEqual(t, a, b)
	})

	t.Run("pending inventory rows collapse to the latest state", func(t *testing.T) {
		first, merged, err := repo.Enqueue(ctx, domain.NewOutboxItem(domain.OutboxInventory, nil, "inv:sku:A", json.RawMessage(`{"stock":"0"}`), now))
		require.NoError(t, err)
		assert.False(t, merged)

		later := now.Add(5 * time.Second)
		second, merged, err := repo.Enqueue(ctx, domain.NewOutboxItem(domain.OutboxInventory, nil, "inv:sku:A", json.RawMessage(`{"stock":null}`), later))
		require.NoError(t, err)
		assert.True(t, merged)
		assert.Equal(t, first, second)
		got, _ := repo.Get(ctx, first)
		assert.JSONEq(t, `{"stock":null}`, string(got.Payload))
		assert.WithinDuration(t, later, got.NextAttemptAt, time.Millisecond)

		other, merged, err := repo.Enqueue(ctx, domain.NewOutboxItem(domain.OutboxInventory, nil, "inv:sku:B", nil, now))
		require.NoError(t, err)
		assert.False(t, merged)
		assert.NotEqual(t, first, other)
	})

	t.Run("a row already sending is never merged into", func(t *testing.T) {
		hubtest.SeedConnection(t, e.db)
		inflight := enqueue(t, repo, domain.OutboxInventory, nil, "inv:sku:C", `{"stock":"0"}`, now)
		claimed, err := repo.Claim(ctx, now, time.Minute, 100)
		require.NoError(t, err)
		assert.Contains(t, ids(claimed), inflight)

		// A newer toggle arrives while the first request is in flight: a fresh row, not a merge.
		fresh, merged, err := repo.Enqueue(ctx, domain.NewOutboxItem(domain.OutboxInventory, nil, "inv:sku:C", json.RawMessage(`{"stock":null}`), now))
		require.NoError(t, err)
		assert.False(t, merged)
		assert.NotEqual(t, inflight, fresh)
		old, _ := repo.Get(ctx, inflight)
		assert.JSONEq(t, `{"stock":"0"}`, string(old.Payload), "the in-flight payload is untouched")

		// FIFO per dedupe key: the fresh row waits for the in-flight one.
		again, err := repo.Claim(ctx, now, time.Minute, 100)
		require.NoError(t, err)
		assert.NotContains(t, ids(again), fresh)
		ok, err := repo.MarkSent(ctx, inflight, now)
		require.NoError(t, err)
		assert.True(t, ok)
		again, _ = repo.Claim(ctx, now, time.Minute, 100)
		assert.Contains(t, ids(again), fresh)
	})

	t.Run("the order must exist", func(t *testing.T) {
		missing := uuid.New()
		_, _, err := repo.Enqueue(ctx, domain.NewOutboxItem(domain.OutboxOrderCreate, &missing, "", nil, now))
		assert.Error(t, err)
	})
}

func TestOutboxClaimGates(t *testing.T) {
	e := newEnv(t)
	repo := infrastructure.NewOutboxRepository(e.db)
	conns := infrastructure.NewConnectionRepository(e.db)
	ctx := t.Context()
	now := time.Now().Truncate(time.Microsecond)

	create := enqueue(t, repo, domain.OutboxOrderCreate, ptr(e.newOrder(t)), "", `{"a":1}`, now)
	status := enqueue(t, repo, domain.OutboxOrderStatus, ptr(e.newOrder(t)), "", `{}`, now)
	inventory := enqueue(t, repo, domain.OutboxInventory, nil, "inv:sku:A", `{}`, now)
	customer := enqueue(t, repo, domain.OutboxCustomer, nil, "", `{}`, now)
	anonymise := enqueue(t, repo, domain.OutboxCustomerAnonymise, nil, "", `{}`, now)
	all := []int64{create, status, inventory, customer, anonymise}

	claim := func() []int64 {
		t.Helper()
		items, err := repo.Claim(ctx, now, time.Minute, 100)
		require.NoError(t, err)
		return ids(items)
	}
	// Putting the claimed rows back so every gate is observed on the same five rows.
	reset := func() {
		t.Helper()
		_, err := e.db.ExecContext(ctx, `UPDATE hubrise_outbox SET state = 'pending', attempts = 0, leased_until = NULL`)
		require.NoError(t, err)
	}

	assert.Empty(t, claim(), "no connection row: standalone, nothing is claimed")

	hubtest.SeedConnection(t, e.db, hubtest.WithEnabled(false))
	assert.Empty(t, claim(), "connected but disabled: paused")

	hubtest.SeedConnection(t, e.db, hubtest.WithFeatures(false, false, false, false, false))
	assert.Equal(t, []int64{anonymise}, claim(), "every toggle off: only the erasure of a deleted account still goes out")
	reset()

	for name, tc := range map[string]struct {
		opt  hubtest.SeedOption
		want []int64
	}{
		"push_orders only":    {hubtest.WithFeatures(true, false, false, false, false), []int64{create, anonymise}},
		"sync_status only":    {hubtest.WithFeatures(false, true, false, false, false), []int64{status, anonymise}},
		"sync_inventory only": {hubtest.WithFeatures(false, false, true, false, false), []int64{inventory, anonymise}},
		"sync_customers only": {hubtest.WithFeatures(false, false, false, false, true), []int64{customer, anonymise}},
	} {
		hubtest.SeedConnection(t, e.db, tc.opt)
		assert.Equal(t, tc.want, claim(), name)
		reset()
	}

	t.Run("auth error and relink pause the pipeline", func(t *testing.T) {
		hubtest.SeedConnection(t, e.db)
		require.NoError(t, conns.RecordAuthError(ctx, now, "revoked"))
		assert.Empty(t, claim())
		require.NoError(t, conns.ClearAuthError(ctx))
		require.NoError(t, conns.SetRelinkRequired(ctx, &now))
		assert.Empty(t, claim())
		require.NoError(t, conns.SetRelinkRequired(ctx, nil))
		assert.Equal(t, all, claim())
		reset()
	})

	t.Run("disconnected token pauses it", func(t *testing.T) {
		require.NoError(t, conns.Disconnect(ctx))
		assert.Empty(t, claim())
	})

	t.Run("claim marks rows sending, counts the attempt and leases them", func(t *testing.T) {
		hubtest.SeedConnection(t, e.db)
		items, err := repo.Claim(ctx, now, 2*time.Minute, 100)
		require.NoError(t, err)
		require.Len(t, items, 5)
		assert.Equal(t, all, ids(items), "oldest first")
		for _, it := range items {
			assert.Equal(t, domain.OutboxSending, it.State)
			assert.Equal(t, 1, it.Attempts)
			assert.WithinDuration(t, now.Add(2*time.Minute), *it.LeasedUntil, time.Millisecond)
		}
		assert.JSONEq(t, `{"a":1}`, string(items[0].Payload))
		assert.Empty(t, claim(), "a leased row is not claimed again")
	})

	t.Run("an expired lease is reclaimed with attempts > 1", func(t *testing.T) {
		items, err := repo.Claim(ctx, now.Add(3*time.Minute), time.Minute, 100)
		require.NoError(t, err)
		require.Len(t, items, 5)
		for _, it := range items {
			assert.Equal(t, 2, it.Attempts, "the second claim is what tells the worker to look the order up first")
		}
	})
}

func ptr[T any](v T) *T { return &v }

func TestOutboxClaimTimingAndLimit(t *testing.T) {
	e := newEnv(t)
	repo := infrastructure.NewOutboxRepository(e.db)
	ctx := t.Context()
	now := time.Now().Truncate(time.Microsecond)
	hubtest.SeedConnection(t, e.db)

	future := enqueue(t, repo, domain.OutboxInventory, nil, "inv:sku:F", `{}`, now.Add(time.Minute))
	a := enqueue(t, repo, domain.OutboxInventory, nil, "inv:sku:1", `{}`, now)
	b := enqueue(t, repo, domain.OutboxInventory, nil, "inv:sku:2", `{}`, now)
	c := enqueue(t, repo, domain.OutboxInventory, nil, "inv:sku:3", `{}`, now)

	items, err := repo.Claim(ctx, now, time.Minute, 2)
	require.NoError(t, err)
	assert.Equal(t, []int64{a, b}, ids(items), "the limit applies, oldest first, and future rows wait")

	items, _ = repo.Claim(ctx, now, time.Minute, 10)
	assert.Equal(t, []int64{c}, ids(items))

	items, _ = repo.Claim(ctx, now.Add(2*time.Minute), time.Minute, 10)
	assert.Equal(t, []int64{future}, ids(items)[:1], "the future row is due later")
}

func TestOutboxFIFOPerOrder(t *testing.T) {
	e := newEnv(t)
	repo := infrastructure.NewOutboxRepository(e.db)
	ctx := t.Context()
	now := time.Now().Truncate(time.Microsecond)
	hubtest.SeedConnection(t, e.db)
	order, other := e.newOrder(t), e.newOrder(t)

	create := enqueue(t, repo, domain.OutboxOrderCreate, &order, "", `{}`, now)
	p1 := enqueue(t, repo, domain.OutboxOrderStatus, &order, "o:1", `{"status":"accepted"}`, now)
	p2 := enqueue(t, repo, domain.OutboxOrderStatus, &order, "o:2", `{"status":"in_preparation"}`, now)
	otherCreate := enqueue(t, repo, domain.OutboxOrderCreate, &other, "", `{}`, now)

	items, err := repo.Claim(ctx, now, time.Minute, 10)
	require.NoError(t, err)
	assert.Equal(t, []int64{create, otherCreate}, ids(items), "only the first row of each order, a PATCH never overtakes its create")

	ok, err := repo.MarkSent(ctx, otherCreate, now)
	require.NoError(t, err)
	require.True(t, ok)

	// Backing off the create keeps blocking its successors.
	ok, err = repo.Release(ctx, create, now.Add(30*time.Second), "HubRise 503", false)
	require.NoError(t, err)
	assert.True(t, ok)
	items, _ = repo.Claim(ctx, now, time.Minute, 10)
	assert.Empty(t, items)
	items, _ = repo.Claim(ctx, now.Add(31*time.Second), time.Minute, 10)
	assert.Equal(t, []int64{create}, ids(items))

	// A parked create blocks the chain behind it.
	ok, err = repo.MarkParked(ctx, create, now, "422 invalid")
	require.NoError(t, err)
	require.True(t, ok)
	items, _ = repo.Claim(ctx, now.Add(time.Hour), time.Minute, 10)
	assert.Empty(t, items, "the parked create blocks the statuses behind it")

	// Once it is sent the next row of the order is released, one at a time.
	_, _ = repo.RetryParked(ctx, create, now)
	items, _ = repo.Claim(ctx, now.Add(time.Hour), time.Minute, 10)
	require.Equal(t, []int64{create}, ids(items))
	ok, _ = repo.MarkSent(ctx, create, now)
	require.True(t, ok)
	items, _ = repo.Claim(ctx, now.Add(time.Hour), time.Minute, 10)
	assert.Equal(t, []int64{p1}, ids(items))
	ok, _ = repo.MarkSent(ctx, p1, now)
	require.True(t, ok)
	items, _ = repo.Claim(ctx, now.Add(time.Hour), time.Minute, 10)
	assert.Equal(t, []int64{p2}, ids(items))

	// A skipped row does not block.
	ok, err = repo.MarkSkipped(ctx, p2, "covered by create")
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestOutboxTransitions(t *testing.T) {
	e := newEnv(t)
	repo := infrastructure.NewOutboxRepository(e.db)
	ctx := t.Context()
	now := time.Now().Truncate(time.Microsecond)
	hubtest.SeedConnection(t, e.db)
	order := e.newOrder(t)

	id := enqueue(t, repo, domain.OutboxOrderCreate, &order, "", `{}`, now)

	// None of the claim-completion calls act on a row nobody claimed.
	ok, _ := repo.MarkSent(ctx, id, now)
	assert.False(t, ok)
	ok, _ = repo.MarkParked(ctx, id, now, "x")
	assert.False(t, ok)
	ok, _ = repo.Release(ctx, id, now, "x", false)
	assert.False(t, ok)

	// Extend renews the lease of a claimed row while it is still ours.
	ok, _ = repo.Extend(ctx, id, now, now.Add(time.Hour))
	assert.False(t, ok, "a row nobody claimed has no lease to renew")

	items, _ := repo.Claim(ctx, now, time.Minute, 10)
	require.Len(t, items, 1)
	ok, err := repo.Extend(ctx, id, now.Add(30*time.Second), now.Add(time.Hour))
	require.NoError(t, err)
	require.True(t, ok)
	got, _ := repo.Get(ctx, id)
	assert.WithinDuration(t, now.Add(time.Hour), *got.LeasedUntil, time.Millisecond)
	ok, _ = repo.Extend(ctx, id, now.Add(2*time.Hour), now.Add(3*time.Hour))
	assert.False(t, ok, "a lease that ran out is not renewed: another replica may hold the row")
	got, _ = repo.Get(ctx, id)
	assert.WithinDuration(t, now.Add(time.Hour), *got.LeasedUntil, time.Millisecond)

	// Connection-level refusal: back to pending with the attempt reverted.
	ok, err = repo.Release(ctx, id, now.Add(time.Minute), "401 revoked", true)
	require.NoError(t, err)
	require.True(t, ok)
	got, _ = repo.Get(ctx, id)
	assert.Equal(t, domain.OutboxPending, got.State)
	assert.Zero(t, got.Attempts)
	assert.Equal(t, "401 revoked", *got.LastError)
	assert.Nil(t, got.LeasedUntil)

	// A plain retry keeps the count.
	_, _ = repo.Claim(ctx, now.Add(time.Minute), time.Minute, 10)
	ok, _ = repo.Release(ctx, id, now.Add(2*time.Minute), "", false)
	require.True(t, ok)
	got, _ = repo.Get(ctx, id)
	assert.Equal(t, 1, got.Attempts)
	assert.Nil(t, got.LastError, "an empty reason clears the message")

	// Reverting never goes below zero.
	_, _ = repo.Claim(ctx, now.Add(2*time.Minute), time.Minute, 10)
	_, err = e.db.ExecContext(ctx, `UPDATE hubrise_outbox SET attempts = 0 WHERE id = $1`, id)
	require.NoError(t, err)
	ok, _ = repo.Release(ctx, id, now.Add(3*time.Minute), "", true)
	require.True(t, ok)
	got, _ = repo.Get(ctx, id)
	assert.Zero(t, got.Attempts)

	// Sent.
	_, _ = repo.Claim(ctx, now.Add(3*time.Minute), time.Minute, 10)
	ok, err = repo.MarkSent(ctx, id, now)
	require.NoError(t, err)
	require.True(t, ok)
	got, _ = repo.Get(ctx, id)
	assert.Equal(t, domain.OutboxSent, got.State)
	assert.WithinDuration(t, now, *got.SentAt, time.Millisecond)
	ok, _ = repo.MarkSent(ctx, id, now)
	assert.False(t, ok, "completion is compare-and-set on the sending state")
	ok, _ = repo.MarkSkipped(ctx, id, "late")
	assert.False(t, ok, "a sent row cannot be skipped")

	_, err = repo.Get(ctx, 987654)
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestOutboxListCountsRetry(t *testing.T) {
	e := newEnv(t)
	repo := infrastructure.NewOutboxRepository(e.db)
	ctx := t.Context()
	now := time.Now().Truncate(time.Microsecond)
	hubtest.SeedConnection(t, e.db)
	order, solo := e.newOrder(t), e.newOrder(t)

	// order: create (parked), two statuses (parked, pending). solo: one inventory-less create (parked).
	c := enqueue(t, repo, domain.OutboxOrderCreate, &order, "", `{}`, now)
	s1 := enqueue(t, repo, domain.OutboxOrderStatus, &order, "", `{}`, now)
	s2 := enqueue(t, repo, domain.OutboxOrderStatus, &order, "", `{}`, now)
	sc := enqueue(t, repo, domain.OutboxOrderCreate, &solo, "", `{}`, now)
	noOrder := enqueue(t, repo, domain.OutboxInventory, nil, "inv:sku:Z", `{}`, now)
	claimed, _ := repo.Claim(ctx, now, time.Minute, 10)
	require.ElementsMatch(t, []int64{c, sc, noOrder}, ids(claimed))
	for _, id := range []int64{c, sc, noOrder} {
		ok, err := repo.MarkParked(ctx, id, now, "422 bad payload")
		require.NoError(t, err)
		require.True(t, ok)
	}
	_, err := e.db.ExecContext(ctx, `UPDATE hubrise_outbox SET state = 'parked' WHERE id = $1`, s1)
	require.NoError(t, err)

	counts, err := repo.Counts(ctx)
	require.NoError(t, err)
	assert.Equal(t, domain.OutboxCounts{Pending: 1, Parked: 4}, counts)

	parked := domain.OutboxParked
	list, err := repo.List(ctx, &parked, 50)
	require.NoError(t, err)
	assert.Equal(t, []int64{noOrder, sc, s1, c}, ids(list), "newest first, one state")
	assert.Equal(t, "422 bad payload", *list[0].LastError)
	assert.NotNil(t, list[0].ParkedAt)
	list, _ = repo.List(ctx, nil, 3)
	assert.Equal(t, []int64{noOrder, sc, s2}, ids(list), "no filter, limited")

	// Retrying one row reactivates the whole parked chain of its order, and only that.
	n, err := repo.RetryParked(ctx, c, now.Add(time.Minute))
	require.NoError(t, err)
	assert.EqualValues(t, 2, n)
	counts, _ = repo.Counts(ctx)
	assert.Equal(t, domain.OutboxCounts{Pending: 3, Parked: 2}, counts)
	got, _ := repo.Get(ctx, c)
	assert.Equal(t, 1, got.Attempts, "attempts are kept: a possibly sent create is looked up first")
	assert.Nil(t, got.ParkedAt)
	assert.Nil(t, got.LastError)
	assert.WithinDuration(t, now.Add(time.Minute), got.NextAttemptAt, time.Millisecond)

	// A row without an order is retried alone.
	n, err = repo.RetryParked(ctx, noOrder, now)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)

	n, err = repo.RetryAllParked(ctx, now)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	counts, _ = repo.Counts(ctx)
	assert.Equal(t, domain.OutboxCounts{Pending: 5}, counts)
}

func TestOutboxClaimTwoWorkers(t *testing.T) {
	e := newEnv(t)
	repo := infrastructure.NewOutboxRepository(e.db)
	now := time.Now().Truncate(time.Microsecond)
	hubtest.SeedConnection(t, e.db)
	for i := range 40 {
		enqueue(t, repo, domain.OutboxInventory, nil, "inv:sku:"+string(rune('A'+i%26))+string(rune('a'+i/26)), `{}`, now)
	}

	var mu sync.Mutex
	seen := map[int64]int{}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for {
				items, err := repo.Claim(t.Context(), now, time.Minute, 3)
				if err != nil || len(items) == 0 {
					return
				}
				mu.Lock()
				for _, it := range items {
					seen[it.ID]++
				}
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	assert.Len(t, seen, 40, "every row is claimed")
	for id, n := range seen {
		assert.Equal(t, 1, n, "row %d claimed %d times", id, n)
	}
}

// rawInventory inserts an inventory row in any state, bypassing Enqueue (a parked row is outside the
// pending unique index, so a pending twin of the same key may coexist).
func rawInventory(t *testing.T, e *env, key string, state domain.OutboxState) int64 {
	t.Helper()
	var id int64
	require.NoError(t, e.db.GetContext(t.Context(), &id, `
		INSERT INTO hubrise_outbox (kind, dedupe_key, state, parked_at, last_error)
		VALUES ('inventory', $1, $2, CASE WHEN $2 = 'parked' THEN now() END, CASE WHEN $2 = 'parked' THEN '422 bad' END)
		RETURNING id`, key, string(state)))
	return id
}

func stateOf(t *testing.T, repo domain.OutboxRepository, id int64) domain.OutboxState {
	t.Helper()
	got, err := repo.Get(t.Context(), id)
	require.NoError(t, err)
	return got.State
}

func TestOutboxRetryParkedInventorySupersession(t *testing.T) {
	e := newEnv(t)
	repo := infrastructure.NewOutboxRepository(e.db)
	ctx := t.Context()
	now := time.Now().Truncate(time.Microsecond)
	order := e.newOrder(t)

	t.Run("a parked row with a pending twin is skipped instead of colliding with the unique index", func(t *testing.T) {
		parked := rawInventory(t, e, "inv:sku:P", domain.OutboxParked)
		pending := rawInventory(t, e, "inv:sku:P", domain.OutboxPending)

		n, err := repo.RetryParked(ctx, parked, now)
		require.NoError(t, err)
		assert.Zero(t, n)
		assert.Equal(t, domain.OutboxSkipped, stateOf(t, repo, parked))
		got, _ := repo.Get(ctx, parked)
		assert.Equal(t, "superseded by a newer inventory row", *got.LastError)
		assert.Equal(t, domain.OutboxPending, stateOf(t, repo, pending), "the newer state stays queued")
	})

	t.Run("retry all keeps going: one superseded row does not block the others", func(t *testing.T) {
		parked := rawInventory(t, e, "inv:sku:Q", domain.OutboxParked)
		pending := rawInventory(t, e, "inv:sku:Q", domain.OutboxPending)
		lone := rawInventory(t, e, "inv:sku:R", domain.OutboxParked)
		create := enqueue(t, repo, domain.OutboxOrderCreate, &order, "", `{}`, now)
		_, err := e.db.ExecContext(ctx, `UPDATE hubrise_outbox SET state = 'parked', parked_at = now() WHERE id = $1`, create)
		require.NoError(t, err)

		n, err := repo.RetryAllParked(ctx, now)
		require.NoError(t, err)
		assert.EqualValues(t, 2, n)
		assert.Equal(t, domain.OutboxSkipped, stateOf(t, repo, parked))
		assert.Equal(t, domain.OutboxPending, stateOf(t, repo, pending))
		assert.Equal(t, domain.OutboxPending, stateOf(t, repo, lone))
		assert.Equal(t, domain.OutboxPending, stateOf(t, repo, create))
	})

	t.Run("only the newest of several parked rows of a key is retried", func(t *testing.T) {
		old := rawInventory(t, e, "inv:sku:S", domain.OutboxParked)
		newest := rawInventory(t, e, "inv:sku:S", domain.OutboxParked)

		n, err := repo.RetryAllParked(ctx, now)
		require.NoError(t, err)
		assert.EqualValues(t, 1, n)
		assert.Equal(t, domain.OutboxSkipped, stateOf(t, repo, old))
		assert.Equal(t, domain.OutboxPending, stateOf(t, repo, newest))
	})

	t.Run("retrying an older parked row retries the newest parked row of its key", func(t *testing.T) {
		old := rawInventory(t, e, "inv:sku:T", domain.OutboxParked)
		newest := rawInventory(t, e, "inv:sku:T", domain.OutboxParked)

		n, err := repo.RetryParked(ctx, old, now)
		require.NoError(t, err)
		assert.EqualValues(t, 1, n)
		assert.Equal(t, domain.OutboxSkipped, stateOf(t, repo, old))
		assert.Equal(t, domain.OutboxPending, stateOf(t, repo, newest))
	})

	t.Run("a newer row already sending supersedes the parked one", func(t *testing.T) {
		parked := rawInventory(t, e, "inv:sku:U", domain.OutboxParked)
		sending := rawInventory(t, e, "inv:sku:U", domain.OutboxSending)

		n, err := repo.RetryAllParked(ctx, now)
		require.NoError(t, err)
		assert.Zero(t, n)
		assert.Equal(t, domain.OutboxSkipped, stateOf(t, repo, parked))
		assert.Equal(t, domain.OutboxSending, stateOf(t, repo, sending))
	})

	t.Run("an older sent row does not supersede", func(t *testing.T) {
		sent := rawInventory(t, e, "inv:sku:V", domain.OutboxSent)
		parked := rawInventory(t, e, "inv:sku:V", domain.OutboxParked)

		n, err := repo.RetryParked(ctx, parked, now)
		require.NoError(t, err)
		assert.EqualValues(t, 1, n)
		assert.Equal(t, domain.OutboxPending, stateOf(t, repo, parked))
		assert.Equal(t, domain.OutboxSent, stateOf(t, repo, sent))
	})
}

// A failure in either statement or at commit rolls the whole retry back and is reported.
func TestOutboxRetryParkedFailuresRollBack(t *testing.T) {
	ctx := t.Context()
	now := time.Now().Truncate(time.Microsecond)

	for name, ddl := range map[string]string{
		"supersede": `CREATE FUNCTION f() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'boom'; END $$;
			CREATE TRIGGER t BEFORE UPDATE ON hubrise_outbox FOR EACH ROW WHEN (NEW.state = 'skipped') EXECUTE FUNCTION f()`,
		"retry": `CREATE FUNCTION f() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'boom'; END $$;
			CREATE TRIGGER t BEFORE UPDATE ON hubrise_outbox FOR EACH ROW WHEN (NEW.state = 'pending') EXECUTE FUNCTION f()`,
		"commit": `CREATE FUNCTION f() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'boom'; END $$;
			CREATE CONSTRAINT TRIGGER t AFTER UPDATE ON hubrise_outbox DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
			WHEN (NEW.state = 'pending') EXECUTE FUNCTION f()`,
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			repo := infrastructure.NewOutboxRepository(e.db)
			// One superseded row (for the first statement) and one plain parked row (for the second).
			superseded := rawInventory(t, e, "inv:sku:X", domain.OutboxParked)
			rawInventory(t, e, "inv:sku:X", domain.OutboxPending)
			plain := rawInventory(t, e, "inv:sku:Y", domain.OutboxParked)
			_, err := e.db.ExecContext(ctx, ddl)
			require.NoError(t, err)

			_, err = repo.RetryAllParked(ctx, now)
			assert.Error(t, err)
			assert.Equal(t, domain.OutboxParked, stateOf(t, repo, superseded), "rolled back")
			assert.Equal(t, domain.OutboxParked, stateOf(t, repo, plain), "rolled back")
		})
	}
}

func TestOutboxActiveDedupeKeys(t *testing.T) {
	e := newEnv(t)
	repo := infrastructure.NewOutboxRepository(e.db)
	ctx := t.Context()
	order := e.newOrder(t)

	rawInventory(t, e, "inv:sku:pending", domain.OutboxPending)
	rawInventory(t, e, "inv:sku:pending", domain.OutboxSent)
	rawInventory(t, e, "inv:sku:sending", domain.OutboxSending)
	rawInventory(t, e, "inv:sku:parked", domain.OutboxParked)
	rawInventory(t, e, "inv:sku:sent", domain.OutboxSent)
	rawInventory(t, e, "inv:sku:skipped", domain.OutboxSkipped)
	enqueue(t, repo, domain.OutboxOrderCreate, &order, "order:key", `{}`, time.Now())

	keys, err := repo.ActiveDedupeKeys(ctx, domain.OutboxInventory)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"inv:sku:pending": true, "inv:sku:sending": true}, keys,
		"only what is still on its way, and only the kind asked for")

	none, err := repo.ActiveDedupeKeys(ctx, domain.OutboxCustomer)
	require.NoError(t, err)
	assert.Empty(t, none)
}

// piiPayload is an order_create payload as the pusher freezes it.
func piiPayload(user uuid.UUID) string {
	return `{"v":1,"ref":"AB12","user_id":"` + user.String() + `","customer_notes":"ring twice","total":"12.80",` +
		`"customer":{"first_name":"John","last_name":"Doe","email":"john@example.com","phone":"+32470000000",` +
		`"address_1":"1 Rue X","city":"Liege","latitude":"50.6","longitude":"5.5","delivery_notes":"3rd floor"},` +
		`"items":[{"product_name":"Salmon","quantity":1}]}`
}

func payloadOf(t *testing.T, repo domain.OutboxRepository, id int64) string {
	t.Helper()
	got, err := repo.Get(t.Context(), id)
	require.NoError(t, err)
	return string(got.Payload)
}

func assertScrubbed(t *testing.T, payload string, msg string) {
	t.Helper()
	for _, pii := range []string{"john@example.com", "+32470000000", "Rue X", "ring twice", "3rd floor", `"customer"`} {
		assert.NotContains(t, payload, pii, msg)
	}
	assert.Contains(t, payload, "Salmon", msg+": the items stay")
	assert.Contains(t, payload, "12.80", msg+": and the total")
}

// The customer frozen in an order_create is dropped once the row is final, so a deleted account
// leaves nothing behind in the outbox.
func TestOutboxDropsThePersonalDataOfFinishedOrderCreates(t *testing.T) {
	e := newEnv(t)
	repo := infrastructure.NewOutboxRepository(e.db)
	ctx := t.Context()
	now := time.Now().Truncate(time.Microsecond)
	hubtest.SeedConnection(t, e.db)
	user := e.fx.RegularUser.ID

	t.Run("sent", func(t *testing.T) {
		id := enqueue(t, repo, domain.OutboxOrderCreate, ptr(e.newOrder(t)), "", piiPayload(user), now)
		assert.Contains(t, payloadOf(t, repo, id), "john@example.com", "queued rows keep it: they are about to be sent")
		items, err := repo.Claim(ctx, now, time.Minute, 10)
		require.NoError(t, err)
		require.Equal(t, []int64{id}, ids(items))
		assert.Contains(t, string(items[0].Payload), "john@example.com", "and the worker gets it")
		ok, err := repo.MarkSent(ctx, id, now)
		require.NoError(t, err)
		require.True(t, ok)
		assertScrubbed(t, payloadOf(t, repo, id), "sent")
	})
	t.Run("skipped", func(t *testing.T) {
		id := enqueue(t, repo, domain.OutboxOrderCreate, ptr(e.newOrder(t)), "", piiPayload(user), now)
		ok, err := repo.MarkSkipped(ctx, id, "not worth sending")
		require.NoError(t, err)
		require.True(t, ok)
		assertScrubbed(t, payloadOf(t, repo, id), "skipped")
	})
	t.Run("parked rows keep it for the retry", func(t *testing.T) {
		id := enqueue(t, repo, domain.OutboxOrderCreate, ptr(e.newOrder(t)), "", piiPayload(user), now)
		_, err := repo.Claim(ctx, now, time.Minute, 10)
		require.NoError(t, err)
		ok, err := repo.MarkParked(ctx, id, now, "422")
		require.NoError(t, err)
		require.True(t, ok)
		assert.Contains(t, payloadOf(t, repo, id), "john@example.com")
	})
	t.Run("other kinds are left as they are", func(t *testing.T) {
		id := enqueue(t, repo, domain.OutboxOrderStatus, ptr(e.newOrder(t)), "o:1", `{"status":"accepted"}`, now)
		_, err := repo.Claim(ctx, now, time.Minute, 10)
		require.NoError(t, err)
		ok, err := repo.MarkSent(ctx, id, now)
		require.NoError(t, err)
		require.True(t, ok)
		assert.JSONEq(t, `{"status":"accepted"}`, payloadOf(t, repo, id))
	})
}

func TestOutboxSkipsDropThePersonalDataToo(t *testing.T) {
	e := newEnv(t)
	repo := infrastructure.NewOutboxRepository(e.db)
	ctx := t.Context()
	now := time.Now().Truncate(time.Microsecond)
	order := e.newOrder(t)
	user := e.fx.RegularUser.ID

	create := enqueue(t, repo, domain.OutboxOrderCreate, &order, "", piiPayload(user), now)
	n, err := repo.SkipPendingUpTo(ctx, order, domain.OutboxOrderCreate, create, "covered")
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	assertScrubbed(t, payloadOf(t, repo, create), "covered by another row")

	keyed := enqueue(t, repo, domain.OutboxOrderCreate, ptr(e.newOrder(t)), "k:1", piiPayload(user), now)
	n, err = repo.SkipOpenByDedupeKey(ctx, domain.OutboxOrderCreate, "k:1", "superseded")
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	assertScrubbed(t, payloadOf(t, repo, keyed), "skipped by key")
}

func TestOutboxScrubCustomerData(t *testing.T) {
	e := newEnv(t)
	repo := infrastructure.NewOutboxRepository(e.db)
	ctx := t.Context()
	now := time.Now().Truncate(time.Microsecond)
	user, other := e.fx.RegularUser.ID, e.fx.AdminUser.ID
	setState := func(id int64, state string) {
		_, err := e.db.ExecContext(ctx, `UPDATE hubrise_outbox SET state = $2 WHERE id = $1`, id, state)
		require.NoError(t, err)
	}

	parked := enqueue(t, repo, domain.OutboxOrderCreate, ptr(e.newOrder(t)), "", piiPayload(user), now)
	setState(parked, "parked")
	sent := enqueue(t, repo, domain.OutboxOrderCreate, ptr(e.newOrder(t)), "", piiPayload(user), now)
	setState(sent, "sent") // a row of an older build, sent with its data
	pending := enqueue(t, repo, domain.OutboxOrderCreate, ptr(e.newOrder(t)), "", piiPayload(user), now)
	someoneElse := enqueue(t, repo, domain.OutboxOrderCreate, ptr(e.newOrder(t)), "", piiPayload(other), now)
	setState(someoneElse, "parked")

	n, err := repo.ScrubCustomerData(ctx, user)
	require.NoError(t, err)
	assert.EqualValues(t, 2, n)
	assertScrubbed(t, payloadOf(t, repo, parked), "parked")
	assertScrubbed(t, payloadOf(t, repo, sent), "sent")
	assert.Contains(t, payloadOf(t, repo, pending), "john@example.com", "a row on its way is scrubbed once sent")
	assert.Contains(t, payloadOf(t, repo, someoneElse), "john@example.com", "other users are untouched")

	n, err = repo.ScrubCustomerData(ctx, user)
	require.NoError(t, err)
	assert.Zero(t, n, "nothing left to scrub")
}

func TestOutboxPurgeFinished(t *testing.T) {
	e := newEnv(t)
	repo := infrastructure.NewOutboxRepository(e.db)
	ctx := t.Context()
	now := time.Now().Truncate(time.Microsecond)
	old := now.Add(-10 * 24 * time.Hour)
	cutoff := now.Add(-7 * 24 * time.Hour)
	user, erased := e.fx.RegularUser.ID, e.fx.AdminUser.ID
	_, err := e.db.ExecContext(ctx, `UPDATE users SET deletion_requested_at = now() WHERE id = $1`, erased)
	require.NoError(t, err)
	set := func(id int64, state string, sentAt, createdAt any) {
		_, err := e.db.ExecContext(ctx, `UPDATE hubrise_outbox SET state = $2, sent_at = $3, created_at = $4 WHERE id = $1`, id, state, sentAt, createdAt)
		require.NoError(t, err)
	}
	exists := func(id int64) bool {
		_, err := repo.Get(ctx, id)
		return err == nil
	}

	oldSent := enqueue(t, repo, domain.OutboxOrderStatus, ptr(e.newOrder(t)), "o:1", `{}`, now)
	set(oldSent, "sent", old, old)
	oldSkipped := enqueue(t, repo, domain.OutboxInventory, nil, "inv:sku:A", `{}`, now)
	set(oldSkipped, "skipped", nil, old)
	recentSent := enqueue(t, repo, domain.OutboxCustomer, nil, "c:1", `{}`, now)
	set(recentSent, "sent", now, now)
	oldPending := enqueue(t, repo, domain.OutboxOrderStatus, ptr(e.newOrder(t)), "o:2", `{}`, now)
	set(oldPending, "pending", nil, old)
	oldParked := enqueue(t, repo, domain.OutboxOrderStatus, ptr(e.newOrder(t)), "o:3", `{}`, now)
	set(oldParked, "parked", nil, old)

	// order_create rows stay (the re-creation guard counts them), without personal data.
	createSent := enqueue(t, repo, domain.OutboxOrderCreate, ptr(e.newOrder(t)), "", piiPayload(user), now)
	set(createSent, "sent", old, old)
	createSkipped := enqueue(t, repo, domain.OutboxOrderCreate, ptr(e.newOrder(t)), "", piiPayload(user), now)
	set(createSkipped, "skipped", nil, now)
	createParked := enqueue(t, repo, domain.OutboxOrderCreate, ptr(e.newOrder(t)), "", piiPayload(user), now)
	set(createParked, "parked", nil, now)
	createParkedErased := enqueue(t, repo, domain.OutboxOrderCreate, ptr(e.newOrder(t)), "", piiPayload(erased), now)
	set(createParkedErased, "parked", nil, now)
	createPendingErased := enqueue(t, repo, domain.OutboxOrderCreate, ptr(e.newOrder(t)), "", piiPayload(erased), now)

	deleted, err := repo.PurgeFinished(ctx, cutoff)
	require.NoError(t, err)
	assert.EqualValues(t, 2, deleted)
	assert.False(t, exists(oldSent))
	assert.False(t, exists(oldSkipped))
	for name, id := range map[string]int64{"recent": recentSent, "pending": oldPending, "parked": oldParked, "create": createSent} {
		assert.True(t, exists(id), name)
	}

	assertScrubbed(t, payloadOf(t, repo, createSent), "sent create")
	assertScrubbed(t, payloadOf(t, repo, createSkipped), "skipped create")
	assert.Contains(t, payloadOf(t, repo, createParked), "john@example.com", "a parked row of a live account waits for its retry")
	assertScrubbed(t, payloadOf(t, repo, createParkedErased), "parked create of an erased account")
	assert.Contains(t, payloadOf(t, repo, createPendingErased), "john@example.com", "a row on its way is scrubbed once sent")
}
