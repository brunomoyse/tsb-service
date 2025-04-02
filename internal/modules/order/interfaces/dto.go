package interfaces

import (
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"time"
	"tsb-service/internal/modules/order/domain"
)

// CreateOrderRequest represents the data a client can submit when creating a new order.
type CreateOrderRequest struct {
	// Indicates whether the order is for delivery or pickup.
	OrderType domain.OrderType `json:"orderType"` // Expected values: "DELIVERY", "PICKUP"

	// Whether the customer chose to pay online.
	IsOnlinePayment bool `json:"isOnlinePayment"`

	// Delivery address information (if OrderType is "DELIVERY").
	AddressID    *uuid.UUID `json:"addressId,omitempty"` // Geocoded BeSt address
	AddressExtra *string    `json:"addressExtra,omitempty"`

	// Any extra comments or special instructions for the order.
	ExtraComment *string `json:"extraComment,omitempty"`

	// Additional extras for the order (e.g., chopsticks, sauces, etc.).
	OrderExtra []domain.OrderExtra `json:"orderExtras,omitempty"`

	// A list of products (by ID and quantity) that the customer is ordering.
	OrderProducts []OrderProductDTO `json:"orderProducts"`
}

// OrderProductDTO represents an individual product in the order form.
// Note: Price is not included because it should be retrieved from a trusted source.
type OrderProductDTO struct {
	ProductID uuid.UUID `json:"productId"`
	Quantity  int64     `json:"quantity"`
}

type UpdateOrderRequest struct {
	OrderStatus domain.OrderStatus `json:"orderStatus"` // Expected values: "PENDING", "CONFIRMED", "PREPARING", "AWAITING_PICK_UP", "PICKED_UP", "OUT_FOR_DELIVERY", "DELIVERED", "CANCELLED", "FAILED"
}

// OrderResponse extends the domain.Order with additional response-specific details.
type OrderResponse struct {
	Order domain.Order `json:"order"`
	// OrderProducts is a list of products in the order with pricing details.
	OrderProducts []OrderProductResponse `json:"orderProducts"`
	// MolliePayment is the payment information associated with the order.
	MolliePayment *MolliePayment `json:"molliePayment,omitempty"`
}

// OrderProductResponse represents an individual product in the order response,
// including the unit price and the total price (per line).
type OrderProductResponse struct {
	Product    ProductResponse `json:"product"`
	Quantity   int64           `json:"quantity"`
	UnitPrice  decimal.Decimal `json:"unitPrice"`
	TotalPrice decimal.Decimal `json:"totalPrice"`
}

type ProductResponse struct {
	ID           uuid.UUID `json:"id"`
	Code         *string   `json:"code"`
	CategoryName string    `json:"categoryName"`
	Name         string    `json:"name"`
}

type MolliePayment struct {
	// The unique identifier returned by Mollie.
	ID string `json:"id"`

	// The ID of the order this payment is associated with.
	OrderID uuid.UUID `json:"orderId"`

	// The current status of the payment (e.g., "open", "paid", "failed").
	Status string `json:"status"`

	// Timestamp when the payment was created.
	CreatedAt time.Time `json:"createdAt"`

	// Timestamp when the payment was completed. This field is nil if the payment hasn't been completed.
	PaidAt *time.Time `json:"paidAt,omitempty"`
}
