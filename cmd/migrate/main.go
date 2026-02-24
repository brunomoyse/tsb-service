package main

import (
	"cmp"
	"database/sql"
	"flag"
	"fmt"
	"os"

	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
	"github.com/pressly/goose/v3"
	"go.uber.org/zap"

	"tsb-service/pkg/logging"
)

const (
	migrationsDir = "migrations"
)

func main() {
	// Load .env file
	if err := godotenv.Load(); err != nil {
		fmt.Println("Warning: .env file not found, using environment variables")
	}

	// Initialize structured logger
	logLevel := cmp.Or(os.Getenv("LOG_LEVEL"), "info")
	logFormat := cmp.Or(os.Getenv("LOG_FORMAT"), "text")
	logging.Setup(logLevel, logFormat)
	defer logging.Sync()

	// Parse flags
	var command string
	flag.StringVar(&command, "cmd", "", "Migration command: up, down, status, create, version, up-to, down-to, redo")
	flag.Parse()

	if command == "" {
		printUsage()
		os.Exit(1)
	}

	// Handle create command early (no DB connection needed)
	if command == "create" {
		args := flag.Args()
		if len(args) < 1 {
			fmt.Println("Error: migration name required for create command")
			fmt.Println("Usage: migrate -cmd=create migration_name")
			os.Exit(1)
		}
		name := args[0]
		if err := goose.Create(nil, migrationsDir, name, "sql"); err != nil {
			zap.L().Fatal("failed to create migration", zap.String("name", name), zap.Error(err))
		}
		zap.L().Info("migration created successfully", zap.String("name", name))
		return
	}

	// Build database connection string
	dbHost := os.Getenv("DB_HOST")
	dbPort := os.Getenv("DB_PORT")
	dbUser := os.Getenv("DB_USERNAME")
	dbPass := os.Getenv("DB_PASSWORD")
	dbName := os.Getenv("DB_DATABASE")
	sslMode := cmp.Or(os.Getenv("DB_SSL_MODE"), "disable")

	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		dbHost, dbPort, dbUser, dbPass, dbName, sslMode)

	// Open database connection
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		zap.L().Fatal("failed to connect to database", zap.Error(err))
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		zap.L().Fatal("failed to ping database", zap.Error(err))
	}

	// Set goose dialect
	if err := goose.SetDialect("postgres"); err != nil {
		zap.L().Fatal("failed to set goose dialect", zap.Error(err))
	}

	// Execute migration command
	var execErr error
	switch command {
	case "up":
		execErr = goose.Up(db, migrationsDir)
	case "down":
		execErr = goose.Down(db, migrationsDir)
	case "status":
		execErr = goose.Status(db, migrationsDir)
	case "version":
		execErr = goose.Version(db, migrationsDir)
	case "redo":
		execErr = goose.Redo(db, migrationsDir)
	case "reset":
		execErr = goose.Reset(db, migrationsDir)
	case "up-to":
		args := flag.Args()
		if len(args) < 1 {
			fmt.Println("Error: version required for up-to command")
			os.Exit(1)
		}
		var version int64
		if _, err := fmt.Sscanf(args[0], "%d", &version); err != nil {
			zap.L().Fatal("invalid version", zap.Error(err))
		}
		execErr = goose.UpTo(db, migrationsDir, version)
	case "down-to":
		args := flag.Args()
		if len(args) < 1 {
			fmt.Println("Error: version required for down-to command")
			os.Exit(1)
		}
		var version int64
		if _, err := fmt.Sscanf(args[0], "%d", &version); err != nil {
			zap.L().Fatal("invalid version", zap.Error(err))
		}
		execErr = goose.DownTo(db, migrationsDir, version)
	default:
		fmt.Printf("Error: unknown command '%s'\n", command)
		printUsage()
		os.Exit(1)
	}

	if execErr != nil {
		zap.L().Fatal("migration command failed",
			zap.String("command", command),
			zap.Error(execErr))
	}

	zap.L().Info("migration command completed successfully", zap.String("command", command))
}

func printUsage() {
	fmt.Println("Migration tool powered by pressly/goose")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  go run cmd/migrate/main.go -cmd=<command> [args]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  up              Migrate the DB to the most recent version available")
	fmt.Println("  down            Roll back the version by 1")
	fmt.Println("  status          Dump the migration status for the current DB")
	fmt.Println("  version         Print the current version of the database")
	fmt.Println("  redo            Re-run the latest migration")
	fmt.Println("  reset           Roll back all migrations")
	fmt.Println("  up-to VERSION   Migrate the DB to a specific VERSION")
	fmt.Println("  down-to VERSION Roll back to a specific VERSION")
	fmt.Println("  create NAME     Create a new migration file with the NAME")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  go run cmd/migrate/main.go -cmd=up")
	fmt.Println("  go run cmd/migrate/main.go -cmd=status")
	fmt.Println("  go run cmd/migrate/main.go -cmd=create add_user_roles")
	fmt.Println("  go run cmd/migrate/main.go -cmd=up-to 20240904231000")
}
