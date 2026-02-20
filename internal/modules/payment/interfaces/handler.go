package interfaces

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"tsb-service/internal/api/graphql/resolver"
	orderApplication "tsb-service/internal/modules/order/application"
	"tsb-service/internal/modules/order/domain"
	paymentApplication "tsb-service/internal/modules/payment/application"
	productApplication "tsb-service/internal/modules/product/application"
	productDomain "tsb-service/internal/modules/product/domain"
	userApplication "tsb-service/internal/modules/user/application"
	"tsb-service/pkg/pubsub"
	es "tsb-service/services/email/scaleway"
)

type PaymentHandler struct {
	service        paymentApplication.PaymentService
	orderService   orderApplication.OrderService
	userService    userApplication.UserService
	productService productApplication.ProductService
	broker         *pubsub.Broker
}

func NewPaymentHandler(
	service paymentApplication.PaymentService,
	orderService orderApplication.OrderService,
	userService userApplication.UserService,
	productService productApplication.ProductService,
	broker *pubsub.Broker,
) *PaymentHandler {
	return &PaymentHandler{
		service:        service,
		orderService:   orderService,
		userService:    userService,
		productService: productService,
		broker:         broker,
	}
}

func (h *PaymentHandler) UpdatePaymentStatusHandler(c *gin.Context) {
	ctx := c.Request.Context()

	var req struct {
		ExternalMolliePaymentID string `form:"id" binding:"required"`
	}

	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request payload"})
		return
	}

	// Validate Mollie payment ID format
	if !strings.HasPrefix(req.ExternalMolliePaymentID, "tr_") {
		slog.WarnContext(ctx, "webhook: invalid payment ID format", "component", "webhook", "payment_id", req.ExternalMolliePaymentID)
		c.JSON(http.StatusOK, gin.H{"message": "ignored"})
		return
	}

	// Verify payment exists in our DB before calling Mollie API
	payment, err := h.service.GetPaymentByExternalID(ctx, req.ExternalMolliePaymentID)
	if err != nil {
		slog.WarnContext(ctx, "webhook: unknown payment ID", "component", "webhook", "payment_id", req.ExternalMolliePaymentID, "error", err)
		c.JSON(http.StatusOK, gin.H{"message": "unknown payment"})
		return
	}

	// Fetch the external payment status from Mollie
	_, externalPayment, err := h.service.GetExternalPaymentByID(ctx, req.ExternalMolliePaymentID)
	if err != nil {
		slog.ErrorContext(ctx, "webhook: failed to retrieve external payment", "component", "webhook", "payment_id", req.ExternalMolliePaymentID, "error", err)
		c.JSON(http.StatusOK, gin.H{"message": "processed"})
		return
	}

	// Idempotency check: if local status already matches Mollie status, skip processing
	if string(payment.Status) == externalPayment.Status {
		c.JSON(http.StatusOK, gin.H{"message": "already processed"})
		return
	}

	err = h.service.UpdatePaymentStatus(c, req.ExternalMolliePaymentID)
	if err != nil {
		slog.ErrorContext(ctx, "webhook: failed to update payment status", "component", "webhook", "payment_id", req.ExternalMolliePaymentID, "error", err)
		c.JSON(http.StatusOK, gin.H{"message": "processed"})
		return
	}

	if externalPayment.Status == "paid" {

		order, orderProducts, err := h.orderService.GetOrderByID(ctx, payment.OrderID)
		if err != nil {
			slog.ErrorContext(ctx, "webhook: failed to retrieve order", "component", "webhook", "payment_id", req.ExternalMolliePaymentID, "error", err)
			c.JSON(http.StatusOK, gin.H{"message": "processed"})
			return
		}
		if order == nil {
			slog.ErrorContext(ctx, "webhook: order not found for payment", "component", "webhook", "payment_id", req.ExternalMolliePaymentID)
			c.JSON(http.StatusOK, gin.H{"message": "processed"})
			return
		}
		if orderProducts == nil {
			slog.ErrorContext(ctx, "webhook: no order products found", "component", "webhook", "payment_id", req.ExternalMolliePaymentID)
			c.JSON(http.StatusOK, gin.H{"message": "processed"})
			return
		}

		// 3. Load product details for the products in the order.
		productIDs := make([]string, len(*orderProducts))
		for i, op := range *orderProducts {
			productIDs[i] = op.ProductID.String()
		}

		products, err := h.productService.GetProductsByIDs(ctx, productIDs)
		if err != nil {
			slog.ErrorContext(ctx, "webhook: failed to retrieve products", "component", "webhook", "payment_id", req.ExternalMolliePaymentID, "error", err)
			c.JSON(http.StatusOK, gin.H{"message": "processed"})
			return
		}

		// Build a lookup map: productID -> product details.
		productMap := make(map[uuid.UUID]productDomain.ProductOrderDetails, len(products))
		for _, p := range products {
			productMap[p.ID] = *p
		}

		// 4. Enrich order products with product details.
		orderProductsResponse := make([]domain.OrderProduct, len(*orderProducts))
		for i, op := range *orderProducts {
			prod, ok := productMap[op.ProductID]
			if !ok {
				slog.ErrorContext(ctx, "webhook: product not found", "component", "webhook", "payment_id", req.ExternalMolliePaymentID, "product_id", op.ProductID)
				c.JSON(http.StatusOK, gin.H{"message": "processed"})
				return
			}
			orderProductsResponse[i] = domain.OrderProduct{
				Product: domain.Product{
					ID:           prod.ID,
					Code:         prod.Code,
					CategoryName: prod.CategoryName,
					Name:         prod.Name,
				},
				Quantity:   op.Quantity,
				UnitPrice:  op.UnitPrice,
				TotalPrice: op.TotalPrice,
			}
		}

		u, err := h.userService.GetUserByID(ctx, order.UserID.String())

		if err != nil {
			slog.ErrorContext(ctx, "webhook: failed to retrieve user", "component", "webhook", "payment_id", req.ExternalMolliePaymentID, "error", err)
			c.JSON(http.StatusOK, gin.H{"message": "processed"})
			return
		}

		h.broker.Publish("orderUpdated", resolver.ToGQLOrder(order))

		err = es.SendOrderPendingEmail(*u, "fr", *order, orderProductsResponse)
		if err != nil {
			slog.ErrorContext(ctx, "webhook: failed to send order pending email", "component", "webhook", "payment_id", req.ExternalMolliePaymentID, "error", err)
		}
	} else if externalPayment.Status == "cancelled" || externalPayment.Status == "failed" || externalPayment.Status == "expired" {
		slog.InfoContext(ctx, "webhook: payment not paid", "component", "webhook", "payment_id", req.ExternalMolliePaymentID, "status", externalPayment.Status)
		canceledStatus := domain.OrderStatusCanceled

		err = h.orderService.UpdateOrder(ctx, payment.OrderID, &canceledStatus, nil)
		if err != nil {
			slog.ErrorContext(ctx, "webhook: failed to update order status", "component", "webhook", "payment_id", req.ExternalMolliePaymentID, "error", err)
			c.JSON(http.StatusOK, gin.H{"message": "processed"})
			return
		}

		// Send payment failed email
		go func() {
			order, _, orderErr := h.orderService.GetOrderByID(ctx, payment.OrderID)
			if orderErr != nil || order == nil {
				slog.ErrorContext(ctx, "webhook: failed to retrieve order for payment failed email", "component", "webhook", "payment_id", req.ExternalMolliePaymentID, "error", orderErr)
				return
			}

			u, userErr := h.userService.GetUserByID(ctx, order.UserID.String())
			if userErr != nil {
				slog.ErrorContext(ctx, "webhook: failed to retrieve user for payment failed email", "component", "webhook", "payment_id", req.ExternalMolliePaymentID, "error", userErr)
				return
			}

			if emailErr := es.SendPaymentFailedEmail(*u, "fr"); emailErr != nil {
				slog.ErrorContext(ctx, "webhook: failed to send payment failed email", "component", "webhook", "payment_id", req.ExternalMolliePaymentID, "error", emailErr)
			}
		}()
	}

	c.JSON(http.StatusOK, gin.H{"message": "payment status updated successfully"})
}
