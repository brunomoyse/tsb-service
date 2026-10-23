package infrastructure

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"tsb-service/internal/modules/hubrise/domain"
)

type oauthStateRepository struct{ store }

// NewOAuthStateRepository returns the store of one-time OAuth state nonces.
func NewOAuthStateRepository(db *sqlx.DB) domain.OAuthStateRepository {
	return &oauthStateRepository{newStore(db)}
}

func (r *oauthStateRepository) Create(ctx context.Context, state string, adminUserID uuid.UUID, expiresAt time.Time) error {
	return r.exec(ctx, "create oauth state",
		`INSERT INTO hubrise_oauth_states (state, admin_user_id, expires_at) VALUES ($1, $2, $3)`,
		state, adminUserID, expiresAt)
}

// Consume always deletes the state it finds, so an expired nonce cannot be tried again either.
func (r *oauthStateRepository) Consume(ctx context.Context, state string, now time.Time) (uuid.UUID, bool, error) {
	var row struct {
		AdminUserID uuid.UUID `db:"admin_user_id"`
		ExpiresAt   time.Time `db:"expires_at"`
	}
	err := r.get(ctx, "consume oauth state", &row,
		`DELETE FROM hubrise_oauth_states WHERE state = $1 RETURNING admin_user_id, expires_at`, state)
	if errors.Is(err, domain.ErrNotFound) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, err
	}
	if !row.ExpiresAt.After(now) {
		return uuid.Nil, false, nil
	}
	return row.AdminUserID, true, nil
}

func (r *oauthStateRepository) PurgeExpired(ctx context.Context, now time.Time) (int64, error) {
	return r.execN(ctx, "purge oauth states", `DELETE FROM hubrise_oauth_states WHERE expires_at <= $1`, now)
}

type leaseRepository struct{ store }

// NewLeaseRepository returns the job lease store (compare-and-set leases between replicas).
func NewLeaseRepository(db *sqlx.DB) domain.LeaseRepository {
	return &leaseRepository{newStore(db)}
}

// Acquire is a single atomic statement: the row is created, or taken over when the lease expired
// or is already ours, and left alone otherwise (no row changed = somebody else holds it).
func (r *leaseRepository) Acquire(ctx context.Context, name, holder string, now time.Time, ttl time.Duration) (bool, error) {
	return r.affected(ctx, "acquire lease", `
		INSERT INTO hubrise_job_leases (name, holder, leased_until) VALUES ($1, $2, $3)
		ON CONFLICT (name) DO UPDATE SET holder = EXCLUDED.holder, leased_until = EXCLUDED.leased_until
		WHERE hubrise_job_leases.leased_until < $4 OR hubrise_job_leases.holder = EXCLUDED.holder`,
		name, holder, now.Add(ttl), now)
}

func (r *leaseRepository) Release(ctx context.Context, name, holder string) error {
	return r.exec(ctx, "release lease", `DELETE FROM hubrise_job_leases WHERE name = $1 AND holder = $2`, name, holder)
}

type foreignOrderRepository struct{ store }

// NewForeignOrderRepository returns the cache of HubRise orders that are not ours.
func NewForeignOrderRepository(db *sqlx.DB) domain.ForeignOrderRepository {
	return &foreignOrderRepository{newStore(db)}
}

func (r *foreignOrderRepository) Mark(ctx context.Context, hubriseOrderID string, at time.Time) error {
	return r.exec(ctx, "mark foreign order", `
		INSERT INTO hubrise_foreign_orders (hubrise_order_id, seen_at) VALUES ($1, $2)
		ON CONFLICT (hubrise_order_id) DO UPDATE SET seen_at = EXCLUDED.seen_at`, hubriseOrderID, at)
}

func (r *foreignOrderRepository) IsForeign(ctx context.Context, hubriseOrderID string) (bool, error) {
	var found bool
	err := r.get(ctx, "is foreign order", &found,
		`SELECT EXISTS (SELECT 1 FROM hubrise_foreign_orders WHERE hubrise_order_id = $1)`, hubriseOrderID)
	return found, err
}

func (r *foreignOrderRepository) Purge(ctx context.Context, seenBefore time.Time) (int64, error) {
	return r.execN(ctx, "purge foreign orders", `DELETE FROM hubrise_foreign_orders WHERE seen_at < $1`, seenBefore)
}
