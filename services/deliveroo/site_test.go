package deliveroo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/joho/godotenv"
)

// TestGetSites tests retrieving sites for a brand
func TestGetSites(t *testing.T) {
	// Load .env from project root
	if err := godotenv.Load("../../.env"); err != nil {
		t.Logf("Warning: .env file not found: %v", err)
	}

	clientID := os.Getenv("DELIVEROO_CLIENT_ID")
	clientSecret := os.Getenv("DELIVEROO_CLIENT_SECRET")
	brandID := os.Getenv("DELIVEROO_BRAND_ID")
	useSandbox := os.Getenv("DELIVEROO_USE_SANDBOX") == "true"

	if clientID == "" || clientSecret == "" || brandID == "" {
		t.Fatal("DELIVEROO_CLIENT_ID, DELIVEROO_CLIENT_SECRET, and DELIVEROO_BRAND_ID must be set")
	}

	t.Logf("Testing GetSitesConfig...")
	t.Logf("  Brand ID: %s", brandID)

	// Create adapter
	adapter := NewAdapter(AdapterConfig{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		UseSandbox:   useSandbox,
	})

	// Get sites
	ctx := context.Background()
	sitesConfig, err := adapter.GetSitesConfig(ctx, brandID)

	if err != nil {
		t.Fatalf("GetSitesConfig failed: %v", err)
	}

	t.Logf("✓ Found %d sites:", len(sitesConfig.Sites))
	for i, site := range sitesConfig.Sites {
		t.Logf("  %d. %s", i+1, site.Name)
		t.Logf("     Location ID: %s", site.LocationID)
		t.Logf("     Webhook Type: %s", site.OrdersAPIWebhookType)
	}
}

// TestGetBrandID retrieves the brand ID from a site ID
func TestGetBrandID(t *testing.T) {
	// Load .env
	if err := godotenv.Load("../../.env"); err != nil {
		t.Logf("Warning: .env file not found: %v", err)
	}

	clientID := os.Getenv("DELIVEROO_CLIENT_ID")
	clientSecret := os.Getenv("DELIVEROO_CLIENT_SECRET")
	siteID := os.Getenv("DELIVEROO_SITE_ID")
	useSandbox := os.Getenv("DELIVEROO_USE_SANDBOX") == "true"

	if clientID == "" || clientSecret == "" || siteID == "" {
		t.Fatal("DELIVEROO_CLIENT_ID, DELIVEROO_CLIENT_SECRET, and DELIVEROO_SITE_ID must be set")
	}

	t.Logf("Getting brand ID for site: %s", siteID)

	adapter := NewAdapter(AdapterConfig{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		UseSandbox:   useSandbox,
	})

	// Make raw API request to get brand ID
	ctx := context.Background()

	// Ensure we have a valid token
	if err := adapter.getAccessToken(ctx); err != nil {
		t.Fatalf("Failed to get access token: %v", err)
	}

	// Call the Site API
	url := fmt.Sprintf("%s/v1/restaurant_locations/%s", adapter.getBaseURL("site"), siteID)
	resp, err := adapter.doRequest(ctx, "GET", url, nil, nil)
	if err != nil {
		t.Fatalf("Failed to get brand ID: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("Get brand ID failed with status %d: %s", resp.StatusCode, string(body))
	}

	// Parse response
	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	t.Logf("✓ Response: %+v", result)

	if brandIDs, ok := result["brand_id"].([]interface{}); ok && len(brandIDs) > 0 {
		brandID := brandIDs[0].(string)
		t.Logf("✓ Brand ID found: %s", brandID)
		t.Logf("\nAdd this to your .env file:")
		t.Logf("DELIVEROO_BRAND_ID=%s", brandID)
	} else {
		t.Fatal("No brand_id found in response")
	}
}