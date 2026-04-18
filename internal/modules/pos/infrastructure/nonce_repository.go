package infrastructure

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"tsb-service/internal/modules/pos/domain"
	"tsb-service/pkg/db"
)

type NonceRepository struct {
	pool *db.DBPool
}

func NewNonceRepository(pool *db.DBPool) *NonceRepository {
	return &NonceRepository{pool: pool}
}

// Remember inserts the nonce with an expires_at of now+ttl. If the nonce is
// already present (PK conflict), it returns ErrReplayedNonce so the caller can
// reject the request.
func (r *NonceRepository) Remember(ctx context.Context, nonce string, ttl time.Duration) error {
	expiresAt := time.Now().Add(ttl)
	const q = `
		INSERT INTO pos_enrollment_nonces (nonce, expires_at)
		VALUES ($1, $2)
		ON CONFLICT (nonce) DO NOTHING
		RETURNING nonce`
	var inserted string
	err := r.pool.ForContext(ctx).QueryRowxContext(ctx, q, nonce, expiresAt).Scan(&inserted)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrNonceAlreadySeen
	}
	return err
}

// Prune removes nonces whose TTL has expired. Run on an interval from a
// background goroutine to keep the table small.
func (r *NonceRepository) Prune(ctx context.Context) error {
	const q = `DELETE FROM pos_enrollment_nonces WHERE expires_at < now()`
	_, err := r.pool.ForContext(ctx).ExecContext(ctx, q)
	return err
}

var _ domain.NonceRepository = (*NonceRepository)(nil)
