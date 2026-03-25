package domain

import (
	"context"

	"github.com/google/uuid"
)

type UserRepository interface {
	Save(ctx context.Context, u *User) (uuid.UUID, error)
	FindByID(ctx context.Context, id string) (*User, error)
	FindByEmail(ctx context.Context, email string) (*User, error)
	FindByZitadelID(ctx context.Context, zitadelID string) (*User, error)
	UpdateUser(ctx context.Context, user *User) (*User, error)
	RequestDeletion(ctx context.Context, userID string) (*User, error)
	CancelDeletionRequest(ctx context.Context, userID string) (*User, error)
	BatchGetUsersByOrderIDs(ctx context.Context, orderIDs []string) (map[string][]*User, error)
}
