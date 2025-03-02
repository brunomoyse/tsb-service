package domain

import (
	"context"

	"github.com/google/uuid"
)

type UserRepository interface {
	Save(ctx context.Context, u *User) (uuid.UUID, error)
	FindByID(ctx context.Context, id string) (*User, error)
	FindByGoogleID(ctx context.Context, googleID string) (*User, error)
	FindByEmail(ctx context.Context, email string) (*User, error)
	UpdateGoogleID(ctx context.Context, userID string, googleID string) (*User, error)
	UpdateUserPassword(ctx context.Context, userID string, password string, salt string) (*User, error)
	UpdateEmailVerifiedAt(ctx context.Context, userID string) (*User, error)
	InvalidateRefreshToken(ctx context.Context, refreshToken string) error
}
