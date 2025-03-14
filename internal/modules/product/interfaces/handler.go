package interfaces

import (
	"encoding/json"
	"net/http"

	"tsb-service/internal/modules/product/application"

	"github.com/gin-gonic/gin"
)

// ProductHandler handles HTTP requests for product operations.
type ProductHandler struct {
	service application.ProductService
}

// NewProductHandler creates a new ProductHandler with the given service.
func NewProductHandler(service application.ProductService) *ProductHandler {
	return &ProductHandler{service: service}
}

// CreateProductHandler handles the HTTP POST request for creating a product.
func (h *ProductHandler) CreateProductHandler(c *gin.Context) {
	// Decode the incoming JSON payload into a CreateProductForm DTO.
	var req CreateProductRequest
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"invalid request payload": err.Error()})
	}

	// Call the application service to create a new product.
	product, err := h.service.CreateProduct(
		c.Request.Context(),
		req.CategoryID,
		req.Price,
		req.Code,
		req.IsVisible,
		req.IsAvailable,
		req.IsHalal,
		req.IsVegan,
		req.Translations,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"failed to create product": err.Error()})
		return
	}

	// Create a response DTO from the product domain object.
	res := NewAdminProductResponse(product)

	c.JSON(http.StatusOK, res)

}

func (h *ProductHandler) GetProductHandler(c *gin.Context) {
	// Retrieve product by ID (omitting error handling for brevity)
	product, _ := h.service.GetProduct(c.Request.Context(), c.Param("id"))

	userLocale := c.GetString("lang")
	if userLocale == "" {
		userLocale = "fr"
	}

	// Build your response DTO including only the chosen translation.
	res := NewPublicProductResponse(product, userLocale)

	c.JSON(http.StatusOK, res)
}

func (h *ProductHandler) GetProductsHandler(c *gin.Context) {
	// Retrieve all products (omitting error handling for brevity)
	products, err := h.service.GetProducts(c.Request.Context())

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Assume you extract the user's preferred locale from the request header.
	userLocale := c.GetHeader("Accept-Language")

	// Build your response DTOs including only the chosen translations.
	var res []PublicProductResponse
	for _, p := range products {
		res = append(res, *NewPublicProductResponse(p, userLocale))
	}

	c.JSON(http.StatusOK, res)
}

func (h *ProductHandler) GetAdminProductsHandler(c *gin.Context) {
	products, err := h.service.GetProducts(c.Request.Context())

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, products)
}

// GetCategories retrieves a list of categories.
func (h *ProductHandler) GetCategoriesHandler(c *gin.Context) {
	// Retrieve all categories (omitting error handling for brevity)
	categories, err := h.service.GetCategories(c.Request.Context())

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Assume you extract the user's preferred locale from the request header.
	userLocale := c.GetHeader("Accept-Language")

	// Build your response DTOs including only the chosen translations.
	var res []PublicCategoryResponse
	for _, c := range categories {
		res = append(res, *NewPublicCategoryResponse(c, userLocale))
	}

	c.JSON(http.StatusOK, res)
}

// GetProductsByCategoryHandler retrieves a list of products for a given category.
func (h *ProductHandler) GetProductsByCategoryHandler(c *gin.Context) {
	// Retrieve products by category ID (omitting error handling for brevity)
	categoryID := c.Param("categoryID")
	products, err := h.service.GetProductsByCategory(c.Request.Context(), categoryID)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Assume you extract the user's preferred locale from the request header.
	userLocale := c.GetHeader("Accept-Language")

	// Build your response DTOs including only the chosen translations.
	var res []PublicProductResponse
	for _, p := range products {
		res = append(res, *NewPublicProductResponse(p, userLocale))
	}

	c.JSON(http.StatusOK, res)
}
