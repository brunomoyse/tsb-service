package controllers

import (
	"log"
	"net/http"
	"tsb-service/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// CreateOrder handles the creation of a new order with a Mollie payment
func (h *Handler) CreateOrder(c *gin.Context) {
	// Get the JSON body
	var form models.CreateOrderForm

	// Check if request body is empty
	if c.Request.Body == http.NoBody {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Request body cannot be empty"})
		return
	}

	// Bind the incoming JSON request to the struct
	if err := c.ShouldBindJSON(&form); err != nil {
		// Handle invalid JSON or missing required fields
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload", "details": err.Error()})
		return
	}

	// Directly retrieve the language from the context since the middleware guarantees it exists
	currentUserLang := c.GetString("lang")

	// Get the user ID from the context (as a string)
	currentUserIdStr := c.GetString("user_id")

	// Parse the string to UUID
	currentUserId, err := uuid.Parse(currentUserIdStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user ID"})
		return
	}

	// Create the order & payment
	order, err := models.CreateOrder(h.client, form, currentUserLang, currentUserId)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Return the order and payment details
	c.JSON(http.StatusOK, order)
}

func (h *Handler) UpdatePaymentStatus(c *gin.Context) {
	// Retrieve the payment ID from the form data (since it's x-www-form-urlencoded)
	paymentID := c.PostForm("id")
	if paymentID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing payment ID"})
		return
	}

	// Get the payment details from Mollie
	_, payment, err := h.client.Payments.Get(c.Request.Context(), paymentID, nil)
	if err != nil {
		log.Printf("API call failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve payment"})
		return
	}

	// Check if the payment is "paid"
	if payment.Status == "paid" {
		// Check if there are no refunds or chargebacks
		if (payment.AmountRefunded == nil || payment.AmountRefunded.Value == "0.00") &&
			(payment.AmountChargedBack == nil || payment.AmountChargedBack.Value == "0.00") {

			log.Printf("Payment is successful for Payment ID: %v", paymentID)

			// Handle any fulfillment or next steps for a successful payment
			// For example, update your order status in the database
			err = models.UpdateOrderStatus(paymentID, payment.Status)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update order status"})
				return
			}
		}
	}

	// Respond with success
	c.JSON(http.StatusOK, gin.H{"message": "Order status updated successfully"})
}

// GetMyOrders returns all orders for the current user
func GetMyOrders(c *gin.Context) {
	// Get the user ID from the context (as a string)
	currentUserIdStr := c.GetString("user_id")

	// Parse the string to UUID
	currentUserId, err := uuid.Parse(currentUserIdStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user ID"})
		return
	}

	// Get the orders for the user
	orders, err := models.GetOrdersForUser(currentUserId)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Return the orders
	c.JSON(http.StatusOK, orders)
}

func GetOrderById(c *gin.Context) {
	// Get the order ID from the URL
	orderID := c.Param("id")

	// Parse the string to UUID
	orderUUID, err := uuid.Parse(orderID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid order ID"})
		return
	}

	// Get the order by ID
	order, err := models.GetOrderById(orderUUID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Check if order's user_id matches the current user
	currentUserIdStr := c.GetString("user_id")
	currentUserId, err := uuid.Parse(currentUserIdStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user ID"})
		return
	}

	if order.UserId != currentUserId {
		c.JSON(http.StatusForbidden, gin.H{"error": "You are not authorized to view this order"})
		return
	}

	// Return the order
	c.JSON(http.StatusOK, order)
}
