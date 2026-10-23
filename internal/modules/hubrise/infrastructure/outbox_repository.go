package infrastructure

import (
	"context"
	"slices"
	"time"

	"github.com/jmoiron/sqlx"

	"tsb-service/internal/modules/hubrise/domain"
)

const outboxColumns = `id, kind, order_id, dedupe_key, payload, state, attempts, leased_until, next_attempt_at,
	last_error, created_at, sent_at, parked_at`

type outboxRepository struct{ store }

// NewOutboxRepository returns the durable outbox.
func NewOutboxRepository(db *sqlx.DB) domain.OutboxRepository {
	return &outboxRepository{store{db}}
}

// Enqueue inserts the item. The ON CONFLICT clause names the partial unique index of pending
// inventory rows, so any other kind (or an inventory row whose twin is already sending) just
// inserts. xmax is non-zero only when the existing row was updated instead.
func (r *outboxRepository) Enqueue(ctx context.Context, item domain.OutboxItem) (int64, bool, error) {
	if len(item.Payload) == 0 {
		item.Payload = []byte(`{}`) // a nil json.RawMessage would be written as SQL NULL
	}
	var row struct {
		ID     int64 `db:"id"`
		Merged bool  `db:"merged"`
	}
	err := r.get(ctx, "enqueue", &row, `
		INSERT INTO hubrise_outbox (kind, order_id, dedupe_key, payload, next_attempt_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (dedupe_key) WHERE state = 'pending' AND kind = 'inventory'
		DO UPDATE SET payload = EXCLUDED.payload, next_attempt_at = EXCLUDED.next_attempt_at
		RETURNING id, (xmax <> 0) AS merged`,
		item.Kind, item.OrderID, item.DedupeKey, []byte(item.Payload), item.NextAttemptAt)
	return row.ID, row.Merged, err
}

// claimSQL is the whole claim in one statement, so the lease and the attempts increment are durable
// before the HTTP call and two replicas never get the same row:
//   - the connection must be active (enabled, token, no auth error, no relink pending) and the
//     toggle of the row's kind on, re-read here so a disable bites on the very next claim;
//   - due rows are pending rows whose time came and sending rows whose lease expired;
//   - only the first row of an order (or of a dedupe key) is claimable: any earlier pending,
//     sending or parked row of the same order blocks it, so a PATCH never overtakes its create.
const claimSQL = `
WITH conn AS (
	SELECT * FROM hubrise_connection
	WHERE enabled AND access_token_enc IS NOT NULL AND auth_error_at IS NULL AND relink_required_at IS NULL
), candidates AS (
	SELECT o.id FROM hubrise_outbox o, conn c
	WHERE ((o.state = 'pending' AND o.next_attempt_at <= $1) OR (o.state = 'sending' AND o.leased_until < $1))
	  AND CASE o.kind
	        WHEN 'order_create' THEN c.push_orders
	        WHEN 'order_status' THEN c.sync_status
	        WHEN 'inventory' THEN c.sync_inventory
	        ELSE c.sync_customers
	      END
	  AND NOT EXISTS (
	        SELECT 1 FROM hubrise_outbox p
	        WHERE p.id < o.id AND p.state IN ('pending', 'sending', 'parked')
	          AND ((o.order_id IS NOT NULL AND p.order_id = o.order_id)
	            OR (o.dedupe_key IS NOT NULL AND p.dedupe_key = o.dedupe_key)))
	ORDER BY o.id
	LIMIT $3
	FOR UPDATE OF o SKIP LOCKED
)
UPDATE hubrise_outbox SET state = 'sending', attempts = attempts + 1, leased_until = $2
WHERE id IN (SELECT id FROM candidates)
RETURNING ` + outboxColumns

func (r *outboxRepository) Claim(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]domain.OutboxItem, error) {
	items := []domain.OutboxItem{}
	if err := r.sel(ctx, "claim outbox", &items, claimSQL, now, now.Add(lease), limit); err != nil {
		return nil, err
	}
	slices.SortFunc(items, func(a, b domain.OutboxItem) int { return int(a.ID - b.ID) })
	return items, nil
}

func (r *outboxRepository) MarkSent(ctx context.Context, id int64, now time.Time) (bool, error) {
	return r.affected(ctx, "mark outbox sent", `
		UPDATE hubrise_outbox SET state = 'sent', sent_at = $2, leased_until = NULL, last_error = NULL
		WHERE id = $1 AND state = 'sending'`, id, now)
}

func (r *outboxRepository) MarkParked(ctx context.Context, id int64, now time.Time, reason string) (bool, error) {
	return r.affected(ctx, "park outbox row", `
		UPDATE hubrise_outbox SET state = 'parked', parked_at = $2, leased_until = NULL, last_error = $3
		WHERE id = $1 AND state = 'sending'`, id, now, reason)
}

func (r *outboxRepository) MarkSkipped(ctx context.Context, id int64, reason string) (bool, error) {
	return r.affected(ctx, "skip outbox row", `
		UPDATE hubrise_outbox SET state = 'skipped', leased_until = NULL, last_error = $2
		WHERE id = $1 AND state IN ('pending', 'sending')`, id, reason)
}

func (r *outboxRepository) Release(ctx context.Context, id int64, next time.Time, reason string, revertAttempt bool) (bool, error) {
	return r.affected(ctx, "release outbox row", `
		UPDATE hubrise_outbox SET state = 'pending', next_attempt_at = $2, last_error = NULLIF($3, ''), leased_until = NULL,
			attempts = CASE WHEN $4 THEN GREATEST(attempts - 1, 0) ELSE attempts END
		WHERE id = $1 AND state = 'sending'`, id, next, reason, revertAttempt)
}

func (r *outboxRepository) Get(ctx context.Context, id int64) (*domain.OutboxItem, error) {
	var item domain.OutboxItem
	if err := r.get(ctx, "get outbox row", &item, `SELECT `+outboxColumns+` FROM hubrise_outbox WHERE id = $1`, id); err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *outboxRepository) List(ctx context.Context, state *domain.OutboxState, limit int) ([]domain.OutboxItem, error) {
	items := []domain.OutboxItem{}
	err := r.sel(ctx, "list outbox", &items,
		`SELECT `+outboxColumns+` FROM hubrise_outbox WHERE $1::text IS NULL OR state = $1 ORDER BY id DESC LIMIT $2`, state, limit)
	return items, err
}

func (r *outboxRepository) Counts(ctx context.Context) (domain.OutboxCounts, error) {
	var c domain.OutboxCounts
	err := r.get(ctx, "count outbox", &c, `
		SELECT count(*) FILTER (WHERE state = 'pending') AS pending,
		       count(*) FILTER (WHERE state = 'parked') AS parked
		FROM hubrise_outbox`)
	return c, err
}

// RetryParked keeps attempts as they are: a row parked after it may have been sent still needs the
// idempotency lookup on its next claim.
func (r *outboxRepository) RetryParked(ctx context.Context, id int64, now time.Time) (int64, error) {
	return r.execN(ctx, "retry parked chain", `
		UPDATE hubrise_outbox SET state = 'pending', next_attempt_at = $2, parked_at = NULL, last_error = NULL
		WHERE state = 'parked'
		  AND (id = $1 OR order_id = (SELECT order_id FROM hubrise_outbox WHERE id = $1))`, id, now)
}

func (r *outboxRepository) RetryAllParked(ctx context.Context, now time.Time) (int64, error) {
	return r.execN(ctx, "retry all parked", `
		UPDATE hubrise_outbox SET state = 'pending', next_attempt_at = $1, parked_at = NULL, last_error = NULL
		WHERE state = 'parked'`, now)
}
