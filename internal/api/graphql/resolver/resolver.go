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
	"tsb-service/internal/modules/product/application"
)

type Resolver struct {
	ProductService application.ProductService
}

// NewResolver constructs the Resolver with required services.
func NewResolver(productService application.ProductService) *Resolver {
	return &Resolver{ProductService: productService}
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
