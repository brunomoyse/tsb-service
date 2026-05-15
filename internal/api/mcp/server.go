// Package mcp wires the Model Context Protocol surface that the
// management chatbot (Telegram bot → LLM) uses to administer the
// restaurant. It re-uses the same application services as the GraphQL
// layer, with the same admin-role enforcement performed by the OIDC
// middleware upstream.
package mcp

import (
	"net/http"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	couponApp "tsb-service/internal/modules/coupon/application"
	orderApp "tsb-service/internal/modules/order/application"
	productApp "tsb-service/internal/modules/product/application"
	restaurantApp "tsb-service/internal/modules/restaurant/application"
)

// Deps groups every application service the MCP tools need. The struct
// is built once in main.go and passed to RegisterHandler.
type Deps struct {
	Product    productApp.ProductService
	Coupon     couponApp.CouponService
	Restaurant restaurantApp.RestaurantService
	Order      orderApp.OrderService
}

// NewServer creates a fresh MCP server with all tools and resources
// registered. A new instance is created per HTTP request by the
// streamable handler so each session has its own transport state.
func NewServer(deps Deps) *mcpsdk.Server {
	s := mcpsdk.NewServer(&mcpsdk.Implementation{
		Name:    "tsb-service",
		Title:   "Tokyo Sushi Bar — Restaurant Management",
		Version: "1.0.0",
	}, nil)

	registerProductTools(s, deps)
	registerChoiceTools(s, deps)
	registerCouponTools(s, deps)
	registerSettingsTools(s, deps)
	registerAnalyticsTools(s, deps)
	registerUtilsTools(s, deps)
	registerResources(s, deps)

	return s
}

// Handler returns the streamable HTTP handler that the Gin router
// mounts at /api/v1/mcp. The OIDC + RequireAdmin middlewares run
// upstream, so by the time the handler is invoked the context already
// carries a verified admin identity.
func Handler(deps Deps) http.Handler {
	return mcpsdk.NewStreamableHTTPHandler(
		func(*http.Request) *mcpsdk.Server { return NewServer(deps) },
		nil,
	)
}

// errorResult marks the tool call as an error so the LLM knows it
// failed and can surface the reason to the user.
func errorResult(msg string) *mcpsdk.CallToolResult {
	return &mcpsdk.CallToolResult{
		IsError: true,
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: msg}},
	}
}

