package main

import (
	"cmp"
	"context"
	cryptoRand "crypto/rand"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/getsentry/sentry-go"
	sentrygin "github.com/getsentry/sentry-go/gin"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"go.uber.org/zap"

	"tsb-service/internal/api/auth"
	"tsb-service/internal/api/feedback"
	images "tsb-service/internal/api/images"
	"tsb-service/internal/api/graphql/resolver"
	productApplication "tsb-service/internal/modules/product/application"
	productInfrastructure "tsb-service/internal/modules/product/infrastructure"
	"tsb-service/pkg/logging"
	"tsb-service/pkg/pubsub"
	"tsb-service/pkg/email/scaleway"

	couponApplication "tsb-service/internal/modules/coupon/application"
	couponInfrastructure "tsb-service/internal/modules/coupon/infrastructure"
	orderApplication "tsb-service/internal/modules/order/application"
	orderInfrastructure "tsb-service/internal/modules/order/infrastructure"
	paymentApplication "tsb-service/internal/modules/payment/application"
	paymentInfrastructure "tsb-service/internal/modules/payment/infrastructure"
	orderInterfaces "tsb-service/internal/modules/order/interfaces"
	paymentInterfaces "tsb-service/internal/modules/payment/interfaces"

	posApplication "tsb-service/internal/modules/pos/application"
	posInfrastructure "tsb-service/internal/modules/pos/infrastructure"
	posInterfaces "tsb-service/internal/modules/pos/interfaces"

	userApplication "tsb-service/internal/modules/user/application"
	userInfrastructure "tsb-service/internal/modules/user/infrastructure"

	addressApplication "tsb-service/internal/modules/address/application"
	addressInfrastructure "tsb-service/internal/modules/address/infrastructure"
	notificationApplication "tsb-service/internal/modules/notification/application"
	notificationInfrastructure "tsb-service/internal/modules/notification/infrastructure"
	restaurantApplication "tsb-service/internal/modules/restaurant/application"
	restaurantInfrastructure "tsb-service/internal/modules/restaurant/infrastructure"
	"tsb-service/internal/shared/middleware"
	"tsb-service/pkg/apns"
	"tsb-service/pkg/fcm"
	"tsb-service/pkg/db"
)

func main() {
	// Load .env file if present; env vars set externally (e.g. Kubernetes secrets) take precedence.
	_ = godotenv.Load()

	// Initialize structured logger
	logLevel := cmp.Or(os.Getenv("LOG_LEVEL"), "info")
	logFormat := os.Getenv("LOG_FORMAT")
	if logFormat == "" {
		if os.Getenv("APP_ENV") == "development" {
			logFormat = "text"
		} else {
			logFormat = "json"
		}
	}
	logging.Setup(logLevel, logFormat)
	defer logging.Sync()

	// Sentry error tracking (skipped when SENTRY_DSN is empty, e.g. local dev)
	if dsn := os.Getenv("SENTRY_DSN"); dsn != "" {
		if err := sentry.Init(sentry.ClientOptions{
			Dsn:              dsn,
			Environment:      cmp.Or(os.Getenv("APP_ENV"), "production"),
			Release:          os.Getenv("APP_VERSION"),
			TracesSampleRate: 0.1,
			AttachStacktrace: true,
		}); err != nil {
			zap.L().Error("sentry init failed", zap.Error(err))
		} else {
			zap.L().Info("sentry initialized", zap.String("environment", cmp.Or(os.Getenv("APP_ENV"), "production")))
			defer sentry.Flush(2 * time.Second)
		}
	}

	// DB connection with retry (dual pool: customer + admin)
	var dbPool *db.DBPool
	var dbErr error
	for i := range 3 {
		dbPool, dbErr = db.ConnectDualDatabase()
		if dbErr == nil {
			break
		}
		zap.L().Error("failed to connect to database", zap.Int("attempt", i+1), zap.Int("max_attempts", 3), zap.Error(dbErr))
		if i < 2 {
			time.Sleep(2 * time.Second)
		}
	}
	if dbErr != nil {
		zap.L().Error("failed to connect to database after all attempts", zap.Error(dbErr))
		os.Exit(1)
	}
	defer func() { _ = dbPool.Close() }()

	// PubSub broker (used by GraphQL subscriptions)
	broker := pubsub.NewBroker()

	// ENV checks & third-party setup
	mollieApiKey := os.Getenv("MOLLIE_API_TOKEN")
	if mollieApiKey == "" {
		zap.L().Error("MOLLIE_API_TOKEN is required")
		os.Exit(1)
	}

	// OIDC env vars (verifier created after userService for user lookup)
	zitadelIssuer := os.Getenv("ZITADEL_ISSUER")
	zitadelClientID := os.Getenv("ZITADEL_CLIENT_ID")
	if zitadelIssuer == "" || zitadelClientID == "" {
		zap.L().Error("ZITADEL_ISSUER and ZITADEL_CLIENT_ID are required")
		os.Exit(1)
	}

	if err := scaleway.InitService(); err != nil {
		zap.L().Error("failed to initialize email service", zap.Error(err))
		os.Exit(1)
	}

	mollieTesting := os.Getenv("MOLLIE_TESTING") == "true"
	var mollieCfg *mollie.Config
	if mollieTesting {
		mollieCfg = mollie.NewAPITestingConfig(true)
		zap.L().Info("mollie client initialized", zap.String("mode", "testing"))
	} else {
		mollieCfg = mollie.NewAPIConfig(true)
		zap.L().Info("mollie client initialized", zap.String("mode", "production"))
	}
	mollieClient, err := mollie.NewClient(nil, mollieCfg)
	if err != nil {
		zap.L().Error("failed to initialize mollie client", zap.Error(err))
		os.Exit(1)
	}

	// Repos / services / handlers
	couponRepo := couponInfrastructure.NewCouponRepository(dbPool)
	notificationRepo := notificationInfrastructure.NewNotificationRepository(dbPool)
	orderRepo := orderInfrastructure.NewOrderRepository(dbPool)
	paymentRepo := paymentInfrastructure.NewPaymentRepository(dbPool)
	productRepo := productInfrastructure.NewProductRepository(dbPool)
	restaurantRepo := restaurantInfrastructure.NewRestaurantRepository(dbPool)
	scheduleOverrideRepo := restaurantInfrastructure.NewScheduleOverrideRepository(dbPool)
	userRepo := userInfrastructure.NewUserRepository(dbPool)

	// Google Maps address caching setup
	googleAPIKey := os.Getenv("GOOGLE_MAPS_API_KEY")
	if googleAPIKey == "" {
		zap.L().Error("GOOGLE_MAPS_API_KEY is required")
		os.Exit(1)
	}
	originLat, err := strconv.ParseFloat(cmp.Or(os.Getenv("RESTAURANT_ORIGIN_LAT"), "50.64245770697728"), 64)
	if err != nil {
		zap.L().Error("RESTAURANT_ORIGIN_LAT must be a float", zap.Error(err))
		os.Exit(1)
	}
	originLng, err := strconv.ParseFloat(cmp.Or(os.Getenv("RESTAURANT_ORIGIN_LNG"), "5.574703166758179"), 64)
	if err != nil {
		zap.L().Error("RESTAURANT_ORIGIN_LNG must be a float", zap.Error(err))
		os.Exit(1)
	}
	googleLang := cmp.Or(os.Getenv("GOOGLE_MAPS_LANGUAGE"), "fr")
	autocompleteRadius, err := strconv.ParseFloat(cmp.Or(os.Getenv("AUTOCOMPLETE_RADIUS_METERS"), "15000"), 64)
	if err != nil {
		zap.L().Error("AUTOCOMPLETE_RADIUS_METERS must be a float", zap.Error(err))
		os.Exit(1)
	}

	addressCacheRepo := addressInfrastructure.NewAddressCacheRepository(dbPool)
	googleClient := addressInfrastructure.NewGoogleClient(googleAPIKey, originLat, originLng, autocompleteRadius, nil)
	addressService := addressApplication.NewAddressService(addressCacheRepo, googleClient, googleLang)
	couponService := couponApplication.NewCouponService(couponRepo)
	notificationService := notificationApplication.NewNotificationService(notificationRepo)
	orderService := orderApplication.NewOrderService(orderRepo)
	productService := productApplication.NewProductService(productRepo)
	restaurantService := restaurantApplication.NewRestaurantService(restaurantRepo, scheduleOverrideRepo, os.Getenv("APP_ENV") != "production")
	userService := userApplication.NewUserService(userRepo, zitadelUserFetcher{})
	paymentService := paymentApplication.NewPaymentService(paymentRepo, *mollieClient, orderService, userService, productService, couponService)

	// OIDC verifier — validates JWTs via JWKS + resolves Zitadel sub → app user UUID
	zitadelInternalURL := os.Getenv("ZITADEL_INTERNAL_URL") // Optional: internal Docker URL for OIDC discovery
	zitadelProjectID := os.Getenv("ZITADEL_PROJECT_ID") // used for project-specific role claim fallback
	oidcVerifier, err := middleware.NewOIDCVerifier(context.Background(), zitadelIssuer, zitadelInternalURL, zitadelClientID, zitadelProjectID, userService)
	if err != nil {
		zap.L().Error("failed to initialize OIDC verifier", zap.Error(err))
		os.Exit(1)
	}
	zap.L().Info("OIDC verifier initialized", zap.String("issuer", zitadelIssuer))

	// POS (shop-floor handheld) auth module — RRN + PIN with device HMAC.
	// Falls back to a generated ephemeral secret if POS_JWT_SECRET is unset so
	// dev servers start cleanly; production MUST set POS_JWT_SECRET.
	posJWTSecret := []byte(os.Getenv("POS_JWT_SECRET"))
	if len(posJWTSecret) == 0 {
		zap.L().Warn("POS_JWT_SECRET not set — generating ephemeral secret (tokens invalid on restart)")
		ephemeral := make([]byte, 32)
		if _, err := cryptoRand.Read(ephemeral); err != nil {
			zap.L().Error("rand for POS_JWT_SECRET failed", zap.Error(err))
			os.Exit(1)
		}
		posJWTSecret = ephemeral
	}
	posDeviceRepo := posInfrastructure.NewDeviceRepository(dbPool)
	posRefreshRepo := posInfrastructure.NewRefreshTokenRepository(dbPool)
	posUserRepo := posInfrastructure.NewPosUserRepository(dbPool)
	posStaffRepo := posInfrastructure.NewStaffRepository(dbPool)
	posNonceRepo := posInfrastructure.NewNonceRepository(dbPool)
	posService := posApplication.NewService(
		posApplication.DefaultConfig(posJWTSecret),
		posDeviceRepo, posRefreshRepo, posUserRepo, posStaffRepo, posNonceRepo,
	)
	oidcVerifier.SetAppJWTVerifier(posService)
	posHandler := posInterfaces.NewHandler(
		posService,
		zitadelIssuer+"/oidc/v1/userinfo",
		os.Getenv("ZITADEL_INTERNAL_URL"),
		func() string {
			if u, err := url.Parse(zitadelIssuer); err == nil {
				return u.Host
			}
			return ""
		}(),
	)

	// Initialize auth proxy handlers with pre-resolved configuration
	auth.Init(auth.Config{
		ZitadelIssuer:      zitadelIssuer,
		ZitadelInternalURL: zitadelInternalURL,
		ZitadelClientID:    zitadelClientID,
		NativeClientID:     os.Getenv("ZITADEL_NATIVE_CLIENT_ID"),
		ServicePAT:         os.Getenv("ZITADEL_SERVICE_PAT"),
		AdminPAT:           os.Getenv("ZITADEL_ADMIN_PAT"),
		AppBaseURL:         os.Getenv("APP_BASE_URL"),
		IdPGoogleID:        os.Getenv("ZITADEL_IDP_GOOGLE_ID"),
		IdPAppleID:         os.Getenv("ZITADEL_IDP_APPLE_ID"),
	})

	// APNs client for iOS push notifications (optional — non-fatal if not configured)
	var apnsClient *apns.Client
	apnsKeyPath := os.Getenv("APNS_AUTH_KEY_PATH")
	apnsKeyID := os.Getenv("APNS_KEY_ID")
	apnsTeamID := os.Getenv("APNS_TEAM_ID")
	apnsBundleID := cmp.Or(os.Getenv("APNS_BUNDLE_ID"), "be.tokyosushibarliege.app")
	if apnsKeyPath != "" && apnsKeyID != "" && apnsTeamID != "" {
		isProduction := os.Getenv("APP_ENV") == "production"
		var apnsErr error
		apnsClient, apnsErr = apns.NewClient(apnsKeyPath, apnsKeyID, apnsTeamID, apnsBundleID, isProduction)
		if apnsErr != nil {
			zap.L().Error("failed to initialize APNs client", zap.Error(apnsErr))
		} else {
			zap.L().Info("APNs client initialized")
		}
	}

	// FCM client for Android push notifications (optional — non-fatal if not configured)
	// Uses GOOGLE_APPLICATION_CREDENTIALS env var for service account authentication
	var fcmClient *fcm.Client
	if os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") != "" {
		var fcmErr error
		fcmClient, fcmErr = fcm.NewClient()
		if fcmErr != nil {
			zap.L().Error("failed to initialize FCM client", zap.Error(fcmErr))
		} else {
			zap.L().Info("FCM client initialized")
		}
	}

	orderHandler := orderInterfaces.NewOrderHandler(orderService, userService, productService)

	// Gin HTTP setup
	router := gin.New()
	// Order matters: Sentry first (catches panics in every subsequent handler),
	// then RequestID + Logger + SentryContext (propagates request_id/user_id as Sentry scope tags).
	router.Use(sentrygin.New(sentrygin.Options{Repanic: true}))
	router.Use(gin.Recovery())
	router.Use(middleware.RequestIDMiddleware())
	router.Use(middleware.ZapRequestLogger())
	router.Use(middleware.SentryContext())
	router.RedirectTrailingSlash = true
	router.RedirectFixedPath = true

	appBaseURL := os.Getenv("APP_BASE_URL")
	appDashboardURL := os.Getenv("APP_DASHBOARD_URL")
	if appBaseURL == "" || appDashboardURL == "" {
		zap.L().Error("APP_BASE_URL and APP_DASHBOARD_URL are required")
		os.Exit(1)
	}

	// Request body size limit (1MB default). GraphQL multipart and the image
	// preview proxy apply their own limits internally, so the global cap is
	// skipped for those two paths.
	router.Use(func(c *gin.Context) {
		p := c.Request.URL.Path
		if p != "/api/v1/graphql" && p != "/api/v1/images/preview" {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
		}
		c.Next()
	})

	router.Use(cors.New(cors.Config{
		AllowOrigins:     []string{appBaseURL, appDashboardURL, "capacitor://localhost", "http://localhost", "https://localhost"},
		CustomSchemas:    []string{"capacitor://"},
		AllowMethods:     []string{"HEAD", "GET", "POST", "PATCH", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization", "Accept-Language"},
		ExposeHeaders: []string{
			"Content-Length", "Authorization", "Content-Disposition",
			"X-Original-Width", "X-Original-Height",
			"X-Post-Rembg-Width", "X-Post-Rembg-Height",
			"X-Post-Trim-Width", "X-Post-Trim-Height",
			"X-Trim-Applied",
		},
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
		orderService, paymentService, productService, userService,
	))

	// Per-user rate limit on validateCoupon GraphQL query to block brute-force
	// enumeration of coupon codes: ~5 req/min, burst 3.
	couponValidateLimiter := middleware.NewRateLimiter(5.0/60, 3)

	// GraphQL
	rootResolver := resolver.NewResolver(
		broker, apnsClient, fcmClient,
		addressService, couponService, notificationService, orderService, paymentService, productService, restaurantService, userService, posService,
		couponValidateLimiter,
	)
	// Payment webhook depends on the resolver to fan out the new-order push
	// notification once the Mollie payment transitions to paid.
	paymentHandler := paymentInterfaces.NewPaymentHandler(paymentService, broker, rootResolver)
	graphqlHandler := resolver.GraphQLHandler(rootResolver, []string{appBaseURL, appDashboardURL, "capacitor://localhost", "https://localhost"}, oidcVerifier)
	optionalAuth := oidcVerifier.OptionalAuthMiddleware()

	api.POST("/graphql", optionalAuth, graphqlHandler)
	api.GET("/graphql", optionalAuth, graphqlHandler)

	// Auth proxy endpoints (proxies to Zitadel Session API with service account PAT)
	authLimiter := middleware.NewRateLimiter(15.0/60, 10) // 15 req/min per IP, burst of 10
	api.POST("/auth/session", authLimiter.Middleware(), auth.CreateSessionHandler)
	api.POST("/auth/finalize", authLimiter.Middleware(), auth.FinalizeOIDCHandler)
	api.POST("/auth/authorize-proxy", authLimiter.Middleware(), auth.AuthorizeProxyHandler)
	api.POST("/auth/token-exchange", authLimiter.Middleware(), auth.TokenExchangeHandler)
	api.POST("/auth/idp/start", authLimiter.Middleware(), auth.StartIdPIntentHandler)
	api.POST("/auth/idp/session", authLimiter.Middleware(), auth.CreateIdPSessionHandler)
	api.POST("/auth/register", authLimiter.Middleware(), auth.RegisterHandler)
	api.POST("/auth/password/request-reset", authLimiter.Middleware(), auth.RequestPasswordResetHandler)
	api.POST("/auth/password/reset", authLimiter.Middleware(), auth.SetNewPasswordHandler)
	api.POST("/auth/verify-email", authLimiter.Middleware(), auth.VerifyEmailHandler)
	api.POST("/auth/resend-verification", authLimiter.Middleware(), auth.ResendVerificationHandler)

	// POS (Sunmi V3H handheld) auth endpoints
	posLimiter := middleware.NewRateLimiter(30.0/60, 10) // 30 req/min per IP
	api.POST("/pos/auth/rrn-login", posLimiter.Middleware(), posHandler.RrnLogin)
	api.POST("/pos/auth/refresh", posLimiter.Middleware(), posHandler.Refresh)
	// Enrollment requires a valid Zitadel admin JWT and is throttled to a
	// slow bucket — every successful enrollment hits the Zitadel userinfo
	// endpoint, so the limiter also protects the upstream IdP from being
	// flooded by a compromised admin session.
	enrollLimiter := middleware.NewRateLimiter(5.0/60, 3) // 5 req/min per IP, burst 3
	api.POST("/pos/devices/enroll", enrollLimiter.Middleware(), oidcVerifier.StrictAuthMiddleware(), posHandler.Enroll)
	// FCM token registration — HMAC-signed, no Zitadel session required.
	api.PATCH("/pos/devices/fcm-token", posLimiter.Middleware(), posHandler.UpdateFCMToken)

	// Other endpoints
	// Mollie's webhook retries are retried ~25 times over 2 days on failure,
	// and normal traffic is 1-3 hits per payment. 60 req/min (burst 10) per
	// source IP is well above that baseline and blocks forced-re-fetch spam
	// from an attacker who has guessed a valid tr_* payment ID.
	mollieLimiter := middleware.NewRateLimiter(1.0, 10) // 60 req/min per IP, burst 10
	api.POST("/payments/webhook", mollieLimiter.Middleware(), paymentHandler.UpdatePaymentStatusHandler)

	strictAuth := oidcVerifier.StrictAuthMiddleware()
	api.POST("/images/preview", strictAuth, images.PreviewHandler)
	api.POST("/auth/change-password", strictAuth, auth.ChangePasswordHandler)
	api.GET("/auth/has-password", strictAuth, auth.HasPasswordHandler)
	api.GET("/orders/:id/invoice", strictAuth, orderHandler.DownloadInvoice)

	feedbackLimiter := middleware.NewRateLimiter(2.0/60, 2) // 2 req/min per IP
	api.POST("/feedback", feedbackLimiter.Middleware(), feedback.HandleFeedback)

	// HTTP server
	// Use ReadHeaderTimeout instead of ReadTimeout, and omit WriteTimeout,
	// because both set deadlines on the underlying net.Conn that persist after
	// WebSocket hijack — killing long-lived subscription connections.
	srv := &http.Server{
		Addr:              ":8080",
		Handler:           router,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Graceful shutdown
	go func() {
		zap.L().Info("HTTP server listening", zap.String("addr", ":8080"))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			zap.L().Error("server error", zap.Error(err))
			os.Exit(1)
		}
	}()

	// Periodic prune of expired POS enrollment nonces.
	noncePruneCtx, noncePruneCancel := context.WithCancel(context.Background())
	defer noncePruneCancel()
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-noncePruneCtx.Done():
				return
			case <-ticker.C:
				if err := posService.PruneExpiredNonces(noncePruneCtx); err != nil {
					zap.L().Warn("pos nonce prune failed", zap.Error(err))
				}
			}
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	zap.L().Info("shutting down server")
	noncePruneCancel()
	authLimiter.Stop()
	feedbackLimiter.Stop()
	broker.Shutdown()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		zap.L().Error("server forced to shutdown", zap.Error(err))
		os.Exit(1)
	}
	zap.L().Info("server exited gracefully")
}

// zitadelUserFetcher adapts auth.GetZitadelUserInfo to userApplication.ZitadelUserFetcher
// so the user service can enrich JIT-created users with profile data that isn't
// present on locally-validated JWT access tokens.
type zitadelUserFetcher struct{}

func (zitadelUserFetcher) FetchUserInfo(ctx context.Context, userID string) (string, string, string, error) {
	return auth.GetZitadelUserInfo(ctx, userID)
}
