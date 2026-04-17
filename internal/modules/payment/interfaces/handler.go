package interfaces

import (
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
	// Webhook is a server-to-server call that needs write access to orders/payments.
	ctx := utils.SetIsAdmin(c.Request.Context(), true)
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

	// Verify payment exists in our DB before calling Mollie API
	payment, err := h.service.GetPaymentByExternalID(ctx, req.ExternalMolliePaymentID)
	if err != nil {
		log.Warn("webhook: unknown payment ID", zap.String("payment_id", req.ExternalMolliePaymentID), zap.Error(err))
		c.JSON(http.StatusOK, gin.H{"message": "unknown payment"})
		return
	}

	previousStatus := payment.Status

	// Fetch latest status from Mollie and update local DB (status + timestamps).
	// Returns 500 on transient failures so Mollie retries naturally.
	statusUpdate, orderID, err := h.service.UpdatePaymentStatus(ctx, req.ExternalMolliePaymentID)
	if err != nil {
		log.Error("webhook: failed to update payment status", zap.String("payment_id", req.ExternalMolliePaymentID), zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "temporary failure"})
		return
	}

	// Idempotency: if status hasn't changed, timestamps were refreshed but no business logic needed
	if statusUpdate.Status == previousStatus {
		c.JSON(http.StatusOK, gin.H{"message": "already processed"})
		return
	}

	// Delegate business logic to the service layer
	switch statusUpdate.Status {
	case paymentDomain.PaymentStatusPaid:
		order, handleErr := h.service.HandlePaymentPaid(ctx, *orderID)
		if handleErr != nil {
			log.Error("webhook: failed to handle paid payment", zap.String("payment_id", req.ExternalMolliePaymentID), zap.Error(handleErr))
		} else if order != nil {
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
		order, handleErr := h.service.HandlePaymentFailed(ctx, *orderID)
		if handleErr != nil {
			log.Error("webhook: failed to handle failed payment", zap.String("payment_id", req.ExternalMolliePaymentID), zap.Error(handleErr))
		} else if order != nil {
			gqlOrder := resolver.ToGQLOrder(order)
			h.broker.Publish("orderUpdated", gqlOrder)
			h.broker.Publish(fmt.Sprintf("orderUpdated:%s", orderID), gqlOrder)
		}
	}

	c.JSON(http.StatusOK, gin.H{"message": "processed"})
}
