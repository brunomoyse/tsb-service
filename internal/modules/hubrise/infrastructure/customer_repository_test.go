package infrastructure_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/internal/modules/hubrise/infrastructure"
	hubtest "tsb-service/internal/modules/hubrise/testhelpers"
)

func TestCustomerLinkLifecycle(t *testing.T) {
	e := newEnv(t)
	links := infrastructure.NewCustomerLinkRepository(e.db)
	ctx := t.Context()
	user := e.fx.RegularUser.ID
	now := time.Now().Truncate(time.Microsecond)

	_, err := links.Get(ctx, user)
	assert.ErrorIs(t, err, domain.ErrNotFound)

	// The first Begin inserts the link, the next ones find it: only then may a customer exist already.
	link, existed, err := links.Begin(ctx, user)
	require.NoError(t, err)
	assert.False(t, existed)
	assert.Nil(t, link.HubriseCustomerID)
	assert.Empty(t, link.ContentHash)
	link, existed, err = links.Begin(ctx, user)
	require.NoError(t, err)
	assert.True(t, existed)
	assert.Equal(t, user, link.UserID)

	require.NoError(t, links.SetCustomer(ctx, user, "cu-1", "p|a", now))
	link, err = links.Get(ctx, user)
	require.NoError(t, err)
	require.NotNil(t, link.HubriseCustomerID)
	assert.Equal(t, "cu-1", *link.HubriseCustomerID)
	assert.Equal(t, "p|a", link.ContentHash)
	require.NotNil(t, link.SyncedAt)
	assert.True(t, link.SyncedAt.Equal(now))
	assert.Nil(t, link.AnonymisedAt)

	require.NoError(t, links.SetHash(ctx, user, "p2|a", now.Add(time.Minute)))
	link, _ = links.Get(ctx, user)
	assert.Equal(t, "p2|a", link.ContentHash)
	assert.True(t, link.SyncedAt.Equal(now.Add(time.Minute)))

	// A customer HubRise lost: forgotten, but the link says one was attempted.
	require.NoError(t, links.Forget(ctx, user))
	link, existed, err = links.Begin(ctx, user)
	require.NoError(t, err)
	assert.True(t, existed)
	assert.Nil(t, link.HubriseCustomerID)
	assert.Empty(t, link.ContentHash)
	assert.Nil(t, link.SyncedAt)

	require.NoError(t, links.SetCustomer(ctx, user, "cu-2", "x|y", now))
	require.NoError(t, links.MarkAnonymised(ctx, user, now))
	link, _ = links.Get(ctx, user)
	require.NotNil(t, link.AnonymisedAt)
	assert.True(t, link.AnonymisedAt.Equal(now))

	// Without a link the writers change nothing and do not fail.
	ghost := uuid.New()
	require.NoError(t, links.SetHash(ctx, ghost, "h", now))
	require.NoError(t, links.Forget(ctx, ghost))
	require.NoError(t, links.MarkAnonymised(ctx, ghost, now))
	_, err = links.Get(ctx, ghost)
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestCustomerLinkRules(t *testing.T) {
	e := newEnv(t)
	links := infrastructure.NewCustomerLinkRepository(e.db)
	ctx := t.Context()
	now := time.Now()

	t.Run("a HubRise customer belongs to one user", func(t *testing.T) {
		other := insertUser(t, e, "second@example.com")
		_, _, err := links.Begin(ctx, e.fx.RegularUser.ID)
		require.NoError(t, err)
		_, _, err = links.Begin(ctx, other)
		require.NoError(t, err)
		require.NoError(t, links.SetCustomer(ctx, e.fx.RegularUser.ID, "cu-dup", "", now))
		assert.Error(t, links.SetCustomer(ctx, other, "cu-dup", "", now))
	})

	t.Run("the link needs a user", func(t *testing.T) {
		_, _, err := links.Begin(ctx, uuid.New())
		assert.Error(t, err)
	})

	t.Run("deleting a user row removes the link", func(t *testing.T) {
		gone := insertUser(t, e, "gone@example.com")
		_, _, err := links.Begin(ctx, gone)
		require.NoError(t, err)
		_, err = e.db.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, gone)
		require.NoError(t, err)
		_, err = links.Get(ctx, gone)
		assert.ErrorIs(t, err, domain.ErrNotFound)
	})
}

func insertUser(t *testing.T, e *env, email string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, e.db.GetContext(t.Context(), &id, `
		INSERT INTO users (first_name, last_name, email, zitadel_user_id) VALUES ('T', 'U', $1, $1) RETURNING id`, email))
	return id
}

func TestCustomerSourceReadsTheProfile(t *testing.T) {
	e := newEnv(t)
	src := infrastructure.NewCustomerSource(e.db)
	ctx := t.Context()

	p, err := src.Profile(ctx, e.fx.RegularUser.ID)
	require.NoError(t, err)
	assert.Equal(t, "John", p.FirstName)
	assert.Equal(t, "Doe", p.LastName)
	assert.Equal(t, "john@example.com", p.Email)
	assert.Empty(t, p.Phone, "no phone is an empty string")
	assert.False(t, p.Deleted)

	_, err = e.db.ExecContext(ctx, `UPDATE users SET phone_number = '+32470000000' WHERE id = $1`, e.fx.RegularUser.ID)
	require.NoError(t, err)
	p, err = src.Profile(ctx, e.fx.RegularUser.ID)
	require.NoError(t, err)
	assert.Equal(t, "+32470000000", p.Phone)

	_, err = e.db.ExecContext(ctx, `UPDATE users SET deletion_requested_at = now() WHERE id = $1`, e.fx.RegularUser.ID)
	require.NoError(t, err)
	p, err = src.Profile(ctx, e.fx.RegularUser.ID)
	require.NoError(t, err)
	assert.True(t, p.Deleted)

	_, err = src.Profile(ctx, uuid.New())
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestOutboxCustomerRows(t *testing.T) {
	e := newEnv(t)
	outbox := infrastructure.NewOutboxRepository(e.db)
	ctx := t.Context()
	now := time.Now().Truncate(time.Microsecond)
	user := e.fx.RegularUser.ID
	key := domain.CustomerDedupeKey(user)
	hubtest.SeedConnection(t, e.db)
	row := func() domain.OutboxItem { return domain.NewOutboxItem(domain.OutboxCustomer, nil, key, nil, now) }

	t.Run("a pending customer row is not queued twice", func(t *testing.T) {
		id1, inserted, err := outbox.EnqueueIfNoPending(ctx, row())
		require.NoError(t, err)
		assert.True(t, inserted)
		assert.NotZero(t, id1)
		id2, inserted, err := outbox.EnqueueIfNoPending(ctx, row())
		require.NoError(t, err)
		assert.False(t, inserted)
		assert.Zero(t, id2)

		// Once the row is on its way a change made meanwhile queues a fresh one.
		items, err := outbox.Claim(ctx, now, time.Minute, 5, domain.OutboxCustomer)
		require.NoError(t, err)
		require.Len(t, items, 1)
		assert.Equal(t, id1, items[0].ID)
		assert.JSONEq(t, `{}`, string(items[0].Payload), "a nil payload is stored as {}")
		id3, inserted, err := outbox.EnqueueIfNoPending(ctx, row())
		require.NoError(t, err)
		assert.True(t, inserted)
		assert.NotEqual(t, id1, id3)

		// FIFO per user: the second row waits for the first.
		again, err := outbox.Claim(ctx, now, time.Minute, 5, domain.OutboxCustomer)
		require.NoError(t, err)
		assert.Empty(t, again)
		ok, err := outbox.MarkSent(ctx, id1, now)
		require.NoError(t, err)
		require.True(t, ok)
		again, err = outbox.Claim(ctx, now, time.Minute, 5, domain.OutboxCustomer)
		require.NoError(t, err)
		require.Len(t, again, 1)
		assert.Equal(t, id3, again[0].ID)
	})

	t.Run("a row that comes back while a newer twin is pending is superseded, not re-pended", func(t *testing.T) {
		e2 := newEnv(t)
		hubtest.SeedConnection(t, e2.db)
		ob := infrastructure.NewOutboxRepository(e2.db)
		for _, kind := range []domain.OutboxKind{domain.OutboxCustomer, domain.OutboxInventory} {
			k := "k-" + string(kind)
			first, _, err := ob.Enqueue(ctx, domain.NewOutboxItem(kind, nil, k, nil, now))
			require.NoError(t, err)
			claimed, err := ob.Claim(ctx, now, time.Minute, 5, kind)
			require.NoError(t, err)
			require.Len(t, claimed, 1)
			twin, _, err := ob.Enqueue(ctx, domain.NewOutboxItem(kind, nil, k, nil, now))
			require.NoError(t, err)
			require.NotEqual(t, first, twin, "a row on its way is outside the unique index")

			ok, err := ob.Release(ctx, first, now.Add(time.Minute), "HubRise 503", false)
			require.NoError(t, err, kind)
			assert.True(t, ok)
			got, err := ob.Get(ctx, first)
			require.NoError(t, err)
			assert.Equal(t, domain.OutboxSkipped, got.State, kind)
			assert.Equal(t, "superseded by a newer row", *got.LastError)
			assert.Nil(t, got.LeasedUntil)
			got, err = ob.Get(ctx, twin)
			require.NoError(t, err)
			assert.Equal(t, domain.OutboxPending, got.State, "the newer row carries on")
		}
	})

	t.Run("an item without a payload is stored with {}", func(t *testing.T) {
		other := "cust:other"
		id, inserted, err := outbox.EnqueueIfNoPending(ctx, domain.OutboxItem{
			Kind: domain.OutboxCustomer, DedupeKey: &other, NextAttemptAt: now, CreatedAt: now,
		})
		require.NoError(t, err)
		require.True(t, inserted)
		item, err := outbox.Get(ctx, id)
		require.NoError(t, err)
		assert.JSONEq(t, `{}`, string(item.Payload))
	})

	t.Run("other kinds with the same key are not merged", func(t *testing.T) {
		other := domain.NewOutboxItem(domain.OutboxCustomerAnonymise, nil, key, []byte(`{"a":1}`), now)
		_, merged, err := outbox.Enqueue(ctx, other)
		require.NoError(t, err)
		assert.False(t, merged)
		_, merged, err = outbox.Enqueue(ctx, other)
		require.NoError(t, err)
		assert.False(t, merged)
	})

	t.Run("skipping the open rows of a key leaves the rows on their way", func(t *testing.T) {
		e2 := newEnv(t)
		ob := infrastructure.NewOutboxRepository(e2.db)
		k := domain.CustomerDedupeKey(e2.fx.RegularUser.ID)
		mk := func(state string) int64 {
			var id int64
			require.NoError(t, e2.db.GetContext(ctx, &id, `
				INSERT INTO hubrise_outbox (kind, dedupe_key, state) VALUES ('customer', $1, $2) RETURNING id`, k, state))
			return id
		}
		sent, sending, parked, pending := mk("sent"), mk("sending"), mk("parked"), mk("pending")
		n, err := ob.SkipOpenByDedupeKey(ctx, domain.OutboxCustomer, k, "the account was deleted")
		require.NoError(t, err)
		assert.EqualValues(t, 2, n)
		for id, want := range map[int64]domain.OutboxState{
			sent: domain.OutboxSent, sending: domain.OutboxSending, parked: domain.OutboxSkipped, pending: domain.OutboxSkipped,
		} {
			item, err := ob.Get(ctx, id)
			require.NoError(t, err)
			assert.Equal(t, want, item.State)
		}
		n, err = ob.SkipOpenByDedupeKey(ctx, domain.OutboxCustomer, "cust:nobody", "x")
		require.NoError(t, err)
		assert.Zero(t, n)
	})
}

func TestCustomerRowsFollowTheSyncCustomersToggle(t *testing.T) {
	e := newEnv(t)
	outbox := infrastructure.NewOutboxRepository(e.db)
	ctx := t.Context()
	now := time.Now().Truncate(time.Microsecond)
	hubtest.SeedConnection(t, e.db)

	_, _, err := outbox.EnqueueIfNoPending(ctx, domain.NewOutboxItem(domain.OutboxCustomer, nil, "cust:a", nil, now))
	require.NoError(t, err)
	_, _, err = outbox.Enqueue(ctx, domain.NewOutboxItem(domain.OutboxCustomerAnonymise, nil, "cust:b", nil, now))
	require.NoError(t, err)

	_, err = e.db.ExecContext(ctx, `UPDATE hubrise_connection SET sync_customers = FALSE`)
	require.NoError(t, err)
	items, err := outbox.Claim(ctx, now, time.Minute, 5, domain.OutboxCustomer, domain.OutboxCustomerAnonymise)
	require.NoError(t, err)
	require.Len(t, items, 1, "only the erasure of a deleted account leaves while the customer sync is off")
	assert.Equal(t, domain.OutboxCustomerAnonymise, items[0].Kind)

	_, err = e.db.ExecContext(ctx, `UPDATE hubrise_outbox SET state = 'pending', leased_until = NULL`)
	require.NoError(t, err)
	_, err = e.db.ExecContext(ctx, `UPDATE hubrise_connection SET sync_customers = TRUE`)
	require.NoError(t, err)
	items, err = outbox.Claim(ctx, now, time.Minute, 5, domain.OutboxCustomer, domain.OutboxCustomerAnonymise)
	require.NoError(t, err)
	assert.Len(t, items, 2)

	// A worker that does not handle customers never leases them.
	_, err = e.db.ExecContext(ctx, `UPDATE hubrise_outbox SET state = 'pending', leased_until = NULL`)
	require.NoError(t, err)
	items, err = outbox.Claim(ctx, now, time.Minute, 5, domain.OutboxOrderCreate, domain.OutboxOrderStatus)
	require.NoError(t, err)
	assert.Empty(t, items)
}

func TestHubriseCustomersMigration(t *testing.T) {
	td := newEnv(t).td
	p := newProvider(t, td)
	ctx := t.Context()
	require.True(t, tableExists(t, td, "hubrise_customers"))

	_, err := p.DownTo(ctx, m6Version)
	require.NoError(t, err)
	assert.False(t, tableExists(t, td, "hubrise_customers"), "Down drops the table")
	var idx *string
	require.NoError(t, td.DB.GetContext(ctx, &idx, `SELECT to_regclass('public.hubrise_outbox_customer_pending_uidx')::text`))
	assert.Nil(t, idx, "and the index")

	_, err = p.Up(ctx)
	require.NoError(t, err)
	assert.True(t, tableExists(t, td, "hubrise_customers"))
}

func TestCustomerSourceListsTheDeletedAccountsNotErased(t *testing.T) {
	e := newEnv(t)
	src := infrastructure.NewCustomerSource(e.db)
	links := infrastructure.NewCustomerLinkRepository(e.db)
	outbox := infrastructure.NewOutboxRepository(e.db)
	ctx := t.Context()
	now := time.Now().Truncate(time.Microsecond)
	user := func(email string, deleted bool) uuid.UUID {
		id := insertUser(t, e, email)
		_, _, err := links.Begin(ctx, id)
		require.NoError(t, err)
		if deleted {
			_, err = e.db.ExecContext(ctx, `UPDATE users SET deletion_requested_at = $2 WHERE id = $1`, id, now)
			require.NoError(t, err)
		}
		return id
	}

	alive := user("alive@example.com", false)
	missed := user("missed@example.com", true)
	queued := user("queued@example.com", true)
	erased := user("erased@example.com", true)
	parked := user("parked@example.com", true)
	skipped := user("skipped@example.com", true)
	_ = alive
	_, _, err := outbox.Enqueue(ctx, domain.NewOutboxItem(domain.OutboxCustomerAnonymise, nil, domain.CustomerDedupeKey(queued), nil, now))
	require.NoError(t, err)
	require.NoError(t, links.MarkAnonymised(ctx, erased, now))
	parkedRow, _, err := outbox.Enqueue(ctx, domain.NewOutboxItem(domain.OutboxCustomerAnonymise, nil, domain.CustomerDedupeKey(parked), nil, now))
	require.NoError(t, err)
	_, err = e.db.ExecContext(ctx, `UPDATE hubrise_outbox SET state = 'parked' WHERE id = $1`, parkedRow)
	require.NoError(t, err)
	// A row that was skipped did not erase anything: the account is listed again.
	skippedRow, _, err := outbox.Enqueue(ctx, domain.NewOutboxItem(domain.OutboxCustomerAnonymise, nil, domain.CustomerDedupeKey(skipped), nil, now))
	require.NoError(t, err)
	_, err = e.db.ExecContext(ctx, `UPDATE hubrise_outbox SET state = 'skipped' WHERE id = $1`, skippedRow)
	require.NoError(t, err)

	got, err := src.DeletedNotErased(ctx, 10)
	require.NoError(t, err)
	assert.ElementsMatch(t, []uuid.UUID{missed, skipped}, got)

	got, err = src.DeletedNotErased(ctx, 1)
	require.NoError(t, err)
	assert.Len(t, got, 1)
}
