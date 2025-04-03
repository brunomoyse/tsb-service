package interfaces

import (
	"github.com/gin-gonic/gin"
	"net/http"
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
		ExternalMolliePaymentID string `form:"id" binding:"required"`
	}

	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request payload"})
		return
	}

	err := h.service.UpdatePaymentStatus(c, req.ExternalMolliePaymentID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update payment status"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "payment status updated successfully"})
}
