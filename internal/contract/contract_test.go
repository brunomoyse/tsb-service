package contract_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"

	"tsb-service/internal/contract"
)

// committed reads a file of the repository-root contract/ directory.
func committed(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "contract", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// checkOrUpdate fails when the committed file differs from the generated content, unless
// UPDATE_CONTRACT=1 is set, in which case it rewrites the file.
func checkOrUpdate(t *testing.T, name string, want []byte) {
	t.Helper()
	path := filepath.Join("..", "..", "contract", name)
	if os.Getenv("UPDATE_CONTRACT") == "1" {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if got := committed(t, name); string(got) != string(want) {
		t.Fatalf("contract/%s is out of date with the code: run `go run ./cmd/contract` (or UPDATE_CONTRACT=1 go test ./internal/contract) and commit the result", name)
	}
}

func TestGraphQLSDLMatchesCommittedFile(t *testing.T) {
	checkOrUpdate(t, "schema.graphql", []byte(contract.GraphQLSDL()))
}

// The committed SDL must stand alone: clients and graphql-inspector load it without the
// gqlgen sources, so it has to parse and validate as a schema by itself.
func TestGraphQLSDLIsAStandaloneSchema(t *testing.T) {
	sdl := contract.GraphQLSDL()
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "schema.graphql", Input: sdl})
	if err != nil {
		t.Fatalf("SDL does not load: %v", err)
	}
	for _, root := range []string{"Query", "Mutation", "Subscription"} {
		if schema.Types[root] == nil {
			t.Errorf("root type %s missing from the SDL", root)
		}
	}
	for _, directive := range []string{"auth", "admin", "staff"} {
		if schema.Directives[directive] == nil {
			t.Errorf("directive @%s missing from the SDL", directive)
		}
	}
	if schema.Types["DateTime"] == nil {
		t.Error("scalar DateTime missing from the SDL")
	}
	if strings.Contains(sdl, "\r") {
		t.Error("SDL must use LF line endings")
	}
}

func TestMCPToolsMatchCommittedFile(t *testing.T) {
	got, err := contract.MCPToolsJSON(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	checkOrUpdate(t, "mcp-tools.json", got)
}

func TestMCPToolsAreStableAndDescriptionFree(t *testing.T) {
	a, err := contract.MCPToolsJSON(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	b, err := contract.MCPToolsJSON(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatal("two dumps of the tool list differ")
	}
	if strings.Contains(string(a), `"description"`) {
		t.Error("descriptions are prompt text and must not be part of the contract")
	}
	if !strings.Contains(string(a), `"name": "search_products"`) {
		t.Error("search_products missing from the tool list")
	}
}
