// Package email provides the persistence layer for transactional-email concerns
// that need database access (currently the hard-bounce suppression list). The
// send path itself lives in pkg/email/scaleway; this repository implements the
// scaleway.SuppressionStore interface and is injected at startup.
package email

import (
	"context"
	"strings"

	"tsb-service/pkg/db"
)

// SuppressionRepository persists suppressed email addresses in email_suppressions.
type SuppressionRepository struct {
	pool *db.DBPool
}

func NewSuppressionRepository(pool *db.DBPool) *SuppressionRepository {
	return &SuppressionRepository{pool: pool}
}

// IsSuppressed reports whether the address is on the suppression list.
func (r *SuppressionRepository) IsSuppressed(ctx context.Context, email string) (bool, error) {
	const query = `SELECT EXISTS(SELECT 1 FROM email_suppressions WHERE email = $1)`
	var exists bool
	if err := r.pool.ForContext(ctx).GetContext(ctx, &exists, query, normalize(email)); err != nil {
		return false, err
	}
	return exists, nil
}

// Suppress adds the address to the suppression list. Idempotent: re-suppressing
// an already-listed address is a no-op (the original reason/timestamp is kept).
func (r *SuppressionRepository) Suppress(ctx context.Context, email, reason string) error {
	const query = `
		INSERT INTO email_suppressions (email, reason)
		VALUES ($1, $2)
		ON CONFLICT (email) DO NOTHING`
	_, err := r.pool.ForContext(ctx).ExecContext(ctx, query, normalize(email), reason)
	return err
}

func normalize(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
