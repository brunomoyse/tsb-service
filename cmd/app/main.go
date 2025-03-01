package main

import (
	"log"
	"net/http"
	"os"

	productApplication "tsb-service/internal/modules/product/application"
	productInfrastructure "tsb-service/internal/modules/product/infrastructure"
	productInterfaces "tsb-service/internal/modules/product/interfaces"

	orderApplication "tsb-service/internal/modules/order/application"
	orderInfrastructure "tsb-service/internal/modules/order/infrastructure"
	orderInterfaces "tsb-service/internal/modules/order/interfaces"

	userApplication "tsb-service/internal/modules/user/application"
	userInfrastructure "tsb-service/internal/modules/user/infrastructure"
	userInterfaces "tsb-service/internal/modules/user/interfaces"

	"tsb-service/internal/shared/middleware"
	"tsb-service/pkg/db"
	"tsb-service/pkg/oauth2"

	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

func main() {
	// Connect to the DB.
	dbConn, err := db.ConnectDatabase()
	if err != nil {
		log.Fatalf("Failed to connect to DB: %v", err)
	}
	defer dbConn.Close()

	// Check required environment variables.
	mollieApiKey := os.Getenv("MOLLIE_API_TOKEN")
	if mollieApiKey == "" {
		log.Fatal("MOLLIE_API_TOKEN is required")
	}
	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		log.Fatal("JWT_SECRET is required")
	}

	// Load Google OAuth credentials.
	oauth2.LoadGoogleOAuth()

	// Initialize the Mollie client.
	mollieConfig := mollie.NewAPITestingConfig(true)
	mollieClient, err := mollie.NewClient(nil, mollieConfig)
	if err != nil {
		log.Fatalf("Failed to initialize Mollie client: %v", err)
	}

	orderRepo := orderInfrastructure.NewOrderRepository(dbConn)
	orderService := orderApplication.NewOrderService(orderRepo, mollieClient)
	orderHandler := orderInterfaces.NewOrderHandler(orderService)

	productRepo := productInfrastructure.NewProductRepository(dbConn)
	productService := productApplication.NewProductService(productRepo)
	productHandler := productInterfaces.NewProductHandler(productService)

	userRepo := userInfrastructure.NewUserRepository(dbConn)
	userService := userApplication.NewUserService(userRepo)
	userHandler := userInterfaces.NewUserHandler(userService, jwtSecret)

	// Initialize Gin router
	router := gin.Default()
	router.RedirectTrailingSlash = true
	router.RedirectFixedPath = true

	appBaseUrl := os.Getenv("APP_BASE_URL")
	if appBaseUrl == "" {
		log.Fatal("APP_BASE_URL is required")
	}

	// CORS Middleware (Allow Specific Origins)
	router.Use(cors.New(cors.Config{
		AllowOrigins:     []string{appBaseUrl},
		AllowMethods:     []string{"HEAD", "GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization", "Accept-Language"},
		ExposeHeaders:    []string{"Content-Length", "Authorization"},
		AllowCredentials: true,
	}))

	// Handle CORS preflight requests (OPTIONS method)
	router.OPTIONS("/*any", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	// Health check for HEAD requests
	router.HEAD("/", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	// Setup routes (grouped by API version or module as needed)
	// Setup routes for /api/v1.
	api := router.Group("/api/v1")
	api.Use(middleware.LanguageExtractor()) // applied to all routes under /api/v1

	// Public routes
	api.GET("/products", productHandler.GetProductsHandler)
	api.GET("/products/:id", productHandler.GetProductHandler)
	api.GET("/categories", productHandler.GetCategoriesHandler)
	api.GET("/categories/:categoryID/products", productHandler.GetProductsByCategoryHandler)

	api.POST("/login", userHandler.LoginHandler)
	api.POST("/register", userHandler.RegisterHandler)

	api.GET("/oauth/google", userHandler.GoogleAuthHandler)
	api.GET("/oauth/google/callback", userHandler.GoogleAuthCallbackHandler)

	api.POST("tokens:refresh", userHandler.RefreshTokenHandler)

	// Create a subgroup for routes that require authentication.
	authGroup := api.Group("/me")
	authGroup.Use(middleware.AuthMiddleware(jwtSecret))
	{
		authGroup.GET("/orders", orderHandler.GetUserOrdersHandler)
	}

	// Create order route at /v1/orders (requires authentication).
	api.POST("/orders", middleware.AuthMiddleware(jwtSecret), orderHandler.CreateOrderHandler)

	if err := router.Run(":8080"); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
