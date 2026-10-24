// Package migrationtest_test proves that the goose migrations of /migrations can be rolled back.
//
// Production rolls back with `tsb-migrate -cmd=down` (the rollback workflow), so a Down section
// that fails or does not undo its Up leaves a rollback broken exactly when it is needed. Every
// test runs on a real PostgreSQL of the production major (see testhelpers).
package migrationtest_test

import (
	"database/sql"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/testhelpers"
)

// schemaQueries describe the public schema: tables and columns, constraints, indexes, enum values,
// functions, triggers, views, sequences and extensions. Each returns one text column per row.
var schemaQueries = []string{
	`SELECT 'column ' || table_name || '.' || column_name || ' ' || data_type || ' ' || udt_name ||
	        ' null=' || is_nullable || ' default=' || COALESCE(column_default, '-') ||
	        ' len=' || COALESCE(character_maximum_length::text, '-') ||
	        ' gen=' || COALESCE(generation_expression, '-')
	   FROM information_schema.columns
	  WHERE table_schema = 'public' AND table_name <> 'goose_db_version'`,
	`SELECT 'constraint ' || c.conrelid::regclass || ' ' || c.conname || ' ' || pg_get_constraintdef(c.oid)
	   FROM pg_constraint c JOIN pg_namespace n ON n.oid = c.connamespace
	  WHERE n.nspname = 'public' AND c.conrelid::regclass::text <> 'goose_db_version'`,
	`SELECT 'index ' || indexdef FROM pg_indexes
	  WHERE schemaname = 'public' AND tablename <> 'goose_db_version'`,
	`SELECT 'enum ' || t.typname || ' ' || e.enumlabel
	   FROM pg_type t JOIN pg_enum e ON e.enumtypid = t.oid JOIN pg_namespace n ON n.oid = t.typnamespace
	  WHERE n.nspname = 'public'`,
	`SELECT 'function ' || pg_get_functiondef(p.oid)
	   FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
	  WHERE n.nspname = 'public' AND p.prokind IN ('f', 'p')`,
	`SELECT 'trigger ' || pg_get_triggerdef(t.oid) FROM pg_trigger t
	  WHERE NOT t.tgisinternal`,
	`SELECT 'view ' || table_name || ' ' || view_definition
	   FROM information_schema.views WHERE table_schema = 'public'`,
	`SELECT 'sequence ' || sequence_name FROM information_schema.sequences WHERE sequence_schema = 'public'`,
	`SELECT 'extension ' || extname FROM pg_extension`,
}

// snapshot returns the sorted description of the schema, one line per object.
func snapshot(t *testing.T, db *sql.DB) []string {
	t.Helper()
	var lines []string
	for _, q := range schemaQueries {
		rows, err := db.Query(q)
		require.NoError(t, err, q)
		for rows.Next() {
			var line string
			require.NoError(t, rows.Scan(&line))
			lines = append(lines, line)
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
	}
	sortStrings(lines)
	return lines
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// diff lists the lines only in one of the two snapshots.
func diff(a, b []string) string {
	inA, inB := map[string]bool{}, map[string]bool{}
	for _, l := range a {
		inA[l] = true
	}
	for _, l := range b {
		inB[l] = true
	}
	var out []string
	for _, l := range a {
		if !inB[l] {
			out = append(out, "- "+l)
		}
	}
	for _, l := range b {
		if !inA[l] {
			out = append(out, "+ "+l)
		}
	}
	return strings.Join(out, "\n")
}

// lossyDowns are the migrations whose Down cannot restore the schema it found, with the reason.
// Their Down must still run, and applying the migration again must give the same schema; only the
// "the schema before it is back" assertion is skipped. Adding a migration here needs a reason that
// is not "the Down is hard to write".
var lossyDowns = map[int64]string{
	20260331130000: "users.is_admin comes from the database that predates the migrations: no migration creates it, so the Down re-adds a column the migrations never had",
	20260401120000: "restaurant_config.ticket_templates is back with an empty default, not the retired template JSON that 20260315120000 wrote",
}

func setup(t *testing.T) (*sql.DB, string, []*goose.Migration) {
	t.Helper()
	td := testhelpers.SetupEmptyDatabase(t)
	require.NoError(t, goose.SetDialect("postgres"))
	goose.SetLogger(log.New(io.Discard, "", 0))
	dir := testhelpers.MigrationsDir(t)
	migrations, err := goose.CollectMigrations(dir, 0, goose.MaxVersion)
	require.NoError(t, err)
	require.NotEmpty(t, migrations)
	return td.DB.DB, dir, migrations
}

// TestMigrationsRoundTrip applies every migration, rolls everything back to an empty schema and
// applies everything again: the second schema must be the first one.
func TestMigrationsRoundTrip(t *testing.T) {
	db, dir, migrations := setup(t)
	empty := snapshot(t, db)

	require.NoError(t, goose.Up(db, dir), "up")
	full := snapshot(t, db)
	require.NotEmpty(t, full)

	require.NoError(t, goose.DownTo(db, dir, 0), "down to zero")
	version, err := goose.GetDBVersion(db)
	require.NoError(t, err)
	require.Zero(t, version)
	after := snapshot(t, db)
	require.Empty(t, diff(empty, after), "the schema left after rolling back every migration must equal the empty one")

	require.NoError(t, goose.Up(db, dir), "up again")
	require.Empty(t, diff(full, snapshot(t, db)), "the schema after up, down, up must equal the one after the first up")

	version, err = goose.GetDBVersion(db)
	require.NoError(t, err)
	require.Equal(t, migrations[len(migrations)-1].Version, version)
}

// TestEachMigrationRollsBack walks the migrations one by one: after each Up, its Down must restore
// the previous schema exactly, and applying it again must give the same schema as before. A Down
// that only works on an empty database or forgets an index shows up on the migration that wrote it.
func TestEachMigrationRollsBack(t *testing.T) {
	db, dir, migrations := setup(t)

	previous := snapshot(t, db)
	for _, m := range migrations {
		name := fmt.Sprintf("%d_%s", m.Version, strings.TrimSuffix(strings.TrimPrefix(m.Source, dir+"/"), ".sql"))
		var applied []string
		t.Run(name, func(t *testing.T) {
			require.NoError(t, goose.UpByOne(db, dir), "up")
			applied = snapshot(t, db)

			require.NoError(t, goose.Down(db, dir), "down")
			if reason, lossy := lossyDowns[m.Version]; lossy {
				t.Logf("lossy down, schema not compared: %s", reason)
			} else {
				require.Empty(t, diff(previous, snapshot(t, db)), "down must restore the schema before this migration")
			}

			require.NoError(t, goose.UpByOne(db, dir), "up again")
			require.Empty(t, diff(applied, snapshot(t, db)), "up after down must give the same schema")
		})
		// Keep walking from the migration's own result, so one broken Down is reported once and
		// does not fail every migration after it.
		if version, err := goose.GetDBVersion(db); err != nil || version < m.Version {
			_ = goose.UpByOne(db, dir) // the subtest already failed; only keep the walk going
		}
		if applied == nil {
			applied = snapshot(t, db)
		}
		previous = applied
	}
}

// TestOlderImageUpIsANoop is the code rollback with the schema left forward: the previous release
// (fewer migration files) runs `tsb-migrate -cmd=up` as the Helm pre-upgrade hook against a
// database that already has the newer migrations. It must neither fail nor touch the schema.
func TestOlderImageUpIsANoop(t *testing.T) {
	db, dir, migrations := setup(t)
	require.NoError(t, goose.Up(db, dir))
	full := snapshot(t, db)
	latest, err := goose.GetDBVersion(db)
	require.NoError(t, err)

	// The older image: the same files minus the last three migrations.
	older := t.TempDir()
	for _, m := range migrations[:len(migrations)-3] {
		data, err := os.ReadFile(m.Source)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(older, filepath.Base(m.Source)), data, 0o600))
	}

	require.NoError(t, goose.Up(db, older), "an older image must be able to run up on a newer schema")
	version, err := goose.GetDBVersion(db)
	require.NoError(t, err)
	require.Equal(t, latest, version)
	require.Empty(t, diff(full, snapshot(t, db)))
}
