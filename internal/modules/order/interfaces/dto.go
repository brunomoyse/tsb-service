package interfaces

import (
	"tsb-service/internal/modules/order/domain"
)

type CreateOrderForm struct {
	ProductsLines []domain.PaymentLine `json:"products"`
	PaymentMode   domain.PaymentMode   `json:"paymentMode"`
	// ShippingAddress *mollie.Address `json:"shipping_address"`
}
