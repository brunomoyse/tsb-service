package handler

import (
	"net/http"
	model "tsb-service/internal/product"
	"tsb-service/internal/product/service"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Handler handles endpoints related to products.
type Handler struct {
	service service.ProductService
}

// NewHandler creates a new product handler with the given UserService.
func NewHandler(s service.ProductService) *Handler {
	return &Handler{service: s}
}

type ProductService interface {
	GetDashboardProducts(lang string) ([]model.DashboardProductListItem, error)
	GetDashboardProductByID(productID uuid.UUID) (model.DashboardProductDetails, error)
	GetDashboardCategories() ([]model.DashboardCategoryDetails, error)
	GetProductsGroupedByCategory(lang string) ([]model.CategoryWithProducts, error)
	GetCategories(lang string) ([]model.Category, error)
	GetProductsByCategory(lang string, categoryID uuid.UUID) ([]model.ProductInfo, error)
	UpdateProduct(productID uuid.UUID, form model.UpdateProductForm) (model.ProductFormResponse, error)
	CreateProduct(form model.CreateProductForm) (model.ProductFormResponse, error)
}

func (h *Handler) GetDashboardProducts(c *gin.Context) {
	currentUserLang := c.GetString("lang")
	products, err := h.service.GetDashboardProducts(currentUserLang)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, products)
}

func (h *Handler) GetDashboardProductById(c *gin.Context) {
	productId, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	product, err := h.service.GetDashboardProductByID(productId)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, product)
}

func (h *Handler) GetDashboardCategories(c *gin.Context) {
	categories, err := h.service.GetDashboardCategories()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, categories)
}

func (h *Handler) GetCategoriesWithProducts(c *gin.Context) {
	currentUserLang := c.GetString("lang")
	products, err := h.service.GetProductsGroupedByCategory(currentUserLang)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, products)
}

func (h *Handler) GetCategories(c *gin.Context) {
	currentUserLang := c.GetString("lang")
	categories, err := h.service.GetCategories(currentUserLang)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, categories)
}

func (h *Handler) GetProductsByCategory(c *gin.Context) {
	currentUserLang := c.GetString("lang")
	categoryID, err := uuid.Parse(c.Param("category"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	products, err := h.service.GetProductsByCategory(currentUserLang, categoryID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, products)
}

func (h *Handler) UpdateProduct(c *gin.Context) {
	var form model.UpdateProductForm
	productId, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := c.ShouldBindJSON(&form); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	updatedProduct, err := h.service.UpdateProduct(productId, form)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, updatedProduct)
}

func (h *Handler) CreateProduct(c *gin.Context) {
	var form model.CreateProductForm
	if err := c.ShouldBindJSON(&form); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	newProduct, err := h.service.CreateProduct(form)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, newProduct)
}
