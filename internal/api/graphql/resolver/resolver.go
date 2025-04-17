//go:generate go run github.com/99designs/gqlgen generate

package resolver

import (
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/lru"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/gin-gonic/gin"
	"tsb-service/internal/api/graphql"
	"tsb-service/internal/modules/product/application"
)

type Resolver struct {
	ProductService application.ProductService
}

// NewResolver constructs the Resolver with required services.
func NewResolver(productService application.ProductService) *Resolver {
	return &Resolver{ProductService: productService}
}

// GraphQLHandler Defining the Graphql handler
func GraphQLHandler(resolver *Resolver) gin.HandlerFunc {
	// Pass the injected resolver into the schema configuration.
	h := handler.New(graphql.NewExecutableSchema(graphql.Config{Resolvers: resolver}))

	h.AddTransport(transport.Options{})
	h.AddTransport(transport.GET{})
	h.AddTransport(transport.POST{})

	h.Use(extension.Introspection{})
	h.Use(extension.AutomaticPersistedQuery{
		//nolint:mnd // Store 100 queries in memory using Least Recently Used (LRU) algorithm
		Cache: lru.New[string](50),
	})
	// h.Use(extension.FixedComplexityLimit(5)) // https://gqlgen.com/reference/complexity/

	return func(c *gin.Context) {
		h.ServeHTTP(c.Writer, c.Request)
	}
}
