package domain

import (
	"time"

	"github.com/google/uuid"
)

type OrderStatus string

const (
	OrderStatusPending        OrderStatus = "PENDING"
	OrderStatusConfirmed      OrderStatus = "CONFIRMED"
	OrderStatusPreparing      OrderStatus = "PREPARING"
	OrderStatusAwaitingUp     OrderStatus = "AWAITING_PICK_UP"
	OrderStatusPickedUp       OrderStatus = "PICKED_UP"
	OrderStatusOutForDelivery OrderStatus = "OUT_FOR_DELIVERY"
	OrderStatusDelivered      OrderStatus = "DELIVERED"
	OrderStatusCancelled      OrderStatus = "CANCELLED"
	OrderStatusFailed         OrderStatus = "FAILED"
)

type OrderType string

const (
	OrderTypeDelivery OrderType = "DELIVERY"
	OrderTypePickUp   OrderType = "PICKUP"
)

type Order struct {
	ID                 uuid.UUID   `db:"id" json:"id"`
	CreatedAt          time.Time   `db:"created_at" json:"createdAt"`
	UpdatedAt          time.Time   `db:"updated_at" json:"updatedAt"`
	UserID             uuid.UUID   `db:"user_id" json:"userId"`
	OrderStatus        OrderStatus `db:"order_status" json:"orderStatus"`
	OrderType          OrderType   `db:"order_type" json:"orderType"`
	IsOnlinePayment    bool        `db:"is_online_payment" json:"isOnlinePayment"`
	PaymentID          *uuid.UUID  `db:"payment_id" json:"paymentId,omitempty"`
	DiscountAmount     float64     `db:"discount_amount" json:"discountAmount"`
	DeliveryFee        *float64    `db:"delivery_fee" json:"deliveryFee,omitempty"`
	TotalPrice         float64     `db:"total_price" json:"totalPrice"`
	EstimatedReadyTime *time.Time  `db:"estimated_ready_time" json:"estimatedReadyTime,omitempty"`
	AddressID          *uuid.UUID  `db:"address_id" json:"addressId,omitempty"`
	AddressExtra       *string     `db:"address_extra" json:"addressExtra,omitempty"`
	ExtraComment       *string     `db:"extra_comment" json:"extraComment,omitempty"`
}
