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

	m1Version int64 = 20261024120000
	m2Version int64 = 20261024130000
	m3Version int64 = 20261024140000
	// m4Version adds the source column to the translation tables.
	m4Version int64 = 20261024150000
	// m5Version adds product_choices.is_available (inventory can sell an option out).
	m5Version int64 = 20261024160000
	// m6Version adds orders.channel, the one change to an existing table.
	m6Version int64 = 20261024170000
	// m7Version creates hubrise_customers, the link to the customer list.
	m7Version int64 = 20261024180000
	// m8Version adds the dashboard-owned menu: publish columns, the publisher row, the published items.
	m8Version int64 = 20261024190000
)

var hubriseTables = []string{
	"hubrise_connection", "hubrise_oauth_states", "hubrise_job_leases", "hubrise_catalog_links",
	"hubrise_orders", "hubrise_outbox", "hubrise_events", "hubrise_foreign_orders", "hubrise_customers",
	"hubrise_catalog_publish", "hubrise_published_items",
}

// startsEmpty are the HubRise tables that hold no row right after the migrations (the publisher's
// singleton row is the one exception: the migration inserts it).
func startsEmpty() []string {
	var out []string
	for _, t := range hubriseTables {
		if t != "hubrise_catalog_publish" {
			out = append(out, t)
		}
	}
	return out
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

	for _, table := range startsEmpty() {
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
	for _, v := range []int64{m1Version, m2Version, m3Version, m4Version, m5Version, m6Version, m7Version, m8Version} {
		res, err := p.UpTo(ctx, v)
		require.NoError(t, err, "UpTo %d", v)
		require.Len(t, res, 1)
		assert.Equal(t, v, res[0].Source.Version)
	}
	res, err := p.Up(ctx)
	require.NoError(t, err, "the later HubRise migrations apply after the earlier ones")
	for _, r := range res {
		assert.Greater(t, r.Source.Version, m8Version)
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
	assert.Less(t, m5Version, m6Version)
	assert.Less(t, m6Version, m7Version)
	assert.Less(t, m7Version, m8Version)

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

func ordersChannelColumn(t *testing.T, td *testhelpers.TestDatabase) (nullable string, exists bool) {
	t.Helper()
	var rows []string
	require.NoError(t, td.DB.SelectContext(t.Context(), &rows, `
		SELECT is_nullable FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'orders' AND column_name = 'channel'`))
	if len(rows) == 0 {
		return "", false
	}
	return rows[0], true
}

func TestOrdersChannelMigrationUpDownAndExistingRows(t *testing.T) {
	td := testhelpers.SetupTestDatabase(t)
	ctx := t.Context()
	p := newProvider(t, td)

	nullable, ok := ordersChannelColumn(t, td)
	require.True(t, ok)
	assert.Equal(t, "YES", nullable, "nullable: old orders and old code stay valid")

	_, err := p.DownTo(ctx, m3Version)
	require.NoError(t, err)
	_, ok = ordersChannelColumn(t, td)
	assert.False(t, ok, "Down drops the column")

	// An order written before the migration reads back with a NULL channel afterwards.
	fx := testhelpers.SeedTestData(t, td.DB)
	var id string
	require.NoError(t, td.DB.GetContext(ctx, &id, `
		INSERT INTO orders (user_id, order_status, order_type, is_online_payment, total_price, language)
		VALUES ($1, 'CONFIRMED', 'PICKUP', false, 10, 'fr') RETURNING id`, fx.RegularUser.ID))
	_, err = p.Up(ctx)
	require.NoError(t, err)
	var channel *string
	require.NoError(t, td.DB.GetContext(ctx, &channel, `SELECT channel FROM orders WHERE id = $1`, id))
	assert.Nil(t, channel)
	_, err = td.DB.ExecContext(ctx, `UPDATE orders SET channel = 'anything' WHERE id = $1`, id)
	assert.NoError(t, err, "no CHECK: a new channel needs no migration")
}

// The ALTER needs an exclusive lock on orders. With a transaction holding the table open it must give
// up after the 5 s lock_timeout instead of queueing behind it (and blocking every order query behind
// itself). Once the blocker is gone the migration applies.
func TestOrdersChannelMigrationDoesNotBlockOrdersForLong(t *testing.T) {
	td := testhelpers.SetupTestDatabase(t)
	ctx := t.Context()
	p := newProvider(t, td)
	_, err := p.DownTo(ctx, m3Version)
	require.NoError(t, err)

	blocker, err := td.DB.BeginTxx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = blocker.Rollback() }()
	var n int
	require.NoError(t, blocker.GetContext(ctx, &n, `SELECT count(*) FROM orders`)) // ACCESS SHARE until the end

	start := time.Now()
	_, err = p.UpTo(ctx, m6Version)
	elapsed := time.Since(start)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lock timeout")
	assert.GreaterOrEqual(t, elapsed, 4*time.Second, "it waited for the lock")
	assert.Less(t, elapsed, 15*time.Second, "and gave up")
	_, ok := ordersChannelColumn(t, td)
	assert.False(t, ok)

	require.NoError(t, blocker.Rollback())
	_, err = p.UpTo(ctx, m6Version)
	require.NoError(t, err)
	_, ok = ordersChannelColumn(t, td)
	assert.True(t, ok)
}

func choiceAvailableColumn(t *testing.T, td *testhelpers.TestDatabase) (nullable, def string, exists bool) {
	t.Helper()
	var rows []struct {
		Nullable string  `db:"is_nullable"`
		Default  *string `db:"column_default"`
	}
	require.NoError(t, td.DB.SelectContext(t.Context(), &rows, `
		SELECT is_nullable, column_default FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'product_choices' AND column_name = 'is_available'`))
	if len(rows) == 0 {
		return "", "", false
	}
	if rows[0].Default != nil {
		def = *rows[0].Default
	}
	return rows[0].Nullable, def, true
}

// A choice that existed before the migration stays available, and Down takes the column away again.
func TestChoiceIsAvailableMigrationUpDownAndExistingRows(t *testing.T) {
	td := testhelpers.SetupTestDatabase(t)
	ctx := t.Context()
	p := newProvider(t, td)

	nullable, def, ok := choiceAvailableColumn(t, td)
	require.True(t, ok)
	assert.Equal(t, "NO", nullable)
	assert.Equal(t, "true", def)

	_, err := p.DownTo(ctx, m3Version)
	require.NoError(t, err)
	_, _, ok = choiceAvailableColumn(t, td)
	assert.False(t, ok, "Down drops the column")

	fx := testhelpers.SeedTestData(t, td.DB)
	var groupID, choiceID string
	require.NoError(t, td.DB.GetContext(ctx, &groupID, `INSERT INTO product_choice_groups (product_id) VALUES ($1) RETURNING id`, fx.SalmonSushi.ID))
	require.NoError(t, td.DB.GetContext(ctx, &choiceID, `
		INSERT INTO product_choices (product_id, choice_group_id, price_modifier) VALUES ($1, $2, 0.50) RETURNING id`, fx.SalmonSushi.ID, groupID))

	_, err = p.Up(ctx)
	require.NoError(t, err)
	var available bool
	require.NoError(t, td.DB.GetContext(ctx, &available, `SELECT is_available FROM product_choices WHERE id = $1`, choiceID))
	assert.True(t, available, "every existing choice stays available")
}

// product_choices is read on every order: the ALTER must give up after the lock timeout instead of
// queueing behind a reader and blocking the readers behind itself.
func TestChoiceIsAvailableMigrationDoesNotBlockChoicesForLong(t *testing.T) {
	td := testhelpers.SetupTestDatabase(t)
	ctx := t.Context()
	p := newProvider(t, td)
	_, err := p.DownTo(ctx, m3Version)
	require.NoError(t, err)

	blocker, err := td.DB.BeginTxx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = blocker.Rollback() }()
	var n int
	require.NoError(t, blocker.GetContext(ctx, &n, `SELECT count(*) FROM product_choices`)) // ACCESS SHARE until the end

	start := time.Now()
	_, err = p.UpTo(ctx, m5Version)
	elapsed := time.Since(start)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lock timeout")
	assert.GreaterOrEqual(t, elapsed, 4*time.Second, "it waited for the lock")
	assert.Less(t, elapsed, 15*time.Second, "and gave up")
	_, _, ok := choiceAvailableColumn(t, td)
	assert.False(t, ok)

	require.NoError(t, blocker.Rollback())
	_, err = p.UpTo(ctx, m5Version)
	require.NoError(t, err)
	_, _, ok = choiceAvailableColumn(t, td)
	assert.True(t, ok)
}

// translationTables are the tables that got the source column.
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

// M4 adds a column to tables that hold production data: the existing rows must come out 'manual'
// and Down must take the column away again.
func TestTranslationSourceColumnBackfillsByDefault(t *testing.T) {
	td := testhelpers.SetupTestDatabase(t)
	ctx := t.Context()
	p := newProvider(t, td)

	_, err := p.DownTo(ctx, m3Version)
	require.NoError(t, err)
	for _, table := range translationTables {
		assert.False(t, columnExists(t, td, table, "source"), table)
	}

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

	_, err = p.DownTo(ctx, m3Version)
	require.NoError(t, err)
	for _, table := range translationTables {
		assert.False(t, columnExists(t, td, table, "source"), "%s: Down drops the column", table)
	}
}

// A migration that cannot get its lock must fail within lock_timeout instead of queueing every
// writer of the table behind an ACCESS EXCLUSIVE request.
func TestTranslationSourceMigrationFailsFastWhenTheTableIsBusy(t *testing.T) {
	td := testhelpers.SetupTestDatabase(t)
	ctx := t.Context()
	p := newProvider(t, td)
	_, err := p.DownTo(ctx, m3Version)
	require.NoError(t, err)

	// An open transaction that has read the table holds ACCESS SHARE until it ends.
	tx, err := td.DB.BeginTxx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	var n int
	require.NoError(t, tx.GetContext(ctx, &n, `SELECT count(*) FROM product_translations`))

	start := time.Now()
	_, err = p.UpTo(ctx, m4Version)
	elapsed := time.Since(start)
	require.Error(t, err, "the busy table makes the migration fail")
	assert.Contains(t, err.Error(), "lock timeout")
	assert.GreaterOrEqual(t, elapsed, 4*time.Second, "it waited for the lock timeout")
	assert.Less(t, elapsed, 20*time.Second, "and no longer")

	v, err := p.GetDBVersion(ctx)
	require.NoError(t, err)
	assert.Equal(t, m3Version, v, "the failed migration changed nothing")

	require.NoError(t, tx.Rollback())
	_, err = p.UpTo(ctx, m4Version)
	require.NoError(t, err)
}

// The migrations that create a table with foreign keys to a hot table need SHARE ROW EXCLUSIVE on it,
// which an open writer blocks. They must give up after lock_timeout instead of queueing every writer
// of the table behind them, and apply once the writer is gone.
func TestMigrationsWithForeignKeysFailFastWhenTheReferencedTableIsBusy(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version int64
		below   int64
		table   string // a table the migration references
		created string
	}{
		{"catalog links", m2Version, m1Version, "products", "hubrise_catalog_links"},
		{"orders and outbox", m3Version, m2Version, "orders", "hubrise_orders"},
		{"customers", m7Version, m6Version, "users", "hubrise_customers"},
		{"catalog publish", m8Version, m7Version, "hubrise_connection", "hubrise_catalog_publish"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			td := testhelpers.SetupTestDatabase(t)
			ctx := t.Context()
			p := newProvider(t, td)
			_, err := p.DownTo(ctx, tc.below)
			require.NoError(t, err)

			// An open writer holds ROW EXCLUSIVE on the table until it ends.
			blocker, err := td.DB.BeginTxx(ctx, nil)
			require.NoError(t, err)
			defer func() { _ = blocker.Rollback() }()
			_, err = blocker.ExecContext(ctx, `UPDATE `+tc.table+` SET id = id WHERE false`)
			require.NoError(t, err)

			start := time.Now()
			_, err = p.UpTo(ctx, tc.version)
			elapsed := time.Since(start)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "lock timeout")
			assert.GreaterOrEqual(t, elapsed, 4*time.Second, "it waited for the lock")
			assert.Less(t, elapsed, 20*time.Second, "and gave up")
			assert.False(t, tableExists(t, td, tc.created), "the failed migration changed nothing")

			require.NoError(t, blocker.Rollback())
			_, err = p.UpTo(ctx, tc.version)
			require.NoError(t, err)
			assert.True(t, tableExists(t, td, tc.created))
		})
	}
}

func connectionColumnExists(t *testing.T, td *testhelpers.TestDatabase, column string) (nullable, def string, ok bool) {
	t.Helper()
	var rows []struct {
		Nullable string  `db:"is_nullable"`
		Default  *string `db:"column_default"`
	}
	require.NoError(t, td.DB.SelectContext(t.Context(), &rows, `
		SELECT is_nullable, column_default FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'hubrise_connection' AND column_name = $1`, column))
	if len(rows) == 0 {
		return "", "", false
	}
	if rows[0].Default != nil {
		def = *rows[0].Default
	}
	return rows[0].Nullable, def, true
}

func originCheckDefs(t *testing.T, td *testhelpers.TestDatabase) []string {
	t.Helper()
	var defs []string
	require.NoError(t, td.DB.SelectContext(t.Context(), &defs, `
		SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conrelid = 'hubrise_catalog_links'::regclass AND contype = 'c'
		  AND pg_get_constraintdef(oid) LIKE '%origin%' AND pg_get_constraintdef(oid) NOT LIKE '%entity_type%'`))
	return defs
}

// The publish migration is additive: two defaulted columns, two new tables, a widened CHECK. Down puts
// the strict CHECK back after rewriting the new origin, and Up again works on the same database.
func TestCatalogPublishMigrationUpDownUp(t *testing.T) {
	td := testhelpers.SetupTestDatabase(t)
	ctx := t.Context()
	p := newProvider(t, td)

	for _, col := range []string{"publish_catalog", "publish_images"} {
		nullable, def, ok := connectionColumnExists(t, td, col)
		require.True(t, ok, col)
		assert.Equal(t, "NO", nullable)
		assert.Equal(t, "false", def, "off by default")
	}
	var rows int
	require.NoError(t, td.DB.GetContext(ctx, &rows, `SELECT count(*) FROM hubrise_catalog_publish`))
	assert.Equal(t, 1, rows, "the publisher row exists after Up")
	defs := originCheckDefs(t, td)
	require.Len(t, defs, 1)
	assert.Contains(t, defs[0], "published")

	fx := testhelpers.SeedTestData(t, td.DB)
	_, err := td.DB.ExecContext(ctx, `INSERT INTO hubrise_catalog_links (entity_type, product_id, hubrise_key, hubrise_ref, origin)
		VALUES ('product', $1, 'SKU-P', 'SKU-P', 'published')`, fx.SalmonSushi.ID)
	require.NoError(t, err, "the widened CHECK accepts published")
	_, err = td.DB.ExecContext(ctx, `INSERT INTO hubrise_catalog_links (entity_type, product_id, hubrise_key, hubrise_ref, origin)
		VALUES ('product', $1, 'SKU-G', 'SKU-G', 'garbage')`, fx.TunaSushi.ID)
	require.Error(t, err, "and still rejects anything else")

	for round := 1; round <= 2; round++ {
		_, err := p.DownTo(ctx, m7Version)
		require.NoError(t, err, "round %d: Down", round)
		for _, col := range []string{"publish_catalog", "publish_images"} {
			_, _, ok := connectionColumnExists(t, td, col)
			assert.False(t, ok, "round %d: %s dropped", round, col)
		}
		assert.False(t, tableExists(t, td, "hubrise_catalog_publish"))
		assert.False(t, tableExists(t, td, "hubrise_published_items"))
		defs := originCheckDefs(t, td)
		require.Len(t, defs, 1, "round %d", round)
		assert.NotContains(t, defs[0], "published")
		if round == 1 {
			var origin string
			require.NoError(t, td.DB.GetContext(ctx, &origin, `SELECT origin FROM hubrise_catalog_links WHERE hubrise_key = 'SKU-P'`))
			assert.Equal(t, "manual", origin, "Down rewrites published links before restoring the strict CHECK")
		}
		_, err = p.Up(ctx)
		require.NoError(t, err, "round %d: Up", round)
		assert.True(t, tableExists(t, td, "hubrise_catalog_publish"))
	}
}

// The previous release's queries (the explicit column lists of the connection and the links) keep working on
// the new schema, and its writes stay valid.
func TestCatalogPublishMigrationKeepsThePreviousReleaseWorking(t *testing.T) {
	td := testhelpers.SetupTestDatabase(t)
	ctx := t.Context()
	fx := testhelpers.SeedTestData(t, td.DB)

	oldConnectionColumns := `enabled, access_token_enc, account_id, account_name, location_id, location_name,
		catalog_id, catalog_name, customer_list_id, customer_list_name, scope,
		push_orders, sync_status, sync_inventory, sync_catalog, sync_customers, catalog_auto_import,
		auto_refund_on_cancel, import_images, catalog_language, variant_ref, channel_name,
		delivery_charge_ref, payment_fee_charge_ref, catalog_hash, catalog_preview_hash, catalog_preview_at,
		catalog_update_available_at, push_orders_since, inventory_baseline_at, relink_required_at,
		last_catalog_sync_at, last_event_at, auth_error_at, last_error, callback_registered_at,
		created_at, updated_at`
	// The old INSERT names no new column, so the defaults apply.
	_, err := td.DB.ExecContext(ctx, `INSERT INTO hubrise_connection (id, access_token_enc, location_id) VALUES (TRUE, '\x01', 'l')`)
	require.NoError(t, err)
	var n int
	require.NoError(t, td.DB.GetContext(ctx, &n, `SELECT count(*) FROM (SELECT `+oldConnectionColumns+` FROM hubrise_connection) q`))
	assert.Equal(t, 1, n)
	var publish bool
	require.NoError(t, td.DB.GetContext(ctx, &publish, `SELECT publish_catalog FROM hubrise_connection`))
	assert.False(t, publish)

	for _, origin := range []string{"import", "adopted", "manual"} {
		_, err = td.DB.ExecContext(ctx, `INSERT INTO hubrise_catalog_links (entity_type, product_id, hubrise_key, hubrise_ref, origin)
			VALUES ('product', $1, $2, $2, $3)`, fx.SalmonSushi.ID, "K-"+origin, origin)
		require.NoError(t, err, "the previous release writes %s links", origin)
		_, err = td.DB.ExecContext(ctx, `DELETE FROM hubrise_catalog_links`)
		require.NoError(t, err)
	}
}

// hubrise_connection is read on every order push: the ALTER must give up after the lock timeout
// instead of queueing behind a reader.
func TestCatalogPublishMigrationDoesNotBlockTheConnectionForLong(t *testing.T) {
	td := testhelpers.SetupTestDatabase(t)
	ctx := t.Context()
	p := newProvider(t, td)
	_, err := p.DownTo(ctx, m7Version)
	require.NoError(t, err)

	blocker, err := td.DB.BeginTxx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = blocker.Rollback() }()
	var n int
	require.NoError(t, blocker.GetContext(ctx, &n, `SELECT count(*) FROM hubrise_connection`)) // ACCESS SHARE until the end

	start := time.Now()
	_, err = p.UpTo(ctx, m8Version)
	elapsed := time.Since(start)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lock timeout")
	assert.GreaterOrEqual(t, elapsed, 4*time.Second)
	assert.Less(t, elapsed, 15*time.Second)
	_, _, ok := connectionColumnExists(t, td, "publish_catalog")
	assert.False(t, ok, "the failed migration changed nothing")
	assert.False(t, tableExists(t, td, "hubrise_catalog_publish"))

	require.NoError(t, blocker.Rollback())
	_, err = p.UpTo(ctx, m8Version)
	require.NoError(t, err)
	_, _, ok = connectionColumnExists(t, td, "publish_catalog")
	assert.True(t, ok)
}
