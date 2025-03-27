package domain

import (
	"github.com/google/uuid"
)

type PaymentMode string

const (
	PaymentModeCash     PaymentMode = "CASH"
	PaymentModeOnline   PaymentMode = "ONLINE"
	PaymentModeTerminal PaymentMode = "TERMINAL"
)

type PaymentLine struct {
	Product    Product `json:"product"`
	Quantity   int     `json:"quantity"`
	UnitPrice  float64 `json:"unitPrice"`
	TotalPrice float64 `json:"totalPrice"`
}

type Product struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}
