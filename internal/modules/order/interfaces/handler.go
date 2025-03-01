package interfaces

import (
	"encoding/json"
	"net/http"
	"tsb-service/internal/modules/order/application"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type OrderHandler struct {
	service application.OrderService
}

func NewOrderHandler(service application.OrderService) *OrderHandler {
	return &OrderHandler{service: service}
}

func (h *OrderHandler) CreateOrderHandler(c *gin.Context) {
	var req CreateOrderForm
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"invalid request payload": err.Error()})
	}

	// Retrieve the logged-in user's ID from the Gin context.
	userIDStr := c.GetString("userID")
	if userIDStr == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user not authenticated"})
		return
	}

	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "invalid user ID"})
		return
	}

	order, err := h.service.CreateOrder(
		c.Request.Context(),
		userID,
		req.ProductsLines,
		req.PaymentMode,
	)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"failed to create order": err.Error()})
		return
	}

	c.JSON(http.StatusOK, order)
}

// GetUserOrders handles GET requests to retrieve orders for the logged-in user.
func (h *OrderHandler) GetUserOrdersHandler(c *gin.Context) {
	// Retrieve the logged-in user's ID from the Gin context.
	userIDStr := c.GetString("userID")
	if userIDStr == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "handler: user not authenticated"})
		return
	}

	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "invalid user ID"})
		return
	}

	// Call the order service to fetch orders for this user.
	orders, err := h.service.GetOrdersByUserID(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to retrieve orders"})
		return
	}

	// Return the orders as JSON.
	c.JSON(http.StatusOK, orders)
}
