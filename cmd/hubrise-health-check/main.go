// Package main is a small CLI invoked via cron every 5 minutes to
// ping the HubRise health endpoint and email an alert if the status
// is degraded or down. Intended to run on the same VPS as tsb-service;
// it calls the local HTTP endpoint and reuses the Scaleway email
// client.
//
// Invocation (systemd timer or cron):
//
//	*/5 * * * * tsbadmin /usr/local/bin/tsb-hubrise-health-check
//
// Exit codes:
//
//	0 — health is ok, or email sent successfully
//	1 — fatal error (HTTP unreachable, JSON parse, email send)
package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"go.uber.org/zap"

	"tsb-service/pkg/alerter"
	scaleway "tsb-service/pkg/email/scaleway"
	"tsb-service/pkg/logging"
)

// healthSnapshot mirrors application.HealthSnapshot to avoid
// importing internal/ packages into a cmd binary.
type healthSnapshot struct {
	Status                  string    `json:"status"`
	OrdersFailedCount       int       `json:"orders_failed_count"`
	OrdersStuckPendingCount int       `json:"orders_stuck_pending_count"`
	LastSuccessfulPushAge   *int      `json:"last_successful_push_age_seconds"`
	CatalogLastPushStatus   *string   `json:"catalog_last_push_status"`
	CatalogLastPushAge      *int      `json:"catalog_last_push_age_seconds"`
	GeneratedAt             time.Time `json:"generated_at"`
	Reasons                 []string  `json:"reasons,omitempty"`
}

func main() {
	_ = godotenv.Load()

	logLevel := cmp.Or(os.Getenv("LOG_LEVEL"), "info")
	logFormat := cmp.Or(os.Getenv("LOG_FORMAT"), "text")
	logging.Setup(logLevel, logFormat)
	defer logging.Sync()

	apiBaseURL := cmp.Or(os.Getenv("API_BASE_URL"), "http://localhost:8080/api/v1")
	healthURL := strings.TrimRight(apiBaseURL, "/") + "/hubrise/webshop/health"

	snap, err := fetchHealth(healthURL)
	if err != nil {
		zap.L().Error("failed to fetch health endpoint",
			zap.String("url", healthURL),
			zap.Error(err))
		os.Exit(1)
	}

	if snap.Status == "ok" {
		zap.L().Info("hubrise health ok",
			zap.Int("failed", snap.OrdersFailedCount),
			zap.Int("stuck_pending", snap.OrdersStuckPendingCount))
		return
	}

	// Initialise the email client only when we actually need to send.
	if err := scaleway.InitService(); err != nil {
		zap.L().Error("email init failed", zap.Error(err))
		os.Exit(1)
	}

	recipients := parseRecipients(os.Getenv("HUBRISE_ALERT_EMAILS"))
	if len(recipients) == 0 {
		zap.L().Warn("no HUBRISE_ALERT_EMAILS configured — alert not sent",
			zap.String("status", snap.Status))
		return
	}

	dedupTTL := parseDuration(os.Getenv("HUBRISE_ALERT_DEDUP_TTL"), 10*time.Minute)
	emailAlerter := alerter.NewEmailAlerter(recipients, dedupTTL)

	severity := alerter.SeverityWarning
	if snap.Status == "down" {
		severity = alerter.SeverityCritical
	}
	title := fmt.Sprintf("HubRise: %s", snap.Status)
	body := buildBody(snap)

	if err := emailAlerter.Alert(context.Background(), severity, title, body); err != nil {
		zap.L().Error("alert send failed", zap.Error(err))
		os.Exit(1)
	}
	zap.L().Info("alert sent",
		zap.String("severity", string(severity)),
		zap.String("status", snap.Status),
		zap.Strings("recipients", recipients))
}

func fetchHealth(url string) (*healthSnapshot, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	// The endpoint returns 503 on "down" with a valid JSON body, so
	// we decode regardless of the HTTP status.
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var snap healthSnapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		return nil, fmt.Errorf("decode health response: %w (body=%s)",
			err, truncate(string(body), 200))
	}
	return &snap, nil
}

func parseRecipients(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	cleaned := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			cleaned = append(cleaned, t)
		}
	}
	return cleaned
}

func parseDuration(raw string, fallback time.Duration) time.Duration {
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return fallback
	}
	return d
}

func buildBody(snap *healthSnapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "HubRise integration status: %s\n\n", strings.ToUpper(snap.Status))
	fmt.Fprintf(&b, "Failed orders (attempts ≥5): %d\n", snap.OrdersFailedCount)
	fmt.Fprintf(&b, "Stuck pending orders:         %d\n", snap.OrdersStuckPendingCount)
	if snap.LastSuccessfulPushAge != nil {
		fmt.Fprintf(&b, "Last successful push:         %d seconds ago\n", *snap.LastSuccessfulPushAge)
	} else {
		fmt.Fprintf(&b, "Last successful push:         never\n")
	}
	if snap.CatalogLastPushStatus != nil {
		fmt.Fprintf(&b, "Catalog last push status:     %s\n", *snap.CatalogLastPushStatus)
	}
	if snap.CatalogLastPushAge != nil {
		fmt.Fprintf(&b, "Catalog last push age:        %d seconds ago\n", *snap.CatalogLastPushAge)
	}
	if len(snap.Reasons) > 0 {
		b.WriteString("\nReasons:\n")
		for _, r := range snap.Reasons {
			fmt.Fprintf(&b, "  - %s\n", r)
		}
	}
	fmt.Fprintf(&b, "\nGenerated at: %s\n", snap.GeneratedAt.Format(time.RFC3339))
	return b.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
