package model

import (
	"time"
	"tsb-service/internal/modules/order/domain"

	"github.com/google/uuid"
)

// Order is the custom GraphQL Order model with denormalized address fields.
// This replaces the auto-generated Order struct so we can include non-schema
// fields used by the Address() resolver.
type Order struct {
	// Schema fields
	ID                 uuid.UUID          `json:"id"`
	CreatedAt          time.Time          `json:"createdAt"`
	UpdatedAt          time.Time          `json:"updatedAt"`
	Status             domain.OrderStatus `json:"status"`
	Type               OrderTypeEnum      `json:"type"`
	IsOnlinePayment    bool               `json:"isOnlinePayment"`
	DiscountAmount     string             `json:"discountAmount"`
	DeliveryFee        *string            `json:"deliveryFee,omitempty"`
	TransactionFee     *string            `json:"transactionFee,omitempty"`
	TotalPrice         string             `json:"totalPrice"`
	PreferredReadyTime *time.Time         `json:"preferredReadyTime,omitempty"`
	EstimatedReadyTime *time.Time         `json:"estimatedReadyTime,omitempty"`
	AddressExtra       *string            `json:"addressExtra,omitempty"`
	OrderNote          *string            `json:"orderNote,omitempty"`
	OrderExtra         []any              `json:"orderExtra,omitempty"`
	CouponCode         *string            `json:"couponCode,omitempty"`
	CancellationReason *domain.OrderCancellationReason `json:"cancellationReason,omitempty"`
	CashPaymentAmount  *string            `json:"cashPaymentAmount,omitempty"`

	// Non-schema fields: denormalized address for Address() resolver
	AddressID        *string  `json:"-"`
	AddressPlaceID   *string  `json:"-"`
	AddressLat       *float64 `json:"-"`
	AddressLng       *float64 `json:"-"`
	StreetName       *string  `json:"-"`
	HouseNumber      *string  `json:"-"`
	BoxNumber        *string  `json:"-"`
	MunicipalityName *string  `json:"-"`
	Postcode         *string  `json:"-"`
	AddressDistance   *float64 `json:"-"`
	IsManualAddr     *bool    `json:"-"`
}

// User is the custom GraphQL User model. It mirrors the schema fields and
// carries DefaultPlaceID as a non-schema field, used by the Address()
// resolver to look up the user's saved default address in address_cache.
type User struct {
	ID                  uuid.UUID  `json:"id"`
	Email               string     `json:"email"`
	FirstName           string     `json:"firstName"`
	LastName            string     `json:"lastName"`
	PhoneNumber         *string    `json:"phoneNumber,omitempty"`
	IsAdmin             bool       `json:"isAdmin"`
	NotifyMarketing     bool       `json:"notifyMarketing"`
	NotifyOrderUpdates  bool       `json:"notifyOrderUpdates"`
	DeletionRequestedAt *time.Time `json:"deletionRequestedAt,omitempty"`
	Rrn                 *string    `json:"rrn,omitempty"`

	DefaultPlaceID *string `json:"-"`
}
