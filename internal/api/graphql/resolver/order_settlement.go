package resolver

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"tsb-service/internal/api/graphql/apperr"
)

// settlePaymentBeforeCancel undoes the payment of an order that is about to be cancelled: a paid
// payment is refunded, an open one is cancelled at the provider so it can no longer be paid.
//
// It runs BEFORE CANCELLED is saved, and the order is only saved when it succeeded. A failure
// therefore leaves the order exactly as it was and the staff member can simply retry. Every step
// is safe to repeat (the refund is only for what is left, a cancelled payment is not cancelled
// again), so a retry after a failure on the way (the refund went through but saving the order
// did not) never refunds twice. Reports whether a refund was issued.
func (r *Resolver) settlePaymentBeforeCancel(ctx context.Context, orderID uuid.UUID) (refunded bool, err error) {
	payment, err := r.PaymentService.GetPaymentByOrderID(ctx, orderID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil // cash order: nothing was charged
	case err != nil:
		zap.L().Error("cannot cancel the order: payment lookup failed", zap.String("order_id", orderID.String()), zap.Error(err))
		return false, apperr.New(apperr.CodePaymentSettlementFailed,
			"the payment of this order could not be looked up, so the order was NOT cancelled; please try again")
	case payment == nil:
		return false, nil
	}

	refunded, err = r.PaymentService.SettleCancelledOrderPayment(ctx, payment)
	if err != nil {
		zap.L().Error("cannot cancel the order: payment settlement failed",
			zap.String("order_id", orderID.String()), zap.String("payment_id", payment.MolliePaymentID), zap.Error(err))
		return false, apperr.New(apperr.CodePaymentSettlementFailed,
			"the payment of this order could not be refunded or cancelled, so the order was NOT cancelled; please try again")
	}
	return refunded, nil
}
