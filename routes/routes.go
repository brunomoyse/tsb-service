package routes

import (
	"net/http"
	"tsb-service/controllers"
	"tsb-service/middleware"

	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/gin-gonic/gin"
)

func SetupRouter(client *mollie.Client, jwtSecret string) *gin.Engine {
	r := gin.Default()
	r.RedirectTrailingSlash = true
	r.RedirectFixedPath = true

	// Apply the language extractor middleware globally
	r.Use(middleware.LanguageExtractor())

	// Create a new handler that holds the Mollie client
	h := controllers.NewHandler(client)

	// Health check for HEAD requests
	r.HEAD("/", func(c *gin.Context) {
		c.Status(http.StatusOK) // Respond with 200 OK
	})

	// Define public routes (no authentication required)
	r.GET("/products-by-categories", controllers.GetCategoriesWithProducts)
	r.POST("/sign-up", controllers.SignUp)
	r.POST("/sign-in", controllers.SignIn)
	r.POST("payments/webhook", h.UpdatePaymentStatus)

	// Define the refresh token route, passing the jwtSecret
	r.POST("/refresh-token", func(c *gin.Context) {
		controllers.RefreshToken(c, jwtSecret)
	})

	// Define routes that require authentication
	authorized := r.Group("/")
	authorized.Use(middleware.AuthMiddleware(jwtSecret)) // Apply auth middleware only for this group

	// User routes
	user := authorized.Group("/user")
	{
		user.POST("/orders", h.CreateOrder)
		user.GET("/my-orders", controllers.GetMyOrders)
	}

	// Admin routes
	admin := authorized.Group("/admin")
	{
		admin.GET("/categories", controllers.GetDashboardCategories)
		admin.GET("/products", controllers.GetDashboardProducts)

		product := admin.Group("/product")
		{
			product.GET("/:id", controllers.GetDashboardProductById)
			product.PUT("/:id", controllers.UpdateProduct)
			product.POST("/:id", controllers.CreateProduct)
			product.POST("/upload-image/:id", controllers.UploadImage)
		}
	}

	return r
}
