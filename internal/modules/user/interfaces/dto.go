package interfaces

import (
	"tsb-service/internal/modules/user/domain"

	"github.com/google/uuid"
)

// LoginRequest is used when a user attempts to log in.
type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// RegistrationRequest is used when a new user registers.
type RegistrationRequest struct {
	Name        string  `json:"name"`
	Email       string  `json:"email"`
	Password    string  `json:"password"`
	PhoneNumber *string `json:"phoneNumber"`
	Address     *string `json:"addressId"`
}

type UpdateUserRequest struct {
	Name        *string `json:"name"`
	Email       *string `json:"email"`
	PhoneNumber *string `json:"phoneNumber"`
	Address     *string `json:"address"`
}

// GoogleAuthRequest is used when a user logs in via Google.
type GoogleAuthRequest struct {
	GoogleID string `json:"googleId"`
	Email    string `json:"email"`
	Name     string `json:"name"`
}

// UserResponse is returned after successful operations involving a user.
type UserResponse struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Email       string    `json:"email"`
	PhoneNumber *string   `json:"phoneNumber"`
	Address     *string   `json:"address"`
}

type LoginResponse struct {
	User        *UserResponse `json:"user"`
	AccessToken string        `json:"accessToken"`
}

func NewUserResponse(u *domain.User) *UserResponse {
	return &UserResponse{
		ID:          u.ID,
		Name:        u.Name,
		Email:       u.Email,
		PhoneNumber: u.PhoneNumber,
		Address:     u.Address,
	}
}

func NewLoginResponse(u *domain.User) *LoginResponse {
	return &LoginResponse{
		User: NewUserResponse(u),
	}
}
