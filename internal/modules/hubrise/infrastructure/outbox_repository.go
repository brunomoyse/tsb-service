package infrastructure

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"tsb-service/internal/modules/hubrise/domain"
)

const outboxColumns = `id, kind, order_id, dedupe_key, payload, state, attempts, leased_until, next_attempt_at,
	last_error, created_at, sent_at, parked_at`

type outboxRepository struct{ store }

// NewOutboxRepository returns the durable outbox.
func NewOutboxRepository(db *sqlx.DB) domain.OutboxRepository {
	return &outboxRepository{newStore(db)}
}

// createdAt is the creation time to store: the item's own when the caller set one (so ages follow the
// application clock), the database's otherwise.
func createdAt(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
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
		INSERT INTO hubrise_outbox (kind, order_id, dedupe_key, payload, next_attempt_at, created_at)
		VALUES ($1, $2, $3, $4, $5, COALESCE($6, now()))
		ON CONFLICT (dedupe_key) WHERE state = 'pending' AND kind = 'inventory'
		DO UPDATE SET payload = EXCLUDED.payload, next_attempt_at = EXCLUDED.next_attempt_at
		RETURNING id, (xmax <> 0) AS merged`,
		item.Kind, item.OrderID, item.DedupeKey, []byte(item.Payload), item.NextAttemptAt, createdAt(item.CreatedAt))
	return row.ID, row.Merged, err
}

// EnqueueIfNoPending relies on the partial unique index of pending customer rows (a bare ON CONFLICT
// DO NOTHING swallows that one conflict and nothing else: the id never conflicts).
func (r *outboxRepository) EnqueueIfNoPending(ctx context.Context, item domain.OutboxItem) (int64, bool, error) {
	if len(item.Payload) == 0 {
		item.Payload = []byte(`{}`) // a nil json.RawMessage would be written as SQL NULL
	}
	var id int64
	err := r.get(ctx, "enqueue unless pending", &id, `
		INSERT INTO hubrise_outbox (kind, order_id, dedupe_key, payload, next_attempt_at, created_at)
		VALUES ($1, $2, $3, $4, $5, COALESCE($6, now()))
		ON CONFLICT DO NOTHING
		RETURNING id`,
		item.Kind, item.OrderID, item.DedupeKey, []byte(item.Payload), item.NextAttemptAt, createdAt(item.CreatedAt))
	if errors.Is(err, domain.ErrNotFound) {
		return 0, false, nil // the conflict inserted nothing, so RETURNING gave no row
	}
	return id, err == nil, err
}

// scrubPayloadSQL is the payload expression of an order_create row that reached a final state: the
// customer (name, email, phone, delivery address, coordinates) and the notes the customer typed are
// dropped, nothing reads them once the order is at HubRise, and the row must not keep them after
// the account is erased. Other kinds carry no personal data.
const scrubPayloadSQL = `CASE WHEN kind = 'order_create' THEN payload - 'customer' - 'customer_notes' ELSE payload END`

func (r *outboxRepository) SkipOpenByDedupeKey(ctx context.Context, kind domain.OutboxKind, dedupeKey, reason string) (int64, error) {
	return r.execN(ctx, "skip open outbox rows", `
		UPDATE hubrise_outbox SET state = 'skipped', last_error = $3, payload = `+scrubPayloadSQL+`
		WHERE kind = $1 AND dedupe_key = $2 AND state IN ('pending', 'parked')`, kind, dedupeKey, reason)
}

// claimSQL is the whole claim in one statement, so the lease and the attempts increment are durable
// before the HTTP call and two replicas never get the same row:
//   - the connection must be active (enabled, token, no auth error, no relink pending) and the
//     toggle of the row's kind on, re-read here so a disable bites on the very next claim. The
//     erasure of a deleted account (customer_anonymise) depends on no toggle: switching the sync off
//     must not leave an erasure request pending;
//   - due rows are pending rows whose time came and sending rows whose lease expired;
//   - only the first row of an order (or of a dedupe key) is claimable: any earlier pending,
//     sending or parked row of the same order blocks it, so a PATCH never overtakes its create;
//   - $4 lists the kinds the caller handles (NULL = all).
const claimSQL = `
WITH conn AS (
	SELECT push_orders, sync_status, sync_inventory, sync_customers FROM hubrise_connection
	WHERE enabled AND access_token_enc IS NOT NULL AND auth_error_at IS NULL AND relink_required_at IS NULL
), candidates AS (
	SELECT o.id FROM hubrise_outbox o, conn c
	WHERE ((o.state = 'pending' AND o.next_attempt_at <= $1) OR (o.state = 'sending' AND o.leased_until < $1))
	  AND ($4::text[] IS NULL OR o.kind = ANY($4))
	  AND CASE o.kind
	        WHEN 'order_create' THEN c.push_orders
	        WHEN 'order_status' THEN c.sync_status
	        WHEN 'inventory' THEN c.sync_inventory
	        WHEN 'customer_anonymise' THEN TRUE
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

func (r *outboxRepository) Claim(ctx context.Context, now time.Time, lease time.Duration, limit int, kinds ...domain.OutboxKind) ([]domain.OutboxItem, error) {
	var filter any // NULL claims every kind
	if len(kinds) > 0 {
		names := make([]string, len(kinds))
		for i, k := range kinds {
			names[i] = string(k)
		}
		filter = pq.Array(names)
	}
	items := []domain.OutboxItem{}
	if err := r.sel(ctx, "claim outbox", &items, claimSQL, now, now.Add(lease), limit, filter); err != nil {
		return nil, err
	}
	slices.SortFunc(items, func(a, b domain.OutboxItem) int { return int(a.ID - b.ID) })
	return items, nil
}

func (r *outboxRepository) Extend(ctx context.Context, id int64, now, until time.Time) (bool, error) {
	return r.affected(ctx, "extend outbox lease", `
		UPDATE hubrise_outbox SET leased_until = $3
		WHERE id = $1 AND state = 'sending' AND leased_until >= $2`, id, now, until)
}

func (r *outboxRepository) MarkSent(ctx context.Context, id int64, now time.Time) (bool, error) {
	return r.affected(ctx, "mark outbox sent", `
		UPDATE hubrise_outbox SET state = 'sent', sent_at = $2, leased_until = NULL, last_error = NULL,
			payload = `+scrubPayloadSQL+`
		WHERE id = $1 AND state = 'sending'`, id, now)
}

func (r *outboxRepository) MarkParked(ctx context.Context, id int64, now time.Time, reason string) (bool, error) {
	return r.affected(ctx, "park outbox row", `
		UPDATE hubrise_outbox SET state = 'parked', parked_at = $2, leased_until = NULL, last_error = $3
		WHERE id = $1 AND state = 'sending'`, id, now, reason)
}

func (r *outboxRepository) MarkSkipped(ctx context.Context, id int64, reason string) (bool, error) {
	return r.affected(ctx, "skip outbox row", `
		UPDATE hubrise_outbox SET state = 'skipped', leased_until = NULL, last_error = $2, payload = `+scrubPayloadSQL+`
		WHERE id = $1 AND state IN ('pending', 'sending')`, id, reason)
}

// supersededSQL is true for a sending row of a kind whose pending rows are unique per dedupe key
// (inventory, customer) when a newer pending twin was queued while it was on its way. Putting the row
// back to pending would then violate the partial unique index, and the twin carries the latest state
// anyway: both kinds are built from the current data when they are sent.
const supersededSQL = `o.kind IN ('inventory', 'customer') AND EXISTS (
	SELECT 1 FROM hubrise_outbox t
	WHERE t.kind = o.kind AND t.dedupe_key = o.dedupe_key AND t.state = 'pending' AND t.id <> o.id)`

// Release gives a claimed row back. A row that a pending twin supersedes (see supersededSQL) is
// skipped instead of re-pended.
func (r *outboxRepository) Release(ctx context.Context, id int64, next time.Time, reason string, revertAttempt bool) (bool, error) {
	return r.affected(ctx, "release outbox row", `
		UPDATE hubrise_outbox o SET
			state = CASE WHEN `+supersededSQL+` THEN 'skipped' ELSE 'pending' END,
			next_attempt_at = $2,
			last_error = CASE WHEN `+supersededSQL+` THEN 'superseded by a newer row' ELSE NULLIF($3, '') END,
			leased_until = NULL,
			attempts = CASE WHEN $4 THEN GREATEST(o.attempts - 1, 0) ELSE o.attempts END
		WHERE o.id = $1 AND o.state = 'sending'`, id, next, reason, revertAttempt)
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

// supersedeParkedInventorySQL skips the parked inventory rows inside the retry scope (the %s
// placeholder) that a newer row of the same dedupe key replaces: a pending twin of any age (it sits
// in the partial unique index, so re-pending the parked row would fail with 23505) or a newer
// sending or parked twin. Only the newest state of an item is worth sending, and sending a stale
// one after it would overwrite it on HubRise.
const supersedeParkedInventorySQL = `
	UPDATE hubrise_outbox o SET state = 'skipped', last_error = 'superseded by a newer inventory row'
	WHERE o.state = 'parked' AND o.kind = 'inventory' AND %s
	  AND EXISTS (
	        SELECT 1 FROM hubrise_outbox t
	        WHERE t.kind = 'inventory' AND t.dedupe_key = o.dedupe_key AND t.id <> o.id
	          AND (t.state = 'pending' OR (t.id > o.id AND t.state IN ('sending', 'parked'))))`

const retryParkedSQL = `
	UPDATE hubrise_outbox o SET state = 'pending', next_attempt_at = $%[1]d, created_at = $%[1]d, parked_at = NULL, last_error = NULL
	WHERE o.state = 'parked' AND %[2]s`

// retryParked supersedes, then reactivates, the parked rows matching scope. The two statements share
// a transaction so a failure leaves the parked rows untouched. scopeArgs fill the scope placeholders
// ($1...) of both statements; the retry time of the second one comes right after them.
func (r *outboxRepository) retryParked(ctx context.Context, what, scope string, now time.Time, scopeArgs ...any) (int64, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("hubrise: %s: %w", what, err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(supersedeParkedInventorySQL, scope), scopeArgs...); err != nil {
		return 0, fmt.Errorf("hubrise: %s: %w", what, err)
	}
	res, err := tx.ExecContext(ctx, fmt.Sprintf(retryParkedSQL, len(scopeArgs)+1, scope), append(scopeArgs, now)...)
	if err != nil {
		return 0, fmt.Errorf("hubrise: %s: %w", what, err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("hubrise: %s: %w", what, err)
	}
	n, _ := res.RowsAffected() // lib/pq always reports it
	return n, nil
}

// RetryParked keeps attempts as they are: a row parked after it may have been sent still needs the
// idempotency lookup on its next claim. It restarts the age of the rows (created_at), so a retry of a
// row that was parked for being too old is not parked again at once. The chain is the parked rows of the row's order or, for an
// inventory row, the parked rows of its dedupe key (of which only the newest is re-pended).
func (r *outboxRepository) RetryParked(ctx context.Context, id int64, now time.Time) (int64, error) {
	return r.retryParked(ctx, "retry parked chain", `
		(o.id = $1 OR o.order_id = (SELECT order_id FROM hubrise_outbox WHERE id = $1)
		 OR (o.kind = 'inventory' AND o.dedupe_key = (SELECT dedupe_key FROM hubrise_outbox WHERE id = $1 AND kind = 'inventory')))`,
		now, id)
}

func (r *outboxRepository) RetryAllParked(ctx context.Context, now time.Time) (int64, error) {
	return r.retryParked(ctx, "retry all parked", `TRUE`, now)
}

func (r *outboxRepository) MaxPendingID(ctx context.Context, orderID uuid.UUID, kind domain.OutboxKind) (int64, error) {
	var id int64
	err := r.get(ctx, "max pending outbox id", &id,
		`SELECT COALESCE(MAX(id), 0) FROM hubrise_outbox WHERE order_id = $1 AND kind = $2 AND state = 'pending'`, orderID, kind)
	return id, err
}

func (r *outboxRepository) SkipPendingUpTo(ctx context.Context, orderID uuid.UUID, kind domain.OutboxKind, upTo int64, reason string) (int64, error) {
	return r.execN(ctx, "skip covered outbox rows", `
		UPDATE hubrise_outbox SET state = 'skipped', last_error = $4, payload = `+scrubPayloadSQL+`
		WHERE order_id = $1 AND kind = $2 AND state = 'pending' AND id <= $3`, orderID, kind, upTo, reason)
}

func (r *outboxRepository) ActiveDedupeKeys(ctx context.Context, kind domain.OutboxKind) (map[string]bool, error) {
	var keys []string
	if err := r.sel(ctx, "list active dedupe keys", &keys, `
		SELECT DISTINCT dedupe_key FROM hubrise_outbox
		WHERE kind = $1 AND dedupe_key IS NOT NULL AND state IN ('pending', 'sending')`, kind); err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(keys))
	for _, k := range keys {
		set[k] = true
	}
	return set, nil
}

func (r *outboxRepository) CountForOrder(ctx context.Context, orderID uuid.UUID, kind domain.OutboxKind) (int, error) {
	var n int
	err := r.get(ctx, "count outbox rows of an order", &n,
		`SELECT count(*) FROM hubrise_outbox WHERE order_id = $1 AND kind = $2`, orderID, kind)
	return n, err
}

// ScrubCustomerData drops the personal data frozen in the order_create rows of the user that no
// longer need it: the sent and skipped ones (already scrubbed when they got there, this catches the
// rows of an older build) and the parked ones (an erased account is not worth a retry with its
// data). Pending and sending rows are left alone, they are on their way and are scrubbed once sent.
func (r *outboxRepository) ScrubCustomerData(ctx context.Context, userID uuid.UUID) (int64, error) {
	return r.execN(ctx, "scrub outbox customer data", `
		UPDATE hubrise_outbox SET payload = payload - 'customer' - 'customer_notes'
		WHERE kind = 'order_create' AND state IN ('sent', 'skipped', 'parked')
		  AND payload->>'user_id' = $1::text
		  AND payload <> payload - 'customer' - 'customer_notes'`, userID)
}

// PurgeFinished is the retention of the outbox: it scrubs the personal data of the order_create
// rows that are final (or parked for an erased account), then deletes the finished rows of the other
// kinds older than before. The order_create rows stay (one per order, without personal data): the
// re-creation guard counts them.
func (r *outboxRepository) PurgeFinished(ctx context.Context, before time.Time) (int64, error) {
	if _, err := r.execN(ctx, "scrub outbox customer data", `
		UPDATE hubrise_outbox o SET payload = payload - 'customer' - 'customer_notes'
		WHERE o.kind = 'order_create' AND payload <> payload - 'customer' - 'customer_notes'
		  AND (o.state IN ('sent', 'skipped')
		       OR (o.state = 'parked' AND EXISTS (
		             SELECT 1 FROM users u WHERE u.id::text = o.payload->>'user_id' AND u.deletion_requested_at IS NOT NULL)))`); err != nil {
		return 0, err
	}
	return r.execN(ctx, "purge outbox", `
		DELETE FROM hubrise_outbox
		WHERE state IN ('sent', 'skipped') AND kind <> 'order_create' AND COALESCE(sent_at, created_at) < $1`, before)
}
