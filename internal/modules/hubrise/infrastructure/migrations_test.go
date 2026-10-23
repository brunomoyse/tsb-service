package infrastructure_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/testhelpers"
)

const (
	// lastPreHubriseVersion is the newest migration that was on main before the HubRise work.
	// Every HubRise migration has to be newer, so an environment that already applied an earlier
	// HubRise task never meets a "missing" lower version.
	lastPreHubriseVersion int64 = 20261018120000

	m1Version int64 = 20261026120000
	m2Version int64 = 20261026130000
	m3Version int64 = 20261026140000
	m4Version int64 = 20261026150000
	m5Version int64 = 20261026160000
)

var hubriseTables = []string{
	"hubrise_connection", "hubrise_oauth_states", "hubrise_job_leases", "hubrise_catalog_links",
	"hubrise_orders", "hubrise_outbox", "hubrise_events", "hubrise_foreign_orders",
}

func newProvider(t *testing.T, td *testhelpers.TestDatabase) *goose.Provider {
	t.Helper()
	// goose's default (no AllowOutofOrder) is what production runs: a lower version that was never
	// applied while a higher one was is an error.
	p, err := goose.NewProvider(goose.DialectPostgres, td.DB.DB, os.DirFS("../../../../migrations"))
	require.NoError(t, err)
	return p
}

func tableExists(t *testing.T, td *testhelpers.TestDatabase, name string) bool {
	t.Helper()
	var reg *string
	require.NoError(t, td.DB.GetContext(t.Context(), &reg, `SELECT to_regclass('public.' || $1)::text`, name))
	return reg != nil
}

func TestHubriseMigrationsUpDownUp(t *testing.T) {
	td := testhelpers.SetupTestDatabase(t)
	p := newProvider(t, td)
	ctx := t.Context()

	for _, table := range hubriseTables {
		require.True(t, tableExists(t, td, table), table)
	}

	for round := 1; round <= 2; round++ {
		down, err := p.DownTo(ctx, lastPreHubriseVersion)
		require.NoError(t, err, "round %d: Down", round)
		require.NotEmpty(t, down)
		for _, table := range hubriseTables {
			assert.False(t, tableExists(t, td, table), "round %d: %s must be gone after Down", round, table)
		}
		v, err := p.GetDBVersion(ctx)
		require.NoError(t, err)
		assert.Equal(t, lastPreHubriseVersion, v)

		up, err := p.Up(ctx)
		require.NoError(t, err, "round %d: Up", round)
		require.NotEmpty(t, up)
		for _, table := range hubriseTables {
			assert.True(t, tableExists(t, td, table), "round %d: %s must exist after Up", round, table)
		}
	}
}

// Rows of the tables that existed before HubRise, by table.
func tableCounts(t *testing.T, td *testhelpers.TestDatabase) map[string]int {
	t.Helper()
	var tables []string
	require.NoError(t, td.DB.SelectContext(t.Context(), &tables, `
		SELECT table_name FROM information_schema.tables
		WHERE table_schema = 'public' AND table_type = 'BASE TABLE'
		  AND table_name NOT LIKE 'hubrise\_%' AND table_name NOT LIKE 'goose\_%'
		ORDER BY table_name`))
	counts := make(map[string]int, len(tables))
	for _, table := range tables {
		var n int
		require.NoError(t, td.DB.GetContext(t.Context(), &n, `SELECT count(*) FROM "`+table+`"`))
		counts[table] = n
	}
	return counts
}

func TestHubriseMigrationsLeaveExistingDataAlone(t *testing.T) {
	td := testhelpers.SetupTestDatabase(t)
	ctx := t.Context()
	p := newProvider(t, td)

	// Go back to the schema production has today, fill it, then roll forward.
	_, err := p.DownTo(ctx, lastPreHubriseVersion)
	require.NoError(t, err)
	fx := testhelpers.SeedTestData(t, td.DB)
	var orderID string
	require.NoError(t, td.DB.GetContext(ctx, &orderID, `
		INSERT INTO orders (user_id, order_status, order_type, is_online_payment, total_price, language)
		VALUES ($1, 'CONFIRMED', 'PICKUP', true, 25.5, 'fr') RETURNING id`, fx.RegularUser.ID))
	_, err = td.DB.ExecContext(ctx, `INSERT INTO order_product (order_id, product_id, unit_price, quantity, total_price, vat_rate_applied)
		VALUES ($1, $2, 12.75, 2, 25.5, 0.06)`, orderID, fx.SalmonSushi.ID)
	require.NoError(t, err)

	before := tableCounts(t, td)
	require.Positive(t, before["users"])
	require.Positive(t, before["orders"])
	require.Positive(t, before["products"])

	_, err = p.Up(ctx)
	require.NoError(t, err)

	assert.Equal(t, before, tableCounts(t, td), "existing tables keep every row")
	var status string
	require.NoError(t, td.DB.GetContext(ctx, &status, `SELECT order_status FROM orders WHERE id = $1`, orderID))
	assert.Equal(t, "CONFIRMED", status)

	for _, table := range hubriseTables {
		var n int
		require.NoError(t, td.DB.GetContext(ctx, &n, `SELECT count(*) FROM `+table))
		assert.Zero(t, n, "%s starts empty: no backfill", table)
	}
}

func TestHubriseMigrationsApplyOneByOneWithoutMissingVersions(t *testing.T) {
	td := testhelpers.SetupTestDatabase(t)
	ctx := t.Context()
	p := newProvider(t, td)

	_, err := p.DownTo(ctx, lastPreHubriseVersion)
	require.NoError(t, err)

	// An environment that deployed task 1 only (the test server deploys on every push), then later
	// tasks: each step must apply cleanly with out-of-order disabled.
	for _, v := range []int64{m1Version, m2Version, m3Version, m4Version, m5Version} {
		res, err := p.UpTo(ctx, v)
		require.NoError(t, err, "UpTo %d", v)
		require.Len(t, res, 1)
		assert.Equal(t, v, res[0].Source.Version)
	}
	res, err := p.Up(ctx)
	require.NoError(t, err, "the later HubRise migrations apply after the earlier ones")
	for _, r := range res {
		assert.Greater(t, r.Source.Version, m5Version)
	}
	pending, err := p.HasPending(ctx)
	require.NoError(t, err)
	assert.False(t, pending)
}

func TestMigrationVersionsAreStrictlyOrdered(t *testing.T) {
	td := testhelpers.SetupTestDatabase(t)
	p := newProvider(t, td)

	sources := p.ListSources()
	require.NotEmpty(t, sources)
	var found bool
	for i, s := range sources {
		if i > 0 {
			assert.Greater(t, s.Version, sources[i-1].Version, "%s must be newer than %s", s.Path, sources[i-1].Path)
		}
		if s.Version == lastPreHubriseVersion {
			found = true
			assert.Equal(t, i, indexOfLastPre(sources), "the pre-HubRise anchor is a real migration")
		}
		if strings.Contains(s.Path, "hubrise") {
			assert.Greater(t, s.Version, lastPreHubriseVersion, "%s must be newer than every pre-HubRise migration", s.Path)
		}
	}
	assert.True(t, found, "lastPreHubriseVersion must name an existing migration")
	assert.Greater(t, m1Version, lastPreHubriseVersion)
	assert.Less(t, m1Version, m2Version)
	assert.Less(t, m2Version, m3Version)
	assert.Less(t, m3Version, m4Version)
	assert.Less(t, m4Version, m5Version)

	// The database really is at the newest version after the template migrations.
	v, err := p.GetDBVersion(context.Background())
	require.NoError(t, err)
	assert.Equal(t, sources[len(sources)-1].Version, v)
}

func indexOfLastPre(sources []*goose.Source) int {
	idx := -1
	for i, s := range sources {
		if s.Version <= lastPreHubriseVersion {
			idx = i
		}
	}
	return idx
}

// translationTables are the tables that got the source column, with the column holding the language.
var translationTables = []string{
	"product_translations", "product_category_translations", "product_choice_translations", "product_choice_group_translations",
}

func columnExists(t *testing.T, td *testhelpers.TestDatabase, table, column string) bool {
	t.Helper()
	var n int
	require.NoError(t, td.DB.GetContext(t.Context(), &n, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2`, table, column))
	return n == 1
}

// M4 and M5 add columns to tables that hold production data: the existing rows must come out
// 'manual' and available, and Down must take the columns away again.
func TestSourceAndAvailabilityColumnsBackfillByDefault(t *testing.T) {
	td := testhelpers.SetupTestDatabase(t)
	ctx := t.Context()
	p := newProvider(t, td)

	_, err := p.DownTo(ctx, m3Version)
	require.NoError(t, err)
	for _, table := range translationTables {
		assert.False(t, columnExists(t, td, table, "source"), table)
	}
	assert.False(t, columnExists(t, td, "product_choices", "is_available"))

	fx := testhelpers.SeedTestData(t, td.DB)
	groupID, choiceID := uuid.New(), uuid.New()
	_, err = td.DB.ExecContext(ctx, `INSERT INTO product_choice_groups (id, product_id) VALUES ($1, $2)`, groupID, fx.SalmonSushi.ID)
	require.NoError(t, err)
	_, err = td.DB.ExecContext(ctx, `INSERT INTO product_choices (id, product_id, choice_group_id) VALUES ($1, $2, $3)`, choiceID, fx.SalmonSushi.ID, groupID)
	require.NoError(t, err)
	_, err = td.DB.ExecContext(ctx, `INSERT INTO product_choice_translations (product_choice_id, locale, name) VALUES ($1, 'fr', 'Soja')`, choiceID)
	require.NoError(t, err)
	_, err = td.DB.ExecContext(ctx, `INSERT INTO product_choice_group_translations (product_choice_group_id, locale, name) VALUES ($1, 'fr', 'Sauce')`, groupID)
	require.NoError(t, err)
	before := tableCounts(t, td)

	_, err = p.Up(ctx)
	require.NoError(t, err)
	assert.Equal(t, before, tableCounts(t, td), "no row is lost")

	for _, table := range translationTables {
		require.True(t, columnExists(t, td, table, "source"), table)
		var n, manual int
		require.NoError(t, td.DB.GetContext(ctx, &n, `SELECT count(*) FROM `+table))
		require.NoError(t, td.DB.GetContext(ctx, &manual, `SELECT count(*) FROM `+table+` WHERE source = 'manual'`))
		assert.Positive(t, n, table)
		assert.Equal(t, n, manual, "%s: every existing translation is manual", table)
	}
	var available bool
	require.NoError(t, td.DB.GetContext(ctx, &available, `SELECT is_available FROM product_choices WHERE id = $1`, choiceID))
	assert.True(t, available, "an existing choice stays available")

	_, err = p.DownTo(ctx, m3Version)
	require.NoError(t, err)
	for _, table := range translationTables {
		assert.False(t, columnExists(t, td, table, "source"), "%s: Down drops the column", table)
	}
	assert.False(t, columnExists(t, td, "product_choices", "is_available"))
}

// A migration that cannot get its lock must fail within lock_timeout instead of queueing every
// writer of the table behind an ACCESS EXCLUSIVE request.
func TestExistingTableMigrationsFailFastWhenTheTableIsBusy(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version int64
		table   string
		prior   int64
	}{
		{"translation source", m4Version, "product_translations", m3Version},
		{"choice availability", m5Version, "product_choices", m4Version},
	} {
		t.Run(tc.name, func(t *testing.T) {
			td := testhelpers.SetupTestDatabase(t)
			ctx := t.Context()
			p := newProvider(t, td)
			_, err := p.DownTo(ctx, tc.prior)
			require.NoError(t, err)

			// An open transaction that has read the table holds ACCESS SHARE until it ends.
			tx, err := td.DB.BeginTxx(ctx, nil)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback() }()
			var n int
			require.NoError(t, tx.GetContext(ctx, &n, `SELECT count(*) FROM `+tc.table))

			start := time.Now()
			_, err = p.UpTo(ctx, tc.version)
			elapsed := time.Since(start)
			require.Error(t, err, "the busy table makes the migration fail")
			assert.Contains(t, err.Error(), "lock timeout")
			assert.GreaterOrEqual(t, elapsed, 4*time.Second, "it waited for the lock timeout")
			assert.Less(t, elapsed, 20*time.Second, "and no longer")

			v, err := p.GetDBVersion(ctx)
			require.NoError(t, err)
			assert.Equal(t, tc.prior, v, "the failed migration changed nothing")

			// Once the table is free, the same migration applies.
			require.NoError(t, tx.Rollback())
			_, err = p.UpTo(ctx, tc.version)
			require.NoError(t, err)
		})
	}
}
