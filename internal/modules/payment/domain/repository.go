package domain

import (
	"context"
	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/google/uuid"
)

type PaymentRepository interface {
	Save(ctx context.Context, payment mollie.Payment, orderID uuid.UUID) (*MolliePayment, error)
	MarkAsRefund(ctx context.Context, externalPaymentID string, amount *mollie.Amount) error
	RefreshStatus(ctx context.Context, externalPayment mollie.Payment) (*uuid.UUID, error)
	FindByOrderID(ctx context.Context, orderID uuid.UUID) (*MolliePayment, error)
	FindByExternalID(ctx context.Context, externalPaymentID string) (*MolliePayment, error)

	FindByOrderIDs(ctx context.Context, orderIDs []string) (map[string][]*MolliePayment, error)
}
