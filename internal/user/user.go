// internal/user/user.go
package user

import (
	"time"

	"github.com/google/uuid"
)

type User struct {
	ID              uuid.UUID  `json:"id"`
	CreatedAt       string     `json:"createdAt"`
	UpdatedAt       string     `json:"updatedAt"`
	Name            string     `json:"name"`
	Email           string     `json:"email"`
	EmailVerifiedAt *time.Time `json:"emailVerifiedAt"`
	PasswordHash    *string    `json:"passwordHash"`
	Salt            *string    `json:"salt"`
	RememberToken   *string    `json:"rememberToken"`
	GoogleID        *string    `json:"googleId"`
}

type UserLogin struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type UserRegister struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

type GoogleUser struct {
	GoogleID string `json:"googleId"`
	Email    string `json:"email"`
	Name     string `json:"name"`
}

type UserResponse struct {
	ID    uuid.UUID `json:"id"`
	Name  string    `json:"name"`
	Email string    `json:"email"`
}
