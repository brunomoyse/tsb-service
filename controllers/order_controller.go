package controllers

import (
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
