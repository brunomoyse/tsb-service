package resolver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"tsb-service/internal/api/graphql/apperr"
	orderDomain "tsb-service/internal/modules/order/domain"
	paymentDomain "tsb-service/internal/modules/payment/domain"
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

// refuseReopeningSettledOrder stops a CANCELLED order from being moved to another status once its
// payment was refunded (even partly) or cancelled/expired/failed: the kitchen would prepare food
// the customer got their money back for, or for a payment that can no longer be made.
//
// This is the only transition rule. The others stay free on purpose (staff correct mistakes by
// moving orders back and forth, see TestUpdateOrderOtherTransitionsStayFree).
func (r *Resolver) refuseReopeningSettledOrder(ctx context.Context, order *orderDomain.Order, newStatus *orderDomain.OrderStatus) error {
	if newStatus == nil || order.OrderStatus != orderDomain.OrderStatusCanceled || *newStatus == orderDomain.OrderStatusCanceled {
		return nil
	}
	payment, err := r.PaymentService.GetPaymentByOrderID(ctx, order.ID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil // cash order: nothing to protect
	case err != nil:
		return fmt.Errorf("failed to look up the payment of the order: %w", err)
	case payment == nil || !paymentSettled(payment):
		return nil
	}
	return apperr.New(apperr.CodeUserError,
		"this order was cancelled and its payment refunded or cancelled, so it cannot be reopened; create a new order instead")
}

// paymentSettled reports whether the payment of a cancelled order was given back or can no longer be paid.
func paymentSettled(p *paymentDomain.MolliePayment) bool {
	if p.AmountRefunded.IsPositive() {
		return true
	}
	switch p.Status {
	case paymentDomain.PaymentStatusCanceled, paymentDomain.PaymentStatusExpired, paymentDomain.PaymentStatusFailed:
		return true
	default:
		return false
	}
}
