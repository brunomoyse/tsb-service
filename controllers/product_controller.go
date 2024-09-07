package controllers

import (
	"net/http"
	"tsb-service/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
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

// UpdateProduct updates a product
func UpdateProduct(c *gin.Context) {
	var form models.UpdateProductForm
	var updatedProduct models.ProductFormResponse
	var err error

	// Bind the request body to the form
	if err := c.ShouldBindJSON(&form); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Cast the string to a UUID
	productId, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	updatedProduct, err = models.UpdateProduct(productId, form)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, updatedProduct)
}

// CreateProduct creates a new product
func CreateProduct(c *gin.Context) {
	var form models.CreateProductForm
	var newProduct models.ProductFormResponse
	var err error

	// Bind the request body to the form
	if err := c.ShouldBindJSON(&form); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	newProduct, err = models.CreateProduct(form)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, newProduct)
}
