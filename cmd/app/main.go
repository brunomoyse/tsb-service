package main

import (
	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"log"
	"net/http"
	"os"
	gqlMiddleware "tsb-service/internal/api/graphql/middleware"
	"tsb-service/internal/api/graphql/resolver"
	productApplication "tsb-service/internal/modules/product/application"
	productInfrastructure "tsb-service/internal/modules/product/infrastructure"
	productInterfaces "tsb-service/internal/modules/product/interfaces"
	"tsb-service/pkg/sse"
	"tsb-service/services/email/scaleway"

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

	// Init mail service
	err = scaleway.InitService()
	if err != nil {
		log.Fatalf("Failed to initialize email service: %v", err)
	}

	// Load Google OAuth credentials.
	oauth2.LoadGoogleOAuth()

	// Initialize the Mollie client.
	mollieConfig := mollie.NewAPITestingConfig(true)
	mollieClient, err := mollie.NewClient(nil, mollieConfig)
	if err != nil {
		log.Fatalf("Failed to initialize Mollie client: %v", err)
	}

	addressRepo := addressInfrastructure.NewAddressRepository(dbConn)
	orderRepo := orderInfrastructure.NewOrderRepository(dbConn)
	paymentRepo := paymentInfrastructure.NewPaymentRepository(dbConn)
	productRepo := productInfrastructure.NewProductRepository(dbConn)
	userRepo := userInfrastructure.NewUserRepository(dbConn)

	addressService := addressApplication.NewAddressService(addressRepo)
	orderService := orderApplication.NewOrderService(orderRepo)
	paymentService := paymentApplication.NewPaymentService(paymentRepo, *mollieClient)
	productService := productApplication.NewProductService(productRepo)
	userService := userApplication.NewUserService(userRepo)

	orderHandler := orderInterfaces.NewOrderHandler(orderService, productService, paymentService, addressService, userService)
	paymentHandler := paymentInterfaces.NewPaymentHandler(paymentService, orderService, userService, productService)
	productHandler := productInterfaces.NewProductHandler(productService)
	userHandler := userInterfaces.NewUserHandler(userService, addressService, jwtSecret)

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

	// Middleware for JWT authentication
	router.Use(middleware.LanguageExtractor())

	// Load DataLoaderMiddleware
	router.Use(
		middleware.DataLoaderMiddleware(
			addressService,
			orderService,
			paymentService,
			productService,
			userService,
		),
	)

	// Create the GraphQL resolver with the injected services
	rootResolver := resolver.NewResolver(
		addressService,
		orderService,
		paymentService,
		productService,
		userService,
	)

	// Create your GraphQL handler (using your favorite GraphQL library)
	graphqlHandler := resolver.GraphQLHandler(rootResolver)
	// Add a middleware to store the userID in the context (extracted from JWT)
	optionalAuthMiddleware := gqlMiddleware.OptionalAuthMiddleware(jwtSecret)

	router.POST("/graphql", optionalAuthMiddleware, graphqlHandler)

	// Setup routes (grouped by API version or module as needed)
	// Setup routes for /api/v1.
	api := router.Group("/api/v1")

	// Health check for HEAD requests
	api.HEAD("/up", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	api.POST("payments/webhook", paymentHandler.UpdatePaymentStatusHandler)

	// Register the SSE endpoint.
	// Since SSE is just HTTP, we can mount it using gin.WrapH.
	api.GET("/sse", gin.WrapH(sse.Hub))

	//
	// PUBLIC ROUTES
	//
	api.POST("/login", userHandler.LoginHandler)
	api.POST("/register", userHandler.RegisterHandler)
	api.GET("/verify", userHandler.VerifyEmailHandler)

	api.GET("/oauth/google", userHandler.GoogleAuthHandler)
	api.GET("/oauth/google/callback", userHandler.GoogleAuthCallbackHandler)

	api.POST("/tokens/refresh", userHandler.RefreshTokenHandler)
	api.GET("/tokens/revoke", userHandler.LogoutHandler)

	//
	// AUTHENTICATED ROUTES
	//
	api.PATCH("/me", middleware.AuthMiddleware(jwtSecret), userHandler.UpdateMeHandler)
	api.GET("/orders/:id", middleware.AuthMiddleware(jwtSecret), orderHandler.GetOrderHandler)
	api.POST("/orders", middleware.AuthMiddleware(jwtSecret), orderHandler.CreateOrderHandler)

	// Admin routes
	api.GET("/admin/products", middleware.AuthMiddleware(jwtSecret), productHandler.GetAdminProductsHandler)
	api.POST("/admin/products", middleware.AuthMiddleware(jwtSecret), productHandler.CreateProductHandler)
	api.PUT("/admin/products/:id", middleware.AuthMiddleware(jwtSecret), productHandler.UpdateProductHandler)

	api.PATCH("/admin/orders/:id", middleware.AuthMiddleware(jwtSecret), orderHandler.UpdateOrderStatusHandler)

	if err := router.Run(":8080"); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
