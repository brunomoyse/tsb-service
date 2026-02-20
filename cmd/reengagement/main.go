package main

import (
	"log/slog"
	"os"
	"time"

	"github.com/joho/godotenv"

	userDomain "tsb-service/internal/modules/user/domain"
	"tsb-service/pkg/db"
	"tsb-service/pkg/logging"
	es "tsb-service/services/email/scaleway"
)

func main() {
	// Load .env file
	if err := godotenv.Load(); err != nil {
		slog.Warn("no .env file found, using environment variables")
	}

	// Initialize structured logger
	logLevel := os.Getenv("LOG_LEVEL")
	if logLevel == "" {
		logLevel = "info"
	}
	logFormat := os.Getenv("LOG_FORMAT")
	if logFormat == "" {
		logFormat = "text"
	}
	logging.Setup(logLevel, logFormat)

	// Connect to database
	dbConn, err := db.ConnectDatabase()
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer dbConn.Close()

	// Initialize email service
	if err := es.InitService(); err != nil {
		slog.Error("failed to initialize email service", "error", err)
		os.Exit(1)
	}

	// Query inactive users: verified, last order > 30 days ago OR registered > 30 days with no orders
	query := `
		SELECT u.id, u.first_name, u.last_name, u.email
		FROM users u
		WHERE u.email_verified_at IS NOT NULL
		AND (
			(SELECT MAX(o.created_at) FROM orders o WHERE o.user_id = u.id) < NOW() - INTERVAL '30 days'
			OR
			(NOT EXISTS (SELECT 1 FROM orders o WHERE o.user_id = u.id) AND u.created_at < NOW() - INTERVAL '30 days')
		)
	`

	var users []userDomain.User
	if err := dbConn.Select(&users, query); err != nil {
		slog.Error("failed to query inactive users", "error", err)
		os.Exit(1)
	}

	slog.Info("found inactive users", "count", len(users))

	sent := 0
	failed := 0
	for _, user := range users {
		if err := es.SendReengagementEmail(user, "fr"); err != nil {
			slog.Error("failed to send re-engagement email", "user_id", user.ID, "email", user.Email, "error", err)
			failed++
		} else {
			slog.Info("re-engagement email sent", "user_id", user.ID, "email", user.Email)
			sent++
		}

		// Rate-limit: 100ms between sends
		time.Sleep(100 * time.Millisecond)
	}

	slog.Info("re-engagement campaign completed", "total", len(users), "sent", sent, "failed", failed)
}
