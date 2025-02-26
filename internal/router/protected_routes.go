package router

import (
	"github.com/gin-gonic/gin"

	"tsb-service/internal/middleware"
	orderHandler "tsb-service/internal/order/handler"
	productHandler "tsb-service/internal/product/handler"
)

// ProtectedRoutes groups endpoints that require authentication.
type ProtectedRoutes struct {
	orderHandler   *orderHandler.Handler
	productHandler *productHandler.Handler
}

// NewProtectedRoutes returns a new ProtectedRoutes registrar.
func NewProtectedRoutes(orderH *orderHandler.Handler, productH *productHandler.Handler) *ProtectedRoutes {
	return &ProtectedRoutes{
		orderHandler:   orderH,
		productHandler: productH,
	}
}

// RegisterRoutes registers all protected endpoints.
func (pr *ProtectedRoutes) RegisterRoutes(r *gin.Engine, jwtSecret string) {
	authorized := r.Group("/")
	authorized.Use(middleware.AuthMiddleware(jwtSecret))

	// User endpoints.
	userGroup := authorized.Group("/orders")
	{
		userGroup.POST("/", pr.orderHandler.CreateOrder)
		userGroup.GET("/", pr.orderHandler.GetMyOrders)
		userGroup.GET("/:id", pr.orderHandler.GetOrderById)
	}

	// Admin endpoints.
	adminGroup := authorized.Group("/admin")
	{
		adminGroup.GET("/categories", pr.productHandler.GetDashboardCategories)
		adminGroup.GET("/products", pr.productHandler.GetDashboardProducts)

		productGroup := adminGroup.Group("/product")
		{
			productGroup.GET("/:id", pr.productHandler.GetDashboardProductById)
			productGroup.PUT("/:id", pr.productHandler.UpdateProduct)
			productGroup.POST("/", pr.productHandler.CreateProduct)
			// productGroup.POST("/upload-image/:id", pr.productHandler.UploadImage)
		}
	}
}
