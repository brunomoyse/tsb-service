package domain

import (
	"encoding/json"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"time"
)

type OrderStatus string
type OrderType string

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

const (
	OrderTypeDelivery OrderType = "DELIVERY"
	OrderTypePickUp   OrderType = "PICKUP"
)

type Order struct {
	ID                 uuid.UUID        `db:"id" json:"id"`
	CreatedAt          time.Time        `db:"created_at" json:"createdAt"`
	UpdatedAt          time.Time        `db:"updated_at" json:"updatedAt"`
	UserID             uuid.UUID        `db:"user_id" json:"userId"`
	OrderStatus        OrderStatus      `db:"order_status" json:"orderStatus"`
	OrderType          OrderType        `db:"order_type" json:"orderType"`
	IsOnlinePayment    bool             `db:"is_online_payment" json:"isOnlinePayment"`
	PaymentID          *uuid.UUID       `db:"payment_id" json:"paymentId,omitempty"`
	DiscountAmount     decimal.Decimal  `db:"discount_amount" json:"discountAmount"`
	DeliveryFee        *decimal.Decimal `db:"delivery_fee" json:"deliveryFee,omitempty"`
	TotalPrice         decimal.Decimal  `db:"total_price" json:"totalPrice"`
	EstimatedReadyTime *time.Time       `db:"estimated_ready_time" json:"estimatedReadyTime,omitempty"`
	AddressID          *uuid.UUID       `db:"address_id" json:"addressId,omitempty"`
	AddressExtra       *string          `db:"address_extra" json:"addressExtra,omitempty"`
	ExtraComment       *string          `db:"extra_comment" json:"extraComment,omitempty"`
	OrderExtra         json.RawMessage  `db:"order_extra" json:"orderExtras,omitempty"`
}

type OrderExtra struct {
	// Name of the extra, e.g., "chopsticks" or "sauces".
	Name string `json:"name"`
	// Options for this extra, e.g., for sauces: ["salt", "sweet"].
	Options []string `json:"options,omitempty"`
}

type OrderProductRaw struct {
	ProductID  uuid.UUID       `json:"productId"`
	Quantity   int64           `json:"quantity"`
	UnitPrice  decimal.Decimal `json:"unitPrice"`
	TotalPrice decimal.Decimal `json:"totalPrice"`
}

type OrderProduct struct {
	Product    Product         `json:"product"`
	Quantity   int64           `json:"quantity"`
	UnitPrice  decimal.Decimal `json:"unitPrice"`
	TotalPrice decimal.Decimal `json:"totalPrice"`
}

type Product struct {
	ID           uuid.UUID `json:"id"`
	Code         *string   `json:"code"`
	CategoryName string    `json:"categoryName"`
	Name         string    `json:"name"`
}

// NewOrder is a constructor function that creates a new Order domain object.
// Prices will be set later in the service layer.
func NewOrder(
	userID uuid.UUID,
	orderType OrderType,
	isOnlinePayment bool,
	addressID *uuid.UUID,
	addressExtra *string,
	extraComment *string,
	orderExtra []OrderExtra,
) *Order {
	orderExtraJSON, _ := json.Marshal(orderExtra)

	return &Order{
		ID:              uuid.Nil,
		UserID:          userID,
		OrderStatus:     OrderStatusPending,
		OrderType:       orderType,
		IsOnlinePayment: isOnlinePayment,
		AddressID:       addressID,
		AddressExtra:    addressExtra,
		ExtraComment:    extraComment,
		OrderExtra:      orderExtraJSON,
	}
}
