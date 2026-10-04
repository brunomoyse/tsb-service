package resolver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"tsb-service/internal/api/graphql/apperr"
	"tsb-service/internal/api/graphql/model"
	orderDomain "tsb-service/internal/modules/order/domain"
	paymentDomain "tsb-service/internal/modules/payment/domain"
)

// cancelOrder cancels an order together with its payment: a paid payment is refunded, an open one is
// cancelled at the provider so it can no longer be paid, and only then is CANCELLED saved.
//
// Settle-then-save keeps a failed settlement harmless: the order is left exactly as it was and the
// staff member can simply retry. Every step is safe to repeat (the refund is only for what Mollie says
// is left, a cancelled payment is not cancelled again), so a retry after a failure on the way (the
// refund went through but saving the order did not) never refunds twice.
//
// That only holds for retries one after another. Two cancels at the same time (a double click, the
// dashboard and a handheld) would both read "nothing refunded yet" and both refund, and a "paid"
// webhook between the settlement and the save would announce the order as newly paid. So the whole
// sequence runs under the advisory lock of the payment, the one the Mollie webhook takes: the order
// is read again inside it, and a caller that finds the order already cancelled does not settle again.
//
// It returns the order as it was when this call took over (the caller compares it with the saved one
// to decide on notifications) and the whether a refund was issued.
func (r *Resolver) cancelOrder(ctx context.Context, orderID uuid.UUID, input model.UpdateOrderInput, seen *orderDomain.Order) (*orderDomain.Order, bool, error) {
	payment, err := r.PaymentService.GetPaymentByOrderID(ctx, orderID)
	switch {
	case errors.Is(err, sql.ErrNoRows), err == nil && payment == nil:
		return seen, false, r.saveOrder(ctx, orderID, input) // cash order: nothing was charged
	case err != nil:
		return nil, false, paymentLookupFailed(orderID, err)
	}

	var (
		previous   = seen
		refunded   bool
		inner      error
	)
	lockErr := r.PaymentService.WithPaymentLock(ctx, payment.MolliePaymentID, func(ctx context.Context) error {
		inner = func() error {
			fresh, _, err := r.OrderService.GetOrderByID(ctx, orderID)
			if err != nil {
				return orderLookupError(err)
			}
			previous = fresh
			if fresh.OrderStatus == orderDomain.OrderStatusCanceled {
				// Someone else cancelled (and settled) it while this call waited for the lock.
				return r.saveOrder(ctx, orderID, input)
			}

			current, err := r.PaymentService.GetPaymentByOrderID(ctx, orderID)
			if err != nil {
				return paymentLookupFailed(orderID, err)
			}
			if current != nil {
				payment = current
			}
			if refunded, err = r.PaymentService.SettleCancelledOrderPayment(ctx, payment); err != nil {
				zap.L().Error("cannot cancel the order: payment settlement failed",
					zap.String("order_id", orderID.String()), zap.String("payment_id", payment.MolliePaymentID), zap.Error(err))
				return apperr.New(apperr.CodePaymentSettlementFailed,
					"the payment of this order could not be refunded or cancelled, so the order was NOT cancelled; please try again")
			}
			return r.saveOrder(ctx, orderID, input)
		}()
		return inner
	})
	switch {
	case inner != nil:
		return nil, false, inner
	case lockErr != nil:
		zap.L().Error("cannot cancel the order: payment lock failed",
			zap.String("order_id", orderID.String()), zap.String("payment_id", payment.MolliePaymentID), zap.Error(lockErr))
		return nil, false, apperr.New(apperr.CodePaymentSettlementFailed,
			"the payment of this order could not be refunded or cancelled, so the order was NOT cancelled; please try again")
	}
	return previous, refunded, nil
}

func (r *Resolver) saveOrder(ctx context.Context, orderID uuid.UUID, input model.UpdateOrderInput) error {
	if err := r.OrderService.UpdateOrder(ctx, orderID, input.Status, input.EstimatedReadyTime, input.CancellationReason); err != nil {
		return fmt.Errorf("failed to update order status: %w", err)
	}
	return nil
}

func paymentLookupFailed(orderID uuid.UUID, err error) error {
	zap.L().Error("cannot cancel the order: payment lookup failed", zap.String("order_id", orderID.String()), zap.Error(err))
	return apperr.New(apperr.CodePaymentSettlementFailed,
		"the payment of this order could not be looked up, so the order was NOT cancelled; please try again")
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

// orderLookupError turns the error of an order lookup into what the client should see: NOT_FOUND
// for an order that does not exist (not the generic internal error), an internal error otherwise.
func orderLookupError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return apperr.New(apperr.CodeNotFound, "order not found")
	}
	return fmt.Errorf("failed to get order: %w", err)
}
