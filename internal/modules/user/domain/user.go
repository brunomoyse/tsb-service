package domain

import (
	"github.com/golang-jwt/jwt/v4"
	"time"

	"github.com/google/uuid"
)

type User struct {
	ID              uuid.UUID  `db:"id" json:"id"`
	CreatedAt       time.Time  `db:"created_at" json:"createdAt"`
	UpdatedAt       time.Time  `db:"updated_at" json:"updatedAt"`
	FirstName       string     `db:"first_name" json:"firstName"`
	LastName        string     `db:"last_name" json:"lastName"`
	Email           string     `db:"email" json:"email"`
	EmailVerifiedAt *time.Time `db:"email_verified_at" json:"emailVerifiedAt"`
	PhoneNumber     *string    `db:"phone_number" json:"phoneNumber"`
	AddressID       *string    `db:"address_id" json:"addressId"`
	PasswordHash    *string    `db:"password_hash" json:"passwordHash"`
	Salt            *string    `db:"salt" json:"salt"`
	RememberToken   *string    `db:"remember_token" json:"rememberToken"`
	GoogleID        *string    `db:"google_id" json:"googleId"`
	IsAdmin         bool       `db:"is_admin" json:"isAdmin"`
}

type JwtClaims struct {
	jwt.RegisteredClaims
	Type string `json:"type"` // "access" or "refresh"
	ID   string `json:"jti"`  // Unique token identifier
}

func NewUser(firstName string, lastName string, email string, phoneNumber *string, addressID *string, passwordHash *string, salt *string) User {
	return User{
		ID:           uuid.Nil,
		FirstName:    firstName,
		LastName:     lastName,
		Email:        email,
		PhoneNumber:  phoneNumber,
		AddressID:    addressID,
		PasswordHash: passwordHash,
		Salt:         salt,
	}
}

func NewGoogleUser(firstName string, lastName string, email string, googleID string) User {
	return User{
		ID:        uuid.Nil,
		FirstName: firstName,
		LastName:  lastName,
		Email:     email,
		GoogleID:  &googleID,
	}
}
