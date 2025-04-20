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
	"tsb-service/pkg/pubsub"
	"tsb-service/services/email/scaleway"

	orderApplication "tsb-service/internal/modules/order/application"
	orderInfrastructure "tsb-service/internal/modules/order/infrastructure"
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

	paymentHandler := paymentInterfaces.NewPaymentHandler(paymentService, orderService, userService, productService)
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

	// Setup routes (grouped by API version or module as needed)
	// Setup routes for /api/v1.
	api := router.Group("/api/v1")

	// Health check for HEAD requests
	api.HEAD("/up", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	
	// Middleware for JWT authentication
	api.Use(middleware.LanguageExtractor())

	// Load DataLoaderMiddleware
	api.Use(
		middleware.DataLoaderMiddleware(
			addressService,
			orderService,
			paymentService,
			productService,
			userService,
		),
	)

	// For graphql subscriptions, we need to use a pubsub broker.
	broker := pubsub.NewBroker()

	// Create the GraphQL resolver with the injected services
	rootResolver := resolver.NewResolver(
		broker,
		addressService,
		orderService,
		paymentService,
		productService,
		userService,
	)

	// Create the GraphQL handler
	graphqlHandler := resolver.GraphQLHandler(rootResolver)
	// Add a middleware to store the userID in the context (extracted from JWT)
	optionalAuthMiddleware := gqlMiddleware.OptionalAuthMiddleware(jwtSecret)

	api.POST("/graphql", optionalAuthMiddleware, graphqlHandler)
	// Force auth check on ws handshake
	api.GET("/graphql", middleware.AuthMiddleware(jwtSecret), graphqlHandler)

	// Mollie webhook
	api.POST("payments/webhook", paymentHandler.UpdatePaymentStatusHandler)

	// Auth routes
	api.POST("/login", userHandler.LoginHandler)
	api.POST("/register", userHandler.RegisterHandler)
	api.GET("/verify", userHandler.VerifyEmailHandler)

	api.POST("/tokens/refresh", userHandler.RefreshTokenHandler)
	api.GET("/tokens/revoke", userHandler.LogoutHandler)

	// Google OAuth
	api.GET("/oauth/google", userHandler.GoogleAuthHandler)
	api.GET("/oauth/google/callback", userHandler.GoogleAuthCallbackHandler)

	if err := router.Run(":8080"); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
