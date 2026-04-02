package domain

import (
	"context"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type PaymentRepository interface {
	Save(ctx context.Context, payment *MolliePayment) error
	MarkAsRefund(ctx context.Context, externalPaymentID string, refundedAmount decimal.Decimal) error
	RefreshStatus(ctx context.Context, externalPaymentID string, update *PaymentStatusUpdate) (*uuid.UUID, error)
	UpdateStatusByOrderID(ctx context.Context, orderID uuid.UUID, status PaymentStatus) (*MolliePayment, error)
	FindByOrderID(ctx context.Context, orderID uuid.UUID) (*MolliePayment, error)
	FindByExternalID(ctx context.Context, externalPaymentID string) (*MolliePayment, error)

	FindByOrderIDs(ctx context.Context, orderIDs []string) (map[string][]*MolliePayment, error)
}
