package interfaces

import (
	"encoding/json"
	"github.com/google/uuid"
	"net/http"
	"tsb-service/internal/modules/product/domain"

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

// UpdateProductHandler handles partial product updates.
func (h *ProductHandler) UpdateProductHandler(c *gin.Context) {
	// Get product id from URL parameter.
	idStr := c.Param("id")
	productID, err := uuid.Parse(idStr)
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid product id"})
		return
	}

	// Retrieve the current product.
	currentProduct, err := h.service.GetProduct(c.Request.Context(), productID.String())
	if err != nil {
		c.JSON(500, gin.H{"error": "failed to retrieve product"})
		return
	}

	// Bind the JSON payload to our update request.
	var req UpdateProductRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "invalid request body"})
		return
	}

	// Merge the update values if provided.
	if req.Price != nil {
		currentProduct.Price = *req.Price
	}
	if req.Code != nil {
		currentProduct.Code = req.Code
	}
	if req.PieceCount != nil {
		currentProduct.PieceCount = req.PieceCount
	}
	if req.IsVisible != nil {
		currentProduct.IsVisible = *req.IsVisible
	}
	if req.IsAvailable != nil {
		currentProduct.IsAvailable = *req.IsAvailable
	}
	if req.IsHalal != nil {
		currentProduct.IsHalal = *req.IsHalal
	}
	if req.IsVegan != nil {
		currentProduct.IsVegan = *req.IsVegan
	}
	if req.Translations != nil {
		var translations []domain.Translation
		for _, t := range *req.Translations {
			translations = append(translations, domain.Translation{
				Language:    t.Language,
				Name:        t.Name,
				Description: t.Description,
			})
		}
		// Replace translations only if provided.
		currentProduct.Translations = translations
	}

	// Call the service layer to update the product.
	if err := h.service.UpdateProduct(c.Request.Context(), currentProduct); err != nil {
		c.JSON(500, gin.H{"error": "failed to update product"})
		return
	}

	// Retrieve the updated product
	updatedProduct, err := h.service.GetProduct(c.Request.Context(), productID.String())
	if err != nil {
		c.JSON(500, gin.H{"error": "failed to retrieve updated product"})
		return
	}

	// Create a response DTO from the product domain object.
	res := NewAdminProductResponse(updatedProduct)

	c.JSON(200, res)
}
