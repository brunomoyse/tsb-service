package contract

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"tsb-service/internal/mcp/tools"
)

// Tool is the part of an MCP tool that callers depend on: its name and the JSON schema
// of its arguments. Descriptions are removed on purpose: they are prompt text that
// changes often and carries no contract.
type Tool struct {
	Name        string `json:"name"`
	InputSchema any    `json:"inputSchema"`
}

// MCPToolsJSON returns the tsb-mcp tool list (sorted by name, indented JSON, trailing
// newline). The real tools are registered on an in-memory server and listed through a
// client session, so the output is exactly what an MCP client sees.
func MCPToolsJSON(ctx context.Context) ([]byte, error) {
	server := mcp.NewServer(&mcp.Implementation{Name: "tsb-mcp", Version: "contract"}, nil)
	// Registration only captures the dependencies in closures: nothing is called.
	tools.Register(server, &tools.Deps{Loc: time.UTC, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})

	ct, st := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		return nil, fmt.Errorf("connect server: %w", err)
	}
	defer func() { _ = ss.Close() }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "contract", Version: "v0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("connect client: %w", err)
	}
	defer func() { _ = cs.Close() }()

	var out []Tool
	for t, err := range cs.Tools(ctx, nil) {
		if err != nil {
			return nil, fmt.Errorf("list tools: %w", err)
		}
		out = append(out, Tool{Name: t.Name, InputSchema: stripDescriptions(t.InputSchema)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// stripDescriptions drops every "description" key of a decoded JSON schema. A property
// that is itself called "description" lives under "properties" and is a schema object, so
// only string-valued "description" keys are dropped.
func stripDescriptions(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			if _, isText := child.(string); isText && k == "description" {
				delete(x, k)
				continue
			}
			x[k] = stripDescriptions(child)
		}
		return x
	case []any:
		for i := range x {
			x[i] = stripDescriptions(x[i])
		}
		return x
	default:
		return v
	}
}
