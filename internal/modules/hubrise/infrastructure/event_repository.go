package infrastructure

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"tsb-service/internal/modules/hubrise/domain"
)

const eventColumns = `id, resource_type, event_type, payload, received_at, run_after, state, attempts,
	next_attempt_at, processed_at, last_error`

type eventRepository struct{ store }

// NewEventRepository returns the store of received callback events.
func NewEventRepository(db *sqlx.DB) domain.EventRepository {
	return &eventRepository{newStore(db)}
}

func (r *eventRepository) Insert(ctx context.Context, e domain.Event) (bool, error) {
	if len(e.Payload) == 0 {
		e.Payload = []byte(`{}`) // a nil json.RawMessage would be written as SQL NULL
	}
	return r.affected(ctx, "insert event", `
		INSERT INTO hubrise_events (id, resource_type, event_type, payload, received_at, run_after)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (id) DO NOTHING`,
		e.ID, e.ResourceType, e.EventType, []byte(e.Payload), e.ReceivedAt, e.RunAfter)
}

func (r *eventRepository) Claim(ctx context.Context, now time.Time, lease time.Duration, limit int, resourceTypes ...string) ([]domain.Event, error) {
	events := []domain.Event{}
	err := r.sel(ctx, "claim events", &events, `
		UPDATE hubrise_events SET attempts = attempts + 1, next_attempt_at = $2
		WHERE id IN (
			SELECT id FROM hubrise_events
			WHERE state = 'pending'
			  AND (run_after IS NULL OR run_after <= $1)
			  AND (next_attempt_at IS NULL OR next_attempt_at <= $1)
			  AND (cardinality($4::text[]) = 0 OR resource_type = ANY($4))
			ORDER BY received_at, id
			LIMIT $3
			FOR UPDATE SKIP LOCKED)
		RETURNING `+eventColumns, now, now.Add(lease), limit, pq.Array(append([]string{}, resourceTypes...)))
	if err != nil {
		return nil, err
	}
	slices.SortFunc(events, func(a, b domain.Event) int {
		if c := a.ReceivedAt.Compare(b.ReceivedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return events, nil
}

func (r *eventRepository) finish(ctx context.Context, what string, state domain.EventState, id string, now time.Time, reason string) error {
	return r.exec(ctx, what, `
		UPDATE hubrise_events SET state = $2, processed_at = $3, last_error = NULLIF($4, '')
		WHERE id = $1`, id, state, now, reason)
}

func (r *eventRepository) MarkDone(ctx context.Context, id string, now time.Time) error {
	return r.finish(ctx, "mark event done", domain.EventDone, id, now, "")
}

func (r *eventRepository) MarkIgnored(ctx context.Context, id string, now time.Time, reason string) error {
	return r.finish(ctx, "mark event ignored", domain.EventIgnored, id, now, reason)
}

func (r *eventRepository) MarkFailed(ctx context.Context, id string, now time.Time, reason string) error {
	return r.finish(ctx, "mark event failed", domain.EventFailed, id, now, reason)
}

func (r *eventRepository) Defer(ctx context.Context, id string, next time.Time) error {
	return r.exec(ctx, "defer event", `
		UPDATE hubrise_events SET next_attempt_at = $2, attempts = GREATEST(attempts - 1, 0)
		WHERE id = $1 AND state = 'pending'`, id, next)
}

func (r *eventRepository) RetryLater(ctx context.Context, id string, next time.Time, reason string) error {
	return r.exec(ctx, "retry event later", `
		UPDATE hubrise_events SET next_attempt_at = $2, last_error = NULLIF($3, '')
		WHERE id = $1 AND state = 'pending'`, id, next, reason)
}

func (r *eventRepository) CollapsePending(ctx context.Context, resourceType string, receivedBefore, now time.Time) (int64, error) {
	return r.execN(ctx, "collapse events", `
		UPDATE hubrise_events SET state = 'done', processed_at = $3
		WHERE state = 'pending' AND resource_type = $1 AND received_at <= $2`, resourceType, receivedBefore, now)
}

func (r *eventRepository) PendingEventTypes(ctx context.Context, resourceType string, receivedBefore time.Time) ([]string, error) {
	var types []string
	err := r.sel(ctx, "pending event types", &types, `
		SELECT DISTINCT event_type FROM hubrise_events
		WHERE state = 'pending' AND resource_type = $1 AND received_at <= $2 ORDER BY event_type`, resourceType, receivedBefore)
	return types, err
}

func (r *eventRepository) OldestPendingReceivedAt(ctx context.Context, resourceType string) (*time.Time, error) {
	var at *time.Time
	err := r.get(ctx, "oldest pending event", &at,
		`SELECT min(received_at) FROM hubrise_events WHERE state = 'pending' AND resource_type = $1`, resourceType)
	return at, err
}

func (r *eventRepository) PurgeFinished(ctx context.Context, receivedBefore time.Time) (int64, error) {
	return r.execN(ctx, "purge events", `DELETE FROM hubrise_events WHERE state <> 'pending' AND received_at < $1`, receivedBefore)
}
