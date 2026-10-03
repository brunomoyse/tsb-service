//go:generate go run github.com/99designs/gqlgen generate

package resolver

import (
	"context"
	"errors"
	"maps"
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
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"

	"tsb-service/internal/api/graphql"
	"tsb-service/internal/api/graphql/apperr"
	"tsb-service/internal/api/graphql/directives"
	addressApplication "tsb-service/internal/modules/address/application"
	assistantApplication "tsb-service/internal/modules/assistant/application"
	couponApplication "tsb-service/internal/modules/coupon/application"
	notificationApplication "tsb-service/internal/modules/notification/application"
	orderApplication "tsb-service/internal/modules/order/application"
	paymentApplication "tsb-service/internal/modules/payment/application"
	posApplication "tsb-service/internal/modules/pos/application"
	productApplication "tsb-service/internal/modules/product/application"
	restaurantApplication "tsb-service/internal/modules/restaurant/application"
	userApplication "tsb-service/internal/modules/user/application"
	"tsb-service/internal/shared/audit"
	"tsb-service/internal/shared/middleware"
	"tsb-service/pkg/apns"
	"tsb-service/pkg/fcm"
	"tsb-service/pkg/logging"
	"tsb-service/pkg/pubsub"
	"tsb-service/pkg/utils"
)

type Resolver struct {
	Broker              *pubsub.Broker
	APNsClient          *apns.Client // nil if APNs not configured
	FCMClient           *fcm.Client  // nil if FCM not configured
	AddressService      addressApplication.AddressService
	CouponService       couponApplication.CouponService
	NotificationService notificationApplication.NotificationService
	OrderService        orderApplication.OrderService
	PaymentService      paymentApplication.PaymentService
	ProductService      productApplication.ProductService
	RestaurantService   restaurantApplication.RestaurantService
	UserService         userApplication.UserService
	PosService          *posApplication.Service
	// AssistantService proxies the WeChat assistant's connection; nil or
	// disabled when no assistant is configured. Set after NewResolver.
	AssistantService      *assistantApplication.Service
	CouponValidateLimiter *middleware.RateLimiter
	// PublicQueryLimiter throttles the unauthenticated, upstream-costly queries (quoteOrder,
	// resolveAddress) per client IP. nil disables it.
	PublicQueryLimiter *middleware.RateLimiter
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
	publicQueryLimiter *middleware.RateLimiter,
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
		PublicQueryLimiter:    publicQueryLimiter,
	}
}

// allowPublicQuery applies the per-IP limit of a public query. Each scope (query name) has its own
// bucket per IP. The IP comes from the HTTP layer (gin's ClientIP, i.e. the CF-Connecting-IP
// trusted-platform logic the other per-IP limiters use); without one (no HTTP request) nothing is
// limited rather than lumping every such call into one bucket.
func (r *Resolver) allowPublicQuery(ctx context.Context, scope string) error {
	if r.PublicQueryLimiter == nil {
		return nil
	}
	ip := utils.GetClientIP(ctx)
	if ip == "" {
		return nil
	}
	if !r.PublicQueryLimiter.AllowKey(scope + ":" + ip) {
		return apperr.New(apperr.CodeRateLimited, "too many requests, please try again in a minute")
	}
	return nil
}

// GraphQLHandler defines the GraphQL endpoint with @auth directive injection
func GraphQLHandler(resolver *Resolver, allowedOrigins []string, oidcVerifier *middleware.OIDCVerifier, auditRecorder *audit.Recorder) gin.HandlerFunc {
	cfg := graphql.Config{Resolvers: resolver}
	cfg.Directives.Auth = directives.Auth
	cfg.Directives.Admin = directives.Admin
	cfg.Directives.Staff = directives.Staff

	h := handler.New(graphql.NewExecutableSchema(cfg))

	h.AddTransport(transport.MultipartForm{
		MaxMemory: 50 << 20,
	})

	h.AddTransport(transport.Websocket{
		// gqlgen v0.17.92 dropped the built-in Gorilla Upgrader field in favour of
		// a pluggable Implementation. We wrap the default Coder implementation with
		// our own origin allowlist (see originCheckedWebsocket) to preserve the prior
		// CheckOrigin behaviour.
		Implementation: originCheckedWebsocket{allowedOrigins: allowedOrigins},
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
			sub, isAdmin, isPOS, exp, err := oidcVerifier.VerifyToken(ctx, tokenStr)
			if err == nil && sub != "" {
				if isPOS {
					// Device tokens carry the device UUID in `sub`; no Zitadel
					// resolution needed.
					ctx = utils.SetUserID(ctx, sub)
				} else {
					appID, lookupErr := resolver.UserService.ResolveZitadelID(ctx, sub, "", "", "")
					if lookupErr != nil {
						// Don't set a raw Zitadel sub (often a numeric Google
						// user ID) as the userID — it will hit Postgres UUID
						// columns and produce "invalid input syntax for type
						// uuid". Leave userID empty so the @auth directive
						// sees no authenticated user and returns UNAUTHENTICATED.
						zap.L().Warn("failed to resolve Zitadel user on WS init — proceeding unauthenticated",
							zap.String("sub", sub), zap.Error(lookupErr))
						return ctx, &initPayload, nil
					}
					ctx = utils.SetUserID(ctx, appID)
				}
				ctx = utils.SetIsAdmin(ctx, isAdmin)
				ctx = utils.SetIsPOS(ctx, isPOS)
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
	h.Use(audit.GraphQLExtension{Recorder: auditRecorder})

	h.SetErrorPresenter(ErrorPresenter)
	h.SetRecoverFunc(func(ctx context.Context, err any) error {
		// SkipSentry: the panic is reported via Recover below with full stack.
		logging.FromContext(ctx).Error("graphql resolver panic", zap.Any("panic", err), logging.SkipSentry)
		if hub := sentry.GetHubFromContext(ctx); hub != nil {
			hub.RecoverWithContext(ctx, err)
		} else {
			sentry.CurrentHub().Recover(err)
		}
		return gqlgraphql.DefaultRecover(ctx, err)
	})

	return func(c *gin.Context) {
		// Hand the trusted-proxy-aware client IP to the resolvers (per-IP limits on public queries).
		c.Request = c.Request.WithContext(utils.SetClientIP(c.Request.Context(), c.ClientIP()))
		h.ServeHTTP(c.Writer, c.Request)
	}
}

// internalErrorMessage is what a client sees for a server fault. The real text (SQL errors, provider
// responses, wrapped causes) stays in the logs and in Sentry only.
const internalErrorMessage = "Internal server error"

// ErrorPresenter turns a resolver error into the GraphQL response error and logs it.
//
// It copies the stable `extensions.code` (and parameters) of apperr errors into the response, logs
// every GraphQL error to zap (HTTP 200 with `errors` in the body leaves no trace in the access log
// middleware otherwise) and forwards unexpected ones to Sentry. User-input / auth errors carry a
// known code (apperr.IsExpected) and are demoted to a warn-level log, not Sentry events.
//
// A server fault (an unexpected code, or no code at all) reaches the client with the generic
// internalErrorMessage only: its own text may hold SQL or provider details. The code and any
// parameters are kept so clients can still map it.
func ErrorPresenter(ctx context.Context, e error) *gqlerror.Error {
	err := gqlgraphql.DefaultErrorPresenter(ctx, e)

	// Typed application errors (apperr) become `extensions.code` + their parameters.
	if appErr, ok := apperr.From(e); ok {
		if err.Extensions == nil {
			err.Extensions = map[string]any{}
		}
		maps.Copy(err.Extensions, appErr.Extensions())
	}

	code, _ := err.Extensions["code"].(string)
	expected := apperr.IsExpected(apperr.Code(code))

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
	switch {
	case expected:
		logger.Warn("graphql user error", fields...)
	case errors.Is(e, context.Canceled) || strings.Contains(e.Error(), "canceling statement due to user request"):
		// Client disconnected mid-request; log at Warn and skip Sentry.
		logger.Warn("graphql resolver error (client disconnect)", append(fields, zap.String("query", query))...)
	case opCtx == nil || opCtx.Operation == nil:
		// gqlgen pre-execution rejection: the request was rejected before any
		// operation was resolved (no operation provided, malformed GET, parse
		// error, validation failure, persisted-query miss). Reached before any
		// resolver ran, so it is always client/crawler noise, never a server
		// fault. The "input:" prefix in the message is gqlerror's default
		// filename, not the error Path — Path is empty here, which is why the
		// previous err.Path.String() == "input" check never matched. Skip Sentry.
	case strings.HasPrefix(e.Error(), "input: "):
		// gqlgen pre-execution rejection (malformed GET, parse error,
		// variable coercion, complexity overflow, etc.); client noise, skip Sentry.
		// Uses strings.HasPrefix instead of err.Path.String() == "input"
		// because nil Path (e.g. Applebot empty GET) serializes to "".
		logger.Warn("graphql resolver error (client malformed request)", append(fields, zap.String("query", query))...)
	default:
		// SkipSentry: this path captures the exception itself below with
		// richer scope, so the zap→Sentry bridge must not also fire.
		logger.Error("graphql resolver error", append(fields, zap.String("query", query), zap.Error(e), logging.SkipSentry)...)
		err.Message = internalErrorMessage
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
}
