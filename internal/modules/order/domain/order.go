package domain

import (
	"time"

	"github.com/google/uuid"
)

type Order struct {
	ID               uuid.UUID     `json:"id"`
	CreatedAt        time.Time     `json:"createdAt"`
	UpdatedAt        *time.Time    `json:"updatedAt"`
	UserID           uuid.UUID     `json:"userId"`
	PaymentMode      *PaymentMode  `json:"paymentMode"`
	MolliePaymentId  *string       `json:"molliePaymentId"`
	MolliePaymentUrl *string       `json:"molliePaymentUrl"`
	Status           OrderStatus   `json:"status"`
	Products         []PaymentLine `json:"products"`
	// ShippingAddress  *mollie.Address       `json:"shipping_address"`
}

// OrderStatus represents the status of an order
type OrderStatus string

const (
	OrderStatusOpen       OrderStatus = "OPEN"
	OrderStatusCanceled   OrderStatus = "CANCELED"
	OrderStatusPending    OrderStatus = "PENDING"
	OrderStatusAuthorized OrderStatus = "AUTHORIZED"
	OrderStatusExpired    OrderStatus = "EXPIRED"
	OrderStatusFailed     OrderStatus = "FAILED"
	OrderStatusPaid       OrderStatus = "PAID"
)

type Product struct {
	ID    uuid.UUID
	Name  string
	Price float64
}

func NewOrder(userId uuid.UUID, products []PaymentLine, paymentMode PaymentMode) Order {
	return Order{
		ID:          uuid.New(),
		CreatedAt:   time.Now(),
		UserID:      userId,
		PaymentMode: &paymentMode,
		Status:      OrderStatusOpen,
		Products:    products,
	}
}
