package domain

import (
	"time"

	"github.com/google/uuid"
)

type Order struct {
	ID               uuid.UUID     `db:"id" json:"id"`
	CreatedAt        time.Time     `db:"created_at" json:"createdAt"`
	UpdatedAt        time.Time     `db:"updated_at" json:"updatedAt"`
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
	OrderStatusPending         OrderStatus = "PENDING"
	OrderStatusConfirmed       OrderStatus = "CONFIRMED"
	OrderStatusPreparing       OrderStatus = "PREPARING"
	OrderStatusAwaitingPayment OrderStatus = "AWAITING_PAYMENT"
	OrderStatusPickedUp        OrderStatus = "PICKED_UP"
	OrderStatusOutForDelivery  OrderStatus = "OUT_FOR_DELIVERY"
	OrderStatusDelivered       OrderStatus = "DELIVERED"
	OrderStatusCancelled       OrderStatus = "CANCELLED"
	OrderStatusFailed          OrderStatus = "FAILED"
)

func NewOrder(userId uuid.UUID, products []PaymentLine) Order {
	paymentMode := PaymentMode("ONLINE")
	return Order{
		ID:          uuid.New(),
		UserID:      userId,
		PaymentMode: &paymentMode,
		Status:      OrderStatusPending,
		Products:    products,
	}
}
