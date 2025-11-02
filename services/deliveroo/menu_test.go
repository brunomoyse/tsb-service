package deliveroo

import (
	"context"
	"os"
	"testing"

	"github.com/joho/godotenv"
)

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestGetMenu tests retrieving a menu using V2 API
func TestGetMenu(t *testing.T) {
	// Load .env from project root
	if err := godotenv.Load("../../.env"); err != nil {
		t.Logf("Warning: .env file not found: %v", err)
	}

	clientID := os.Getenv("DELIVEROO_CLIENT_ID")
	clientSecret := os.Getenv("DELIVEROO_CLIENT_SECRET")
	brandID := os.Getenv("DELIVEROO_BRAND_ID")
	siteID := os.Getenv("DELIVEROO_SITE_ID")
	useSandbox := os.Getenv("DELIVEROO_USE_SANDBOX") == "true"

	if clientID == "" || clientSecret == "" || brandID == "" || siteID == "" {
		t.Fatal("All Deliveroo environment variables must be set")
	}

	t.Logf("Testing GetMenuV2...")
	t.Logf("  Brand ID: %s", brandID)
	t.Logf("  Site ID: %s", siteID)

	// Create adapter
	adapter := NewAdapter(AdapterConfig{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		UseSandbox:   useSandbox,
	})

	// Get menu
	ctx := context.Background()
	menu, err := adapter.GetMenuV2(ctx, brandID, siteID)

	if err != nil {
		t.Fatalf("GetMenuV2 failed: %v", err)
	}

	t.Logf("✓ Menu retrieved successfully!")
	t.Logf("  Menu name: %s", menu.Name)
	t.Logf("  Categories: %d", len(menu.Menu.Categories))
	t.Logf("  Items: %d", len(menu.Menu.Items))

	// Print first few items
	if len(menu.Menu.Items) > 0 {
		t.Logf("\n  Sample items:")
		for i := 0; i < min(3, len(menu.Menu.Items)); i++ {
			item := menu.Menu.Items[i]
			name := "Unknown"
			if n, ok := item.Name["en"]; ok {
				name = n
			} else if n, ok := item.Name["fr"]; ok {
				name = n
			}
			t.Logf("    - %s (€%.2f)", name, float64(item.PriceInfo.Price)/100.0)
		}
	}
}

// TestCreateSampleMenu creates a sample Japanese restaurant menu
// NOTE: Uses V1 Menu API because V2 is read-only and V3 is for large menus only
func TestCreateSampleMenu(t *testing.T) {
	// Load .env
	if err := godotenv.Load("../../.env"); err != nil {
		t.Logf("Warning: .env file not found: %v", err)
	}

	clientID := os.Getenv("DELIVEROO_CLIENT_ID")
	clientSecret := os.Getenv("DELIVEROO_CLIENT_SECRET")
	brandID := os.Getenv("DELIVEROO_BRAND_ID")
	siteID := os.Getenv("DELIVEROO_SITE_ID")
	useSandbox := os.Getenv("DELIVEROO_USE_SANDBOX") == "true"

	if clientID == "" || clientSecret == "" || brandID == "" || siteID == "" {
		t.Fatal("All Deliveroo environment variables must be set")
	}

	t.Logf("Creating sample Japanese restaurant menu...")
	t.Logf("  Brand ID: %s", brandID)
	t.Logf("  Site ID: %s", siteID)

	adapter := NewAdapter(AdapterConfig{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		UseSandbox:   useSandbox,
	})

	// Create a simple Japanese restaurant menu
	menu := &MenuUploadRequest{
		Name: "Tokyo Sushi Test Menu",
		Menu: MenuContent{
			Categories: []Category{
				{
					ID:          "sushi",
					Name:        map[string]string{"en": "Sushi", "fr": "Sushi"},
					Description: map[string]string{"en": "Fresh sushi", "fr": "Sushi frais"},
					ItemIDs:     []string{"salmon-sushi", "tuna-sushi"},
				},
				{
					ID:          "maki",
					Name:        map[string]string{"en": "Maki Rolls", "fr": "Makis"},
					Description: map[string]string{"en": "Traditional maki rolls", "fr": "Makis traditionnels"},
					ItemIDs:     []string{"california-roll", "spicy-tuna-roll"},
				},
			},
			Mealtimes: []Mealtime{
				{
					ID:          "all-day-menu",
					Name:        map[string]string{"en": "All Day Menu", "fr": "Menu Toute la Journée"},
					Description: map[string]string{"en": "Available all day", "fr": "Disponible toute la journée"},
					Image:       &ImageInfo{URL: "https://via.placeholder.com/1920x1080"}, // Placeholder image
					CategoryIDs: []string{"sushi", "maki"},
					Schedule: []MealtimeSchedule{
						// Monday (0) through Sunday (6), available 00:00-23:59
						{DayOfWeek: 0, TimePeriods: []TimePeriod{{Start: "00:00", End: "23:59"}}},
						{DayOfWeek: 1, TimePeriods: []TimePeriod{{Start: "00:00", End: "23:59"}}},
						{DayOfWeek: 2, TimePeriods: []TimePeriod{{Start: "00:00", End: "23:59"}}},
						{DayOfWeek: 3, TimePeriods: []TimePeriod{{Start: "00:00", End: "23:59"}}},
						{DayOfWeek: 4, TimePeriods: []TimePeriod{{Start: "00:00", End: "23:59"}}},
						{DayOfWeek: 5, TimePeriods: []TimePeriod{{Start: "00:00", End: "23:59"}}},
						{DayOfWeek: 6, TimePeriods: []TimePeriod{{Start: "00:00", End: "23:59"}}},
					},
				},
			},
			Items: []Item{
				{
					ID:                        "salmon-sushi",
					Name:                      map[string]string{"en": "Salmon Sushi", "fr": "Sushi Saumon"},
					Description:               map[string]string{"en": "Fresh salmon nigiri", "fr": "Nigiri au saumon frais"},
					OperationalName:           "Salmon Sushi",
					PriceInfo:                 PriceInfo{Price: 450}, // €4.50
					TaxRate:                   "6",                   // Belgium food tax rate
					PLU:                       "SALMON-001",
					ContainsAlcohol:           false,
					Allergies:                 []string{"fish"},
					Diets:                     []string{},
					Type:                      "ITEM",
					IsEligibleAsReplacement:   true,
					IsEligibleForSubstitution: true,
				},
				{
					ID:                        "tuna-sushi",
					Name:                      map[string]string{"en": "Tuna Sushi", "fr": "Sushi Thon"},
					Description:               map[string]string{"en": "Fresh tuna nigiri", "fr": "Nigiri au thon frais"},
					OperationalName:           "Tuna Sushi",
					PriceInfo:                 PriceInfo{Price: 500}, // €5.00
					TaxRate:                   "6",                   // Belgium food tax rate
					PLU:                       "TUNA-001",
					ContainsAlcohol:           false,
					Allergies:                 []string{"fish"},
					Diets:                     []string{},
					Type:                      "ITEM",
					IsEligibleAsReplacement:   true,
					IsEligibleForSubstitution: true,
				},
				{
					ID:                        "california-roll",
					Name:                      map[string]string{"en": "California Roll", "fr": "California Roll"},
					Description:               map[string]string{"en": "Avocado, crab, cucumber", "fr": "Avocat, crabe, concombre"},
					OperationalName:           "California Roll",
					PriceInfo:                 PriceInfo{Price: 850}, // €8.50
					TaxRate:                   "6",                   // Belgium food tax rate
					PLU:                       "CALI-001",
					ContainsAlcohol:           false,
					Allergies:                 []string{"crustaceans"}, // Use crustaceans instead of shellfish
					Diets:                     []string{},
					Type:                      "ITEM",
					IsEligibleAsReplacement:   true,
					IsEligibleForSubstitution: true,
				},
				{
					ID:                        "spicy-tuna-roll",
					Name:                      map[string]string{"en": "Spicy Tuna Roll", "fr": "Maki Thon Épicé"},
					Description:               map[string]string{"en": "Tuna with spicy mayo", "fr": "Thon avec mayo épicée"},
					OperationalName:           "Spicy Tuna Roll",
					PriceInfo:                 PriceInfo{Price: 900}, // €9.00
					TaxRate:                   "6",                   // Belgium food tax rate
					PLU:                       "SPICY-TUNA-001",
					ContainsAlcohol:           false,
					Allergies:                 []string{"fish"},
					Diets:                     []string{},
					Type:                      "ITEM",
					IsEligibleAsReplacement:   true,
					IsEligibleForSubstitution: true,
				},
			},
		},
		SiteIDs: []string{siteID},
	}

	// Use a test menu ID
	menuID := "test-japanese-menu"

	// Upload menu using V1 API
	ctx := context.Background()
	if err := adapter.PushMenu(ctx, brandID, menuID, menu); err != nil {
		t.Fatalf("Failed to upload menu: %v", err)
	}

	t.Logf("✓ Menu created successfully!")
	t.Logf("  Menu ID: %s", menuID)
	t.Logf("  Categories: %d", len(menu.Menu.Categories))
	t.Logf("  Items: %d", len(menu.Menu.Items))
	t.Logf("\nYou can now fetch this menu using GetMenuV2 with site_id=%s", siteID)
	t.Logf("Or add to your .env: DELIVEROO_MENU_ID=%s", menuID)
}