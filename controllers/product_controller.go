package controllers

import (
	"net/http"
	"tsb-service/models"

	"github.com/gin-gonic/gin"
)

// GetProducts returns all products grouped by category
func GetProducts(c *gin.Context) {
	// Directly retrieve the language from the context since the middleware guarantees it exists
	currentUserLang := c.GetString("lang")

	// Pass the language to the model
	products, err := models.GetProductsGroupedByCategory(currentUserLang)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, products)
}
