package order

import (
	"time"

	"github.com/google/uuid"
)

// Order struct to represent an order
type Order struct {
	ID               uuid.UUID         `json:"id"`
	CreatedAt        time.Time         `json:"createdAt"`
	UpdatedAt        *time.Time        `json:"updatedAt"`
	UserId           uuid.UUID         `json:"userId"`
	PaymentMode      *OrderPaymentMode `json:"paymentMode"`
	MolliePaymentId  *string           `json:"molliePaymentId"`
	MolliePaymentUrl *string           `json:"molliePaymentUrl"`
	Status           OrderStatus       `json:"status"`
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

// OrderPaymentMode represents the payment mode for an order
type OrderPaymentMode string

const (
	PaymentModeCash     OrderPaymentMode = "CASH"
	PaymentModeOnline   OrderPaymentMode = "ONLINE"
	PaymentModeTerminal OrderPaymentMode = "TERMINAL"
)

type CreateOrderForm struct {
	// ShippingAddress *mollie.Address `json:"shipping_address"`
	ProductsLines []ProductLine `json:"products"`
}

type ProductLine struct {
	Product  ProductInfo `json:"product"`
	Quantity int         `json:"quantity"`
}

type ProductInfo struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Description *string   `json:"description"`
	Price       float64   `json:"price"`
	Code        *string   `json:"code"`
	Slug        *string   `json:"slug"`
	IsActive    bool      `json:"isActive"`
	IsHalal     bool      `json:"isHalal"`
	IsVegan     bool      `json:"isVegan"`
}
