package testhelpers

import (
	"net/http"
	"net/http/httptest"

	"github.com/99designs/gqlgen/graphql/handler"

	"tsb-service/internal/api/graphql"
	"tsb-service/internal/api/graphql/directives"
	"tsb-service/internal/api/graphql/resolver"
	addressApplication "tsb-service/internal/modules/address/application"
	orderApplication "tsb-service/internal/modules/order/application"
	paymentApplication "tsb-service/internal/modules/payment/application"
	productApplication "tsb-service/internal/modules/product/application"
	userApplication "tsb-service/internal/modules/user/application"
	"tsb-service/pkg/utils"
)

// GraphQLTestClient wraps a GraphQL test server and provides helper methods
type GraphQLTestClient struct {
	server   *httptest.Server
	resolver *resolver.Resolver
	handler  http.Handler
}

// NewGraphQLTestClient creates a new GraphQL test client with the given resolver
func NewGraphQLTestClient(r *resolver.Resolver, jwtSecret string) *GraphQLTestClient {
	// Setup GraphQL handler with directives
	cfg := graphql.Config{Resolvers: r}
	cfg.Directives.Auth = directives.Auth
	cfg.Directives.Admin = directives.Admin

	srv := handler.NewDefaultServer(graphql.NewExecutableSchema(cfg))

	// Create a simple HTTP handler that wraps the GraphQL server with auth and DataLoaders
	httpHandler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ctx := req.Context()

		// Attach DataLoaders to context (required for resolvers)
		ctx = addressApplication.AttachDataLoaders(ctx, r.AddressService)
		ctx = productApplication.AttachDataLoaders(ctx, r.ProductService)
		ctx = paymentApplication.AttachDataLoaders(ctx, r.PaymentService)
		ctx = orderApplication.AttachDataLoaders(ctx, r.OrderService)
		ctx = userApplication.AttachDataLoaders(ctx, r.UserService)

		// Extract Authorization header and set user context if present
		authHeader := req.Header.Get("Authorization")
		if authHeader != "" && len(authHeader) > 7 && authHeader[:7] == "Bearer " {
			token := authHeader[7:]
			// Parse and validate the token to extract userID and isAdmin
			userID, isAdmin, err := ParseTestToken(token, jwtSecret)
			if err == nil {
				ctx = utils.SetUserID(ctx, userID)
				ctx = utils.SetIsAdmin(ctx, isAdmin)
			}
		}

		// Extract Accept-Language header for multi-language support
		language := req.Header.Get("Accept-Language")
		if language == "" {
			language = "en" // Default to English
		}
		ctx = utils.SetLang(ctx, language)

		req = req.WithContext(ctx)
		srv.ServeHTTP(w, req)
	})

	// Create test server
	server := httptest.NewServer(httpHandler)

	return &GraphQLTestClient{
		server:   server,
		resolver: r,
		handler:  httpHandler,
	}
}

// Handler returns the http.Handler for direct use with gqlgen client
func (c *GraphQLTestClient) Handler() http.Handler {
	return c.handler
}

// URL returns the test server URL
func (c *GraphQLTestClient) URL() string {
	return c.server.URL + "/api/v1/graphql"
}

// Close shuts down the test server
func (c *GraphQLTestClient) Close() {
	c.server.Close()
}

// NewRequest creates a new HTTP request with optional auth token
func (c *GraphQLTestClient) NewRequest(query string, token *string, language *string) *http.Request {
	req := httptest.NewRequest("POST", c.URL(), nil)
	req.Header.Set("Content-Type", "application/json")

	if token != nil && *token != "" {
		req.Header.Set("Authorization", "Bearer "+*token)
	}

	if language != nil && *language != "" {
		req.Header.Set("Accept-Language", *language)
	} else {
		req.Header.Set("Accept-Language", "en") // Default language
	}

	return req
}
