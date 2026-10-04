package db_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/graph-gophers/dataloader"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/testhelpers"
	"tsb-service/pkg/db"
	"tsb-service/pkg/utils"
)

// pointAtDatabase sets the DB_* variables to the test container.
func pointAtDatabase(t *testing.T, tdb *testhelpers.TestDatabase) {
	t.Helper()
	host, port, err := net.SplitHostPort(tdb.Resource.GetHostPort("5432/tcp"))
	require.NoError(t, err)
	t.Setenv("DB_HOST", host)
	t.Setenv("DB_PORT", port)
	t.Setenv("DB_DATABASE", "testdb")
	t.Setenv("DB_USERNAME", "testuser")
	t.Setenv("DB_PASSWORD", "testpass")
	t.Setenv("DB_SSL_MODE", "disable")
	t.Setenv("DB_ADMIN_USERNAME", "")
	t.Setenv("DB_ADMIN_PASSWORD", "")
	for _, k := range []string{"DB_MAX_OPEN_CONNS", "DB_MAX_IDLE_CONNS", "DB_CUSTOMER_MAX_OPEN_CONNS", "DB_CUSTOMER_MAX_IDLE_CONNS",
		"DB_ADMIN_MAX_OPEN_CONNS", "DB_ADMIN_MAX_IDLE_CONNS", "DB_CONN_MAX_LIFETIME_MIN", "DB_CONN_MAX_IDLE_TIME_MIN"} {
		t.Setenv(k, "")
	}
}

func TestConnectDatabase(t *testing.T) {
	tdb := testhelpers.SetupTestDatabase(t)

	t.Run("connects and applies the default pool limits", func(t *testing.T) {
		pointAtDatabase(t, tdb)
		conn, err := db.ConnectDatabase()
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })
		require.NoError(t, conn.PingContext(t.Context()))
		stats := conn.Stats()
		assert.Equal(t, 25, stats.MaxOpenConnections)
	})

	t.Run("pool limits come from the environment, junk values fall back to the defaults", func(t *testing.T) {
		pointAtDatabase(t, tdb)
		t.Setenv("DB_MAX_OPEN_CONNS", "7")
		conn, err := db.ConnectDatabase()
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })
		assert.Equal(t, 7, conn.Stats().MaxOpenConnections)

		t.Setenv("DB_MAX_OPEN_CONNS", "lots")
		conn2, err := db.ConnectDatabase()
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn2.Close() })
		assert.Equal(t, 25, conn2.Stats().MaxOpenConnections)
	})

	t.Run("bad credentials are an error naming the role", func(t *testing.T) {
		pointAtDatabase(t, tdb)
		t.Setenv("DB_PASSWORD", "wrong")
		conn, err := db.ConnectDatabase()
		require.ErrorContains(t, err, "error connecting to database (default)")
		assert.Nil(t, conn)
	})
}

func TestConnectDualDatabase(t *testing.T) {
	tdb := testhelpers.SetupTestDatabase(t)

	t.Run("without admin credentials both roles share one connection", func(t *testing.T) {
		pointAtDatabase(t, tdb)
		pool, err := db.ConnectDualDatabase()
		require.NoError(t, err)
		assert.Same(t, pool.Customer, pool.Admin)
		assert.Equal(t, 25, pool.Customer.Stats().MaxOpenConnections, "the shared connection carries both loads")
		assert.Same(t, pool.Customer, pool.DB())
		require.NoError(t, pool.Close())
		assert.Error(t, pool.Customer.PingContext(t.Context()), "closed")
	})

	t.Run("with admin credentials there are two separately tuned connections", func(t *testing.T) {
		pointAtDatabase(t, tdb)
		t.Setenv("DB_ADMIN_USERNAME", "testuser")
		t.Setenv("DB_ADMIN_PASSWORD", "testpass")
		pool, err := db.ConnectDualDatabase()
		require.NoError(t, err)
		require.NotSame(t, pool.Customer, pool.Admin)
		assert.Equal(t, 15, pool.Customer.Stats().MaxOpenConnections)
		assert.Equal(t, 10, pool.Admin.Stats().MaxOpenConnections)
		require.NoError(t, pool.Customer.PingContext(t.Context()))
		require.NoError(t, pool.Admin.PingContext(t.Context()))

		require.NoError(t, pool.Close())
		assert.Error(t, pool.Customer.PingContext(t.Context()))
		assert.Error(t, pool.Admin.PingContext(t.Context()))
	})

	t.Run("pool limits of each role come from their own variables", func(t *testing.T) {
		pointAtDatabase(t, tdb)
		t.Setenv("DB_ADMIN_USERNAME", "testuser")
		t.Setenv("DB_ADMIN_PASSWORD", "testpass")
		t.Setenv("DB_CUSTOMER_MAX_OPEN_CONNS", "4")
		t.Setenv("DB_ADMIN_MAX_OPEN_CONNS", "3")
		pool, err := db.ConnectDualDatabase()
		require.NoError(t, err)
		t.Cleanup(func() { _ = pool.Close() })
		assert.Equal(t, 4, pool.Customer.Stats().MaxOpenConnections)
		assert.Equal(t, 3, pool.Admin.Stats().MaxOpenConnections)
	})

	t.Run("a bad customer login fails before the admin one is tried", func(t *testing.T) {
		pointAtDatabase(t, tdb)
		t.Setenv("DB_PASSWORD", "wrong")
		pool, err := db.ConnectDualDatabase()
		require.ErrorContains(t, err, "(customer)")
		assert.Nil(t, pool)
	})

	t.Run("a bad admin login is an error naming the admin role", func(t *testing.T) {
		pointAtDatabase(t, tdb)
		t.Setenv("DB_ADMIN_USERNAME", "testuser")
		t.Setenv("DB_ADMIN_PASSWORD", "wrong")
		pool, err := db.ConnectDualDatabase()
		require.ErrorContains(t, err, "(admin)")
		assert.Nil(t, pool)
	})
}

func TestPoolForContext(t *testing.T) {
	tdb := testhelpers.SetupTestDatabase(t)
	pointAtDatabase(t, tdb)
	t.Setenv("DB_ADMIN_USERNAME", "testuser")
	t.Setenv("DB_ADMIN_PASSWORD", "testpass")
	pool, err := db.ConnectDualDatabase()
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close() })

	bg := context.Background()
	assert.Same(t, pool.Customer, pool.ForContext(bg), "anonymous callers and customers use the customer pool")
	assert.Same(t, pool.Customer, pool.ForContext(utils.SetUserID(bg, "u")))
	assert.Same(t, pool.Admin, pool.ForContext(utils.SetIsAdmin(bg, true)), "admins use the privileged pool")
	assert.Same(t, pool.Admin, pool.ForContext(utils.SetIsPOS(bg, true)), "so do POS devices")
	assert.Same(t, pool.Customer, pool.ForContext(utils.SetIsAdmin(bg, false)))
}

func TestPoolCloseReportsAnError(t *testing.T) {
	tdb := testhelpers.SetupTestDatabase(t)
	other := testhelpers.SetupTestDatabase(t)
	pool := &db.DBPool{Customer: tdb.DB, Admin: other.DB}
	require.NoError(t, pool.Close())
	assert.Error(t, tdb.DB.PingContext(t.Context()))
	assert.Error(t, other.DB.PingContext(t.Context()), "both connections are closed")
}

func TestTypedLoader(t *testing.T) {
	ctx := context.Background()

	t.Run("loads one value list per key, an unknown key is an empty list", func(t *testing.T) {
		var batches [][]string
		loader := db.NewTypedLoader(func(_ context.Context, keys []string) (map[string][]int, error) {
			batches = append(batches, append([]string(nil), keys...))
			out := map[string][]int{}
			for i, k := range keys {
				if k != "missing" {
					out[k] = []int{i, i * 10}
				}
			}
			return out, nil
		}, "failed to fetch numbers")

		got, err := loader.Load(ctx, "a")
		require.NoError(t, err)
		assert.Equal(t, []int{0, 0}, got)

		none, err := loader.Load(ctx, "missing")
		require.NoError(t, err)
		assert.Equal(t, []int{}, none)
	})

	t.Run("concurrent loads are batched into one fetch", func(t *testing.T) {
		calls := make(chan []string, 4)
		loader := db.NewTypedLoader(func(_ context.Context, keys []string) (map[string][]string, error) {
			calls <- append([]string(nil), keys...)
			out := map[string][]string{}
			for _, k := range keys {
				out[k] = []string{strings.ToUpper(k)}
			}
			return out, nil
		}, "x")

		type res struct {
			key  string
			vals []string
			err  error
		}
		results := make(chan res, 3)
		for _, k := range []string{"a", "b", "c"} {
			go func() { v, err := loader.Load(ctx, k); results <- res{k, v, err} }()
		}
		for range 3 {
			r := <-results
			require.NoError(t, r.err)
			assert.Equal(t, []string{strings.ToUpper(r.key)}, r.vals)
		}
		close(calls)
		total := 0
		for batch := range calls {
			total += len(batch)
		}
		assert.Equal(t, 3, total, "every key was fetched exactly once")
	})

	t.Run("a fetch failure fails every key with the configured message and the cause", func(t *testing.T) {
		boom := errors.New("db down")
		loader := db.NewTypedLoader(func(context.Context, []string) (map[string][]int, error) { return nil, boom }, "failed to fetch numbers")
		_, err := loader.Load(ctx, "a")
		require.ErrorIs(t, err, boom)
		assert.ErrorContains(t, err, "failed to fetch numbers")
	})

	t.Run("a result of an unexpected type is reported, not panicked on", func(t *testing.T) {
		// Feed the underlying loader a value that is not a []int.
		wrong := &db.TypedLoader[int]{Loader: dataloader.NewBatchedLoader(func(context.Context, dataloader.Keys) []*dataloader.Result {
			return []*dataloader.Result{{Data: "not a slice"}}
		})}
		_, err := wrong.Load(ctx, "k")
		require.ErrorContains(t, err, "unexpected type from typed loader")
	})

	t.Run("the loader caches within its lifetime", func(t *testing.T) {
		calls := 0
		loader := db.NewTypedLoader(func(_ context.Context, keys []string) (map[string][]int, error) {
			calls++
			return map[string][]int{keys[0]: {1}}, nil
		}, "x")
		for range 3 {
			_, err := loader.Load(ctx, "same")
			require.NoError(t, err)
		}
		assert.Equal(t, 1, calls)
	})
}
