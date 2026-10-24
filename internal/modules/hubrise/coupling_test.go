package hubrise_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The shop's own code knows nothing of HubRise: the order, payment, product, user and every other
// module reach the integration through hooks and observers that the composition root installs, and
// the integration reaches them through the resolver. This test reads the imports of the whole
// repository and fails when production code outside the places below starts importing the
// integration. Adding such an import is a decision, so the list is the thing to edit then.
func TestOnlyTheCompositionCodeImportsTheIntegration(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	allowed := []string{
		"internal/modules/hubrise",      // the integration itself
		"pkg/hubrise",                   // its API client and mock
		"internal/api/graphql/resolver", // the hooks, the transitioner and the admin API
		"internal/api/hubrisewire",      // the composition root
		"cmd/app",                       // which calls it
	}
	imports := func(path string) []string {
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		require.NoError(t, err, path)
		var out []string
		for _, spec := range f.Imports {
			p, err := strconv.Unquote(spec.Path.Value)
			require.NoError(t, err)
			out = append(out, p)
		}
		return out
	}

	var offenders []string
	checked := 0
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "node_modules" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, filepath.Dir(path))
		require.NoError(t, err)
		rel = filepath.ToSlash(rel)
		checked++
		for _, imp := range imports(path) {
			if !strings.HasPrefix(imp, "tsb-service/internal/modules/hubrise") && !strings.HasPrefix(imp, "tsb-service/pkg/hubrise") {
				continue
			}
			ok := slices.ContainsFunc(allowed, func(a string) bool { return rel == a || strings.HasPrefix(rel, a+"/") })
			if !ok {
				offenders = append(offenders, rel+" imports "+imp)
			}
		}
		return nil
	}))
	assert.Greater(t, checked, 200, "the walk reached the sources")
	assert.Empty(t, offenders, "production code outside the composition root must reach HubRise through the hooks")
}
