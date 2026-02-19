package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"

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
)

func main() {
	// Load .env file
	if err := godotenv.Load(); err != nil {
		panic("Warning: .env file not found")
	}

	// DB connection
	dbConn, err := db.ConnectDatabase()
	if err != nil {
		log.Fatalf("Failed to connect to DB: %v", err)
	}
	defer dbConn.Close()

	// PubSub broker (used by GraphQL subscriptions)
	broker := pubsub.NewBroker()

	// ENV checks & third-party setup
	mollieApiKey := os.Getenv("MOLLIE_API_TOKEN")
	if mollieApiKey == "" {
		log.Fatal("MOLLIE_API_TOKEN is required")
	}
	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		log.Fatal("JWT_SECRET is required")
	}

	if err := scaleway.InitService(); err != nil {
		log.Fatalf("Failed to initialize email service: %v", err)
	}
	oauth2.LoadGoogleOAuth()

	mollieTesting := os.Getenv("MOLLIE_TESTING") != "false"
	var mollieCfg *mollie.Config
	if mollieTesting {
		mollieCfg = mollie.NewAPITestingConfig(true)
		log.Println("Mollie client initialized in TESTING mode")
	} else {
		mollieCfg = mollie.NewAPIConfig(true)
		log.Println("Mollie client initialized in PRODUCTION mode")
	}
	mollieClient, err := mollie.NewClient(nil, mollieCfg)
	if err != nil {
		log.Fatalf("Failed to initialize Mollie client: %v", err)
	}

	// Repos / services / handlers
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

	paymentHandler := paymentInterfaces.NewPaymentHandler(paymentService, orderService, userService, productService, broker)
	userHandler := userInterfaces.NewUserHandler(userService, addressService, jwtSecret)

	// Gin HTTP setup
	router := gin.Default()
	router.RedirectTrailingSlash = true
	router.RedirectFixedPath = true

	appBaseURL := os.Getenv("APP_BASE_URL")
	appDashboardURL := os.Getenv("APP_DASHBOARD_URL")
	if appBaseURL == "" || appDashboardURL == "" {
		log.Fatal("APP_BASE_URL and APP_DASHBOARD_URL are required")
	}

	router.Use(cors.New(cors.Config{
		AllowOrigins:     []string{appBaseURL, appDashboardURL},
		AllowMethods:     []string{"HEAD", "GET", "POST", "PATCH", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization", "Accept-Language"},
		ExposeHeaders:    []string{"Content-Length", "Authorization"},
		AllowCredentials: true,
	}))
	router.OPTIONS("/*any", func(c *gin.Context) { c.Status(http.StatusOK) })

	// API routes
	api := router.Group("/api/v1")
	healthCheck := func(c *gin.Context) {
		if err := dbConn.Ping(); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unhealthy", "db": "unreachable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	}
	api.HEAD("/up", healthCheck)
	api.GET("/up", healthCheck)
	api.Use(middleware.LanguageExtractor())
	api.Use(middleware.DataLoaderMiddleware(
		addressService, orderService, paymentService, productService, userService,
	))

	// GraphQL
	rootResolver := resolver.NewResolver(
		broker,
		addressService, orderService, paymentService, productService, userService,
	)
	graphqlHandler := resolver.GraphQLHandler(rootResolver, []string{appBaseURL, appDashboardURL})
	optionalAuth := gqlMiddleware.OptionalAuthMiddleware(jwtSecret)

	api.POST("/graphql", optionalAuth, graphqlHandler)
	api.GET("/graphql", middleware.AuthMiddleware(jwtSecret), graphqlHandler)

	// Other endpoints
	api.POST("/payments/webhook", paymentHandler.UpdatePaymentStatusHandler)
	authLimiter := middleware.NewRateLimiter(5.0/60, 5) // 5 req/min per IP
	api.POST("/login", authLimiter.Middleware(), userHandler.LoginHandler)
	api.POST("/register", authLimiter.Middleware(), userHandler.RegisterHandler)
	api.GET("/verify", userHandler.VerifyEmailHandler)
	api.POST("/tokens/refresh", userHandler.RefreshTokenHandler)
	api.POST("/logout", userHandler.LogoutHandler)
	api.GET("/oauth/google", userHandler.GoogleAuthHandler)
	api.GET("/oauth/google/callback", userHandler.GoogleAuthCallbackHandler)

	// HTTP server
	srv := &http.Server{
		Addr:         ":8080",
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown
	go func() {
		log.Println("HTTP server listening on :8080")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}
	log.Println("Server exited gracefully")
}
