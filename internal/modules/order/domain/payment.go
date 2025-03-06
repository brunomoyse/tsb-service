package domain

import (
	productDomain "tsb-service/internal/modules/product/domain"

	"github.com/google/uuid"
)

type PaymentProductLine struct {
	Product  productDomain.Product `json:"product"`
	Quantity int                   `json:"quantity"`
}

type PaymentMode string

const (
	PaymentModeCash     PaymentMode = "CASH"
	PaymentModeOnline   PaymentMode = "ONLINE"
	PaymentModeTerminal PaymentMode = "TERMINAL"
)

type PaymentLine struct {
	Product  Product `json:"product"`
	Quantity int     `json:"quantity"`
}

type Product struct {
	ID    uuid.UUID `json:"id"`
	Name  string    `json:"name"`
	Price float64   `json:"price"`
}
