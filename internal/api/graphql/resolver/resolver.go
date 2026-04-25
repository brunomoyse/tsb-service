//go:generate go run github.com/99designs/gqlgen generate

package resolver

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"

	gqlgraphql "github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/lru"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/getsentry/sentry-go"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"

	"tsb-service/internal/api/graphql"
	"tsb-service/internal/api/graphql/directives"
	addressApplication "tsb-service/internal/modules/address/application"
	couponApplication "tsb-service/internal/modules/coupon/application"
	notificationApplication "tsb-service/internal/modules/notification/application"
	orderApplication "tsb-service/internal/modules/order/application"
	paymentApplication "tsb-service/internal/modules/payment/application"
	posApplication "tsb-service/internal/modules/pos/application"
	productApplication "tsb-service/internal/modules/product/application"
	restaurantApplication "tsb-service/internal/modules/restaurant/application"
	userApplication "tsb-service/internal/modules/user/application"
	"tsb-service/internal/shared/middleware"
	"tsb-service/pkg/apns"
	"tsb-service/pkg/fcm"
	"tsb-service/pkg/logging"
	"tsb-service/pkg/pubsub"
	"tsb-service/pkg/utils"
)

type Resolver struct {
	Broker                *pubsub.Broker
	APNsClient            *apns.Client // nil if APNs not configured
	FCMClient             *fcm.Client  // nil if FCM not configured
	AddressService        addressApplication.AddressService
	CouponService         couponApplication.CouponService
	NotificationService   notificationApplication.NotificationService
	OrderService          orderApplication.OrderService
	PaymentService        paymentApplication.PaymentService
	ProductService        productApplication.ProductService
	RestaurantService     restaurantApplication.RestaurantService
	UserService           userApplication.UserService
	PosService            *posApplication.Service
	CouponValidateLimiter *middleware.RateLimiter
}

// NewResolver constructs the Resolver with required services.
func NewResolver(
	broker *pubsub.Broker,
	apnsClient *apns.Client,
	fcmClient *fcm.Client,
	addressService addressApplication.AddressService,
	couponService couponApplication.CouponService,
	notificationService notificationApplication.NotificationService,
	orderService orderApplication.OrderService,
	paymentService paymentApplication.PaymentService,
	productService productApplication.ProductService,
	restaurantService restaurantApplication.RestaurantService,
	userService userApplication.UserService,
	posService *posApplication.Service,
	couponValidateLimiter *middleware.RateLimiter,
) *Resolver {
	return &Resolver{
		Broker:                broker,
		APNsClient:            apnsClient,
		FCMClient:             fcmClient,
		AddressService:        addressService,
		CouponService:         couponService,
		NotificationService:   notificationService,
		OrderService:          orderService,
		PaymentService:        paymentService,
		ProductService:        productService,
		RestaurantService:     restaurantService,
		UserService:           userService,
		PosService:            posService,
		CouponValidateLimiter: couponValidateLimiter,
	}
}

// GraphQLHandler defines the GraphQL endpoint with @auth directive injection
func GraphQLHandler(resolver *Resolver, allowedOrigins []string, oidcVerifier *middleware.OIDCVerifier) gin.HandlerFunc {
	cfg := graphql.Config{Resolvers: resolver}
	cfg.Directives.Auth = directives.Auth
	cfg.Directives.Admin = directives.Admin
	cfg.Directives.Staff = directives.Staff

	h := handler.New(graphql.NewExecutableSchema(cfg))

	h.AddTransport(transport.MultipartForm{
		// same 10MB limit you used in the REST handler
		MaxMemory: 10 << 20,
	})

	h.AddTransport(transport.Websocket{
		Upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin: func(r *http.Request) bool {
				origin := strings.TrimRight(strings.ToLower(r.Header.Get("Origin")), "/")
				// Native apps (Android/iOS) don't send an Origin header — allow empty
				if origin == "" {
					return true
				}
				for _, allowed := range allowedOrigins {
					if origin == strings.TrimRight(strings.ToLower(allowed), "/") {
						return true
					}
				}
				zap.L().Warn("WebSocket origin rejected",
					zap.String("origin", origin),
					zap.Strings("allowed", allowedOrigins),
				)
				return false
			},
		},
		InitFunc: func(ctx context.Context, initPayload transport.InitPayload) (context.Context, *transport.InitPayload, error) {
			// If auth was already set by HTTP middleware (via cookie), keep it
			if utils.GetUserID(ctx) != "" {
				return ctx, &initPayload, nil
			}
			// Fall back to connectionParams Authorization header
			auth := initPayload.Authorization()
			if auth == "" {
				return ctx, &initPayload, nil
			}
			tokenStr := strings.TrimPrefix(auth, "Bearer ")
			sub, isAdmin, isStaff, exp, err := oidcVerifier.VerifyToken(ctx, tokenStr)
			if err == nil && sub != "" {
				isPOS := isStaff && !isAdmin
				if isPOS {
					// POS staff tokens already carry the app UUID in `sub`; don't
					// hit Zitadel's user API for them.
					ctx = utils.SetUserID(ctx, sub)
				} else {
					// Zitadel tokens: resolve sub → app UUID via JIT provisioning.
					appID, lookupErr := resolver.UserService.ResolveZitadelID(ctx, sub, "", "", "")
					if lookupErr == nil {
						ctx = utils.SetUserID(ctx, appID)
					} else {
						ctx = utils.SetUserID(ctx, sub)
					}
				}
				ctx = utils.SetIsAdmin(ctx, isAdmin)
				ctx = utils.SetIsStaff(ctx, isStaff)
				ctx = utils.SetTokenExpiry(ctx, exp)
				// Bind the WebSocket context lifetime to the access token.
				// When exp hits, ctx.Done() fires, every in-flight subscription
				// unblocks on its <-ctx.Done() select arm, and gqlgen tears
				// down the connection. Clients must reconnect with a fresh
				// token (both tsb-core and tsb-dashboard already do this via
				// silentRenew + graphql-ws retry).
				//
				// The WS transport cancels the parent ctx on socket close, so
				// the cancel func here is redundant — the deadline fires via
				// the runtime clock and its Timer is GC'd by context.cancelCtx
				// once the parent terminates. Kept in a named var so govet's
				// lostcancel check is satisfied.
				if !exp.IsZero() {
					var cancel context.CancelFunc
					ctx, cancel = context.WithDeadline(ctx, exp)
					_ = cancel
				}
			}
			return ctx, &initPayload, nil
		},
		KeepAlivePingInterval: 10 * time.Second,
	})
	h.AddTransport(transport.Options{})
	h.AddTransport(transport.POST{})
	h.AddTransport(transport.GET{})

	if os.Getenv("ENABLE_GQL_INTROSPECTION") == "true" {
		h.Use(extension.Introspection{})
	}

	h.Use(extension.AutomaticPersistedQuery{
		//nolint:mnd // Store 50 queries in memory using Least Recently Used (LRU) algorithm
		Cache: lru.New[string](50),
	})
	h.Use(extension.FixedComplexityLimit(100))

	// Log every GraphQL error to zap (HTTP 200 with `errors` in the body leaves
	// no trace in the access log middleware otherwise) and forward unexpected
	// ones to Sentry. User-input / auth errors carry a known code in
	// `extensions.code` and are demoted to a warn-level log, not Sentry events.
	h.SetErrorPresenter(func(ctx context.Context, e error) *gqlerror.Error {
		err := gqlgraphql.DefaultErrorPresenter(ctx, e)

		code, _ := err.Extensions["code"].(string)
		expected := code == "USER_ERROR" || code == "UNAUTHENTICATED" || code == "FORBIDDEN" || code == "NOT_FOUND"

		opCtx := gqlgraphql.GetOperationContext(ctx)
		var opName, query string
		if opCtx != nil {
			opName = opCtx.OperationName
			query = opCtx.RawQuery
		}
		path := err.Path.String()

		fields := []zap.Field{
			zap.String("operation", opName),
			zap.String("code", code),
			zap.String("path", path),
			zap.String("message", err.Message),
		}
		logger := logging.FromContext(ctx)
		if expected {
			logger.Warn("graphql user error", fields...)
		} else {
			logger.Error("graphql resolver error", append(fields, zap.String("query", query), zap.Error(e))...)
			if hub := sentry.GetHubFromContext(ctx); hub != nil {
				hub.WithScope(func(scope *sentry.Scope) {
					scope.SetTag("graphql.operation", opName)
					scope.SetTag("graphql.path", path)
					scope.SetContext("graphql", map[string]any{"query": query})
					hub.CaptureException(e)
				})
			} else {
				sentry.CaptureException(e)
			}
		}
		return err
	})
	h.SetRecoverFunc(func(ctx context.Context, err any) error {
		logging.FromContext(ctx).Error("graphql resolver panic", zap.Any("panic", err))
		if hub := sentry.GetHubFromContext(ctx); hub != nil {
			hub.RecoverWithContext(ctx, err)
		} else {
			sentry.CurrentHub().Recover(err)
		}
		return gqlgraphql.DefaultRecover(ctx, err)
	})

	return func(c *gin.Context) {
		h.ServeHTTP(c.Writer, c.Request)
	}
}
