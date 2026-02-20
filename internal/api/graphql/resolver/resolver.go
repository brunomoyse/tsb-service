//go:generate go run github.com/99designs/gqlgen generate

package resolver

import (
	"net/http"
	"os"
	"time"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/lru"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"tsb-service/internal/api/graphql"
	"tsb-service/internal/api/graphql/directives"
	addressApplication "tsb-service/internal/modules/address/application"
	orderApplication "tsb-service/internal/modules/order/application"
	paymentApplication "tsb-service/internal/modules/payment/application"
	productApplication "tsb-service/internal/modules/product/application"
	restaurantApplication "tsb-service/internal/modules/restaurant/application"
	userApplication "tsb-service/internal/modules/user/application"
	"tsb-service/pkg/pubsub"
)

type Resolver struct {
	Broker            *pubsub.Broker
	AddressService    addressApplication.AddressService
	OrderService      orderApplication.OrderService
	PaymentService    paymentApplication.PaymentService
	ProductService    productApplication.ProductService
	RestaurantService restaurantApplication.RestaurantService
	UserService       userApplication.UserService
}

// NewResolver constructs the Resolver with required services.
func NewResolver(
	broker *pubsub.Broker,
	addressService addressApplication.AddressService,
	orderService orderApplication.OrderService,
	paymentService paymentApplication.PaymentService,
	productService productApplication.ProductService,
	restaurantService restaurantApplication.RestaurantService,
	userService userApplication.UserService,
) *Resolver {
	return &Resolver{
		Broker:            broker,
		AddressService:    addressService,
		OrderService:      orderService,
		PaymentService:    paymentService,
		ProductService:    productService,
		RestaurantService: restaurantService,
		UserService:       userService,
	}
}

// GraphQLHandler defines the GraphQL endpoint with @auth directive injection
func GraphQLHandler(resolver *Resolver, allowedOrigins []string) gin.HandlerFunc {
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
				origin := r.Header.Get("Origin")
				for _, allowed := range allowedOrigins {
					if origin == allowed {
						return true
					}
				}
				return false
			},
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
	h.Use(extension.FixedComplexityLimit(50))

	return func(c *gin.Context) {
		h.ServeHTTP(c.Writer, c.Request)
	}
}
