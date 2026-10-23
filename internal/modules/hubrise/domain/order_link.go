package domain

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// PushState is the state of the push of one order.
type PushState string

const (
	PushPending PushState = "pending"
	PushSent    PushState = "sent"
	PushParked  PushState = "parked"
	PushSkipped PushState = "skipped"
)

// OrderLink ties a local order to its HubRise order. Its existence is also the idempotency anchor
// of the order_create push.
type OrderLink struct {
	OrderID          uuid.UUID  `db:"order_id"`
	HubriseOrderID   *string    `db:"hubrise_order_id"`
	PushState        PushState  `db:"push_state"`
	PushedAt         *time.Time `db:"pushed_at"`
	LastRemoteStatus *string    `db:"last_remote_status"`
	// LastRemoteEventAt is set only from remote event or remote GET data, never from our clock.
	LastRemoteEventAt *time.Time `db:"last_remote_event_at"`
	// LastLocalPatchAt is set when we send a status PATCH.
	LastLocalPatchAt *time.Time       `db:"last_local_patch_at"`
	RemoteTotal      *decimal.Decimal `db:"remote_total"`
	NeedsAttention   *string          `db:"needs_attention"`
	CreatedAt        time.Time        `db:"created_at"`
	UpdatedAt        time.Time        `db:"updated_at"`
}

// OrderLinkRepository stores the order links.
type OrderLinkRepository interface {
	// EnqueueCreate is the idempotent enqueue of an order_create: in one transaction it inserts the
	// link (ON CONFLICT DO NOTHING) and, only if it was inserted, the outbox row. enqueued is false
	// when the order was already known (a redelivered webhook, a second call).
	EnqueueCreate(ctx context.Context, orderID uuid.UUID, payload json.RawMessage, due time.Time) (enqueued bool, err error)
	// Get returns ErrNotFound when the order has no link.
	Get(ctx context.Context, orderID uuid.UUID) (*OrderLink, error)
	// FindByHubriseID returns ErrNotFound when no order has that HubRise id.
	FindByHubriseID(ctx context.Context, hubriseOrderID string) (*OrderLink, error)
	// MarkSent stores the HubRise id and status after a successful create (or an adoption through
	// private_ref) and sets the push state to sent.
	MarkSent(ctx context.Context, orderID uuid.UUID, hubriseOrderID, remoteStatus string, at time.Time) error
	// SetPushState changes the push state (parked, skipped...).
	SetPushState(ctx context.Context, orderID uuid.UUID, state PushState) error
	// ResetToPending forgets the HubRise id and goes back to pending (HubRise does not know the
	// order: the create is enqueued again).
	ResetToPending(ctx context.Context, orderID uuid.UUID) error
	// RecordRemoteStatus stores the last status seen at HubRise and, when eventAt is non-nil, the
	// time of the remote event or read it came from.
	RecordRemoteStatus(ctx context.Context, orderID uuid.UUID, status string, eventAt *time.Time) error
	// RecordLocalPatch stamps a status PATCH we sent and the status it carried.
	RecordLocalPatch(ctx context.Context, orderID uuid.UUID, status string, at time.Time) error
	// SetRemoteTotal stores the total HubRise computed.
	SetRemoteTotal(ctx context.Context, orderID uuid.UUID, total decimal.Decimal) error
	// SetNeedsAttention flags the order for staff (empty clears the flag).
	SetNeedsAttention(ctx context.Context, orderID uuid.UUID, code string) error
	// CountNeedsAttention counts the flagged orders.
	CountNeedsAttention(ctx context.Context) (int, error)
}
