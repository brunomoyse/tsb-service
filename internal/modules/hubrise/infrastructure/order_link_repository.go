package infrastructure

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/shopspring/decimal"

	"tsb-service/internal/modules/hubrise/domain"
)

const orderLinkColumns = `order_id, hubrise_order_id, push_state, pushed_at, last_remote_status, last_remote_event_at,
	last_local_patch_at, last_remote_confirmed_time, remote_total, needs_attention, created_at, updated_at`

type orderLinkRepository struct{ store }

// NewOrderLinkRepository returns the store of order links.
func NewOrderLinkRepository(db *sqlx.DB) domain.OrderLinkRepository {
	return &orderLinkRepository{newStore(db)}
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
		`INSERT INTO hubrise_outbox (kind, order_id, payload, next_attempt_at, created_at) VALUES ('order_create', $1, $2, $3, $3)`,
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
			last_remote_status = NULL, last_remote_confirmed_time = NULL, updated_at = now()
		WHERE order_id = $1`, orderID)
}

func (r *orderLinkRepository) RecordRemoteStatus(ctx context.Context, orderID uuid.UUID, status string, eventAt *time.Time) error {
	return r.mustChange(ctx, "record remote status", `
		UPDATE hubrise_orders SET last_remote_status = $2,
			last_remote_event_at = GREATEST($3, last_remote_event_at), updated_at = now()
		WHERE order_id = $1`, orderID, status, eventAt)
}

func (r *orderLinkRepository) RecordLocalPatch(ctx context.Context, orderID uuid.UUID, status string, at time.Time) error {
	return r.mustChange(ctx, "record local patch", `
		UPDATE hubrise_orders SET last_remote_status = $2, last_local_patch_at = $3, updated_at = now()
		WHERE order_id = $1`, orderID, status, at)
}

func (r *orderLinkRepository) RecordRemoteConfirmedTime(ctx context.Context, orderID uuid.UUID, t time.Time) error {
	return r.mustChange(ctx, "record remote confirmed time",
		`UPDATE hubrise_orders SET last_remote_confirmed_time = $2, updated_at = now() WHERE order_id = $1`, orderID, t)
}

func (r *orderLinkRepository) HasQueuedStatus(ctx context.Context, orderID uuid.UUID) (bool, error) {
	var queued bool
	err := r.get(ctx, "check queued status", &queued, `
		SELECT EXISTS (SELECT 1 FROM hubrise_outbox WHERE order_id = $1 AND kind = 'order_status' AND state IN ('pending', 'sending'))`, orderID)
	return queued, err
}

func (r *orderLinkRepository) SetRemoteTotal(ctx context.Context, orderID uuid.UUID, total decimal.Decimal) error {
	return r.mustChange(ctx, "set remote total",
		`UPDATE hubrise_orders SET remote_total = $2, updated_at = now() WHERE order_id = $1`, orderID, total)
}

func (r *orderLinkRepository) SetNeedsAttention(ctx context.Context, orderID uuid.UUID, code string) error {
	return r.mustChange(ctx, "set needs attention",
		`UPDATE hubrise_orders SET needs_attention = NULLIF($2, ''), updated_at = now() WHERE order_id = $1`, orderID, code)
}

func (r *orderLinkRepository) FlagAttention(ctx context.Context, orderID uuid.UUID, code string) error {
	return r.exec(ctx, "flag attention",
		`UPDATE hubrise_orders SET needs_attention = $2, updated_at = now() WHERE order_id = $1 AND needs_attention IS NULL`, orderID, code)
}

func (r *orderLinkRepository) MarkNotAccepted(ctx context.Context, orderID uuid.UUID) error {
	return r.mustChange(ctx, "mark not accepted", `
		UPDATE hubrise_orders SET needs_attention = COALESCE(needs_attention, $2), updated_at = now()
		WHERE order_id = $1`, orderID, domain.AttentionNotAccepted)
}

func (r *orderLinkRepository) MarkStuckAlerted(ctx context.Context, orderID uuid.UUID, at time.Time) error {
	return r.mustChange(ctx, "mark stuck alerted",
		`UPDATE hubrise_orders SET stuck_alerted_at = $2, updated_at = now() WHERE order_id = $1`, orderID, at)
}

func (r *orderLinkRepository) ClearAttention(ctx context.Context, orderID uuid.UUID, codes ...string) error {
	return r.exec(ctx, "clear attention",
		`UPDATE hubrise_orders SET needs_attention = NULL, updated_at = now() WHERE order_id = $1 AND needs_attention = ANY($2)`,
		orderID, pq.Array(codes))
}

func (r *orderLinkRepository) StampRemoteRead(ctx context.Context, orderID uuid.UUID, at time.Time) error {
	return r.mustChange(ctx, "stamp remote read",
		`UPDATE hubrise_orders SET last_remote_read_at = $2, updated_at = now() WHERE order_id = $1`, orderID, at)
}

func (r *orderLinkRepository) Recreate(ctx context.Context, orderID uuid.UUID, payload json.RawMessage, due time.Time) error {
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("hubrise: recreate order: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
		UPDATE hubrise_outbox SET state = 'skipped', leased_until = NULL, last_error = 'covered by the re-created order'
		WHERE order_id = $1 AND kind = 'order_status' AND state IN ('pending', 'sending')`, orderID); err != nil {
		return fmt.Errorf("hubrise: recreate order: %w", err)
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE hubrise_orders SET hubrise_order_id = NULL, push_state = 'pending', pushed_at = NULL,
			last_remote_status = NULL, last_remote_event_at = NULL, last_remote_read_at = NULL, last_remote_confirmed_time = NULL, remote_total = NULL, updated_at = now()
		WHERE order_id = $1`, orderID)
	if err != nil {
		return fmt.Errorf("hubrise: recreate order: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 { // lib/pq always reports it
		return domain.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO hubrise_outbox (kind, order_id, payload, next_attempt_at, created_at) VALUES ('order_create', $1, $2, $3, $3)`,
		orderID, []byte(payload), due); err != nil {
		return fmt.Errorf("hubrise: recreate order: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("hubrise: recreate order: %w", err)
	}
	return nil
}

func (r *orderLinkRepository) HasUnsettledCreate(ctx context.Context) (bool, error) {
	var busy bool
	err := r.get(ctx, "check unsettled creates", &busy, `
		SELECT EXISTS (
			SELECT 1 FROM hubrise_orders h
			JOIN hubrise_outbox o ON o.order_id = h.order_id AND o.kind = 'order_create'
			WHERE h.hubrise_order_id IS NULL AND h.push_state = 'pending' AND o.state IN ('pending', 'sending'))`)
	return busy, err
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
