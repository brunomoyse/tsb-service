// Package mcp_test holds architecture guards for the MCP server.
package mcp_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestMCPOnlyTalksHTTP ensures no MCP package imports the backend's domain
// modules or database: the MCP server must go through the same GraphQL API as
// the dashboard so every backend side effect keeps happening.
func TestMCPOnlyTalksHTTP(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", "tsb-service/cmd/tsb-mcp", "tsb-service/internal/mcp/...").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	for pkg := range strings.FieldsSeq(string(out)) {
		if !strings.HasPrefix(pkg, "tsb-service/") {
			continue
		}
		if !strings.HasPrefix(pkg, "tsb-service/internal/mcp") && pkg != "tsb-service/cmd/tsb-mcp" {
			t.Errorf("the MCP server must not depend on %s", pkg)
		}
	}
	for _, banned := range []string{"github.com/jmoiron/sqlx", "github.com/lib/pq", "github.com/jackc/pgx"} {
		if strings.Contains(string(out), banned+"\n") {
			t.Errorf("the MCP server must not use %s", banned)
		}
	}
}
