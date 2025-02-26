package handler

import (
	"log"
	"net/http"

	"tsb-service/internal/order"
	"tsb-service/internal/order/service"

	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Handler handles order endpoints, including payment-related actions.
type Handler struct {
	service service.OrderService
	client  *mollie.Client
}

// NewHandler creates a new order handler with the provided OrderService and Mollie client.
func NewHandler(s service.OrderService, client *mollie.Client) *Handler {
	return &Handler{service: s, client: client}
}

// CreateOrder handles the creation of a new order with a Mollie payment.
func (h *Handler) CreateOrder(c *gin.Context) {
	var form order.CreateOrderForm

	// Check for empty request body.
	if c.Request.Body == http.NoBody {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Request body cannot be empty"})
		return
	}

	// Bind the JSON request to the order form.
	if err := c.ShouldBindJSON(&form); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload", "details": err.Error()})
		return
	}

	// Retrieve language and user ID from context (set by middleware).
	currentUserLang := c.GetString("lang")
	currentUserIdStr := c.GetString("user_id")
	currentUserId, err := uuid.Parse(currentUserIdStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user ID"})
		return
	}

	// Create the order using the OrderService.
	ord, err := h.service.CreateOrder(c.Request.Context(), h.client, form, currentUserLang, currentUserId)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Return the newly created order.
	c.JSON(http.StatusOK, ord)
}

// UpdatePaymentStatus handles updating an order's status based on payment details from Mollie.
func (h *Handler) UpdatePaymentStatus(c *gin.Context) {
	// Retrieve the payment ID from the form data.
	paymentID := c.PostForm("id")
	if paymentID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing payment ID"})
		return
	}

	// Retrieve payment details from Mollie.
	_, payment, err := h.client.Payments.Get(c.Request.Context(), paymentID, nil)
	if err != nil {
		log.Printf("API call failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve payment"})
		return
	}

	// If payment is marked as paid and there are no refunds or chargebacks, update order status.
	if payment.Status == "paid" {
		if (payment.AmountRefunded == nil || payment.AmountRefunded.Value == "0.00") &&
			(payment.AmountChargedBack == nil || payment.AmountChargedBack.Value == "0.00") {

			log.Printf("Payment is successful for Payment ID: %v", paymentID)
			err = h.service.UpdateOrderStatus(c.Request.Context(), paymentID, payment.Status)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update order status"})
				return
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{"message": "Order status updated successfully"})
}

// GetMyOrders returns all orders for the current user.
func (h *Handler) GetMyOrders(c *gin.Context) {
	currentUserIdStr := c.GetString("user_id")
	currentUserId, err := uuid.Parse(currentUserIdStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user ID"})
		return
	}

	orders, err := h.service.GetOrdersForUser(c.Request.Context(), currentUserId)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, orders)
}

// GetOrderById returns a specific order by its ID if the current user is authorized to view it.
func (h *Handler) GetOrderById(c *gin.Context) {
	orderIDStr := c.Param("id")
	orderUUID, err := uuid.Parse(orderIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid order ID"})
		return
	}

	ord, err := h.service.GetOrderById(c.Request.Context(), orderUUID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Verify that the order belongs to the current user.
	currentUserIdStr := c.GetString("user_id")
	currentUserId, err := uuid.Parse(currentUserIdStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user ID"})
		return
	}

	if ord.UserId != currentUserId {
		c.JSON(http.StatusForbidden, gin.H{"error": "You are not authorized to view this order"})
		return
	}

	c.JSON(http.StatusOK, ord)
}
