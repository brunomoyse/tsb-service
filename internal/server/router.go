// Package server builds the HTTP router of tsb-service: the middleware stack, CORS, the request body cap, the security
// headers, the rate limits and the route table. The handlers are passed in, so cmd/app wires the real ones and the tests
// of this package wire stubs and assert what the router itself guarantees.
package server

import (
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"

	sentrygin "github.com/getsentry/sentry-go/gin"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"tsb-service/internal/shared/middleware"
)

const (
	// BodyLimit is the request body cap of every endpoint that does not apply its own limit.
	BodyLimit = 1 << 20
	// GraphQLPath is the GraphQL endpoint (HTTP and WebSocket).
	GraphQLPath = "/api/v1/graphql"
	// ImagePreviewPath is the image preview proxy, which applies its own size limit.
	ImagePreviewPath = "/api/v1/images/preview"
)

// Config is what the router needs besides the handlers.
type Config struct {
	// WebOrigins are the browser origins of the customer site and the dashboard (plus any extra origin kept allowed
	// while a host moves). The Capacitor and localhost origins of the apps are added here.
	WebOrigins []string
}

// Handlers are the endpoints and middlewares the router mounts. Every field is required: New refuses a nil one, so a
// route cannot be silently left without its handler.
type Handlers struct {
	// Health answers the health check: nil when the service can serve (the database answers).
	Health func() error

	// DataLoader is the /api/v1 group middleware that attaches the dataloaders.
	DataLoader gin.HandlerFunc
	// OptionalAuth lets anonymous requests through and attaches the user of a valid token.
	OptionalAuth gin.HandlerFunc
	// StrictAuth rejects a request without a valid token.
	StrictAuth gin.HandlerFunc

	GraphQL gin.HandlerFunc

	OTPRequest         gin.HandlerFunc
	OTPVerify          gin.HandlerFunc
	OTPResend          gin.HandlerFunc
	OTPCompleteProfile gin.HandlerFunc
	ReviewLastOTP      gin.HandlerFunc
	TOTPVerify         gin.HandlerFunc
	Finalize           gin.HandlerFunc
	AuthorizeProxy     gin.HandlerFunc
	TokenExchange      gin.HandlerFunc
	IdPStart           gin.HandlerFunc
	IdPSession         gin.HandlerFunc

	MFAStatus      gin.HandlerFunc
	MFAStartTOTP   gin.HandlerFunc
	MFAConfirmTOTP gin.HandlerFunc
	MFARemoveTOTP  gin.HandlerFunc

	POSDeviceLogin    gin.HandlerFunc
	POSUpdateFCMToken gin.HandlerFunc

	PaymentWebhook gin.HandlerFunc
	ImagePreview   gin.HandlerFunc
	OrderInvoice   gin.HandlerFunc
	Feedback       gin.HandlerFunc
}

// missing lists the nil fields of h, by name.
func (h Handlers) missing() []string {
	var names []string
	value := reflect.ValueOf(h)
	for i := range value.NumField() {
		if value.Field(i).IsNil() {
			names = append(names, value.Type().Field(i).Name)
		}
	}
	return names
}

// New builds the router. The returned stop function ends the rate limiters' cleanup goroutines (call it on shutdown).
func New(cfg Config, h Handlers) (*gin.Engine, func(), error) {
	if names := h.missing(); len(names) > 0 {
		return nil, nil, fmt.Errorf("server: missing handlers: %s", strings.Join(names, ", "))
	}

	router := gin.New()
	// Behind Cloudflare Tunnel: trust CF-Connecting-IP (set by Cloudflare at the
	// edge, and client-supplied copies are stripped there) instead of the default
	// of trusting client-supplied X-Forwarded-For. Without this, ClientIP() is
	// spoofable and per-IP rate limits on auth/OTP endpoints can be bypassed.
	router.TrustedPlatform = gin.PlatformCloudflare
	// Never fall back to a client-supplied X-Forwarded-For: requests without
	// CF-Connecting-IP use the TCP peer address.
	// (An empty proxy list has nothing to parse: this cannot fail.)
	_ = router.SetTrustedProxies(nil)
	// Order matters: Sentry first (catches panics in every subsequent handler),
	// then RequestID + Logger + SentryContext (propagates request_id/user_id as Sentry scope tags).
	router.Use(sentrygin.New(sentrygin.Options{Repanic: true}))
	router.Use(gin.Recovery())
	router.Use(middleware.RequestIDMiddleware())
	router.Use(middleware.ZapRequestLogger())
	router.Use(middleware.PrometheusMetrics())
	router.Use(middleware.SentryContext())
	router.RedirectTrailingSlash = true
	router.RedirectFixedPath = true

	// Request body size limit (1MB default). GraphQL multipart uploads and the
	// image preview proxy apply their own limits internally, so the global cap
	// is skipped for those. Plain GraphQL JSON POSTs keep the 1MB cap: gqlgen
	// reads them with io.ReadAll and has no limit of its own.
	router.Use(func(c *gin.Context) {
		p := c.Request.URL.Path
		multipartGraphQL := p == GraphQLPath &&
			strings.HasPrefix(strings.ToLower(c.GetHeader("Content-Type")), "multipart/form-data")
		if !multipartGraphQL && p != ImagePreviewPath {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, BodyLimit)
		}
		c.Next()
	})

	router.Use(cors.New(cors.Config{
		AllowOrigins:  append(slices.Clone(cfg.WebOrigins), "capacitor://localhost", "http://localhost", "https://localhost"),
		CustomSchemas: []string{"capacitor://"},
		AllowMethods:  []string{"HEAD", "GET", "POST", "PATCH", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:  []string{"Origin", "Content-Type", "Authorization", "Accept-Language"},
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

	// Prometheus scrape endpoint. Registered at the router level (outside
	// /api/v1) so it isn't auth/rate-limited/CORS'd. Public ingress only
	// forwards /api/v1/* and /auth/*, so this stays in-cluster.
	router.GET("/metrics", gin.WrapH(promhttp.Handler()))

	// API routes
	api := router.Group("/api/v1")
	healthCheck := func(c *gin.Context) {
		if err := h.Health(); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unhealthy", "db": "unreachable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	}
	api.HEAD("/up", healthCheck)
	api.GET("/up", healthCheck)
	api.Use(middleware.LanguageExtractor())
	api.Use(h.DataLoader)

	api.POST("/graphql", h.OptionalAuth, h.GraphQL)
	api.GET("/graphql", h.OptionalAuth, h.GraphQL)

	// Auth proxy endpoints (proxies to Zitadel Session API with service account PAT)
	authLimiter := middleware.NewRateLimiter(15.0/60, 10) // 15 req/min per IP, burst of 10
	// Passwordless email-OTP login (Zitadel Session API otpEmail challenge)
	api.POST("/auth/session/otp/request", authLimiter.Middleware(), h.OTPRequest)
	api.POST("/auth/session/otp/verify", authLimiter.Middleware(), h.OTPVerify)
	api.POST("/auth/session/otp/resend", authLimiter.Middleware(), h.OTPResend)
	api.POST("/auth/session/otp/complete-profile", authLimiter.Middleware(), h.OTPCompleteProfile)
	// Store-review OTP retrieval (Google Play / App Store reviewers, no mailbox).
	// Disabled unless REVIEW_OTP_KEY + REVIEW_OTP_LOGINS are set.
	api.GET("/auth/review/last-otp", authLimiter.Middleware(), h.ReviewLastOTP)
	api.POST("/auth/session/totp/verify", authLimiter.Middleware(), h.TOTPVerify)
	api.POST("/auth/finalize", authLimiter.Middleware(), h.Finalize)
	api.POST("/auth/authorize-proxy", authLimiter.Middleware(), h.AuthorizeProxy)
	api.POST("/auth/token-exchange", authLimiter.Middleware(), h.TokenExchange)
	api.POST("/auth/idp/start", authLimiter.Middleware(), h.IdPStart)
	api.POST("/auth/idp/session", authLimiter.Middleware(), h.IdPSession)

	// POS (Sunmi V3H handheld) auth endpoints: HMAC-signed, no Zitadel.
	posLimiter := middleware.NewRateLimiter(30.0/60, 10) // 30 req/min per IP
	api.POST("/pos/auth/device-login", posLimiter.Middleware(), h.POSDeviceLogin)
	api.PATCH("/pos/devices/fcm-token", posLimiter.Middleware(), h.POSUpdateFCMToken)

	// Other endpoints
	// Mollie's webhook retries are retried ~25 times over 2 days on failure,
	// and normal traffic is 1-3 hits per payment. 60 req/min (burst 10) per
	// source IP is well above that baseline and blocks forced-re-fetch spam
	// from an attacker who has guessed a valid tr_* payment ID.
	mollieLimiter := middleware.NewRateLimiter(1.0, 10) // 60 req/min per IP, burst 10
	api.POST("/payments/webhook", mollieLimiter.Middleware(), h.PaymentWebhook)

	// Staff second factor (TOTP) self-service, admins only (checked in handlers).
	api.GET("/auth/mfa", h.StrictAuth, h.MFAStatus)
	api.POST("/auth/mfa/totp", authLimiter.Middleware(), h.StrictAuth, h.MFAStartTOTP)
	api.POST("/auth/mfa/totp/verify", authLimiter.Middleware(), h.StrictAuth, h.MFAConfirmTOTP)
	api.POST("/auth/mfa/totp/remove", authLimiter.Middleware(), h.StrictAuth, h.MFARemoveTOTP)
	api.POST("/images/preview", h.StrictAuth, h.ImagePreview)
	api.GET("/orders/:id/invoice", h.StrictAuth, h.OrderInvoice)

	feedbackLimiter := middleware.NewRateLimiter(2.0/60, 2) // 2 req/min per IP
	api.POST("/feedback", feedbackLimiter.Middleware(), h.Feedback)

	stop := func() {
		authLimiter.Stop()
		posLimiter.Stop()
		mollieLimiter.Stop()
		feedbackLimiter.Stop()
	}
	return router, stop, nil
}
