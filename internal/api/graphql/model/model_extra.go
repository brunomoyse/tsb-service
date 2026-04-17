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
	TotalPrice         string             `json:"totalPrice"`
	PreferredReadyTime *time.Time         `json:"preferredReadyTime,omitempty"`
	EstimatedReadyTime *time.Time         `json:"estimatedReadyTime,omitempty"`
	AddressExtra       *string            `json:"addressExtra,omitempty"`
	OrderNote          *string            `json:"orderNote,omitempty"`
	OrderExtra         map[string]any     `json:"orderExtra,omitempty"`
	CouponCode         *string            `json:"couponCode,omitempty"`
	CancellationReason *domain.OrderCancellationReason `json:"cancellationReason,omitempty"`

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
