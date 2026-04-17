package infrastructure

import (
	"context"
	"fmt"
	"tsb-service/internal/modules/user/domain"
	"tsb-service/pkg/db"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

type UserRepository struct {
	pool *db.DBPool
}

func NewUserRepository(pool *db.DBPool) domain.UserRepository {
	return &UserRepository{pool: pool}
}

func (r *UserRepository) Save(ctx context.Context, user *domain.User) (uuid.UUID, error) {
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
	if err := r.pool.ForContext(ctx).GetContext(ctx, &u, query, email); err != nil {
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

func (r *UserRepository) RequestDeletion(ctx context.Context, userID string) (*domain.User, error) {
	query := `UPDATE users SET deletion_requested_at = NOW() WHERE id = $1`
	_, err := r.pool.ForContext(ctx).ExecContext(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	return r.FindByID(ctx, userID)
}

func (r *UserRepository) CancelDeletionRequest(ctx context.Context, userID string) (*domain.User, error) {
	query := `UPDATE users SET deletion_requested_at = NULL WHERE id = $1`
	_, err := r.pool.ForContext(ctx).ExecContext(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	return r.FindByID(ctx, userID)
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
