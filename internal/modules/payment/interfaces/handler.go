package interfaces

import (
	"github.com/gin-gonic/gin"
	paymentApplication "tsb-service/internal/modules/payment/application"
)

type PaymentHandler struct {
	service paymentApplication.PaymentService
}

func NewPaymentHandler(service paymentApplication.PaymentService) *PaymentHandler {
	return &PaymentHandler{service: service}
}

func (h *PaymentHandler) UpdatePaymentStatusHandler(c *gin.Context) {
	var req struct {
		ExternalMolliePaymentID string `json:"id"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "invalid request payload"})
		return
	}

	if req.ExternalMolliePaymentID == "" {
		c.JSON(400, gin.H{"error": "missing payment ID"})
		return
	}

	err := h.service.UpdatePaymentStatus(c, req.ExternalMolliePaymentID)
	if err != nil {
		c.JSON(500, gin.H{"error": "failed to update payment status"})
		return
	}

	c.JSON(200, gin.H{"message": "payment status updated successfully"})
}
