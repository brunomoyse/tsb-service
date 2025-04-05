package interfaces

import (
	addressDomain "tsb-service/internal/modules/address/domain"
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
	FirstName   string  `json:"firstName"`
	LastName    string  `json:"lastName"`
	Email       string  `json:"email"`
	Password    string  `json:"password"`
	PhoneNumber *string `json:"phoneNumber"`
	AddressID   *string `json:"addressId"`
}

type UpdateUserRequest struct {
	FirstName   *string `json:"firstName"`
	LastName    *string `json:"lastName"`
	Email       *string `json:"email"`
	PhoneNumber *string `json:"phoneNumber"`
	AddressID   *string `json:"addressId"`
}

// GoogleAuthRequest is used when a user logs in via Google.
type GoogleAuthRequest struct {
	GoogleID  string `json:"googleId"`
	Email     string `json:"email"`
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
}

// UserResponse is returned after successful operations involving a user.
type UserResponse struct {
	ID          uuid.UUID              `json:"id"`
	FirstName   string                 `json:"firstName"`
	LastName    string                 `json:"lastName"`
	Email       string                 `json:"email"`
	PhoneNumber *string                `json:"phoneNumber"`
	Address     *addressDomain.Address `json:"address"`
}

type LoginResponse struct {
	User        *UserResponse `json:"user"`
	AccessToken string        `json:"accessToken"`
}

func NewUserResponse(u *domain.User, a *addressDomain.Address) *UserResponse {
	return &UserResponse{
		ID:          u.ID,
		FirstName:   u.FirstName,
		LastName:    u.LastName,
		Email:       u.Email,
		PhoneNumber: u.PhoneNumber,
		Address:     a,
	}
}

func NewLoginResponse(u *domain.User, a *addressDomain.Address) *LoginResponse {
	return &LoginResponse{
		User: NewUserResponse(u, a),
	}
}
