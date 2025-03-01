package domain

import (
	"time"

	"github.com/google/uuid"
)

type Order struct {
	ID               uuid.UUID     `db:"id" json:"id"`
	CreatedAt        time.Time     `db:"created_at" json:"createdAt"`
	UpdatedAt        time.Time    `db:"updated_at" json:"updatedAt"`
	UserID           uuid.UUID     `db:"user_id" json:"userId"`
	PaymentMode      *PaymentMode  `db:"payment_mode" json:"paymentMode"`
	MolliePaymentId  *string       `db:"mollie_payment_id" json:"molliePaymentId"`
	MolliePaymentUrl *string       `db:"mollie_payment_url" json:"molliePaymentUrl"`
	Status           OrderStatus   `db:"status" json:"status"`
	Products         []PaymentLine `db:"-" json:"products"`
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



func NewOrder(userId uuid.UUID, products []PaymentLine, paymentMode PaymentMode) Order {
	return Order{
		ID:          uuid.New(),
		UserID:      userId,
		PaymentMode: &paymentMode,
		Status:      OrderStatusOpen,
		Products:    products,
	}
}
