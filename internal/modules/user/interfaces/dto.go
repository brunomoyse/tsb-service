package interfaces

import (
	addressDomain "tsb-service/internal/modules/address/domain"
	"tsb-service/internal/modules/user/domain"

	"github.com/google/uuid"
)

type UpdateUserRequest struct {
	FirstName   *string `json:"firstName"`
	LastName    *string `json:"lastName"`
	Email       *string `json:"email"`
	PhoneNumber *string `json:"phoneNumber"`
	AddressID   *string `json:"addressId"`
}

type UserResponse struct {
	ID          uuid.UUID              `json:"id"`
	FirstName   string                 `json:"firstName"`
	LastName    string                 `json:"lastName"`
	Email       string                 `json:"email"`
	PhoneNumber *string                `json:"phoneNumber"`
	Address     *addressDomain.Address `json:"address"`
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
