package interfaces

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"tsb-service/internal/api/graphql/resolver"
	orderDomain "tsb-service/internal/modules/order/domain"
	paymentApplication "tsb-service/internal/modules/payment/application"
	paymentDomain "tsb-service/internal/modules/payment/domain"
	"tsb-service/pkg/logging"
	"tsb-service/pkg/pubsub"
	"tsb-service/pkg/utils"
)

// NewOrderNotifier fans out push notifications when an online-payment order
// transitions to paid. Satisfied by *resolver.Resolver.
type NewOrderNotifier interface {
	SendNewOrderPush(order *orderDomain.Order)
}

type PaymentHandler struct {
	service  paymentApplication.PaymentService
	broker   *pubsub.Broker
	notifier NewOrderNotifier
}

func NewPaymentHandler(service paymentApplication.PaymentService, broker *pubsub.Broker, notifier NewOrderNotifier) *PaymentHandler {
	return &PaymentHandler{service: service, broker: broker, notifier: notifier}
}

// UpdatePaymentStatusHandler handles Mollie webhook callbacks.
//
// Security model: Mollie standard webhooks do NOT include a signature header.
// The webhook body contains only a payment ID (e.g. "tr_xxx"). We always re-fetch
// the payment from the Mollie API to get the authoritative status. This means a
// spoofed webhook cannot change payment state — the Mollie API is the source of truth.
func (h *PaymentHandler) UpdatePaymentStatusHandler(c *gin.Context) {
	// The webhook itself runs under the plain request context — only the
	// service calls that need write access to orders/payments run under an
	// admin-flagged context. This keeps the elevation narrow: pubsub events
	// and any future downstream code added here don't silently inherit admin
	// privileges from a request whose only authenticated party is Mollie.
	ctx := c.Request.Context()
	adminCtx := utils.SetIsAdmin(ctx, true)
	log := logging.FromContext(ctx)

	var req struct {
		ExternalMolliePaymentID string `form:"id" binding:"required"`
	}

	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request payload"})
		return
	}

	// Validate Mollie payment ID format
	if !strings.HasPrefix(req.ExternalMolliePaymentID, "tr_") {
		log.Warn("webhook: invalid payment ID format", zap.String("payment_id", req.ExternalMolliePaymentID))
		c.JSON(http.StatusOK, gin.H{"message": "ignored"})
		return
	}

	paymentID := req.ExternalMolliePaymentID

	// Serialize concurrent webhook deliveries for this payment (Mollie can fan out
	// retries that overlap, possibly across replicas) so the order business logic
	// runs at most once per transition and the idempotency check below sees a
	// consistent stored status.
	if lockErr := h.service.WithPaymentLock(adminCtx, paymentID, func(adminCtx context.Context) error {
		// Verify the payment exists in our DB. A genuine not-found means a spoofed
		// or stale ID — ack with 200 so Mollie stops. Any OTHER error (e.g. a
		// transient DB failure) must return 500 so Mollie retries; acking it would
		// silently drop the webhook.
		payment, err := h.service.GetPaymentByExternalID(adminCtx, paymentID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				log.Warn("webhook: unknown payment ID", zap.String("payment_id", paymentID))
				c.JSON(http.StatusOK, gin.H{"message": "unknown payment"})
				return nil
			}
			log.Error("webhook: failed to look up payment", zap.String("payment_id", paymentID), zap.Error(err))
			c.JSON(http.StatusInternalServerError, gin.H{"error": "temporary failure"})
			return nil
		}

		// Fetch the authoritative status from Mollie WITHOUT persisting it yet.
		update, err := h.service.FetchMollieStatus(adminCtx, paymentID)
		if err != nil {
			log.Error("webhook: failed to fetch payment status from Mollie", zap.String("payment_id", paymentID), zap.Error(err))
			c.JSON(http.StatusInternalServerError, gin.H{"error": "temporary failure"})
			return nil
		}

		// Idempotency: the stored status is the commit marker. If it already matches
		// Mollie, the work for this transition was done (possibly by a concurrent
		// delivery that held the lock just before us) — nothing more to do.
		if update.Status == payment.Status {
			c.JSON(http.StatusOK, gin.H{"message": "already processed"})
			return nil
		}

		orderID := payment.OrderID

		// Run order business logic BEFORE persisting the new status. On failure we
		// return 500 and leave the stored status untouched, so Mollie's retry re-runs
		// the logic instead of being short-circuited by an "already processed" status.
		switch update.Status {
		case paymentDomain.PaymentStatusPaid:
			order, handleErr := h.service.HandlePaymentPaid(adminCtx, orderID)
			if handleErr != nil {
				log.Error("webhook: failed to handle paid payment", zap.String("payment_id", paymentID), zap.Error(handleErr))
				c.JSON(http.StatusInternalServerError, gin.H{"error": "temporary failure"})
				return nil
			}
			if persistErr := h.service.PersistPaymentStatus(adminCtx, paymentID, update); persistErr != nil {
				log.Error("webhook: failed to persist payment status", zap.String("payment_id", paymentID), zap.Error(persistErr))
				c.JSON(http.StatusInternalServerError, gin.H{"error": "temporary failure"})
				return nil
			}
			switch {
			case order == nil:
				// nothing to publish
			case order.IsTest:
				// Store-review test order: stays fully invisible to staff — no
				// subscription publish, no push. It will auto-cancel after 10 min.
				// TEMPORARY (revert after launch).
				log.Info("webhook: store-review test order paid — suppressing publish/push", zap.String("order_id", order.ID.String()))
			default:
				gqlOrder := resolver.ToGQLOrder(order)
				// First time the dashboard sees this online-payment order — publish
				// orderCreated so admin clients add it to their store. CreateOrder
				// suppressed that event for online orders until payment confirmed.
				h.broker.Publish("orderCreated", gqlOrder)
				h.broker.Publish("orderUpdated", gqlOrder)
				h.broker.Publish(fmt.Sprintf("orderUpdated:%s", orderID), gqlOrder)
				// Same rationale applies to push notifications — we only wake up
				// admin phones and POS handhelds once the payment is confirmed.
				if h.notifier != nil {
					h.notifier.SendNewOrderPush(order)
				}
			}
		case paymentDomain.PaymentStatusCanceled, paymentDomain.PaymentStatusFailed, paymentDomain.PaymentStatusExpired:
			order, handleErr := h.service.HandlePaymentFailed(adminCtx, orderID)
			if handleErr != nil {
				log.Error("webhook: failed to handle failed payment", zap.String("payment_id", paymentID), zap.Error(handleErr))
				c.JSON(http.StatusInternalServerError, gin.H{"error": "temporary failure"})
				return nil
			}
			if persistErr := h.service.PersistPaymentStatus(adminCtx, paymentID, update); persistErr != nil {
				log.Error("webhook: failed to persist payment status", zap.String("payment_id", paymentID), zap.Error(persistErr))
				c.JSON(http.StatusInternalServerError, gin.H{"error": "temporary failure"})
				return nil
			}
			if order != nil {
				gqlOrder := resolver.ToGQLOrder(order)
				h.broker.Publish("orderUpdated", gqlOrder)
				h.broker.Publish(fmt.Sprintf("orderUpdated:%s", orderID), gqlOrder)
			}
		default:
			// Non-terminal status (open/pending/authorized): no business logic, just
			// persist the refreshed status + timestamps.
			if persistErr := h.service.PersistPaymentStatus(adminCtx, paymentID, update); persistErr != nil {
				log.Error("webhook: failed to persist payment status", zap.String("payment_id", paymentID), zap.Error(persistErr))
				c.JSON(http.StatusInternalServerError, gin.H{"error": "temporary failure"})
				return nil
			}
		}

		c.JSON(http.StatusOK, gin.H{"message": "processed"})
		return nil
	}); lockErr != nil {
		log.Error("webhook: failed to acquire payment lock", zap.String("payment_id", paymentID), zap.Error(lockErr))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "temporary failure"})
	}
}
