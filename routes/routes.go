package routes

import (
	"net/http"
	"tsb-service/controllers"
	"tsb-service/middleware"

	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

func SetupRouter(client *mollie.Client, jwtSecret string) *gin.Engine {
	r := gin.Default()
	r.RedirectTrailingSlash = true
	r.RedirectFixedPath = true

	// CORS Middleware (Allow Specific Origins)
	r.Use(cors.New(cors.Config{
		AllowOrigins:     []string{"http://localhost:3000", "https://nuagemagique.dev"},
		AllowMethods:     []string{"HEAD", "GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization", "Accept-Language"},
		ExposeHeaders:    []string{"Content-Length", "Authorization"},
		AllowCredentials: true,
	}))

	// Handle CORS preflight requests (OPTIONS method)
	r.OPTIONS("/*any", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	// Apply language extractor middleware
	r.Use(middleware.LanguageExtractor())

	// Create a new handler that holds the Mollie client
	h := controllers.NewHandler(client)

	// Health check for HEAD requests
	r.HEAD("/", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	// Define public routes
	r.GET("/categories", controllers.GetCategories)
	r.GET("/products-by-category/:category", controllers.GetProductsByCategory)
	r.GET("/categories-with-products", controllers.GetCategoriesWithProducts)
	r.POST("/sign-up", controllers.SignUp)
	r.POST("/sign-in", controllers.SignIn)
	r.POST("/payments/webhook", h.UpdatePaymentStatus)

	// Refresh token route
	r.POST("/refresh-token", func(c *gin.Context) {
		controllers.RefreshToken(c, jwtSecret)
	})

	// Protected Routes (Require Authentication)
	authorized := r.Group("/")
	authorized.Use(middleware.AuthMiddleware(jwtSecret))

	// User Routes
	user := authorized.Group("/user")
	{
		user.POST("/orders", h.CreateOrder)
		user.GET("/my-orders", controllers.GetMyOrders)
	}

	// Admin Routes
	admin := authorized.Group("/admin")
	{
		admin.GET("/categories", controllers.GetDashboardCategories)
		admin.GET("/products", controllers.GetDashboardProducts)

		product := admin.Group("/product")
		{
			product.GET("/:id", controllers.GetDashboardProductById)
			product.PUT("/:id", controllers.UpdateProduct)
			product.POST("/", controllers.CreateProduct)
			product.POST("/upload-image/:id", controllers.UploadImage)
		}
	}

	return r
}
