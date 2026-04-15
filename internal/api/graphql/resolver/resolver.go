//go:generate go run github.com/99designs/gqlgen generate

package resolver

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/lru"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
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
) *Resolver {
	return &Resolver{
		Broker:              broker,
		APNsClient:          apnsClient,
		FCMClient:           fcmClient,
		AddressService:      addressService,
		CouponService:       couponService,
		NotificationService: notificationService,
		OrderService:        orderService,
		PaymentService:      paymentService,
		ProductService:      productService,
		RestaurantService:   restaurantService,
		UserService:         userService,
		PosService:          posService,
	}
}

// GraphQLHandler defines the GraphQL endpoint with @auth directive injection
func GraphQLHandler(resolver *Resolver, allowedOrigins []string, oidcVerifier *middleware.OIDCVerifier) gin.HandlerFunc {
	cfg := graphql.Config{Resolvers: resolver}
	cfg.Directives.Auth = directives.Auth
	cfg.Directives.Admin = directives.Admin

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
			sub, isAdmin, err := oidcVerifier.VerifyToken(ctx, tokenStr)
			if err == nil && sub != "" {
				// Resolve Zitadel sub → app UUID
				appID, lookupErr := resolver.UserService.ResolveZitadelID(ctx, sub, "", "", "")
				if lookupErr == nil {
					ctx = utils.SetUserID(ctx, appID)
				} else {
					ctx = utils.SetUserID(ctx, sub)
				}
				ctx = utils.SetIsAdmin(ctx, isAdmin)
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

	return func(c *gin.Context) {
		h.ServeHTTP(c.Writer, c.Request)
	}
}
