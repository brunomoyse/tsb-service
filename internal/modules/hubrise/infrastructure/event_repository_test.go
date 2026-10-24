package infrastructure_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/internal/modules/hubrise/infrastructure"
)

func event(id, resource string, received time.Time) domain.Event {
	return domain.Event{ID: id, ResourceType: resource, EventType: "update", Payload: json.RawMessage(`{"id":"` + id + `"}`), ReceivedAt: received}
}

func eventIDs(events []domain.Event) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.ID
	}
	return out
}

func TestEventRepository(t *testing.T) {
	e := newEnv(t)
	repo := infrastructure.NewEventRepository(e.db)
	ctx := t.Context()
	now := time.Now().Truncate(time.Microsecond)

	t.Run("insert is idempotent on the HubRise event id", func(t *testing.T) {
		ok, err := repo.Insert(ctx, event("ev-1", "order", now.Add(-3*time.Minute)))
		require.NoError(t, err)
		assert.True(t, ok)
		ok, err = repo.Insert(ctx, event("ev-1", "order", now))
		require.NoError(t, err)
		assert.False(t, ok, "a redelivery changes nothing")

		// A nil payload becomes {} rather than NULL.
		ok, err = repo.Insert(ctx, domain.Event{ID: "ev-nil", ResourceType: "inventory", EventType: "patch", ReceivedAt: now.Add(-2 * time.Minute)})
		require.NoError(t, err)
		assert.True(t, ok)
		var payload string
		require.NoError(t, e.db.GetContext(ctx, &payload, `SELECT payload::text FROM hubrise_events WHERE id = 'ev-nil'`))
		assert.JSONEq(t, `{}`, payload)
	})

	t.Run("claim respects run_after and next_attempt_at, leases and orders", func(t *testing.T) {
		later := now.Add(time.Minute)
		withRunAfter := event("ev-wait", "order", now.Add(-time.Minute))
		withRunAfter.RunAfter = &later
		_, err := repo.Insert(ctx, withRunAfter)
		require.NoError(t, err)
		_, err = repo.Insert(ctx, event("ev-2", "order", now.Add(-30*time.Second)))
		require.NoError(t, err)

		got, err := repo.Claim(ctx, now, time.Minute, 2)
		require.NoError(t, err)
		assert.Equal(t, []string{"ev-1", "ev-nil"}, eventIDs(got), "oldest first, limited, ev-wait is not due")
		assert.Equal(t, 1, got[0].Attempts)
		assert.WithinDuration(t, now.Add(time.Minute), *got[0].NextAttemptAt, time.Millisecond)
		assert.JSONEq(t, `{"id":"ev-1"}`, string(got[0].Payload))

		got, _ = repo.Claim(ctx, now, time.Minute, 10)
		assert.Equal(t, []string{"ev-2"}, eventIDs(got), "leased events are not claimed again")

		got, _ = repo.Claim(ctx, now.Add(2*time.Minute), time.Minute, 10)
		assert.ElementsMatch(t, []string{"ev-1", "ev-nil", "ev-2", "ev-wait"}, eventIDs(got), "after the lease and the run_after they all come back")
		assert.Equal(t, 2, got[0].Attempts)
	})

	t.Run("outcomes", func(t *testing.T) {
		require.NoError(t, repo.MarkDone(ctx, "ev-1", now))
		require.NoError(t, repo.MarkIgnored(ctx, "ev-nil", now, "foreign order"))
		require.NoError(t, repo.MarkFailed(ctx, "ev-2", now, "refund impossible"))
		state := func(id string) (domain.EventState, *string) {
			var ev domain.Event
			require.NoError(t, e.db.GetContext(ctx, &ev, `SELECT `+"id, resource_type, event_type, payload, received_at, run_after, state, attempts, next_attempt_at, processed_at, last_error"+` FROM hubrise_events WHERE id = $1`, id))
			return ev.State, ev.LastError
		}
		st, msg := state("ev-1")
		assert.Equal(t, domain.EventDone, st)
		assert.Nil(t, msg)
		st, msg = state("ev-nil")
		assert.Equal(t, domain.EventIgnored, st)
		assert.Equal(t, "foreign order", *msg)
		st, msg = state("ev-2")
		assert.Equal(t, domain.EventFailed, st)
		assert.Equal(t, "refund impossible", *msg)

		got, _ := repo.Claim(ctx, now.Add(10*time.Minute), time.Minute, 10)
		assert.Equal(t, []string{"ev-wait"}, eventIDs(got), "finished events are never claimed")
	})

	t.Run("defer does not count as an attempt, retry later does", func(t *testing.T) {
		load := func() domain.Event {
			var ev domain.Event
			require.NoError(t, e.db.GetContext(ctx, &ev, `SELECT id, resource_type, event_type, payload, received_at, run_after, state, attempts, next_attempt_at, processed_at, last_error FROM hubrise_events WHERE id = 'ev-wait'`))
			return ev
		}
		start := load().Attempts
		require.Positive(t, start)

		next := now.Add(time.Hour)
		require.NoError(t, repo.Defer(ctx, "ev-wait", next))
		deferred := load()
		assert.Equal(t, start-1, deferred.Attempts, "the claim that is being deferred is taken back")
		assert.WithinDuration(t, next, *deferred.NextAttemptAt, time.Millisecond)

		got, _ := repo.Claim(ctx, now.Add(2*time.Hour), time.Minute, 10)
		require.Equal(t, []string{"ev-wait"}, eventIDs(got))
		assert.Equal(t, start, got[0].Attempts)

		require.NoError(t, repo.RetryLater(ctx, "ev-wait", next.Add(2*time.Hour), "HubRise 503"))
		after := load()
		assert.Equal(t, start, after.Attempts, "a failure keeps the claim counted")
		assert.Equal(t, "HubRise 503", *after.LastError)
		assert.WithinDuration(t, next.Add(2*time.Hour), *after.NextAttemptAt, time.Millisecond)
		assert.Equal(t, domain.EventPending, after.State)
	})

	t.Run("debounce helpers for catalog events", func(t *testing.T) {
		oldest, err := repo.OldestPendingReceivedAt(ctx, "catalog")
		require.NoError(t, err)
		assert.Nil(t, oldest, "no catalog event yet")

		for i, age := range []time.Duration{90 * time.Second, 60 * time.Second, 5 * time.Second} {
			ev := event("cat-"+string(rune('a'+i)), "catalog", now.Add(-age))
			ev.EventType = "update"
			_, err := repo.Insert(ctx, ev)
			require.NoError(t, err)
		}
		oldest, err = repo.OldestPendingReceivedAt(ctx, "catalog")
		require.NoError(t, err)
		require.NotNil(t, oldest)
		assert.WithinDuration(t, now.Add(-90*time.Second), *oldest, time.Millisecond)

		types, err := repo.PendingEventTypes(ctx, "catalog", now.Add(-30*time.Second))
		require.NoError(t, err)
		assert.Equal(t, []string{"update"}, types)
		del := event("cat-del", "catalog", now.Add(-45*time.Second))
		del.EventType = "delete"
		_, err = repo.Insert(ctx, del)
		require.NoError(t, err)
		types, err = repo.PendingEventTypes(ctx, "catalog", now.Add(-30*time.Second))
		require.NoError(t, err)
		assert.Equal(t, []string{"delete", "update"}, types, "distinct, sorted, only up to the cutoff")
		types, err = repo.PendingEventTypes(ctx, "catalog", now.Add(-2*time.Minute))
		require.NoError(t, err)
		assert.Empty(t, types)
		_, err = e.db.ExecContext(ctx, `DELETE FROM hubrise_events WHERE id = 'cat-del'`)
		require.NoError(t, err)

		// The two events older than 30 s collapse into one import; the fresh one waits.
		n, err := repo.CollapsePending(ctx, "catalog", now.Add(-30*time.Second), now)
		require.NoError(t, err)
		assert.EqualValues(t, 2, n)
		oldest, _ = repo.OldestPendingReceivedAt(ctx, "catalog")
		assert.WithinDuration(t, now.Add(-5*time.Second), *oldest, time.Millisecond)
		n, _ = repo.CollapsePending(ctx, "order", now, now)
		assert.EqualValues(t, 1, n, "collapse is per resource type (ev-wait is the one pending order event)")
	})

	t.Run("purge removes finished events only", func(t *testing.T) {
		n, err := repo.PurgeFinished(ctx, now.Add(time.Hour))
		require.NoError(t, err)
		assert.Positive(t, n)
		var left int
		require.NoError(t, e.db.GetContext(ctx, &left, `SELECT count(*) FROM hubrise_events`))
		assert.Equal(t, 1, left, "only the pending catalog event is left")
	})
}

func TestEventClaimOrdersTiesById(t *testing.T) {
	e := newEnv(t)
	repo := infrastructure.NewEventRepository(e.db)
	now := time.Now().Truncate(time.Microsecond)
	for _, id := range []string{"b", "c", "a"} {
		_, err := repo.Insert(t.Context(), event(id, "order", now.Add(-time.Minute)))
		require.NoError(t, err)
	}
	got, err := repo.Claim(t.Context(), now, time.Minute, 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "c"}, eventIDs(got))
}
