package domain

import (
	"time"

	"errors"

	"github.com/google/uuid"
)

type User struct {
	ID              uuid.UUID  `db:"id" json:"id"`
	CreatedAt       time.Time  `db:"created_at" json:"createdAt"`
	UpdatedAt       time.Time  `db:"updated_at" json:"updatedAt"`
	Name            string     `db:"name" json:"name"`
	Email           string     `db:"email" json:"email"`
	EmailVerifiedAt *time.Time `db:"email_verified_at" json:"emailVerifiedAt"`
	PasswordHash    *string    `db:"password_hash" json:"passwordHash"`
	Salt            *string    `db:"salt" json:"salt"`
	RememberToken   *string    `db:"remember_token" json:"rememberToken"`
	GoogleID        *string    `db:"google_id" json:"googleId"`
}

func NewUser(name string, email string, passwordHash *string, googleID *string) User {
	return User{
		ID:           uuid.Nil,
		Name:         name,
		Email:        email,
		PasswordHash: passwordHash,
		GoogleID:     googleID,
	}
}

// ErrNotFound is returned when a requested resource is not found.
var ErrNotFound = errors.New("resource not found")
