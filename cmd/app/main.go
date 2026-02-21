package main

import (
	"context"
	"log/slog"
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
	"tsb-service/pkg/logging"
	"tsb-service/pkg/pubsub"
	"tsb-service/services/email/scaleway"

	couponApplication "tsb-service/internal/modules/coupon/application"
	couponInfrastructure "tsb-service/internal/modules/coupon/infrastructure"
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
	restaurantApplication "tsb-service/internal/modules/restaurant/application"
	restaurantInfrastructure "tsb-service/internal/modules/restaurant/infrastructure"
	"tsb-service/internal/shared/middleware"
	"tsb-service/pkg/db"
	"tsb-service/pkg/oauth2"
)

func main() {
	// Load .env file
	if err := godotenv.Load(); err != nil {
		panic("Warning: .env file not found")
	}

	// Initialize structured logger
	logLevel := os.Getenv("LOG_LEVEL")
	if logLevel == "" {
		logLevel = "info"
	}
	logFormat := os.Getenv("LOG_FORMAT")
	if logFormat == "" {
		if os.Getenv("APP_ENV") == "development" {
			logFormat = "text"
		} else {
			logFormat = "json"
		}
	}
	logging.Setup(logLevel, logFormat)

	// DB connection with retry (dual pool: customer + admin)
	var dbPool *db.DBPool
	var dbErr error
	for i := 0; i < 3; i++ {
		dbPool, dbErr = db.ConnectDualDatabase()
		if dbErr == nil {
			break
		}
		slog.Error("failed to connect to database", "attempt", i+1, "max_attempts", 3, "error", dbErr)
		if i < 2 {
			time.Sleep(2 * time.Second)
		}
	}
	if dbErr != nil {
		slog.Error("failed to connect to database after all attempts", "error", dbErr)
		os.Exit(1)
	}
	defer dbPool.Close()

	// PubSub broker (used by GraphQL subscriptions)
	broker := pubsub.NewBroker()

	// ENV checks & third-party setup
	mollieApiKey := os.Getenv("MOLLIE_API_TOKEN")
	if mollieApiKey == "" {
		slog.Error("MOLLIE_API_TOKEN is required")
		os.Exit(1)
	}
	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		slog.Error("JWT_SECRET is required")
		os.Exit(1)
	}

	if err := scaleway.InitService(); err != nil {
		slog.Error("failed to initialize email service", "error", err)
		os.Exit(1)
	}
	oauth2.LoadGoogleOAuth()

	mollieTesting := os.Getenv("MOLLIE_TESTING") == "true"
	var mollieCfg *mollie.Config
	if mollieTesting {
		mollieCfg = mollie.NewAPITestingConfig(true)
		slog.Info("mollie client initialized", "mode", "testing")
	} else {
		mollieCfg = mollie.NewAPIConfig(true)
		slog.Info("mollie client initialized", "mode", "production")
	}
	mollieClient, err := mollie.NewClient(nil, mollieCfg)
	if err != nil {
		slog.Error("failed to initialize mollie client", "error", err)
		os.Exit(1)
	}

	// Repos / services / handlers
	addressRepo := addressInfrastructure.NewAddressRepository(dbPool)
	couponRepo := couponInfrastructure.NewCouponRepository(dbPool)
	orderRepo := orderInfrastructure.NewOrderRepository(dbPool)
	paymentRepo := paymentInfrastructure.NewPaymentRepository(dbPool)
	productRepo := productInfrastructure.NewProductRepository(dbPool)
	restaurantRepo := restaurantInfrastructure.NewRestaurantRepository(dbPool)
	userRepo := userInfrastructure.NewUserRepository(dbPool)

	addressService := addressApplication.NewAddressService(addressRepo)
	couponService := couponApplication.NewCouponService(couponRepo)
	orderService := orderApplication.NewOrderService(orderRepo)
	paymentService := paymentApplication.NewPaymentService(paymentRepo, *mollieClient)
	productService := productApplication.NewProductService(productRepo)
	restaurantService := restaurantApplication.NewRestaurantService(restaurantRepo, os.Getenv("APP_ENV") != "production")
	userService := userApplication.NewUserService(userRepo)

	paymentHandler := paymentInterfaces.NewPaymentHandler(paymentService, orderService, userService, productService, broker)
	userHandler := userInterfaces.NewUserHandler(userService, addressService, jwtSecret)

	// Gin HTTP setup
	router := gin.New()
	router.Use(gin.Recovery())
	router.Use(middleware.RequestIDMiddleware())
	router.Use(middleware.SlogRequestLogger())
	router.RedirectTrailingSlash = true
	router.RedirectFixedPath = true

	appBaseURL := os.Getenv("APP_BASE_URL")
	appDashboardURL := os.Getenv("APP_DASHBOARD_URL")
	if appBaseURL == "" || appDashboardURL == "" {
		slog.Error("APP_BASE_URL and APP_DASHBOARD_URL are required")
		os.Exit(1)
	}

	// Request body size limit (1MB default, GraphQL multipart has its own 10MB limit)
	router.Use(func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
		c.Next()
	})

	router.Use(cors.New(cors.Config{
		AllowOrigins:     []string{appBaseURL, appDashboardURL},
		AllowMethods:     []string{"HEAD", "GET", "POST", "PATCH", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization", "Accept-Language"},
		ExposeHeaders:    []string{"Content-Length", "Authorization"},
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
	}))
	router.OPTIONS("/*any", func(c *gin.Context) { c.Status(http.StatusOK) })

	// Global security headers
	router.Use(func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "strict-origin-when-cross-origin")
		c.Next()
	})

	// API routes
	api := router.Group("/api/v1")
	healthCheck := func(c *gin.Context) {
		if err := dbPool.DB().Ping(); err != nil {
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
		addressService, couponService, orderService, paymentService, productService, restaurantService, userService,
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
	api.POST("/tokens/refresh", authLimiter.Middleware(), userHandler.RefreshTokenHandler)
	api.POST("/logout", userHandler.LogoutHandler)
	api.POST("/forgot-password", authLimiter.Middleware(), userHandler.ForgotPasswordHandler)
	api.POST("/reset-password", authLimiter.Middleware(), userHandler.ResetPasswordHandler)
	api.GET("/oauth/google", authLimiter.Middleware(), userHandler.GoogleAuthHandler)
	api.GET("/oauth/google/callback", authLimiter.Middleware(), userHandler.GoogleAuthCallbackHandler)

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
		slog.Info("HTTP server listening", "addr", ":8080")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	slog.Info("shutting down server")
	broker.Shutdown()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("server forced to shutdown", "error", err)
		os.Exit(1)
	}
	slog.Info("server exited gracefully")
}
