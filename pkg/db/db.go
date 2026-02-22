package db

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"go.uber.org/zap"

	"tsb-service/pkg/utils"
)

// DBPool holds separate database connections for customer and admin contexts.
// If admin credentials are not configured, Admin falls back to Customer.
type DBPool struct {
	Customer *sqlx.DB
	Admin    *sqlx.DB
}

// ForContext returns the appropriate database connection based on the isAdmin
// flag in the context. Admin requests use the Admin pool; everything else uses Customer.
func (p *DBPool) ForContext(ctx context.Context) *sqlx.DB {
	if utils.GetIsAdmin(ctx) {
		return p.Admin
	}
	return p.Customer
}

// DB returns the Customer connection as a sensible default. Use this for
// operations that don't have a context (e.g., health checks).
func (p *DBPool) DB() *sqlx.DB {
	return p.Customer
}

// Close closes both database connections.
func (p *DBPool) Close() error {
	if err := p.Customer.Close(); err != nil {
		return err
	}
	if p.Admin != p.Customer {
		return p.Admin.Close()
	}
	return nil
}

func getEnvInt(key string, defaultVal int) int {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return defaultVal
	}
	return n
}

func connectWithCreds(user, password, label string) (*sqlx.DB, error) {
	dbHost := os.Getenv("DB_HOST")
	dbPort := os.Getenv("DB_PORT")
	dbName := os.Getenv("DB_DATABASE")

	dbSSLMode := os.Getenv("DB_SSL_MODE")
	if dbSSLMode == "" {
		dbSSLMode = "require"
	}

	connectionString := fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		dbHost, dbPort, user, password, dbName, dbSSLMode,
	)

	db, err := sqlx.Connect("postgres", connectionString)
	if err != nil {
		return nil, fmt.Errorf("error connecting to database (%s): %v", label, err)
	}

	zap.L().Info("database connection established", zap.String("role", label), zap.String("host", dbHost), zap.String("port", dbPort), zap.String("database", dbName))
	return db, nil
}

func tunePool(db *sqlx.DB, maxOpen, maxIdle int) {
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
	db.SetConnMaxLifetime(time.Duration(getEnvInt("DB_CONN_MAX_LIFETIME_MIN", 5)) * time.Minute)
	db.SetConnMaxIdleTime(time.Duration(getEnvInt("DB_CONN_MAX_IDLE_TIME_MIN", 2)) * time.Minute)
}

// ConnectDatabase establishes the customer connection (backwards-compatible).
func ConnectDatabase() (*sqlx.DB, error) {
	dbUser := os.Getenv("DB_USERNAME")
	dbPassword := os.Getenv("DB_PASSWORD")
	db, err := connectWithCreds(dbUser, dbPassword, "default")
	if err != nil {
		return nil, err
	}
	tunePool(db, getEnvInt("DB_MAX_OPEN_CONNS", 25), getEnvInt("DB_MAX_IDLE_CONNS", 5))
	return db, nil
}

// ConnectDualDatabase creates a DBPool with separate customer and admin connections.
// If DB_ADMIN_USERNAME is not set, both pools share the same connection.
func ConnectDualDatabase() (*DBPool, error) {
	// Customer connection (always required)
	customerUser := os.Getenv("DB_USERNAME")
	customerPassword := os.Getenv("DB_PASSWORD")
	customerDB, err := connectWithCreds(customerUser, customerPassword, "customer")
	if err != nil {
		return nil, err
	}
	tunePool(customerDB, getEnvInt("DB_CUSTOMER_MAX_OPEN_CONNS", 15), getEnvInt("DB_CUSTOMER_MAX_IDLE_CONNS", 3))

	// Admin connection (optional — falls back to customer if not set)
	adminUser := os.Getenv("DB_ADMIN_USERNAME")
	adminPassword := os.Getenv("DB_ADMIN_PASSWORD")

	if adminUser == "" {
		zap.L().Info("DB_ADMIN_USERNAME not set, using single connection for both roles")
		// Tune the single connection with combined limits
		tunePool(customerDB, getEnvInt("DB_MAX_OPEN_CONNS", 25), getEnvInt("DB_MAX_IDLE_CONNS", 5))
		return &DBPool{Customer: customerDB, Admin: customerDB}, nil
	}

	adminDB, err := connectWithCreds(adminUser, adminPassword, "admin")
	if err != nil {
		customerDB.Close()
		return nil, err
	}
	tunePool(adminDB, getEnvInt("DB_ADMIN_MAX_OPEN_CONNS", 10), getEnvInt("DB_ADMIN_MAX_IDLE_CONNS", 2))

	return &DBPool{Customer: customerDB, Admin: adminDB}, nil
}
