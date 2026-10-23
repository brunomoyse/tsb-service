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
	// Enqueue inserts the item (its CreatedAt is stored when set, so the age of a row follows the
	// application clock; the database clock otherwise). An inventory item whose dedupe key already has a pending row
	// replaces that row's payload and due time instead (latest state wins). merged reports that
	// case; the returned id is the row's.
	Enqueue(ctx context.Context, item OutboxItem) (id int64, merged bool, err error)
	// EnqueueIfNoPending inserts a customer row unless the dedupe key already has a pending row of
	// that kind (the profile is read when the row is sent, so a second row would only repeat the
	// first). inserted is false in that case; the returned id is the new row's, 0 when not inserted.
	EnqueueIfNoPending(ctx context.Context, item OutboxItem) (id int64, inserted bool, err error)
	// SkipOpenByDedupeKey skips the pending and parked rows of the kind with the dedupe key, with the
	// reason, and returns how many. A sending row is left alone: it is on its way.
	SkipOpenByDedupeKey(ctx context.Context, kind OutboxKind, dedupeKey, reason string) (int64, error)
	// Claim leases up to limit due rows to a worker: it flips them to sending, increments
	// attempts and sets the lease, in one statement. Pending rows are due when next_attempt_at has
	// passed; sending rows whose lease expired are reclaimed. Only the first row of an order (or
	// of a dedupe key) is claimable, and only while the connection is active with the matching
	// feature toggle on. When kinds is not empty only rows of those kinds are claimed (a worker
	// that handles some kinds must never lease the others).
	Claim(ctx context.Context, now time.Time, lease time.Duration, limit int, kinds ...OutboxKind) ([]OutboxItem, error)
	// Extend pushes the lease of a claimed row to until, as long as the row is still sending and its
	// lease has not run out at now. It returns false when the row is no longer ours (another
	// replica may have reclaimed it): the caller must then leave it alone.
	Extend(ctx context.Context, id int64, now, until time.Time) (bool, error)
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
	// MaxPendingID returns the highest id among the pending rows of the order with the kind, 0
	// when there is none.
	MaxPendingID(ctx context.Context, orderID uuid.UUID, kind OutboxKind) (int64, error)
	// SkipPendingUpTo skips the pending rows of the order with the kind whose id is at most upTo,
	// with the reason, and returns how many.
	SkipPendingUpTo(ctx context.Context, orderID uuid.UUID, kind OutboxKind, upTo int64, reason string) (int64, error)
	// ActiveDedupeKeys returns the dedupe keys of the rows of the kind that are pending or being sent:
	// what is still on its way to HubRise.
	ActiveDedupeKeys(ctx context.Context, kind OutboxKind) (map[string]bool, error)
	// CountForOrder counts the rows of the order with the kind, in any state.
	CountForOrder(ctx context.Context, orderID uuid.UUID, kind OutboxKind) (int, error)
	// RetryParked reactivates the whole parked chain of the order of row id (all its parked rows),
	// or, for an inventory row, the parked rows of its dedupe key. Parked inventory rows replaced by a
	// newer pending, sending or parked row of the same key are skipped instead (only the newest state
	// of an item is worth sending). It returns how many rows were reactivated.
	RetryParked(ctx context.Context, id int64, now time.Time) (int64, error)
	// RetryAllParked reactivates every parked row, with the same inventory supersession rule.
	RetryAllParked(ctx context.Context, now time.Time) (int64, error)
	// ScrubCustomerData drops the customer and the customer notes from the sent, skipped and parked
	// order_create rows of the user (the account was erased), and returns how many rows changed.
	ScrubCustomerData(ctx context.Context, userID uuid.UUID) (int64, error)
	// PurgeFinished is the retention of the outbox: it scrubs the personal data left in final
	// order_create rows (and in the parked ones of an erased account), then deletes the sent and
	// skipped rows of the other kinds created or sent before the cutoff. It returns the deleted count.
	PurgeFinished(ctx context.Context, before time.Time) (int64, error)
}
