package domain

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// OutboxKind is the kind of request a queued row stands for.
type OutboxKind string

const (
	OutboxOrderCreate       OutboxKind = "order_create"
	OutboxOrderStatus       OutboxKind = "order_status"
	OutboxInventory         OutboxKind = "inventory"
	OutboxCustomer          OutboxKind = "customer"
	OutboxCustomerAnonymise OutboxKind = "customer_anonymise"
)

// OutboxState is the life cycle of a queued row.
type OutboxState string

const (
	OutboxPending OutboxState = "pending"
	OutboxSending OutboxState = "sending"
	OutboxSent    OutboxState = "sent"
	OutboxParked  OutboxState = "parked"
	OutboxSkipped OutboxState = "skipped"
)

// OutboxItem is one queued request to HubRise.
type OutboxItem struct {
	ID            int64           `db:"id"`
	Kind          OutboxKind      `db:"kind"`
	OrderID       *uuid.UUID      `db:"order_id"`
	DedupeKey     *string         `db:"dedupe_key"`
	Payload       json.RawMessage `db:"payload"`
	State         OutboxState     `db:"state"`
	Attempts      int             `db:"attempts"`
	LeasedUntil   *time.Time      `db:"leased_until"`
	NextAttemptAt time.Time       `db:"next_attempt_at"`
	LastError     *string         `db:"last_error"`
	CreatedAt     time.Time       `db:"created_at"`
	SentAt        *time.Time      `db:"sent_at"`
	ParkedAt      *time.Time      `db:"parked_at"`
}

// NewOutboxItem returns a pending item due at due. A nil payload becomes {} (the column is NOT
// NULL and a nil json.RawMessage would be written as SQL NULL).
func NewOutboxItem(kind OutboxKind, orderID *uuid.UUID, dedupeKey string, payload json.RawMessage, due time.Time) OutboxItem {
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	item := OutboxItem{Kind: kind, OrderID: orderID, Payload: payload, State: OutboxPending, NextAttemptAt: due}
	if dedupeKey != "" {
		item.DedupeKey = &dedupeKey
	}
	return item
}

// OutboxCounts is the backlog summary shown in the dashboard.
type OutboxCounts struct {
	Pending int
	Parked  int
}

// OutboxRepository is the durable queue of everything that leaves for HubRise.
type OutboxRepository interface {
	// Enqueue inserts the item. An inventory item whose dedupe key already has a pending row
	// replaces that row's payload and due time instead (latest state wins). merged reports that
	// case; the returned id is the row's.
	Enqueue(ctx context.Context, item OutboxItem) (id int64, merged bool, err error)
	// Claim leases up to limit due rows to a worker: it flips them to sending, increments
	// attempts and sets the lease, in one statement. Pending rows are due when next_attempt_at has
	// passed; sending rows whose lease expired are reclaimed. Only the first row of an order (or
	// of a dedupe key) is claimable, and only while the connection is active with the matching
	// feature toggle on.
	Claim(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]OutboxItem, error)
	// MarkSent completes a claimed row; false when it was no longer sending.
	MarkSent(ctx context.Context, id int64, now time.Time) (bool, error)
	// MarkParked parks a claimed row with the reason; false when it was no longer sending.
	MarkParked(ctx context.Context, id int64, now time.Time, reason string) (bool, error)
	// MarkSkipped skips a pending or claimed row with the reason.
	MarkSkipped(ctx context.Context, id int64, reason string) (bool, error)
	// Release puts a claimed row back to pending, due at next. revertAttempt undoes the claim's
	// attempts increment (connection-level refusals must not burn attempts).
	Release(ctx context.Context, id int64, next time.Time, reason string, revertAttempt bool) (bool, error)
	// Get returns ErrNotFound when the row does not exist.
	Get(ctx context.Context, id int64) (*OutboxItem, error)
	// List returns the newest rows first, optionally only one state.
	List(ctx context.Context, state *OutboxState, limit int) ([]OutboxItem, error)
	// Counts summarizes the backlog.
	Counts(ctx context.Context) (OutboxCounts, error)
	// RetryParked reactivates the whole parked chain of the order of row id (all its parked rows),
	// or, for a row without an order, that row alone. It returns how many rows were reactivated.
	RetryParked(ctx context.Context, id int64, now time.Time) (int64, error)
	// RetryAllParked reactivates every parked row.
	RetryAllParked(ctx context.Context, now time.Time) (int64, error)
}
