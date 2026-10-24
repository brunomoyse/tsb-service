// Package contract produces the committed, machine-readable descriptions of what
// tsb-service promises to its clients: the complete GraphQL SDL and the tsb-mcp tool
// list. Both are generated from code, committed under contract/ and checked for drift
// by the tests in this package and by CI (see docs/contract/README.md).
package contract

import (
	"strings"

	"github.com/vektah/gqlparser/v2/formatter"

	"tsb-service/internal/api/graphql"
)

// GraphQLSDL returns the SDL of the executable schema (every type, directive and
// scalar, with the schema files already merged by gqlgen). Operations are validated
// against it by the clients, and graphql-inspector diffs it between branches.
func GraphQLSDL() string {
	var sb strings.Builder
	formatter.NewFormatter(&sb, formatter.WithIndent("  ")).FormatSchema(graphql.NewExecutableSchema(graphql.Config{}).Schema())
	return sb.String()
}
