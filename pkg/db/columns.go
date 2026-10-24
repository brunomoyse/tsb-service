package db

import (
	"fmt"
	"reflect"
	"strings"
)

// Columns returns the comma-separated column list a struct is scanned from, built from its `db`
// tags, each column prefixed with "alias." when alias is not empty. Embedded structs without a tag
// contribute their own columns.
//
// Queries list their columns with it instead of SELECT *: migrations run before the new pods
// replace the old ones, so the code still serving must keep working when a release adds a
// column, and sqlx refuses to scan a column the struct has no field for.
//
// It panics on an exported field without a `db` tag: such a field is a mistake, and the panic
// fires when the package initialises, so every test of the package catches it.
func Columns(v any, alias string) string {
	t := reflect.TypeOf(v)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		panic(fmt.Sprintf("db.Columns: %s is not a struct", t))
	}
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	cols := appendColumns(nil, t, prefix)
	if len(cols) == 0 {
		panic(fmt.Sprintf("db.Columns: %s has no db column", t))
	}
	return strings.Join(cols, ", ")
}

func appendColumns(cols []string, t reflect.Type, prefix string) []string {
	for f := range t.Fields() {
		tag, tagged := f.Tag.Lookup("db")
		switch {
		case tag == "-":
		case f.Anonymous && !tagged && f.Type.Kind() == reflect.Struct:
			cols = appendColumns(cols, f.Type, prefix)
		case !f.IsExported():
		case !tagged || tag == "":
			panic(fmt.Sprintf("db.Columns: field %s.%s has no db tag", t, f.Name))
		default:
			cols = append(cols, prefix+tag)
		}
	}
	return cols
}
