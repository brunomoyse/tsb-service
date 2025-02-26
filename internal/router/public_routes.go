package router

import (
	"github.com/gin-gonic/gin"

	orderHandler "tsb-service/internal/order/handler"
	productHandler "tsb-service/internal/product/handler"
	userHandler "tsb-service/internal/user/handler"
)

// PublicRoutes groups all endpoints that do not require userentication.
type PublicRoutes struct {
	orderHandler   *orderHandler.Handler
	productHandler *productHandler.Handler
	userHandler    *userHandler.Handler
}

// NewPublicRoutes returns a new PublicRoutes registrar.
func NewPublicRoutes(orderH *orderHandler.Handler, productH *productHandler.Handler, userH *userHandler.Handler) *PublicRoutes {
	return &PublicRoutes{
		orderHandler:   orderH,
		productHandler: productH,
		userHandler:    userH,
	}
}

// RegisterRoutes registers all public endpoints.
func (pr *PublicRoutes) RegisterRoutes(r *gin.Engine, jwtSecret string) {
	// Product endpoints.
	r.GET("/categories", pr.productHandler.GetCategories)
	r.GET("/categories:withProducts", pr.productHandler.GetCategoriesWithProducts)
	r.GET("/categories/:category/products", pr.productHandler.GetProductsByCategory)

	r.POST("/payments/webhook", pr.orderHandler.UpdatePaymentStatus)

	// User endpoints under /auth.
	authGroup := r.Group("/auth")
	{
		authGroup.POST("/sign-up", pr.userHandler.SignUp)
		authGroup.POST("/sign-in", pr.userHandler.SignIn)
		authGroup.GET("/google/sign-in", pr.userHandler.HandleGoogleAuthLogin)
		authGroup.GET("/google/callback", pr.userHandler.HandleGoogleAuthCallback)
		authGroup.POST("/refresh", func(c *gin.Context) { pr.userHandler.RefreshToken(c) })
	}
}
