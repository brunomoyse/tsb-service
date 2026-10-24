package db_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/pkg/db"
)

type base struct {
	ID string `db:"id"`
}

type row struct {
	base
	Name    string  `db:"name"`
	Note    *string `db:"note"`
	Skipped string  `db:"-"`
	hidden  string
}

func TestColumns(t *testing.T) {
	_ = row{}.hidden

	t.Run("lists the db tags in field order, embedded structs included", func(t *testing.T) {
		assert.Equal(t, "id, name, note", db.Columns(row{}, ""))
	})

	t.Run("prefixes every column with the alias", func(t *testing.T) {
		assert.Equal(t, "o.id, o.name, o.note", db.Columns(row{}, "o"))
	})

	t.Run("accepts a pointer", func(t *testing.T) {
		assert.Equal(t, "id, name, note", db.Columns(&row{}, ""))
	})

	t.Run("panics on a field without a db tag", func(t *testing.T) {
		type untagged struct {
			ID   string `db:"id"`
			Name string
		}
		assert.PanicsWithValue(t, "db.Columns: field db_test.untagged.Name has no db tag", func() { db.Columns(untagged{}, "") })
	})

	t.Run("panics on something that is not a struct", func(t *testing.T) {
		assert.PanicsWithValue(t, "db.Columns: string is not a struct", func() { db.Columns("x", "") })
	})

	t.Run("panics on a struct without columns", func(t *testing.T) {
		type empty struct{}
		assert.PanicsWithValue(t, "db.Columns: db_test.empty has no db column", func() { db.Columns(empty{}, "") })
	})
}

// selectStar matches "SELECT *" and an "alias.*" column in SQL text.
var selectStar = regexp.MustCompile(`(?i)\bSELECT\s+\*|\b[a-z_][a-z0-9_]*\.\*`)

// TestNoSelectStar keeps production queries on explicit column lists. Migrations run before the
// new pods replace the old ones, so the running code must survive an added column, and sqlx fails
// a scan into a struct when the row has a column the struct lacks.
func TestNoSelectStar(t *testing.T) {
	root := filepath.Join("..", "..")
	var offenders []string
	for _, dir := range []string{"internal", "pkg", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for i, line := range strings.Split(string(src), "\n") {
				if code, _, _ := strings.Cut(line, "//"); selectStar.MatchString(code) {
					offenders = append(offenders, path+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
				}
			}
			return nil
		})
		require.NoError(t, err)
	}
	assert.Empty(t, offenders, "list the columns (db.Columns) instead of SELECT *")
}
