package domain

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// ErrDuplicateUser is returned by Save when an insert violates a uniqueness
// constraint (email or zitadel_user_id). It lets the application layer detect a
// concurrent create-then-create race and recover by re-fetching the existing row
// without depending on the database driver.
var ErrDuplicateUser = errors.New("user already exists")

type UserRepository interface {
	Save(ctx context.Context, u *User) (uuid.UUID, error)
	FindByID(ctx context.Context, id string) (*User, error)
	FindByEmail(ctx context.Context, email string) (*User, error)
	FindByZitadelID(ctx context.Context, zitadelID string) (*User, error)
	UpdateUser(ctx context.Context, user *User) (*User, error)
	// AnonymizeForDeletion erases all PII on the user row (keeping the row so
	// VAT-retained orders survive ON DELETE RESTRICT) and drops device push tokens.
	AnonymizeForDeletion(ctx context.Context, userID string) error
	BatchGetUsersByOrderIDs(ctx context.Context, orderIDs []string) (map[string][]*User, error)
}
