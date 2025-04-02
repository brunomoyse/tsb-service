package domain

import (
	"context"
	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/google/uuid"
)

type PaymentRepository interface {
	Save(ctx context.Context, payment mollie.Payment, orderID uuid.UUID) (*MolliePayment, error)
	RefreshStatus(ctx context.Context, externalPayment mollie.Payment) (*uuid.UUID, error)
	FindByOrderID(ctx context.Context, orderID uuid.UUID) (*MolliePayment, error)
}
