package controllers

import (
	"net/http"
	"tsb-service/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func GetDashboardProducts(c *gin.Context) {
	// Directly retrieve the language from the context since the middleware guarantees it exists
	currentUserLang := c.GetString("lang")

	// Pass the language to the model
	products, err := models.FetchDashboardProducts(currentUserLang)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, products)
}

func GetDashboardProductById(c *gin.Context) {
	// Cast the string to a UUID
	productId, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Pass the language to the model
	product, err := models.FetchDashboardProductById(productId)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, product)
}

func GetDashboardCategories(c *gin.Context) {
	categories, err := models.FetchDashboardCategories()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, categories)
}

// GetProducts returns all products grouped by category
func GetCategoriesWithProducts(c *gin.Context) {
	// Directly retrieve the language from the context since the middleware guarantees it exists
	currentUserLang := c.GetString("lang")

	// Pass the language to the model
	products, err := models.FetchProductsGroupedByCategory(currentUserLang)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, products)
}

func GetCategories(c *gin.Context) {
	currentUserLang := c.GetString("lang")

	categories, err := models.FetchCategories(currentUserLang)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, categories)
}

// Get Products by Category returns all products for a specific category
func GetProductsByCategory(c *gin.Context) {
	// Directly retrieve the language from the context since the middleware guarantees it exists
	currentUserLang := c.GetString("lang")

	// Get the category from the URL
	category, err := uuid.Parse(c.Param("category"))

	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Pass the language to the model
	products, err := models.FetchProductsByCategory(currentUserLang, category)
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
