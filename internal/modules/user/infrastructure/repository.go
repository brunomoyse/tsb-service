package infrastructure

import (
	"context"
	"fmt"
	"tsb-service/internal/modules/user/domain"

	"github.com/jmoiron/sqlx"
)

type UserRepository struct {
	db *sqlx.DB
}

func NewUserRepository(db *sqlx.DB) domain.UserRepository {
	return &UserRepository{db: db}
}

func (r *UserRepository) Save(ctx context.Context, user *domain.User) (err error) {
	query := `
		INSERT INTO users (name, email, password_hash, salt)
		VALUES ($1, $2, $3, $4)
	`

	_, err = r.db.ExecContext(ctx, query, user.Name, user.Email, user.PasswordHash, user.Salt)
	if err != nil {
		return fmt.Errorf("failed to insert user: %w", err)
	}

	return nil
}

func (r *UserRepository) FindByEmail(ctx context.Context, email string) (*domain.User, error) {
	var u domain.User
	query := `
		SELECT 
			id, 
			created_at, 
			updated_at, 
			name, 
			email, 
			email_verified_at, 
			password_hash, 
			salt, 
			google_id 
		FROM users 
		WHERE email = $1;
	`
	if err := r.db.GetContext(ctx, &u, query, email); err != nil {
		return nil, fmt.Errorf("failed to get user by email: %w", err)
	}
	return &u, nil
}

func (r *UserRepository) FindByID(ctx context.Context, id string) (*domain.User, error) {
	var u domain.User
	query := `
		SELECT 
			id, 
			created_at, 
			updated_at, 
			name, 
			email, 
			email_verified_at, 
			password_hash, 
			salt, 
			google_id 
		FROM users 
		WHERE id = $1;
	`
	if err := r.db.GetContext(ctx, &u, query, id); err != nil {
		return nil, fmt.Errorf("failed to get user by ID: %w", err)
	}
	return &u, nil
}

func (r *UserRepository) FindByGoogleID(ctx context.Context, googleID string) (*domain.User, error) {
	var u domain.User
	query := `
		SELECT 
			id, 
			created_at, 
			updated_at, 
			name, 
			email, 
			email_verified_at, 
			password_hash, 
			salt, 
			google_id 
		FROM users 
		WHERE google_id = $1;
	`
	if err := r.db.GetContext(ctx, &u, query, googleID); err != nil {
		return nil, fmt.Errorf("failed to get user by Google ID: %w", err)
	}
	return &u, nil
}

func (r *UserRepository) UpdateGoogleID(ctx context.Context, userID string, googleID string) (*domain.User, error) {
	query := `UPDATE users SET google_id = $1 WHERE id = $2`
	_, err := r.db.ExecContext(ctx, query, googleID, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to update Google ID: %v", err)
	}
	return r.FindByID(ctx, userID)
}

func (r *UserRepository) UpdateUserPassword(ctx context.Context, userID string, passwordHash string, salt string) (*domain.User, error) {
	query := `UPDATE users SET password_hash = $1, salt = $2 WHERE id = $3`
	_, err := r.db.ExecContext(ctx, query, passwordHash, salt, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to update user password: %v", err)
	}
	return r.FindByID(ctx, userID)
}

func (r *UserRepository) UpdateEmailVerifiedAt(ctx context.Context, userID string) (*domain.User, error) {
	query := `UPDATE users SET email_verified_at = NOW() WHERE id = $1`
	_, err := r.db.ExecContext(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to update email verified at: %v", err)
	}
	return r.FindByID(ctx, userID)
}

func (r *UserRepository) InvalidateRefreshToken(ctx context.Context, refreshToken string) error {
	// @TODO: Implement refresh_tokens in DB + add check in RefreshTokenHandler.
	// query := `DELETE FROM refresh_tokens WHERE token = $1`
	// _, err := r.db.ExecContext(ctx, query, refreshToken)
	return nil
}
