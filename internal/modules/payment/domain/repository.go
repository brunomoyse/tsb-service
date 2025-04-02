package domain

import (
	"context"
	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/google/uuid"
)

type PaymentRepository interface {
	Save(ctx context.Context, payment mollie.Payment, orderID uuid.UUID) (*MolliePayment, error)
}
