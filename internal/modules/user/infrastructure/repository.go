package infrastructure

import (
	"context"
	"fmt"
	"strings"
	"tsb-service/internal/modules/user/domain"
	"tsb-service/pkg/db"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

// normalizeEmail is a defense-in-depth duplicate of the application-layer
// helper — every email written to or queried from the users table goes through
// it so capitalisation can never leak into storage even if a caller forgets.
func normalizeEmail(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

type UserRepository struct {
	pool *db.DBPool
}

func NewUserRepository(pool *db.DBPool) domain.UserRepository {
	return &UserRepository{pool: pool}
}

func (r *UserRepository) Save(ctx context.Context, user *domain.User) (uuid.UUID, error) {
	user.Email = normalizeEmail(user.Email)
	query := `
		INSERT INTO users (first_name, last_name, email, phone_number, address_id, zitadel_user_id)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id;
	`
	var id uuid.UUID
	if err := r.pool.ForContext(ctx).QueryRowContext(ctx, query, user.FirstName, user.LastName, user.Email, user.PhoneNumber, user.AddressID, user.ZitadelUserID).Scan(&id); err != nil {
		return uuid.Nil, err
	}
	user.ID = id
	return id, nil
}

func (r *UserRepository) FindByEmail(ctx context.Context, email string) (*domain.User, error) {
	var u domain.User
	query := `
		SELECT *
		FROM users
		WHERE email = $1;
	`
	if err := r.pool.ForContext(ctx).GetContext(ctx, &u, query, normalizeEmail(email)); err != nil {
		return nil, err
	}
	return &u, nil
}

func (r *UserRepository) FindByID(ctx context.Context, id string) (*domain.User, error) {
	var u domain.User
	query := `
		SELECT *
		FROM users
		WHERE id = $1;
	`
	if err := r.pool.ForContext(ctx).GetContext(ctx, &u, query, id); err != nil {
		return nil, err
	}
	return &u, nil
}

func (r *UserRepository) FindByZitadelID(ctx context.Context, zitadelID string) (*domain.User, error) {
	var u domain.User
	query := `
		SELECT *
		FROM users
		WHERE zitadel_user_id = $1;
	`
	if err := r.pool.ForContext(ctx).GetContext(ctx, &u, query, zitadelID); err != nil {
		return nil, err
	}
	return &u, nil
}

func (r *UserRepository) UpdateUser(ctx context.Context, user *domain.User) (*domain.User, error) {
	user.Email = normalizeEmail(user.Email)
	query := `
		UPDATE users
		SET first_name = $1, last_name = $2, email = $3, phone_number = $4, address_id = $5, default_place_id = $6, notify_marketing = $7, notify_order_updates = $8, zitadel_user_id = $9
		WHERE id = $10
	`
	_, err := r.pool.ForContext(ctx).ExecContext(ctx, query, user.FirstName, user.LastName, user.Email, user.PhoneNumber, user.AddressID, user.DefaultPlaceID, user.NotifyMarketing, user.NotifyOrderUpdates, user.ZitadelUserID, user.ID)
	if err != nil {
		return nil, err
	}

	return r.FindByID(ctx, user.ID.String())
}

func (r *UserRepository) AnonymizeForDeletion(ctx context.Context, userID string) error {
	// Erase every personal field while keeping the row (orders.user_id is
	// ON DELETE RESTRICT and must be retained for VAT). The email is replaced
	// with a per-id placeholder on the reserved .invalid TLD (RFC 2606) so the
	// users_email_unique constraint holds and the address is never deliverable.
	//
	// zitadel_user_id is deliberately NOT nulled. Access tokens are validated
	// locally via JWKS (no introspection), so a token issued before deletion
	// stays valid until it expires. If we dropped the sub, the next request on
	// that stale token would miss FindByZitadelID, miss FindByEmail (email is
	// anonymized), and JIT-provision a fresh row from the token claims —
	// resurrecting the account with the PII we just erased. Keeping the (now
	// dead, never-reused) Zitadel sub makes FindByZitadelID return this
	// anonymized row instead, so the stale token resolves to "Anonyme Anonyme"
	// and no new row is created.
	const anonymize = `
		UPDATE users SET
			first_name            = 'Anonyme',
			last_name             = 'Anonyme',
			email                 = 'deleted+' || id::text || '@deleted.invalid',
			phone_number          = NULL,
			address_id            = NULL,
			default_place_id      = NULL,
			notify_marketing      = false,
			notify_order_updates  = false,
			deletion_requested_at = NOW()
		WHERE id = $1`
	if _, err := r.pool.ForContext(ctx).ExecContext(ctx, anonymize, userID); err != nil {
		return fmt.Errorf("anonymize user: %w", err)
	}

	// Drop device push tokens so a deleted account receives no further pushes.
	// (live_activity_tokens are keyed by order with a 12h TTL and self-purge.)
	if _, err := r.pool.ForContext(ctx).ExecContext(ctx,
		`DELETE FROM device_push_tokens WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("delete device push tokens: %w", err)
	}

	return nil
}

func (r *UserRepository) BatchGetUsersByOrderIDs(ctx context.Context, orderIDs []string) (map[string][]*domain.User, error) {
	if len(orderIDs) == 0 {
		return map[string][]*domain.User{}, nil
	}

	const query = `
    SELECT
      o.id   AS order_id,
      u.*
    FROM users AS u
    JOIN orders AS o ON o.user_id = u.id
    WHERE o.id = ANY($1)
    `

	type userRow struct {
		OrderID     string `db:"order_id"`
		domain.User
	}

	var rows []userRow
	if err := r.pool.ForContext(ctx).SelectContext(ctx, &rows, query, pq.Array(orderIDs)); err != nil {
		return nil, fmt.Errorf("failed to batch‑get users by order IDs: %w", err)
	}

	userMap := make(map[string][]*domain.User, len(rows))
	for _, row := range rows {
		u := row.User
		userMap[row.OrderID] = append(userMap[row.OrderID], &u)
	}

	return userMap, nil
}
