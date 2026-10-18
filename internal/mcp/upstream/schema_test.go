package upstream_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/validator/rules"

	"tsb-service/internal/mcp/upstream"
)

// loadSchema parses tsb-service's GraphQL schema files. It reads them from
// disk: the MCP server still never imports a backend package.
func loadSchema(t *testing.T) *ast.Schema {
	t.Helper()
	files, err := filepath.Glob("../../api/graphql/schema/*.graphql")
	if err != nil || len(files) == 0 {
		t.Fatalf("schema files: %v %v", files, err)
	}
	var srcs []*ast.Source
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		srcs = append(srcs, &ast.Source{Name: f, Input: string(b)})
	}
	schema, err := gqlparser.LoadSchema(srcs...)
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

// TestDocumentsMatchSchema catches a field or argument the API does not have
// before it reaches production as a GraphQL validation error.
func TestDocumentsMatchSchema(t *testing.T) {
	schema := loadSchema(t)
	for name, doc := range upstream.Documents {
		if _, errs := gqlparser.LoadQueryWithRules(schema, doc, rules.NewDefaultRules()); len(errs) > 0 {
			t.Errorf("%s: %v", name, errs)
		}
	}
}

// TestCouponInputMatchesSchema checks that every CouponInput field exists on
// both GraphQL inputs it is sent as.
func TestCouponInputMatchesSchema(t *testing.T) {
	schema := loadSchema(t)
	typ := reflect.TypeFor[upstream.CouponInput]()
	for _, input := range []string{"CreateCouponInput", "UpdateCouponInput"} {
		def := schema.Types[input]
		if def == nil {
			t.Fatalf("no input %s in the schema", input)
		}
		for f := range typ.Fields() {
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if def.Fields.ForName(name) == nil {
				t.Errorf("%s has no field %s (CouponInput.%s)", input, name, f.Name)
			}
		}
	}
}
