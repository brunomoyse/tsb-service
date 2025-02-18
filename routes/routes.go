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
	r.POST("/payments/webhook", h.UpdatePaymentStatus)

	// Group all `/auth` routes together
	authGroup := r.Group("/auth")
	{
		authGroup.POST("/sign-up", controllers.SignUp)
		authGroup.POST("/sign-in", controllers.SignIn)
		authGroup.GET("/google/sign-in", controllers.HandleGoogleAuthLogin)
		authGroup.GET("/google/callback", controllers.HandleGoogleAuthCallback)
		authGroup.POST("/refresh", func(c *gin.Context) {
			controllers.RefreshTokenHandler(c, jwtSecret)
		})
	}

	// Protected Routes (Require Authentication)
	authorized := r.Group("/")
	authorized.Use(middleware.AuthMiddleware(jwtSecret))

	// User Routes
	user := authorized.Group("/user")
	{
		user.POST("/orders", h.CreateOrder)
		user.GET("/my-orders", controllers.GetMyOrders)
		user.GET("/order/:id", controllers.GetOrderById)
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
