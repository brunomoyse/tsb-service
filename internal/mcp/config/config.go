// Package config loads the MCP server configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds every setting of the MCP server. All values come from env vars.
type Config struct {
	// UpstreamBaseURL is the tsb-service API root, e.g. https://tokyosushibarliege.be/api/v1.
	UpstreamBaseURL string

	ZitadelIssuer       string
	ZitadelInternalURL  string
	ZitadelClientID     string
	ZitadelClientSecret string
	ZitadelProjectID    string

	MCPAuthToken      string
	InternalAPIToken  string
	DBPath            string
	PendingTTL        time.Duration
	PriceMaxChangePct int
	Location          *time.Location
	LogLevel          slog.Level
}

// Load reads and validates the configuration.
func Load() (*Config, error) {
	c := &Config{
		UpstreamBaseURL:     strings.TrimRight(os.Getenv("UPSTREAM_BASE_URL"), "/"),
		ZitadelIssuer:       strings.TrimRight(os.Getenv("ZITADEL_ISSUER"), "/"),
		ZitadelInternalURL:  strings.TrimRight(os.Getenv("ZITADEL_INTERNAL_URL"), "/"),
		ZitadelClientID:     os.Getenv("ZITADEL_CLIENT_ID"),
		ZitadelClientSecret: os.Getenv("ZITADEL_CLIENT_SECRET"),
		ZitadelProjectID:    os.Getenv("ZITADEL_PROJECT_ID"),
		MCPAuthToken:        os.Getenv("MCP_AUTH_TOKEN"),
		InternalAPIToken:    os.Getenv("INTERNAL_API_TOKEN"),
		DBPath:              envOr("DB_PATH", "tsb-mcp.db"),
	}

	var errs []error
	require := func(name, v string) {
		if v == "" {
			errs = append(errs, fmt.Errorf("%s is required", name))
		}
	}
	require("UPSTREAM_BASE_URL", c.UpstreamBaseURL)
	require("ZITADEL_ISSUER", c.ZitadelIssuer)
	require("ZITADEL_CLIENT_ID", c.ZitadelClientID)
	require("ZITADEL_CLIENT_SECRET", c.ZitadelClientSecret)
	require("ZITADEL_PROJECT_ID", c.ZitadelProjectID)
	require("INTERNAL_API_TOKEN", c.InternalAPIToken)

	if c.UpstreamBaseURL != "" {
		if u, err := url.Parse(c.UpstreamBaseURL); err != nil || u.Scheme == "" || u.Host == "" {
			errs = append(errs, errors.New("UPSTREAM_BASE_URL must be an absolute URL"))
		}
	}

	ttl, err := time.ParseDuration(envOr("PENDING_TTL", "10m"))
	if err != nil || ttl <= 0 {
		errs = append(errs, errors.New("PENDING_TTL must be a positive duration such as 10m"))
	}
	c.PendingTTL = ttl

	pct, err := strconv.Atoi(envOr("PRICE_MAX_CHANGE_PCT", "50"))
	if err != nil || pct <= 0 || pct > 1000 {
		errs = append(errs, errors.New("PRICE_MAX_CHANGE_PCT must be an integer between 1 and 1000"))
	}
	c.PriceMaxChangePct = pct

	loc, err := time.LoadLocation(envOr("TZ_DEFAULT", "Europe/Brussels"))
	if err != nil {
		errs = append(errs, fmt.Errorf("TZ_DEFAULT: %w", err))
	}
	c.Location = loc

	if err := c.LogLevel.UnmarshalText([]byte(envOr("LOG_LEVEL", "info"))); err != nil {
		errs = append(errs, fmt.Errorf("LOG_LEVEL: %w", err))
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return c, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
