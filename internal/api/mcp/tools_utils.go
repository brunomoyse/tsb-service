package mcp

import (
	"context"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type healthOut struct {
	Status  string    `json:"status"`
	Version string    `json:"version"`
	Time    time.Time `json:"time"`
}

func registerUtilsTools(s *mcpsdk.Server, _ Deps) {
	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "health_check",
			Description: "Returns the current MCP server status and timestamp. Useful to verify connectivity from the chatbot.",
		},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, _ struct{}) (*mcpsdk.CallToolResult, healthOut, error) {
			return nil, healthOut{
				Status:  "ok",
				Version: "1.0.0",
				Time:    time.Now().UTC(),
			}, nil
		},
	)
}
