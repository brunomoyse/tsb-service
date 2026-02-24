package testhelpers

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/ory/dockertest/v3"
	"github.com/ory/dockertest/v3/docker"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

// TestDatabase represents a test database instance
type TestDatabase struct {
	DB       *sqlx.DB
	Pool     *dockertest.Pool
	Resource *dockertest.Resource
}

// SetupTestDatabase creates a PostgreSQL container and runs migrations
func SetupTestDatabase(t *testing.T) *TestDatabase {
	// Create dockertest pool
	// On macOS, Docker Desktop uses a different socket path
	endpoint := os.Getenv("DOCKER_HOST")
	if endpoint == "" {
		// Try macOS Docker Desktop socket first
		homeDir, _ := os.UserHomeDir()
		macOSSocket := filepath.Join(homeDir, ".docker/run/docker.sock")
		if _, err := os.Stat(macOSSocket); err == nil {
			endpoint = "unix://" + macOSSocket
		}
	}

	pool, err := dockertest.NewPool(endpoint)
	require.NoError(t, err, "Could not connect to Docker")

	// Set Docker API version
	pool.MaxWait = 0 // No limit on wait time for container to be ready

	// Start PostgreSQL container
	resource, err := pool.RunWithOptions(&dockertest.RunOptions{
		Repository: "postgres",
		Tag:        "15-alpine",
		Env: []string{
			"POSTGRES_USER=testuser",
			"POSTGRES_PASSWORD=testpass",
			"POSTGRES_DB=testdb",
			"listen_addresses = '*'",
		},
	}, func(config *docker.HostConfig) {
		config.AutoRemove = true
		config.RestartPolicy = docker.RestartPolicy{Name: "no"}
	})
	require.NoError(t, err, "Could not start PostgreSQL container")

	// Get connection string
	hostAndPort := resource.GetHostPort("5432/tcp")
	databaseURL := fmt.Sprintf("postgres://testuser:testpass@%s/testdb?sslmode=disable", hostAndPort)

	// Wait for database to be ready
	var db *sqlx.DB
	err = pool.Retry(func() error {
		var retryErr error
		db, retryErr = sqlx.Connect("postgres", databaseURL)
		if retryErr != nil {
			return retryErr
		}
		return db.Ping()
	})
	require.NoError(t, err, "Could not connect to PostgreSQL container")

	t.Logf("Test database ready at %s", hostAndPort)

	// Run migrations
	runMigrations(t, db.DB)

	testDB := &TestDatabase{
		DB:       db,
		Pool:     pool,
		Resource: resource,
	}

	// Register cleanup
	t.Cleanup(func() {
		testDB.Teardown(t)
	})

	return testDB
}

// Teardown closes the database connection and removes the container
func (td *TestDatabase) Teardown(t *testing.T) {
	if td.DB != nil {
		err := td.DB.Close()
		if err != nil {
			t.Logf("Error closing database connection: %v", err)
		}
	}

	if td.Pool != nil && td.Resource != nil {
		err := td.Pool.Purge(td.Resource)
		if err != nil {
			t.Logf("Error purging PostgreSQL container: %v", err)
		}
	}
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
		"refresh_tokens",
		"address_distances",
		"users",
	}

	for _, table := range tables {
		_, err := td.DB.Exec(fmt.Sprintf("TRUNCATE TABLE %s CASCADE", table))
		require.NoError(t, err, "Could not truncate table: %s", table)
	}
}
