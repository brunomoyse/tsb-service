//go:generate go run github.com/99designs/gqlgen generate

package resolver

import (
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/lru"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/gin-gonic/gin"
	"tsb-service/internal/api/graphql"
	"tsb-service/internal/api/graphql/directives"
	addressApplication "tsb-service/internal/modules/address/application"
	orderApplication "tsb-service/internal/modules/order/application"
	paymentApplication "tsb-service/internal/modules/payment/application"
	productApplication "tsb-service/internal/modules/product/application"
	userApplication "tsb-service/internal/modules/user/application"
)

type Resolver struct {
	AddressService addressApplication.AddressService
	OrderService   orderApplication.OrderService
	PaymentService paymentApplication.PaymentService
	ProductService productApplication.ProductService
	UserService    userApplication.UserService
}

// NewResolver constructs the Resolver with required services.
func NewResolver(
	addressService addressApplication.AddressService,
	orderService orderApplication.OrderService,
	paymentService paymentApplication.PaymentService,
	productService productApplication.ProductService,
	userService userApplication.UserService,
) *Resolver {
	return &Resolver{
		AddressService: addressService,
		OrderService:   orderService,
		PaymentService: paymentService,
		ProductService: productService,
		UserService:    userService,
	}
}

// GraphQLHandler defines the GraphQL endpoint with @auth directive injection
func GraphQLHandler(resolver *Resolver) gin.HandlerFunc {
	cfg := graphql.Config{Resolvers: resolver}
	cfg.Directives.Auth = directives.Auth

	h := handler.New(graphql.NewExecutableSchema(cfg))

	h.AddTransport(transport.Options{})
	h.AddTransport(transport.POST{})

	h.Use(extension.AutomaticPersistedQuery{
		//nolint:mnd // Store 50 queries in memory using Least Recently Used (LRU) algorithm
		Cache: lru.New[string](50),
	})
	h.Use(extension.FixedComplexityLimit(50))

	return func(c *gin.Context) {
		h.ServeHTTP(c.Writer, c.Request)
	}
}
