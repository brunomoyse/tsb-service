package testhelpers

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/ory/dockertest/v3"
	"github.com/ory/dockertest/v3/docker"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

// TestDatabase is one isolated database of the package's shared PostgreSQL server.
type TestDatabase struct {
	DB       *sqlx.DB
	Pool     *dockertest.Pool
	Resource *dockertest.Resource // the shared server's container

	// Name, User and Password are the credentials of the database; HostPort is the server.
	Name, User, Password, HostPort string
}

// DSN is a connection string for the database, for tests that open further connections (a
// connection they then close, a second pool, ...).
func (td *TestDatabase) DSN() string {
	return fmt.Sprintf("postgres://%s:%s@%s/%s?sslmode=disable", td.User, td.Password, td.HostPort, td.Name)
}

const (
	pgUser       = "testuser"
	pgPassword   = "testpass"
	templateName = "tsb_template"
)

// sharedServer is the one PostgreSQL container of a test binary. Starting a container and running
// the migrations dominated the run time (one container per test); now the container starts once
// per package, the migrations run once into a template database, and every test gets its own
// database with CREATE DATABASE ... TEMPLATE (a file copy, about 100 ms). The server runs without
// fsync and with its data directory on tmpfs: the data is thrown away anyway.
type sharedServer struct {
	pool     *dockertest.Pool
	resource *dockertest.Resource
	admin    *sqlx.DB // connected to the "postgres" database, creates and drops the test databases
	hostPort string

	createMu sync.Mutex // CREATE DATABASE ... TEMPLATE needs the template to be unused
	seq      atomic.Int64
}

var (
	sharedMu sync.Mutex
	shared   *sharedServer
)

// maxWait bounds how long a container may take to accept connections. A stuck container used to
// make the pool retry for ever until the test binary's timeout.
const maxWait = 3 * time.Minute

// containerLifetime is a safety net for a test binary that has no TestMain calling Main: Docker
// removes the container this many seconds after it started.
const containerLifetime = 3600

func startShared(t *testing.T) *sharedServer {
	t.Helper()

	// On macOS, Docker Desktop uses a different socket path
	endpoint := os.Getenv("DOCKER_HOST")
	if endpoint == "" {
		homeDir, _ := os.UserHomeDir()
		macOSSocket := filepath.Join(homeDir, ".docker/run/docker.sock")
		if _, err := os.Stat(macOSSocket); err == nil {
			endpoint = "unix://" + macOSSocket
		}
	}
	pool, err := dockertest.NewPool(endpoint)
	require.NoError(t, err, "Could not connect to Docker")
	pool.MaxWait = maxWait

	resource, err := pool.RunWithOptions(&dockertest.RunOptions{
		Repository: "postgres",
		Tag:        "15-alpine",
		Env: []string{
			"POSTGRES_USER=" + pgUser,
			"POSTGRES_PASSWORD=" + pgPassword,
			"POSTGRES_DB=postgres",
		},
		Cmd: []string{
			"postgres",
			"-c", "fsync=off", "-c", "synchronous_commit=off", "-c", "full_page_writes=off",
			"-c", "max_connections=300",
		},
	}, func(config *docker.HostConfig) {
		config.AutoRemove = true
		config.RestartPolicy = docker.RestartPolicy{Name: "no"}
		config.Tmpfs = map[string]string{"/var/lib/postgresql/data": "rw,nosuid,nodev,size=2g"}
	})
	require.NoError(t, err, "Could not start PostgreSQL container")
	require.NoError(t, resource.Expire(containerLifetime))

	hostPort := resource.GetHostPort("5432/tcp")
	admin, err := connectWhenReady(pool, fmt.Sprintf("postgres://%s:%s@%s/postgres?sslmode=disable", pgUser, pgPassword, hostPort))
	if err != nil {
		_ = pool.Purge(resource)
		require.NoError(t, err, "Could not connect to PostgreSQL container")
	}
	admin.SetMaxOpenConns(2)

	srv := &sharedServer{pool: pool, resource: resource, admin: admin, hostPort: hostPort}

	// The template: an empty database with every migration applied.
	_, err = admin.Exec("CREATE DATABASE " + templateName)
	require.NoError(t, err)
	tmpl, err := connectWhenReady(pool, srv.url(templateName))
	require.NoError(t, err)
	runMigrations(t, tmpl.DB)
	require.NoError(t, tmpl.Close(), "the template must have no open connection")
	t.Logf("Shared test PostgreSQL ready at %s", hostPort)
	return srv
}

func (s *sharedServer) url(db string) string {
	return fmt.Sprintf("postgres://%s:%s@%s/%s?sslmode=disable", pgUser, pgPassword, s.hostPort, db)
}

func connectWhenReady(pool *dockertest.Pool, url string) (*sqlx.DB, error) {
	var db *sqlx.DB
	err := pool.Retry(func() error {
		var retryErr error
		db, retryErr = sqlx.Connect("postgres", url)
		if retryErr != nil {
			return retryErr
		}
		return db.Ping()
	})
	return db, err
}

func sharedFor(t *testing.T) *sharedServer {
	t.Helper()
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if shared == nil {
		shared = startShared(t)
	}
	return shared
}

// SetupTestDatabase returns a fresh, fully migrated database of the package's PostgreSQL
// container (started on first use), dropped when the test ends. Call Main from the package's
// TestMain so the container is removed when the package's tests are done.
func SetupTestDatabase(t *testing.T) *TestDatabase {
	t.Helper()
	srv := sharedFor(t)
	name := fmt.Sprintf("t%d_%d", os.Getpid(), srv.seq.Add(1))

	srv.createMu.Lock()
	_, err := srv.admin.Exec("CREATE DATABASE " + name + " TEMPLATE " + templateName)
	srv.createMu.Unlock()
	require.NoError(t, err, "Could not create the test database")

	db, err := connectWhenReady(srv.pool, srv.url(name))
	require.NoError(t, err, "Could not connect to the test database")

	testDB := &TestDatabase{
		DB: db, Pool: srv.pool, Resource: srv.resource,
		Name: name, User: pgUser, Password: pgPassword, HostPort: srv.hostPort,
	}
	t.Cleanup(func() { testDB.Teardown(t) })
	return testDB
}

// Teardown closes the connection and drops the database.
func (td *TestDatabase) Teardown(t *testing.T) {
	if td.DB != nil {
		if err := td.DB.Close(); err != nil {
			t.Logf("Error closing database connection: %v", err)
		}
	}
	sharedMu.Lock()
	srv := shared
	sharedMu.Unlock()
	if srv == nil || td.Name == "" {
		return
	}
	if _, err := srv.admin.Exec("DROP DATABASE IF EXISTS " + td.Name + " WITH (FORCE)"); err != nil {
		t.Logf("Error dropping test database %s: %v", td.Name, err)
	}
}

// Shutdown removes the package's PostgreSQL container, if one was started.
func Shutdown() {
	sharedMu.Lock()
	srv := shared
	shared = nil
	sharedMu.Unlock()
	if srv == nil {
		return
	}
	_ = srv.admin.Close()
	_ = srv.pool.Purge(srv.resource)
}

// Main runs the package's tests and then removes the shared PostgreSQL container. Use it as
//
//	func TestMain(m *testing.M) { os.Exit(testhelpers.Main(m)) }
func Main(m *testing.M) int {
	code := m.Run()
	Shutdown()
	return code
}

// ClosedDB is a database handle whose connection is already closed: every query fails, which
// reaches the "the store is down" branch of whatever it is given.
func ClosedDB(t *testing.T) *sqlx.DB {
	t.Helper()
	conn, err := sqlx.Open("postgres", "host=127.0.0.1 port=1 user=x dbname=x sslmode=disable")
	require.NoError(t, err)
	require.NoError(t, conn.Close())
	return conn
}

// ClosedConnection returns a second, already closed connection to the same database.
func (td *TestDatabase) ClosedConnection(t *testing.T) *sqlx.DB {
	t.Helper()
	conn, err := sqlx.Connect("postgres", td.DSN())
	require.NoError(t, err)
	require.NoError(t, conn.Close())
	return conn
}

// FailCommitTrigger makes every INSERT/UPDATE/DELETE (event) on table fail at COMMIT time with
// "forced failure", through a deferred constraint trigger: the statement itself succeeds, so the
// code under test gets as far as committing, which is the failure path that proves nothing is left
// half written. The trigger is removed when the test ends.
func FailCommitTrigger(t *testing.T, db *sqlx.DB, table, event string) {
	t.Helper()
	FailTrigger(t, db, table, "AFTER", event, true)
}

// FailTrigger makes every row of the event (INSERT, UPDATE or DELETE) on table raise "forced
// failure": at once ("BEFORE"/"AFTER" the statement, deferred=false) or at COMMIT (deferred=true,
// always AFTER). It replaces an earlier trigger of the same helper on the table and is removed when
// the test ends.
func FailTrigger(t *testing.T, db *sqlx.DB, table, timing, event string, deferred bool) {
	t.Helper()
	ctx := t.Context()
	_, err := db.ExecContext(ctx, `CREATE OR REPLACE FUNCTION fail_it() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'forced failure'; END $$`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `DROP TRIGGER IF EXISTS fail_trg ON `+table)
	require.NoError(t, err)
	stmt := `CREATE TRIGGER fail_trg ` + timing + ` ` + event + ` ON ` + table + ` FOR EACH ROW EXECUTE FUNCTION fail_it()`
	if deferred {
		stmt = `CREATE CONSTRAINT TRIGGER fail_trg AFTER ` + event + ` ON ` + table + ` DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_it()`
	}
	_, err = db.ExecContext(ctx, stmt)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DROP TRIGGER IF EXISTS fail_trg ON ` + table) })
}

// runMigrations executes all goose migrations
func runMigrations(t *testing.T, db *sql.DB) {
	// Get the project root directory (assuming tests run from project root or subdirs)
	workDir, err := os.Getwd()
	require.NoError(t, err, "Could not get working directory")

	// Navigate to migrations directory
	migrationsPath := findMigrationsDir(workDir)
	require.NotEmpty(t, migrationsPath, "Could not find migrations directory")

	// Use goose to run migrations
	err = goose.SetDialect("postgres")
	require.NoError(t, err, "Could not set goose dialect")

	err = goose.Up(db, migrationsPath)
	require.NoError(t, err, "Could not run goose migrations")

	version, err := goose.GetDBVersion(db)
	require.NoError(t, err, "Could not get migration version")

	t.Logf("All migrations applied successfully (version: %d)", version)
}

// findMigrationsDir searches for the migrations directory from the current working directory
func findMigrationsDir(startPath string) string {
	// Try current directory first
	migrationsPath := filepath.Join(startPath, "migrations")
	if _, err := os.Stat(migrationsPath); err == nil {
		return migrationsPath
	}

	// Try parent directories (up to 5 levels)
	currentPath := startPath
	for range 5 {
		parentPath := filepath.Dir(currentPath)
		if parentPath == currentPath {
			break // Reached root
		}
		currentPath = parentPath

		migrationsPath = filepath.Join(currentPath, "migrations")
		if _, err := os.Stat(migrationsPath); err == nil {
			return migrationsPath
		}
	}

	return ""
}

// TruncateAllTables removes all data from tables (useful for test isolation)
func (td *TestDatabase) TruncateAllTables(t *testing.T) {
	tables := []string{
		"order_product",
		"orders",
		"mollie_payments",
		"product_translations",
		"products",
		"product_category_translations",
		"product_categories",
		"address_distances",
		"users",
	}

	for _, table := range tables {
		_, err := td.DB.Exec(fmt.Sprintf("TRUNCATE TABLE %s CASCADE", table))
		require.NoError(t, err, "Could not truncate table: %s", table)
	}
}
