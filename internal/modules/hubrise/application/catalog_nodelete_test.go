package application_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Order lines reference products, choices and choice groups, so the importer retires what HubRise
// dropped and never deletes it. This scans the production code of the module for any way to do it.
func TestTheImporterNeverDeletesMenuRows(t *testing.T) {
	forbidden := []*regexp.Regexp{
		regexp.MustCompile(`\bDeleteProduct\b`),
		regexp.MustCompile(`\bDeleteChoice\b`),
		regexp.MustCompile(`\bDeleteChoiceGroup\b`),
		regexp.MustCompile(`(?i)DELETE\s+FROM\s+(public\.)?(products|product_choices|product_choice_groups|product_categories)\b`),
		regexp.MustCompile(`(?i)\bTRUNCATE\s+(TABLE\s+)?(ONLY\s+)?(public\.)?(products|product_choices|product_choice_groups|product_categories)\b`),
	}
	scanned := 0
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "testhelpers") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, rerr := os.ReadFile(path)
		require.NoError(t, rerr)
		scanned++
		for _, re := range forbidden {
			assert.False(t, re.Match(src), "%s must not match %s", path, re)
		}
		return nil
	})
	require.NoError(t, err)
	assert.Greater(t, scanned, 20, "the scan found the module")
}
