package main

import (
	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"log"
	"net/http"
	"os"
	productApplication "tsb-service/internal/modules/product/application"
	productInfrastructure "tsb-service/internal/modules/product/infrastructure"
	productInterfaces "tsb-service/internal/modules/product/interfaces"
	"tsb-service/pkg/sse"

	orderApplication "tsb-service/internal/modules/order/application"
	orderInfrastructure "tsb-service/internal/modules/order/infrastructure"
	orderInterfaces "tsb-service/internal/modules/order/interfaces"

	paymentApplication "tsb-service/internal/modules/payment/application"
	paymentInfrastructure "tsb-service/internal/modules/payment/infrastructure"
	paymentInterfaces "tsb-service/internal/modules/payment/interfaces"

	userApplication "tsb-service/internal/modules/user/application"
	userInfrastructure "tsb-service/internal/modules/user/infrastructure"
	userInterfaces "tsb-service/internal/modules/user/interfaces"

	addressApplication "tsb-service/internal/modules/address/application"
	addressInfrastructure "tsb-service/internal/modules/address/infrastructure"
	addressInterfaces "tsb-service/internal/modules/address/interfaces"

	"tsb-service/internal/shared/middleware"
	"tsb-service/pkg/db"
	"tsb-service/pkg/oauth2"

	// "github.com/VictorAvelar/mollie-api-go/v4/mollie"
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

	productRepo := productInfrastructure.NewProductRepository(dbConn)
	productService := productApplication.NewProductService(productRepo)
	productHandler := productInterfaces.NewProductHandler(productService)

	paymentRepo := paymentInfrastructure.NewPaymentRepository(dbConn)
	paymentService := paymentApplication.NewPaymentService(paymentRepo, *mollieClient)
	paymentHandler := paymentInterfaces.NewPaymentHandler(paymentService)

	orderRepo := orderInfrastructure.NewOrderRepository(dbConn)
	orderService := orderApplication.NewOrderService(orderRepo)
	orderHandler := orderInterfaces.NewOrderHandler(orderService, productService, paymentService)

	userRepo := userInfrastructure.NewUserRepository(dbConn)
	userService := userApplication.NewUserService(userRepo)
	userHandler := userInterfaces.NewUserHandler(userService, jwtSecret)

	addressRepo := addressInfrastructure.NewAddressRepository(dbConn)
	addressService := addressApplication.NewAddressService(addressRepo)
	addressHandler := addressInterfaces.NewAddressHandler(addressService)

	// Initialize Gin router
	router := gin.Default()
	router.RedirectTrailingSlash = true
	router.RedirectFixedPath = true

	appBaseUrl := os.Getenv("APP_BASE_URL")
	if appBaseUrl == "" {
		log.Fatal("APP_BASE_URL is required")
	}

	appDashboardUrl := os.Getenv("APP_DASHBOARD_URL")
	if appDashboardUrl == "" {
		log.Fatal("APP_DASHBOARD_URL is required")
	}

	// CORS Middleware (Allow Specific Origins)
	router.Use(cors.New(cors.Config{
		AllowOrigins:     []string{appBaseUrl, appDashboardUrl},
		AllowMethods:     []string{"HEAD", "GET", "POST", "PATCH", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization", "Accept-Language"},
		ExposeHeaders:    []string{"Content-Length", "Authorization"},
		AllowCredentials: true,
	}))

	// Handle CORS preflight requests (OPTIONS method)
	router.OPTIONS("/*any", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	// Setup routes (grouped by API version or module as needed)
	// Setup routes for /api/v1.
	api := router.Group("/api/v1")

	// Health check for HEAD requests
	api.HEAD("/up", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	api.Use(middleware.LanguageExtractor()) // applied to all routes under /api/v1

	api.POST("payments/webhook", paymentHandler.UpdatePaymentStatusHandler)

	// Register the SSE endpoint.
	// Since SSE is just HTTP, we can mount it using gin.WrapH.
	api.GET("/sse", gin.WrapH(sse.Hub))

	//
	// PUBLIC ROUTES
	//
	api.GET("/products", productHandler.GetProductsHandler)
	api.GET("/products/:id", productHandler.GetProductHandler)
	api.GET("/categories", productHandler.GetCategoriesHandler)
	api.GET("/categories/:categoryID/products", productHandler.GetProductsByCategoryHandler)

	api.GET("/addresses/streets", addressHandler.GetStreetNamesHandler)
	api.GET("/addresses/house-numbers", addressHandler.GetHouseNumbersHandler)
	api.GET("/addresses/box-numbers", addressHandler.GetBoxNumbersHandler)
	api.GET("/addresses/final-address", addressHandler.GetFinalAddressHandler)
	api.GET("/addresses/:id", addressHandler.GetAddressByIDHandler)

	api.POST("/login", userHandler.LoginHandler)
	api.POST("/register", userHandler.RegisterHandler)

	api.GET("/oauth/google", userHandler.GoogleAuthHandler)
	api.GET("/oauth/google/callback", userHandler.GoogleAuthCallbackHandler)

	api.POST("/tokens/refresh", userHandler.RefreshTokenHandler)
	api.GET("/tokens/revoke", userHandler.LogoutHandler)

	//
	// AUTHENTICATED ROUTES
	//
	api.GET("/me", middleware.AuthMiddleware(jwtSecret), userHandler.GetUserProfileHandler)
	api.PATCH("/me", middleware.AuthMiddleware(jwtSecret), userHandler.UpdateMeHandler)
	api.GET("/me/orders", middleware.AuthMiddleware(jwtSecret), orderHandler.GetUserOrdersHandler)
	api.GET("/orders/:id", middleware.AuthMiddleware(jwtSecret), orderHandler.GetOrderHandler)
	api.POST("/orders", middleware.AuthMiddleware(jwtSecret), orderHandler.CreateOrderHandler)

	// Admin routes
	api.GET("/admin/products", middleware.AuthMiddleware(jwtSecret), productHandler.GetAdminProductsHandler)
	api.POST("/admin/products", middleware.AuthMiddleware(jwtSecret), productHandler.CreateProductHandler)
	api.PUT("/admin/products/:id", middleware.AuthMiddleware(jwtSecret), productHandler.UpdateProductHandler)

	api.GET("/admin/orders", middleware.AuthMiddleware(jwtSecret), orderHandler.GetAdminOrdersHandler)
	api.GET("/admin/orders/:id", middleware.AuthMiddleware(jwtSecret), orderHandler.GetAdminOrderHandler)
	api.PATCH("/admin/orders/:id", middleware.AuthMiddleware(jwtSecret), orderHandler.UpdateOrderStatusHandler)

	if err := router.Run(":8080"); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
