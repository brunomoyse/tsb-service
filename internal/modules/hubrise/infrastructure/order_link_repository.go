package infrastructure

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"

	"tsb-service/internal/modules/hubrise/domain"
)

const orderLinkColumns = `order_id, hubrise_order_id, push_state, pushed_at, last_remote_status, last_remote_event_at,
	last_local_patch_at, remote_total, needs_attention, created_at, updated_at`

type orderLinkRepository struct{ store }

// NewOrderLinkRepository returns the store of order links.
func NewOrderLinkRepository(db *sqlx.DB) domain.OrderLinkRepository {
	return &orderLinkRepository{store{db}}
}

// EnqueueCreate: the link insert is the idempotency gate. Only the call that inserted it writes
// the outbox row, in the same transaction, so a crash leaves neither or both.
func (r *orderLinkRepository) EnqueueCreate(ctx context.Context, orderID uuid.UUID, payload json.RawMessage, due time.Time) (bool, error) {
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("hubrise: enqueue order create: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `INSERT INTO hubrise_orders (order_id) VALUES ($1) ON CONFLICT (order_id) DO NOTHING`, orderID)
	if err != nil {
		return false, fmt.Errorf("hubrise: enqueue order create: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 { // lib/pq always reports it
		return false, nil
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO hubrise_outbox (kind, order_id, payload, next_attempt_at) VALUES ('order_create', $1, $2, $3)`,
		orderID, []byte(payload), due); err != nil {
		return false, fmt.Errorf("hubrise: enqueue order create: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("hubrise: enqueue order create: %w", err)
	}
	return true, nil
}

func (r *orderLinkRepository) Get(ctx context.Context, orderID uuid.UUID) (*domain.OrderLink, error) {
	var l domain.OrderLink
	if err := r.get(ctx, "get order link", &l, `SELECT `+orderLinkColumns+` FROM hubrise_orders WHERE order_id = $1`, orderID); err != nil {
		return nil, err
	}
	return &l, nil
}

func (r *orderLinkRepository) FindByHubriseID(ctx context.Context, hubriseOrderID string) (*domain.OrderLink, error) {
	var l domain.OrderLink
	if err := r.get(ctx, "find order link by hubrise id", &l,
		`SELECT `+orderLinkColumns+` FROM hubrise_orders WHERE hubrise_order_id = $1`, hubriseOrderID); err != nil {
		return nil, err
	}
	return &l, nil
}

func (r *orderLinkRepository) MarkSent(ctx context.Context, orderID uuid.UUID, hubriseOrderID, remoteStatus string, at time.Time) error {
	return r.mustChange(ctx, "mark order sent", `
		UPDATE hubrise_orders SET hubrise_order_id = $2, push_state = 'sent', pushed_at = $4,
			last_remote_status = NULLIF($3, ''), updated_at = now()
		WHERE order_id = $1`, orderID, hubriseOrderID, remoteStatus, at)
}

func (r *orderLinkRepository) SetPushState(ctx context.Context, orderID uuid.UUID, state domain.PushState) error {
	return r.mustChange(ctx, "set push state",
		`UPDATE hubrise_orders SET push_state = $2, updated_at = now() WHERE order_id = $1`, orderID, state)
}

func (r *orderLinkRepository) ResetToPending(ctx context.Context, orderID uuid.UUID) error {
	return r.mustChange(ctx, "reset order link", `
		UPDATE hubrise_orders SET hubrise_order_id = NULL, push_state = 'pending', pushed_at = NULL,
			last_remote_status = NULL, updated_at = now()
		WHERE order_id = $1`, orderID)
}

func (r *orderLinkRepository) RecordRemoteStatus(ctx context.Context, orderID uuid.UUID, status string, eventAt *time.Time) error {
	return r.mustChange(ctx, "record remote status", `
		UPDATE hubrise_orders SET last_remote_status = $2,
			last_remote_event_at = COALESCE($3, last_remote_event_at), updated_at = now()
		WHERE order_id = $1`, orderID, status, eventAt)
}

func (r *orderLinkRepository) RecordLocalPatch(ctx context.Context, orderID uuid.UUID, status string, at time.Time) error {
	return r.mustChange(ctx, "record local patch", `
		UPDATE hubrise_orders SET last_remote_status = $2, last_local_patch_at = $3, updated_at = now()
		WHERE order_id = $1`, orderID, status, at)
}

func (r *orderLinkRepository) SetRemoteTotal(ctx context.Context, orderID uuid.UUID, total decimal.Decimal) error {
	return r.mustChange(ctx, "set remote total",
		`UPDATE hubrise_orders SET remote_total = $2, updated_at = now() WHERE order_id = $1`, orderID, total)
}

func (r *orderLinkRepository) SetNeedsAttention(ctx context.Context, orderID uuid.UUID, code string) error {
	return r.mustChange(ctx, "set needs attention",
		`UPDATE hubrise_orders SET needs_attention = NULLIF($2, ''), updated_at = now() WHERE order_id = $1`, orderID, code)
}

func (r *orderLinkRepository) CountNeedsAttention(ctx context.Context) (int, error) {
	var n int
	err := r.get(ctx, "count needs attention", &n, `SELECT count(*) FROM hubrise_orders WHERE needs_attention IS NOT NULL`)
	return n, err
}

// mustChange runs a statement that must hit the order's link, and returns ErrNotFound otherwise.
func (r *orderLinkRepository) mustChange(ctx context.Context, what, query string, args ...any) error {
	ok, err := r.affected(ctx, what, query, args...)
	if err != nil {
		return err
	}
	if !ok {
		return domain.ErrNotFound
	}
	return nil
}
