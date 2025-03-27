package interfaces

import (
	"tsb-service/internal/modules/order/domain"
)

type NewOrderProductLine struct {
	ProductID string `json:"productID"`
	Quantity  int    `json:"quantity"`
}

type CreateOrderForm struct {
	ProductsLines []NewOrderProductLine `json:"products"`
	PaymentMode   domain.PaymentMode    `json:"paymentMode"`
	// ShippingAddress *mollie.Address `json:"shipping_address"`
}
