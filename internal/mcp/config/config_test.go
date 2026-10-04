package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

var allEnv = []string{"UPSTREAM_BASE_URL", "ZITADEL_ISSUER", "ZITADEL_INTERNAL_URL", "ZITADEL_CLIENT_ID", "ZITADEL_CLIENT_SECRET", "ZITADEL_PROJECT_ID",
	"MCP_AUTH_TOKEN", "INTERNAL_API_TOKEN", "DB_PATH", "PENDING_TTL", "PRICE_MAX_CHANGE_PCT", "TZ_DEFAULT", "LOG_LEVEL"}

// setEnv clears every setting, then applies the given ones.
func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for _, k := range allEnv {
		t.Setenv(k, "")
	}
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

func minimal() map[string]string {
	return map[string]string{
		"UPSTREAM_BASE_URL": "https://tokyosushibarliege.be/api/v1", "ZITADEL_ISSUER": "https://auth.example.com", "ZITADEL_CLIENT_ID": "mcp",
		"ZITADEL_CLIENT_SECRET": "s3cret-value", "ZITADEL_PROJECT_ID": "123", "INTERNAL_API_TOKEN": "internal",
	}
}

func with(base map[string]string, kv map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range kv {
		out[k] = v
	}
	return out
}

func TestLoadDefaults(t *testing.T) {
	setEnv(t, minimal())
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.UpstreamBaseURL != "https://tokyosushibarliege.be/api/v1" || c.ZitadelIssuer != "https://auth.example.com" || c.ZitadelClientID != "mcp" || c.ZitadelClientSecret != "s3cret-value" ||
		c.ZitadelProjectID != "123" || c.InternalAPIToken != "internal" {
		t.Errorf("required values: %+v", c)
	}
	if c.DBPath != "tsb-mcp.db" || c.PendingTTL != 10*time.Minute || c.PriceMaxChangePct != 50 || c.Location.String() != "Europe/Brussels" || c.LogLevel != slog.LevelInfo {
		t.Errorf("defaults: %+v", c)
	}
	if c.MCPAuthToken != "" || c.ZitadelInternalURL != "" {
		t.Errorf("optional values must default to empty: %+v", c)
	}
}

func TestLoadOverrides(t *testing.T) {
	setEnv(t, with(minimal(), map[string]string{
		"ZITADEL_INTERNAL_URL": "http://zitadel.auth.svc:8080/", "MCP_AUTH_TOKEN": "mcp-token", "DB_PATH": "/data/mcp.db", "PENDING_TTL": "90s",
		"PRICE_MAX_CHANGE_PCT": "25", "TZ_DEFAULT": "Asia/Shanghai", "LOG_LEVEL": "debug",
	}))
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.ZitadelInternalURL != "http://zitadel.auth.svc:8080" || c.MCPAuthToken != "mcp-token" || c.DBPath != "/data/mcp.db" || c.PendingTTL != 90*time.Second ||
		c.PriceMaxChangePct != 25 || c.Location.String() != "Asia/Shanghai" || c.LogLevel != slog.LevelDebug {
		t.Errorf("overrides: %+v", c)
	}
}

func TestLoadTrimsTrailingSlashes(t *testing.T) {
	setEnv(t, with(minimal(), map[string]string{"UPSTREAM_BASE_URL": "https://api.example.com/api/v1///", "ZITADEL_ISSUER": "https://auth.example.com/"}))
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	// The issuer is compared with the token's `iss`, the base URL gets "/graphql" appended.
	if c.UpstreamBaseURL != "https://api.example.com/api/v1" || c.ZitadelIssuer != "https://auth.example.com" {
		t.Errorf("urls: %q %q", c.UpstreamBaseURL, c.ZitadelIssuer)
	}
}

func TestLoadRequiredSettings(t *testing.T) {
	for _, name := range []string{"UPSTREAM_BASE_URL", "ZITADEL_ISSUER", "ZITADEL_CLIENT_ID", "ZITADEL_CLIENT_SECRET", "ZITADEL_PROJECT_ID", "INTERNAL_API_TOKEN"} {
		t.Run(name, func(t *testing.T) {
			kv := minimal()
			delete(kv, name)
			setEnv(t, kv)
			c, err := Load()
			if err == nil || c != nil {
				t.Fatalf("a missing %s must fail the start: %v %v", name, c, err)
			}
			if !strings.Contains(err.Error(), name+" is required") {
				t.Errorf("error = %q", err)
			}
		})
	}
	// Every problem is reported at once, and secrets are never echoed.
	setEnv(t, map[string]string{"ZITADEL_CLIENT_SECRET": "s3cret-value", "PENDING_TTL": "soon", "PRICE_MAX_CHANGE_PCT": "lots", "TZ_DEFAULT": "Mars/Base", "LOG_LEVEL": "loud"})
	_, err := Load()
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"UPSTREAM_BASE_URL is required", "ZITADEL_ISSUER is required", "ZITADEL_CLIENT_ID is required", "ZITADEL_PROJECT_ID is required", "INTERNAL_API_TOKEN is required",
		"PENDING_TTL must be a positive duration", "PRICE_MAX_CHANGE_PCT must be an integer between 1 and 1000", "TZ_DEFAULT:", "LOG_LEVEL:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "s3cret-value") {
		t.Error("the error must not echo secrets")
	}
	// The optional MCP token is not required (the server then refuses to start elsewhere, not here).
	setEnv(t, minimal())
	if _, err := Load(); err != nil {
		t.Errorf("MCP_AUTH_TOKEN is optional: %v", err)
	}
}

func TestLoadValidatesValues(t *testing.T) {
	tests := []struct {
		name string
		kv   map[string]string
		want string // empty = valid
	}{
		{"relative base url", map[string]string{"UPSTREAM_BASE_URL": "/api/v1"}, "UPSTREAM_BASE_URL must be an absolute URL"},
		{"base url without scheme", map[string]string{"UPSTREAM_BASE_URL": "tokyosushibarliege.be/api/v1"}, "UPSTREAM_BASE_URL must be an absolute URL"},
		{"base url without host", map[string]string{"UPSTREAM_BASE_URL": "https:///api"}, "UPSTREAM_BASE_URL must be an absolute URL"},
		{"unparseable base url", map[string]string{"UPSTREAM_BASE_URL": "http://[::1"}, "UPSTREAM_BASE_URL must be an absolute URL"},
		{"in-cluster base url", map[string]string{"UPSTREAM_BASE_URL": "http://tsb-service.tokyosushi.svc:8080/api/v1"}, ""},
		{"ttl garbage", map[string]string{"PENDING_TTL": "ten"}, "PENDING_TTL"},
		{"ttl without unit", map[string]string{"PENDING_TTL": "10"}, "PENDING_TTL"},
		{"ttl zero", map[string]string{"PENDING_TTL": "0s"}, "PENDING_TTL"},
		{"ttl negative", map[string]string{"PENDING_TTL": "-5m"}, "PENDING_TTL"},
		{"ttl hours", map[string]string{"PENDING_TTL": "1h"}, ""},
		{"pct zero", map[string]string{"PRICE_MAX_CHANGE_PCT": "0"}, "PRICE_MAX_CHANGE_PCT"},
		{"pct negative", map[string]string{"PRICE_MAX_CHANGE_PCT": "-10"}, "PRICE_MAX_CHANGE_PCT"},
		{"pct too high", map[string]string{"PRICE_MAX_CHANGE_PCT": "1001"}, "PRICE_MAX_CHANGE_PCT"},
		{"pct decimal", map[string]string{"PRICE_MAX_CHANGE_PCT": "12.5"}, "PRICE_MAX_CHANGE_PCT"},
		{"pct min", map[string]string{"PRICE_MAX_CHANGE_PCT": "1"}, ""},
		{"pct max", map[string]string{"PRICE_MAX_CHANGE_PCT": "1000"}, ""},
		{"unknown timezone", map[string]string{"TZ_DEFAULT": "Brussels"}, "TZ_DEFAULT"},
		{"unknown log level", map[string]string{"LOG_LEVEL": "verbose"}, "LOG_LEVEL"},
		{"warn level", map[string]string{"LOG_LEVEL": "WARN"}, ""},
		{"error level", map[string]string{"LOG_LEVEL": "error"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setEnv(t, with(minimal(), tt.kv))
			c, err := Load()
			if tt.want == "" {
				if err != nil || c == nil {
					t.Fatalf("want a valid config, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) || c != nil {
				t.Errorf("want error containing %q, got %v (config %v)", tt.want, err, c)
			}
		})
	}
}

func TestLogLevelsMap(t *testing.T) {
	for in, want := range map[string]slog.Level{"debug": slog.LevelDebug, "info": slog.LevelInfo, "warn": slog.LevelWarn, "error": slog.LevelError} {
		setEnv(t, with(minimal(), map[string]string{"LOG_LEVEL": in}))
		c, err := Load()
		if err != nil || c.LogLevel != want {
			t.Errorf("LOG_LEVEL=%s: %v %v", in, c, err)
		}
	}
}

func TestEnvOr(t *testing.T) {
	t.Setenv("CFG_TEST_SET", "value")
	t.Setenv("CFG_TEST_EMPTY", "")
	if envOr("CFG_TEST_SET", "def") != "value" || envOr("CFG_TEST_EMPTY", "def") != "def" || envOr("CFG_TEST_UNSET_X", "def") != "def" {
		t.Error("envOr")
	}
}
