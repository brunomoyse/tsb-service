package domain

import (
	productDomain "tsb-service/internal/modules/product/domain"
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
	Product  productDomain.Product `json:"product"`
	Quantity int                   `json:"quantity"`
}
