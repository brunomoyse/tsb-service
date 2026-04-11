package infrastructure

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"

	"tsb-service/internal/modules/hubrise_webshop/domain"
	"tsb-service/pkg/db"
)

// OrderPushRepository implements domain.OrderPushRepository against
// the existing `orders` table columns added in migration
// 20260410120001. It uses the admin DB pool unconditionally because
// push operations run in background contexts (retry worker, webhook
// handlers) where there's no customer request context.
type OrderPushRepository struct {
	pool *db.DBPool
}

func NewOrderPushRepository(pool *db.DBPool) *OrderPushRepository {
	return &OrderPushRepository{pool: pool}
}

var _ domain.OrderPushRepository = (*OrderPushRepository)(nil)

func (r *OrderPushRepository) MarkPushPending(ctx context.Context, orderID uuid.UUID) error {
	const q = `
		UPDATE orders
		SET hubrise_push_status = 'pending',
		    hubrise_last_push_at = now()
		WHERE id = $1
	`
	_, err := r.pool.ForContext(ctx).ExecContext(ctx, q, orderID.String())
	return err
}

func (r *OrderPushRepository) MarkPushed(
	ctx context.Context, orderID uuid.UUID, hubriseOrderID string,
) error {
	const q = `
		UPDATE orders
		SET hubrise_push_status = 'pushed',
		    hubrise_order_id = $2,
		    hubrise_last_push_at = now()
		WHERE id = $1
	`
	_, err := r.pool.ForContext(ctx).ExecContext(ctx, q, orderID.String(), hubriseOrderID)
	return err
}

func (r *OrderPushRepository) MarkPushFailed(
	ctx context.Context, orderID uuid.UUID, _ string,
) (int, error) {
	// We don't have a dedicated error_msg column on orders, so the
	// message is logged upstream. Here we atomically bump the
	// attempt counter and return the new value.
	const q = `
		UPDATE orders
		SET hubrise_push_status = 'failed',
		    hubrise_push_attempts = hubrise_push_attempts + 1,
		    hubrise_last_push_at = now()
		WHERE id = $1
		RETURNING hubrise_push_attempts
	`
	var attempts int
	err := r.pool.ForContext(ctx).QueryRowxContext(ctx, q, orderID.String()).Scan(&attempts)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	return attempts, nil
}

func (r *OrderPushRepository) ListRetryable(
	ctx context.Context, maxAttempts int, limit int,
) ([]uuid.UUID, error) {
	// Exponential backoff window computed in SQL so each row is
	// evaluated atomically against `now()`. Capped at 30 minutes.
	const q = `
		SELECT id
		FROM orders
		WHERE hubrise_push_status IN ('pending', 'failed')
		  AND hubrise_push_attempts < $1
		  AND (
		      hubrise_last_push_at IS NULL
		      OR hubrise_last_push_at < now() - LEAST(
		          INTERVAL '60 seconds' * POWER(2, hubrise_push_attempts),
		          INTERVAL '30 minutes'
		      )
		  )
		ORDER BY hubrise_last_push_at NULLS FIRST
		LIMIT $2
	`
	rows, err := r.pool.ForContext(ctx).QueryxContext(ctx, q, maxAttempts, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var ids []uuid.UUID
	for rows.Next() {
		var idStr string
		if err := rows.Scan(&idStr); err != nil {
			return nil, err
		}
		id, err := uuid.Parse(idStr)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *OrderPushRepository) CountFailed(
	ctx context.Context, minAttempts int,
) (int, error) {
	const q = `
		SELECT count(*)
		FROM orders
		WHERE hubrise_push_status = 'failed'
		  AND hubrise_push_attempts >= $1
	`
	var count int
	err := r.pool.ForContext(ctx).QueryRowxContext(ctx, q, minAttempts).Scan(&count)
	return count, err
}

func (r *OrderPushRepository) CountStuckPending(
	ctx context.Context, minAge time.Duration,
) (int, error) {
	const q = `
		SELECT count(*)
		FROM orders
		WHERE hubrise_push_status = 'pending'
		  AND hubrise_last_push_at IS NOT NULL
		  AND hubrise_last_push_at < now() - make_interval(secs => $1::int)
	`
	var count int
	err := r.pool.ForContext(ctx).QueryRowxContext(ctx, q, int(minAge.Seconds())).Scan(&count)
	return count, err
}

func (r *OrderPushRepository) LastSuccessfulPushAgeSeconds(
	ctx context.Context,
) (*int, error) {
	const q = `
		SELECT EXTRACT(EPOCH FROM (now() - max(hubrise_last_push_at)))::INT
		FROM orders
		WHERE hubrise_push_status = 'pushed'
	`
	var age sql.NullInt64
	err := r.pool.ForContext(ctx).QueryRowxContext(ctx, q).Scan(&age)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if !age.Valid {
		return nil, nil
	}
	v := int(age.Int64)
	return &v, nil
}
