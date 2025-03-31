package domain

import (
	"github.com/golang-jwt/jwt/v4"
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
	PhoneNumber     *string    `db:"phone_number" json:"phoneNumber"`
	Address         *string    `db:"address" json:"address"`
	PasswordHash    *string    `db:"password_hash" json:"passwordHash"`
	Salt            *string    `db:"salt" json:"salt"`
	RememberToken   *string    `db:"remember_token" json:"rememberToken"`
	GoogleID        *string    `db:"google_id" json:"googleId"`
}

type JwtClaims struct {
	jwt.RegisteredClaims
	Type string `json:"type"` // "access" or "refresh"
	ID   string `json:"jti"`  // Unique token identifier
}

func NewUser(name string, email string, phoneNumber *string, address *string, passwordHash *string, salt *string) User {
	return User{
		ID:           uuid.Nil,
		Name:         name,
		Email:        email,
		PhoneNumber:  phoneNumber,
		Address:      address,
		PasswordHash: passwordHash,
		Salt:         salt,
	}
}

func NewGoogleUser(name string, email string, googleID string) User {
	return User{
		ID:       uuid.Nil,
		Name:     name,
		Email:    email,
		GoogleID: &googleID,
	}
}

// ErrNotFound is returned when a requested resource is not found.
var ErrNotFound = errors.New("resource not found")
