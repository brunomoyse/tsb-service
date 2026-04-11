package main

import (
	"cmp"
	"context"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"go.uber.org/zap"

	"tsb-service/internal/api/auth"
	"tsb-service/internal/api/feedback"
	"tsb-service/internal/api/graphql/resolver"
	graphqlModel "tsb-service/internal/api/graphql/model"
	productApplication "tsb-service/internal/modules/product/application"
	productInfrastructure "tsb-service/internal/modules/product/infrastructure"
	"tsb-service/pkg/logging"
	"tsb-service/pkg/pubsub"
	"tsb-service/pkg/email/scaleway"

	couponApplication "tsb-service/internal/modules/coupon/application"
	couponInfrastructure "tsb-service/internal/modules/coupon/infrastructure"
	hubriseApplication "tsb-service/internal/modules/hubrise_webshop/application"
	hubriseInfrastructure "tsb-service/internal/modules/hubrise_webshop/infrastructure"
	hubriseInterfaces "tsb-service/internal/modules/hubrise_webshop/interfaces"
	orderApplication "tsb-service/internal/modules/order/application"
	orderInfrastructure "tsb-service/internal/modules/order/infrastructure"
	paymentApplication "tsb-service/internal/modules/payment/application"
	paymentInfrastructure "tsb-service/internal/modules/payment/infrastructure"
	orderInterfaces "tsb-service/internal/modules/order/interfaces"
	paymentInterfaces "tsb-service/internal/modules/payment/interfaces"

	userApplication "tsb-service/internal/modules/user/application"
	userInfrastructure "tsb-service/internal/modules/user/infrastructure"

	addressApplication "tsb-service/internal/modules/address/application"
	addressInfrastructure "tsb-service/internal/modules/address/infrastructure"
	notificationApplication "tsb-service/internal/modules/notification/application"
	notificationInfrastructure "tsb-service/internal/modules/notification/infrastructure"
	restaurantApplication "tsb-service/internal/modules/restaurant/application"
	restaurantInfrastructure "tsb-service/internal/modules/restaurant/infrastructure"
	"tsb-service/internal/shared/middleware"
	"tsb-service/pkg/alerter"
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
	addressRepo := addressInfrastructure.NewAddressRepository(dbPool)
	couponRepo := couponInfrastructure.NewCouponRepository(dbPool)
	notificationRepo := notificationInfrastructure.NewNotificationRepository(dbPool)
	orderRepo := orderInfrastructure.NewOrderRepository(dbPool)
	paymentRepo := paymentInfrastructure.NewPaymentRepository(dbPool)
	productRepo := productInfrastructure.NewProductRepository(dbPool)
	restaurantRepo := restaurantInfrastructure.NewRestaurantRepository(dbPool)
	userRepo := userInfrastructure.NewUserRepository(dbPool)

	addressService := addressApplication.NewAddressService(addressRepo)
	couponService := couponApplication.NewCouponService(couponRepo)
	notificationService := notificationApplication.NewNotificationService(notificationRepo)
	orderService := orderApplication.NewOrderService(orderRepo)
	productService := productApplication.NewProductService(productRepo)
	restaurantService := restaurantApplication.NewRestaurantService(restaurantRepo, os.Getenv("APP_ENV") != "production")
	userService := userApplication.NewUserService(userRepo)
	paymentService := paymentApplication.NewPaymentService(paymentRepo, *mollieClient, orderService, userService, productService)

	// OIDC verifier — validates JWTs via JWKS + resolves Zitadel sub → app user UUID
	zitadelInternalURL := os.Getenv("ZITADEL_INTERNAL_URL") // Optional: internal Docker URL for OIDC discovery
	oidcVerifier, err := middleware.NewOIDCVerifier(context.Background(), zitadelIssuer, zitadelInternalURL, zitadelClientID, userService)
	if err != nil {
		zap.L().Error("failed to initialize OIDC verifier", zap.Error(err))
		os.Exit(1)
	}
	zap.L().Info("OIDC verifier initialized", zap.String("issuer", zitadelIssuer))

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

	// HubRise webshop integration (optional, controlled via HUBRISE_ENABLED).
	// Handlers created here, routes wired after appDashboardURL resolution below.
	hubriseEnabled := os.Getenv("HUBRISE_ENABLED") == "true"
	hubriseBaseURL := cmp.Or(os.Getenv("HUBRISE_BASE_URL"), "https://api.hubrise.com/v1")
	hubriseOAuthBaseURL := cmp.Or(os.Getenv("HUBRISE_OAUTH_BASE_URL"), "https://manager.hubrise.com/oauth2/v1")
	hubriseClientID := os.Getenv("HUBRISE_WEBSHOP_CLIENT_ID")
	hubriseClientSecret := os.Getenv("HUBRISE_WEBSHOP_CLIENT_SECRET")
	hubriseRedirectURI := os.Getenv("HUBRISE_WEBSHOP_REDIRECT_URI")
	hubriseScope := cmp.Or(os.Getenv("HUBRISE_WEBSHOP_SCOPE"), "location[orders.write,customer_list.write,catalog.write]")

	hubriseConnRepo := hubriseInfrastructure.NewConnectionRepository(dbPool)
	hubriseSyncRepo := hubriseInfrastructure.NewCatalogSyncStateRepository(dbPool)
	hubriseWebhookRepo := hubriseInfrastructure.NewWebhookEventRepository(dbPool)
	hubrisePushRepo := hubriseInfrastructure.NewOrderPushRepository(dbPool)

	// Alerter is NoopAlerter by default and upgraded to EmailAlerter
	// when HUBRISE_ALERT_ENABLED=true. Scaleway TEM is already
	// initialised via scaleway.InitService() earlier in main.
	var hubriseAlerter alerter.Alerter = alerter.NoopAlerter{}
	if os.Getenv("HUBRISE_ALERT_ENABLED") == "true" {
		recipients := splitAndTrim(os.Getenv("HUBRISE_ALERT_EMAILS"), ",")
		if len(recipients) == 0 {
			zap.L().Warn("HUBRISE_ALERT_ENABLED=true but no HUBRISE_ALERT_EMAILS configured — falling back to NoopAlerter")
		} else {
			dedupTTL := 10 * time.Minute
			if raw := os.Getenv("HUBRISE_ALERT_DEDUP_TTL"); raw != "" {
				if d, err := time.ParseDuration(raw); err == nil {
					dedupTTL = d
				}
			}
			hubriseAlerter = alerter.NewEmailAlerter(recipients, dedupTTL)
			zap.L().Info("hubrise email alerter enabled",
				zap.Int("recipients", len(recipients)),
				zap.Duration("dedup_ttl", dedupTTL))
		}
	}

	hubriseCatalogPusher := hubriseApplication.NewCatalogPusher(
		hubriseBaseURL, productService, hubriseConnRepo, hubriseSyncRepo,
	)
	hubriseOrderPusher := hubriseApplication.NewOrderPusher(
		hubriseBaseURL, hubriseConnRepo, hubrisePushRepo, hubriseAlerter,
	)
	hubriseOrderLoader := hubriseApplication.NewDefaultOrderLoader(orderService)
	hubriseRetryWorker := hubriseApplication.NewRetryWorker(
		hubrisePushRepo, hubriseOrderPusher, hubriseOrderLoader,
	)
	hubriseOAuthService := hubriseApplication.NewOAuthService(
		hubriseApplication.OAuthConfig{
			OAuthBaseURL: hubriseOAuthBaseURL,
			ClientID:     hubriseClientID,
			ClientSecret: hubriseClientSecret,
			RedirectURI:  hubriseRedirectURI,
			Scope:        hubriseScope,
		},
		hubriseConnRepo,
	)
	hubriseWebhookService := hubriseApplication.NewWebhookService(hubriseWebhookRepo, broker)
	hubriseHealthService := hubriseApplication.NewHealthService(hubrisePushRepo, hubriseSyncRepo)

	// Phase C: circuit breaker. Wires into the existing pusher via
	// SetHealthMonitor below so RecordSuccess/RecordFailure fire on
	// every push attempt. The probe loop runs in a separate goroutine.
	hubriseHealthMonitor := hubriseApplication.NewHealthMonitor(
		hubriseBaseURL, hubriseConnRepo, restaurantService, hubriseAlerter, broker,
	)
	hubriseOrderPusher.SetHealthMonitor(hubriseHealthMonitor)

	// hubriseRetryCancel / hubriseProbeCancel are set when HubRise is
	// enabled so the shutdown sequence can gracefully stop the
	// background goroutines.
	var hubriseRetryCancel context.CancelFunc
	var hubriseProbeCancel context.CancelFunc

	if hubriseEnabled {
		zap.L().Info("HubRise webshop integration enabled",
			zap.String("base_url", hubriseBaseURL),
			zap.String("oauth_base_url", hubriseOAuthBaseURL))

		// Start the retry worker so failed pushes are replayed with
		// exponential backoff until they succeed or exceed
		// MaxRetryAttempts (~55 min coverage).
		var retryCtx context.Context
		retryCtx, hubriseRetryCancel = context.WithCancel(context.Background())
		go hubriseRetryWorker.Run(retryCtx)

		// Start the circuit breaker probe loop. It only makes HTTP
		// calls when the circuit is already open, so idle cost is
		// essentially zero.
		var probeCtx context.Context
		probeCtx, hubriseProbeCancel = context.WithCancel(context.Background())
		go hubriseHealthMonitor.ProbeLoop(probeCtx)

		// Subscribe to orderUpdated broker events and push orders to
		// HubRise when the Mollie webhook confirms a payment. The
		// push itself is tracked durably via OrderPushRepository, so
		// transient failures are retried by hubriseRetryWorker above.
		orderEventsCh := broker.Subscribe("orderUpdated")
		go func() {
			for evt := range orderEventsCh {
				gqlOrder, ok := evt.(*graphqlModel.Order)
				if !ok || gqlOrder == nil {
					continue
				}
				// Only push once the order is confirmed (paid).
				if string(gqlOrder.Status) != "CONFIRMED" {
					continue
				}
				go func(orderID uuid.UUID) {
					bgCtx := context.Background()
					remoteID, err := hubriseOrderPusher.PushOrder(bgCtx, hubriseOrderLoader, orderID)
					if err != nil {
						// Error already persisted in hubrise_push_status
						// via OrderPusher.handleFailure; the retry
						// worker will replay this order on its next tick.
						zap.L().Error("hubrise order push failed (will retry)",
							zap.String("order_id", orderID.String()),
							zap.Error(err))
						return
					}
					if remoteID != "" {
						zap.L().Info("hubrise order pushed",
							zap.String("order_id", orderID.String()),
							zap.String("remote_id", remoteID))
					}
				}(gqlOrder.ID)
			}
		}()
	}
	// Wire the catalog push trigger so product mutations in the
	// GraphQL resolvers automatically push the menu to HubRise
	// (debounced, 2 seconds after the last change).
	var changeLogger *productApplication.MenuChangeLogger
	if hubriseEnabled {
		changeLogger = productApplication.NewMenuChangeLogger(hubriseCatalogPusher)
		zap.L().Info("catalog auto-push enabled (HubRise CatalogPusher as trigger)")
	} else {
		changeLogger = productApplication.NewMenuChangeLogger(nil)
	}

	_ = hubriseOrderPusher
	_ = hubriseOrderLoader
	_ = hubriseWebhookService
	_ = hubriseRetryWorker

	orderHandler := orderInterfaces.NewOrderHandler(orderService, userService, productService)
	paymentHandler := paymentInterfaces.NewPaymentHandler(paymentService, broker)

	// Gin HTTP setup
	router := gin.New()
	router.Use(gin.Recovery())
	router.Use(middleware.RequestIDMiddleware())
	router.Use(middleware.ZapRequestLogger())
	router.RedirectTrailingSlash = true
	router.RedirectFixedPath = true

	appBaseURL := os.Getenv("APP_BASE_URL")
	appDashboardURL := os.Getenv("APP_DASHBOARD_URL")
	if appBaseURL == "" || appDashboardURL == "" {
		zap.L().Error("APP_BASE_URL and APP_DASHBOARD_URL are required")
		os.Exit(1)
	}

	// Request body size limit (1MB default, GraphQL multipart has its own 10MB limit)
	router.Use(func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
		c.Next()
	})

	router.Use(cors.New(cors.Config{
		AllowOrigins:     []string{appBaseURL, appDashboardURL, "capacitor://localhost", "http://localhost", "https://localhost"},
		CustomSchemas:    []string{"capacitor://"},
		AllowMethods:     []string{"HEAD", "GET", "POST", "PATCH", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization", "Accept-Language"},
		ExposeHeaders:    []string{"Content-Length", "Authorization", "Content-Disposition"},
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
		broker, apnsClient, fcmClient,
		addressService, couponService, notificationService, orderService, paymentService, productService, restaurantService, userService,
		changeLogger,
	)
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

	// Other endpoints
	api.POST("/payments/webhook", paymentHandler.UpdatePaymentStatusHandler)

	// HubRise webshop routes (handlers were constructed earlier).
	hubriseOAuthHandler := hubriseInterfaces.NewOAuthHandler(hubriseOAuthService, appDashboardURL)
	hubriseWebhookHandler := hubriseInterfaces.NewWebhookHandler(hubriseWebhookService, hubriseClientSecret)
	hubriseAdminHandler := hubriseInterfaces.NewAdminHandler(hubriseConnRepo, hubriseSyncRepo, hubriseCatalogPusher)
	hubriseHealthHandler := hubriseInterfaces.NewHealthHandler(hubriseHealthService)

	api.GET("/hubrise/webshop/oauth/authorize", hubriseOAuthHandler.Authorize)
	api.GET("/hubrise/webshop/oauth/callback", hubriseOAuthHandler.Callback)
	api.POST("/hubrise/webshop/webhook", hubriseWebhookHandler.Handle)
	// Health endpoint is public (no OIDC) so cron + dashboard can
	// consume it without credentials. It exposes only counts — no PII.
	api.GET("/hubrise/webshop/health", hubriseHealthHandler.Handle)

	strictAuth := oidcVerifier.StrictAuthMiddleware()
	api.POST("/auth/change-password", strictAuth, auth.ChangePasswordHandler)
	api.GET("/auth/has-password", strictAuth, auth.HasPasswordHandler)
	api.GET("/orders/:id/invoice", strictAuth, orderHandler.DownloadInvoice)

	// HubRise admin endpoints — protected by OIDC strict auth.
	api.GET("/hubrise/webshop/status", strictAuth, hubriseAdminHandler.Status)
	api.POST("/hubrise/webshop/catalog/push", strictAuth, hubriseAdminHandler.PushCatalog)
	api.POST("/hubrise/webshop/disconnect", strictAuth, hubriseAdminHandler.Disconnect)

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

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	zap.L().Info("shutting down server")
	authLimiter.Stop()
	feedbackLimiter.Stop()
	if hubriseRetryCancel != nil {
		hubriseRetryCancel()
	}
	if hubriseProbeCancel != nil {
		hubriseProbeCancel()
	}
	broker.Shutdown()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		zap.L().Error("server forced to shutdown", zap.Error(err))
		os.Exit(1)
	}
	zap.L().Info("server exited gracefully")
}

// splitAndTrim splits s on sep and returns the non-empty, whitespace-
// trimmed parts. Used for comma-separated env-var lists such as
// HUBRISE_ALERT_EMAILS.
func splitAndTrim(s, sep string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, sep)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
