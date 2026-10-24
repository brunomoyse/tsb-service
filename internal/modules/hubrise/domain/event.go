package domain

import (
	"context"
	"encoding/json"
	"time"
)

// EventState is the life cycle of a received callback event.
type EventState string

const (
	EventPending EventState = "pending"
	EventDone    EventState = "done"
	EventFailed  EventState = "failed"
	EventIgnored EventState = "ignored"
)

// Event is a callback event received from HubRise. The HubRise event id is the key, so a
// redelivery is harmless.
type Event struct {
	ID            string          `db:"id"`
	ResourceType  string          `db:"resource_type"`
	EventType     string          `db:"event_type"`
	Payload       json.RawMessage `db:"payload"`
	ReceivedAt    time.Time       `db:"received_at"`
	RunAfter      *time.Time      `db:"run_after"`
	State         EventState      `db:"state"`
	Attempts      int             `db:"attempts"`
	NextAttemptAt *time.Time      `db:"next_attempt_at"`
	ProcessedAt   *time.Time      `db:"processed_at"`
	LastError     *string         `db:"last_error"`
}

// EventRepository stores received events.
type EventRepository interface {
	// Insert stores the event as pending; inserted is false when the id is already known. A nil
	// payload is stored as {}.
	Insert(ctx context.Context, e Event) (inserted bool, err error)
	// Claim leases up to limit pending events that are due (run_after and next_attempt_at passed),
	// oldest first: it increments attempts and pushes next_attempt_at out by lease, so a crashed
	// worker's events come back after the lease. When resourceTypes is not empty only events of those
	// resource types are claimed (a processor that handles some resources must never lease the
	// others).
	Claim(ctx context.Context, now time.Time, lease time.Duration, limit int, resourceTypes ...string) ([]Event, error)
	// MarkDone, MarkIgnored and MarkFailed end the processing of a claimed event.
	MarkDone(ctx context.Context, id string, now time.Time) error
	MarkIgnored(ctx context.Context, id string, now time.Time, reason string) error
	MarkFailed(ctx context.Context, id string, now time.Time, reason string) error
	// Defer keeps a claimed event pending and due at next, without counting the claim as an
	// attempt (the event is waiting for something, it did not fail).
	Defer(ctx context.Context, id string, next time.Time) error
	// RetryLater keeps a claimed event pending, due at next, and records the failure reason; the
	// claim stays counted as an attempt.
	RetryLater(ctx context.Context, id string, next time.Time, reason string) error
	// CollapsePending marks done all pending events of a resource type received at or before the
	// cutoff and returns how many (catalog events collapse into one import).
	CollapsePending(ctx context.Context, resourceType string, receivedBefore, now time.Time) (int64, error)
	// PendingEventTypes returns the distinct event types ("update", "delete"...) of the pending
	// events of a resource type received at or before the cutoff.
	PendingEventTypes(ctx context.Context, resourceType string, receivedBefore time.Time) ([]string, error)
	// OldestPendingReceivedAt returns when the oldest pending event of the resource type arrived,
	// or nil when none is pending.
	OldestPendingReceivedAt(ctx context.Context, resourceType string) (*time.Time, error)
	// PurgeFinished deletes events that are not pending and were received before the cutoff.
	PurgeFinished(ctx context.Context, receivedBefore time.Time) (int64, error)
}

// ForeignOrderRepository caches the HubRise orders known not to be ours.
type ForeignOrderRepository interface {
	Mark(ctx context.Context, hubriseOrderID string, at time.Time) error
	IsForeign(ctx context.Context, hubriseOrderID string) (bool, error)
	Purge(ctx context.Context, seenBefore time.Time) (int64, error)
}
