package main

import (
	"os"
	"time"

	"github.com/joho/godotenv"
	"go.uber.org/zap"

	userDomain "tsb-service/internal/modules/user/domain"
	"tsb-service/pkg/db"
	"tsb-service/pkg/logging"
	es "tsb-service/services/email/scaleway"
)

func main() {
	// Load .env file
	if err := godotenv.Load(); err != nil {
		zap.L().Warn("no .env file found, using environment variables")
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
	defer logging.Sync()

	// Connect to database
	dbConn, err := db.ConnectDatabase()
	if err != nil {
		zap.L().Error("failed to connect to database", zap.Error(err))
		os.Exit(1)
	}
	defer dbConn.Close()

	// Initialize email service
	if err := es.InitService(); err != nil {
		zap.L().Error("failed to initialize email service", zap.Error(err))
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
		zap.L().Error("failed to query inactive users", zap.Error(err))
		os.Exit(1)
	}

	zap.L().Info("found inactive users", zap.Int("count", len(users)))

	sent := 0
	failed := 0
	for _, user := range users {
		if err := es.SendReengagementEmail(user, "fr"); err != nil {
			zap.L().Error("failed to send re-engagement email", zap.String("user_id", user.ID.String()), zap.String("email", user.Email), zap.Error(err))
			failed++
		} else {
			zap.L().Info("re-engagement email sent", zap.String("user_id", user.ID.String()), zap.String("email", user.Email))
			sent++
		}

		// Rate-limit: 100ms between sends
		time.Sleep(100 * time.Millisecond)
	}

	zap.L().Info("re-engagement campaign completed", zap.Int("total", len(users)), zap.Int("sent", sent), zap.Int("failed", failed))
}
