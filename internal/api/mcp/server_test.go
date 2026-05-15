package mcp_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	mcpapi "tsb-service/internal/api/mcp"
)

// TestHealthCheck exercises the MCP wiring end-to-end via the
// in-memory transport: server construction, tool registration,
// JSON-RPC handshake, and structured output decoding. The Deps fields
// are left nil because health_check does not touch any service.
func TestHealthCheck(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	server := mcpapi.NewServer(mcpapi.Deps{})

	t1, t2 := mcpsdk.NewInMemoryTransports()
	if _, err := server.Connect(ctx, t1, nil); err != nil {
		t.Fatalf("server.Connect: %v", err)
	}

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      "health_check",
		Arguments: struct{}{},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("health_check returned error: %+v", result.Content)
	}

	// Structured output is rendered as JSON inside StructuredContent.
	if result.StructuredContent == nil {
		t.Fatalf("expected structured content, got nil")
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var out struct {
		Status  string `json:"status"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Status != "ok" {
		t.Errorf("status = %q, want ok", out.Status)
	}
	if out.Version == "" {
		t.Errorf("version is empty")
	}
}

// TestListToolsAdvertised verifies the full surface is registered (so
// adding new tools fails this test if their registration is skipped).
func TestListToolsAdvertised(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	server := mcpapi.NewServer(mcpapi.Deps{})
	t1, t2 := mcpsdk.NewInMemoryTransports()
	if _, err := server.Connect(ctx, t1, nil); err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	want := []string{
		"list_products", "get_product", "create_product", "update_product",
		"toggle_product_availability", "list_categories",
		"list_product_choice_groups", "create_product_choice_group",
		"update_product_choice_group", "delete_product_choice_group",
		"create_product_choice", "update_product_choice", "delete_product_choice",
		"list_coupons", "get_coupon", "create_coupon", "update_coupon", "disable_coupon",
		"get_restaurant_config", "toggle_ordering", "set_preparation_minutes",
		"set_opening_hours", "set_ordering_hours", "list_schedule_overrides",
		"upsert_schedule_override", "delete_schedule_override",
		"get_customer_stats", "health_check",
	}

	have := make(map[string]struct{}, len(tools.Tools))
	for _, t := range tools.Tools {
		have[t.Name] = struct{}{}
	}
	for _, name := range want {
		if _, ok := have[name]; !ok {
			t.Errorf("missing tool: %s", name)
		}
	}
}
