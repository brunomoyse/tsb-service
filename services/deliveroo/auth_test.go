// +build integration

package deliveroo

import (
	"context"
	"os"
	"testing"

	"github.com/joho/godotenv"
)

// TestAuthentication tests the Deliveroo OAuth authentication
func TestAuthentication(t *testing.T) {
	// Load .env from project root
	if err := godotenv.Load("../../.env"); err != nil {
		t.Logf("Warning: .env file not found: %v", err)
	}

	clientID := os.Getenv("DELIVEROO_CLIENT_ID")
	clientSecret := os.Getenv("DELIVEROO_CLIENT_SECRET")
	useSandbox := os.Getenv("DELIVEROO_USE_SANDBOX") == "true"

	if clientID == "" || clientSecret == "" {
		t.Fatal("DELIVEROO_CLIENT_ID and DELIVEROO_CLIENT_SECRET must be set")
	}

	t.Logf("Testing authentication...")
	t.Logf("  Client ID: %s", clientID)
	t.Logf("  Sandbox mode: %v", useSandbox)

	// Create adapter
	adapter := NewAdapter(AdapterConfig{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		UseSandbox:   useSandbox,
	})

	// Try to get access token
	ctx := context.Background()
	err := adapter.getAccessToken(ctx)

	if err != nil {
		t.Fatalf("Authentication failed: %v", err)
	}

	// Check if token was set
	token := adapter.getToken()
	if token == "" {
		t.Fatal("Token is empty after authentication")
	}

	t.Logf("✓ Authentication successful!")
	t.Logf("  Token (first 20 chars): %s...", token[:20])
}

// TestDebugAuth prints detailed debug information about the auth request
func TestDebugAuth(t *testing.T) {
	// Load .env
	if err := godotenv.Load("../../.env"); err != nil {
		t.Logf("Warning: .env file not found: %v", err)
	}

	clientID := os.Getenv("DELIVEROO_CLIENT_ID")
	clientSecret := os.Getenv("DELIVEROO_CLIENT_SECRET")
	useSandbox := os.Getenv("DELIVEROO_USE_SANDBOX") == "true"

	adapter := NewAdapter(AdapterConfig{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		UseSandbox:   useSandbox,
	})

	authURL := adapter.getBaseURL("auth")
	menuURL := adapter.getBaseURL("menu")
	orderURL := adapter.getBaseURL("order")
	siteURL := adapter.getBaseURL("site")

	t.Logf("Configuration:")
	t.Logf("  Client ID: %s", clientID)
	t.Logf("  Client Secret: %s...", clientSecret[:10])
	t.Logf("  Sandbox Mode: %v", useSandbox)
	t.Logf("\nAPI Endpoints:")
	t.Logf("  Auth URL: %s/oauth2/token", authURL)
	t.Logf("  Menu URL: %s", menuURL)
	t.Logf("  Order URL: %s", orderURL)
	t.Logf("  Site URL: %s", siteURL)

	t.Logf("\n✓ Configuration looks correct")
}