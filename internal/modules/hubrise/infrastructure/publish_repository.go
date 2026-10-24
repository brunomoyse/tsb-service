package infrastructure

import (
	"context"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"tsb-service/internal/modules/hubrise/domain"
)

// publishStateColumns lists every column of the publisher row (the table also has the singleton id,
// which has no field).
const publishStateColumns = `change_seq, dirty_since, last_change_at, next_attempt_at, attempts, lease_holder,
	leased_until, last_started_at, last_published_at, last_published_seq, last_published_local_hash,
	last_published_remote_hash, first_publish_confirmed_at, external_change_at, last_error, last_error_code,
	last_error_at, blocked_at, updated_at`

// The debounce arithmetic of amendment A15.5, in seconds, rendered into the statements below. It is
// the SQL twin of domain.NextDue.
var (
	debounceSecs    = int(domain.PublishDebounce / time.Second)
	minIntervalSecs = int(domain.PublishMinInterval / time.Second)
	maxDebounceSecs = int(domain.PublishMaxDebounce / time.Second)
	leaseSecs       = int(domain.PublishLeaseTTL / time.Second)
)

type publishStateRepository struct{ store }

// NewPublishStateRepository returns the repository of the single hubrise_catalog_publish row.
func NewPublishStateRepository(db *sqlx.DB) domain.PublishStateRepository {
	return &publishStateRepository{newStore(db)}
}

func (r *publishStateRepository) Get(ctx context.Context) (*domain.PublishState, error) {
	var s domain.PublishState
	if err := r.get(ctx, "get publish state", &s, `SELECT `+publishStateColumns+` FROM hubrise_catalog_publish`); err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *publishStateRepository) MarkDirty(ctx context.Context, now time.Time) (int64, error) {
	// Every right hand side reads the row as it was before the update, so dirty_since is the first
	// change of the burst. The upsert only matters if the row of the migration was deleted by hand.
	var seq int64
	err := r.get(ctx, "mark catalog dirty", &seq, fmt.Sprintf(`
		INSERT INTO hubrise_catalog_publish (id, change_seq, last_change_at, dirty_since, next_attempt_at, updated_at)
		VALUES (TRUE, 1, $1, $1, $1::timestamptz + interval '%[1]d seconds', $1)
		ON CONFLICT (id) DO UPDATE SET
			change_seq = hubrise_catalog_publish.change_seq + 1,
			last_change_at = $1,
			dirty_since = COALESCE(hubrise_catalog_publish.dirty_since, $1),
			blocked_at = NULL,
			next_attempt_at = GREATEST(
				LEAST($1::timestamptz + interval '%[1]d seconds', COALESCE(hubrise_catalog_publish.dirty_since, $1) + interval '%[2]d seconds'),
				COALESCE(hubrise_catalog_publish.last_started_at, '-infinity'::timestamptz) + interval '%[3]d seconds'),
			updated_at = $1
		RETURNING change_seq`, debounceSecs, maxDebounceSecs, minIntervalSecs), now)
	return seq, err
}

// healthyConnection is the part of the claim predicate that reads the connection: publishing is on
// and nothing pauses the pipeline. The connection is read inside the claim, so a disable takes
// effect on the next claim of every replica.
const healthyConnection = `EXISTS (SELECT 1 FROM hubrise_connection c WHERE c.publish_catalog AND c.enabled
		AND c.access_token_enc IS NOT NULL AND COALESCE(c.catalog_id, '') <> '' AND c.auth_error_at IS NULL AND c.relink_required_at IS NULL)`

func (r *publishStateRepository) Claim(ctx context.Context, holder string, now time.Time) (int64, bool, error) {
	return r.claim(ctx, "claim catalog publish", holder, now, `
		p.dirty_since IS NOT NULL AND p.blocked_at IS NULL
		AND (p.next_attempt_at IS NULL OR p.next_attempt_at <= $3)
		AND p.first_publish_confirmed_at IS NOT NULL`)
}

func (r *publishStateRepository) ClaimNow(ctx context.Context, holder string, now time.Time) (int64, bool, error) {
	return r.claim(ctx, "claim catalog publish now", holder, now, `TRUE`)
}

func (r *publishStateRepository) claim(ctx context.Context, what, holder string, now time.Time, condition string) (int64, bool, error) {
	var seq int64
	err := r.get(ctx, what, &seq, fmt.Sprintf(`
		UPDATE hubrise_catalog_publish p SET lease_holder = $1, leased_until = $2, last_started_at = $3, updated_at = $3
		WHERE (%s) AND (p.leased_until IS NULL OR p.leased_until < $3 OR p.lease_holder = $1) AND `+healthyConnection+`
		RETURNING p.change_seq`, condition), holder, now.Add(time.Duration(leaseSecs)*time.Second), now)
	if err == domain.ErrNotFound {
		return 0, false, nil
	}
	return seq, err == nil, err
}

func (r *publishStateRepository) Extend(ctx context.Context, holder string, now time.Time) (bool, error) {
	return r.affected(ctx, "extend catalog publish lease", `
		UPDATE hubrise_catalog_publish SET leased_until = $2, updated_at = $3 WHERE lease_holder = $1`,
		holder, now.Add(time.Duration(leaseSecs)*time.Second), now)
}

func (r *publishStateRepository) Release(ctx context.Context, holder string) error {
	return r.exec(ctx, "release catalog publish lease", `
		UPDATE hubrise_catalog_publish SET lease_holder = NULL, leased_until = NULL, updated_at = now() WHERE lease_holder = $1`, holder)
}

func (r *publishStateRepository) Complete(ctx context.Context, holder string, seq int64, res domain.PublishResult, items []domain.PublishedItem, ids []domain.LinkHubriseID) error {
	return r.inTx(ctx, "complete catalog publish", func(tx store) error {
		// The row is updated first: a lost lease (no row) rolls the whole thing back, so a run that was
		// taken over writes neither items nor ids.
		ok, err := tx.affected(ctx, "complete catalog publish", fmt.Sprintf(`
			UPDATE hubrise_catalog_publish SET
				last_published_at = $3, last_published_seq = $2, last_published_local_hash = $4, last_published_remote_hash = $5,
				external_change_at = NULL, attempts = 0, blocked_at = NULL,
				last_error = NULL, last_error_code = NULL, last_error_at = NULL,
				dirty_since = CASE WHEN change_seq = $2 THEN NULL ELSE dirty_since END,
				next_attempt_at = CASE WHEN change_seq = $2 THEN NULL ELSE $3::timestamptz + interval '%d seconds' END,
				lease_holder = NULL, leased_until = NULL, updated_at = $3
			WHERE lease_holder = $1`, minIntervalSecs), holder, seq, res.At, res.LocalHash, res.RemoteHash)
		if err != nil {
			return err
		}
		if !ok {
			return domain.ErrPublishBusy
		}
		if err := replacePublishedItems(ctx, tx, items); err != nil {
			return err
		}
		links := &catalogLinkRepository{tx}
		for _, id := range ids {
			if err := links.SetHubriseID(ctx, id.EntityType, id.Key, id.HubriseID); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *publishStateRepository) RecordError(ctx context.Context, holder string, f domain.PublishFailure, now time.Time) error {
	attempts := 0
	if f.CountAttempt {
		attempts = 1
	}
	return r.exec(ctx, "record catalog publish error", `
		UPDATE hubrise_catalog_publish SET
			last_error = $3, last_error_code = $4, last_error_at = $2,
			attempts = attempts + $5, next_attempt_at = COALESCE($6, next_attempt_at),
			lease_holder = CASE WHEN $1 = '' THEN lease_holder ELSE NULL END,
			leased_until = CASE WHEN $1 = '' THEN leased_until ELSE NULL END,
			updated_at = $2
		WHERE $1 = '' OR lease_holder = $1`,
		holder, now, truncateText(f.Message, 500), string(f.Code), attempts, f.RetryAt)
}

func (r *publishStateRepository) Block(ctx context.Context, holder string, code domain.PublishErrorCode, message string, now time.Time) error {
	return r.exec(ctx, "block catalog publish", `
		UPDATE hubrise_catalog_publish SET
			last_error = $3, last_error_code = $4, last_error_at = $2, blocked_at = $2,
			lease_holder = CASE WHEN $1 = '' THEN lease_holder ELSE NULL END,
			leased_until = CASE WHEN $1 = '' THEN leased_until ELSE NULL END,
			updated_at = $2
		WHERE $1 = '' OR lease_holder = $1`,
		holder, now, truncateText(message, 500), string(code))
}

func (r *publishStateRepository) SetFirstPublishConfirmed(ctx context.Context, at *time.Time) error {
	return r.exec(ctx, "set first publish confirmed",
		`UPDATE hubrise_catalog_publish SET first_publish_confirmed_at = $1, updated_at = now()`, at)
}

func (r *publishStateRepository) SetExternalChange(ctx context.Context, at *time.Time) error {
	return r.exec(ctx, "set external change", `
		UPDATE hubrise_catalog_publish SET
			external_change_at = CASE WHEN $1::timestamptz IS NULL THEN NULL ELSE COALESCE(external_change_at, $1::timestamptz) END,
			updated_at = now()`, at)
}

func (r *publishStateRepository) ClearError(ctx context.Context) error {
	return r.exec(ctx, "clear catalog publish error", `
		UPDATE hubrise_catalog_publish SET last_error = NULL, last_error_code = NULL, last_error_at = NULL,
			blocked_at = NULL, attempts = 0, updated_at = now()`)
}

func (r *publishStateRepository) Reset(ctx context.Context, now time.Time) error {
	return r.exec(ctx, "reset catalog publisher", resetPublisherSQL, now)
}

// truncateText cuts a message to n bytes on a rune boundary.
func truncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut]
}

// ---- published items ----

const publishedItemColumns = `entity_type, hubrise_key, hubrise_ref, content_hash, published_at`

type publishedItemRepository struct{ store }

// NewPublishedItemRepository returns the store of what the last PUT contained.
func NewPublishedItemRepository(db *sqlx.DB) domain.PublishedItemRepository {
	return &publishedItemRepository{newStore(db)}
}

func (r *publishedItemRepository) Replace(ctx context.Context, items []domain.PublishedItem) error {
	return r.inTx(ctx, "replace published items", func(tx store) error { return replacePublishedItems(ctx, tx, items) })
}

func replacePublishedItems(ctx context.Context, tx store, items []domain.PublishedItem) error {
	if err := tx.exec(ctx, "clear published items", `DELETE FROM hubrise_published_items`); err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	types, keys, refs, hashes := make([]string, len(items)), make([]string, len(items)), make([]string, len(items)), make([]string, len(items))
	at := make([]time.Time, len(items))
	for i, it := range items {
		types[i], keys[i], refs[i], hashes[i], at[i] = string(it.EntityType), it.HubriseKey, it.HubriseRef, it.ContentHash, it.PublishedAt
	}
	return tx.exec(ctx, "insert published items", `
		INSERT INTO hubrise_published_items (entity_type, hubrise_key, hubrise_ref, content_hash, published_at)
		SELECT u.entity_type, u.hubrise_key, u.hubrise_ref, u.content_hash, u.published_at
		FROM unnest($1::text[], $2::text[], $3::text[], $4::text[], $5::timestamptz[])
			AS u (entity_type, hubrise_key, hubrise_ref, content_hash, published_at)
		ON CONFLICT (entity_type, hubrise_key) DO UPDATE SET hubrise_ref = EXCLUDED.hubrise_ref,
			content_hash = EXCLUDED.content_hash, published_at = EXCLUDED.published_at`,
		pq.Array(types), pq.Array(keys), pq.Array(refs), pq.Array(hashes), pq.Array(at))
}

func (r *publishedItemRepository) List(ctx context.Context) ([]domain.PublishedItem, error) {
	items := []domain.PublishedItem{}
	err := r.sel(ctx, "list published items", &items,
		`SELECT `+publishedItemColumns+` FROM hubrise_published_items ORDER BY entity_type, hubrise_key`)
	return items, err
}

func (r *publishedItemRepository) ByKeys(ctx context.Context, t domain.EntityType, keys []string) (map[string]domain.PublishedItem, error) {
	out := map[string]domain.PublishedItem{}
	if len(keys) == 0 {
		return out, nil
	}
	var items []domain.PublishedItem
	if err := r.sel(ctx, "list published items by key", &items, `
		SELECT `+publishedItemColumns+` FROM hubrise_published_items WHERE entity_type = $1 AND hubrise_key = ANY($2)`,
		t, pq.Array(keys)); err != nil {
		return nil, err
	}
	for _, it := range items {
		out[it.HubriseKey] = it
	}
	return out, nil
}

func (r *publishedItemRepository) Count(ctx context.Context, t domain.EntityType) (int, error) {
	var n int
	err := r.get(ctx, "count published items", &n, `SELECT count(*) FROM hubrise_published_items WHERE entity_type = $1`, t)
	return n, err
}

func (r *publishedItemRepository) Clear(ctx context.Context) error {
	return r.exec(ctx, "clear published items", `DELETE FROM hubrise_published_items`)
}
