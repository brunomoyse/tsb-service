package infrastructure

import (
	"context"
	"tsb-service/internal/modules/user/domain"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

type UserRepository struct {
	db *sqlx.DB
}

func NewUserRepository(db *sqlx.DB) domain.UserRepository {
	return &UserRepository{db: db}
}

func (r *UserRepository) Save(ctx context.Context, user *domain.User) (uuid.UUID, error) {
	query := `
		INSERT INTO users (first_name, last_name, email, phone_number, address_id, password_hash, salt, google_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id;
	`
	var id uuid.UUID
	if err := r.db.QueryRowContext(ctx, query, user.FirstName, user.LastName, user.Email, user.PhoneNumber, user.AddressID, user.PasswordHash, user.Salt, user.GoogleID).Scan(&id); err != nil {
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
	if err := r.db.GetContext(ctx, &u, query, email); err != nil {
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
	if err := r.db.GetContext(ctx, &u, query, id); err != nil {
		return nil, err
	}
	return &u, nil
}

func (r *UserRepository) FindByGoogleID(ctx context.Context, googleID string) (*domain.User, error) {
	var u domain.User
	query := `
		SELECT *
		FROM users 
		WHERE google_id = $1;
	`
	if err := r.db.GetContext(ctx, &u, query, googleID); err != nil {
		return nil, err
	}
	return &u, nil
}

func (r *UserRepository) UpdateGoogleID(ctx context.Context, userID string, googleID string) (*domain.User, error) {
	query := `UPDATE users SET google_id = $1 WHERE id = $2`
	_, err := r.db.ExecContext(ctx, query, googleID, userID)
	if err != nil {
		return nil, err
	}
	return r.FindByID(ctx, userID)
}

func (r *UserRepository) UpdateUser(ctx context.Context, user *domain.User) (*domain.User, error) {
	query := `
		UPDATE users
		SET first_name = $1, last_name = $2, email = $3, phone_number = $4, address_id = $5, email_verified_at = $6
		WHERE id = $7
	`
	_, err := r.db.ExecContext(ctx, query, user.FirstName, user.LastName, user.Email, user.PhoneNumber, user.AddressID, user.EmailVerifiedAt, user.ID)
	if err != nil {
		return nil, err
	}

	return r.FindByID(ctx, user.ID.String())
}

func (r *UserRepository) UpdateUserPassword(ctx context.Context, userID string, passwordHash string, salt string) (*domain.User, error) {
	query := `UPDATE users SET password_hash = $1, salt = $2 WHERE id = $3`
	_, err := r.db.ExecContext(ctx, query, passwordHash, salt, userID)
	if err != nil {
		return nil, err
	}
	return r.FindByID(ctx, userID)
}

func (r *UserRepository) UpdateEmailVerifiedAt(ctx context.Context, userID string) (*domain.User, error) {
	query := `UPDATE users SET email_verified_at = NOW() WHERE id = $1`
	_, err := r.db.ExecContext(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	return r.FindByID(ctx, userID)
}

func (r *UserRepository) InvalidateRefreshToken(ctx context.Context, refreshToken string) error {
	// @TODO: Implement refresh_tokens in DB + add check in RefreshTokenHandler.
	// query := `DELETE FROM refresh_tokens WHERE token = $1`
	// _, err := r.db.ExecContext(ctx, query, refreshToken)
	return nil
}
