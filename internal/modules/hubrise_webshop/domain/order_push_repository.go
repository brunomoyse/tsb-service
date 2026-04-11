package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// OrderPushRepository tracks the HubRise push state of local orders.
//
// It uses the columns created in migration 20260410120001 on the
// `orders` table (hubrise_order_id, hubrise_push_status,
// hubrise_push_attempts, hubrise_last_push_at) that were added but
// initially left unused. The retry worker reads via ListRetryable and
// writes via MarkPushPending / MarkPushed / MarkPushFailed in the
// normal OrderPusher path.
type OrderPushRepository interface {
	// MarkPushPending sets status='pending' and updates last_push_at.
	// Called before attempting the POST to HubRise so we have a
	// durable record that a push is in flight.
	MarkPushPending(ctx context.Context, orderID uuid.UUID) error

	// MarkPushed sets status='pushed', stores the remote HubRise order
	// id, and updates last_push_at. Called on success.
	MarkPushed(ctx context.Context, orderID uuid.UUID, hubriseOrderID string) error

	// MarkPushFailed increments hubrise_push_attempts by 1, sets
	// status='failed', updates last_push_at. Returns the new attempt
	// count so the caller can decide whether to alert on the
	// exhausted-retries threshold.
	MarkPushFailed(ctx context.Context, orderID uuid.UUID, errMsg string) (int, error)

	// ListRetryable returns the IDs of orders that should be retried
	// now: status IN ('pending', 'failed'), attempts < maxAttempts,
	// and last_push_at IS NULL OR older than the exponential backoff
	// window (60s * 2^attempts, capped at 30 min). Ordered by oldest
	// last_push_at first.
	ListRetryable(ctx context.Context, maxAttempts int, limit int) ([]uuid.UUID, error)

	// CountFailed returns the number of orders currently in 'failed'
	// state with attempts >= minAttempts. Used by the health service.
	CountFailed(ctx context.Context, minAttempts int) (int, error)

	// CountStuckPending returns the number of orders in 'pending'
	// state where last_push_at is older than minAge. A stuck pending
	// order usually means a process crash between MarkPushPending and
	// MarkPushed/MarkPushFailed.
	CountStuckPending(ctx context.Context, minAge time.Duration) (int, error)

	// LastSuccessfulPushAgeSeconds returns the time in seconds since
	// the most recent 'pushed' order. Nil if no pushed order exists.
	LastSuccessfulPushAgeSeconds(ctx context.Context) (*int, error)
}
