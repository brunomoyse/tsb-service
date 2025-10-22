package deliveroo

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"
)

// This file contains comprehensive examples for the Deliveroo Menu API

// ExampleDailyItemUnavailability demonstrates marking items as sold out
func ExampleDailyItemUnavailability() {
	adapter := NewAdapter(AdapterConfig{
		ClientID:     "your-client-id",
		ClientSecret: "your-client-secret",
		UseSandbox:   false,
	})

	ctx := context.Background()
	brandID := "your-brand-id"
	siteID := "your-site-id"

	// Mark specific items as unavailable (sold out for the day)
	req := UpdateItemUnavailabilitiesRequest{
		ItemUnavailabilities: []ItemUnavailability{
			{
				ItemID: "chicken-breast",
				Status: StatusUnavailable, // Sold out, greyed out in app
			},
			{
				ItemID: "salmon",
				Status: StatusUnavailable,
			},
		},
	}

	if err := adapter.UpdateItemUnavailabilitiesV2(ctx, brandID, siteID, req); err != nil {
		log.Printf("Failed to mark items unavailable: %v", err)
		return
	}

	log.Println("✓ Items marked as sold out")
}

// ExampleBulkUnavailabilityUpdate demonstrates replacing all unavailabilities
func ExampleBulkUnavailabilityUpdate() {
	adapter := NewAdapter(AdapterConfig{
		ClientID:     "your-client-id",
		ClientSecret: "your-client-secret",
		UseSandbox:   false,
	})

	ctx := context.Background()
	brandID := "your-brand-id"
	siteID := "your-site-id"

	// Replace ALL unavailabilities (clears previous ones)
	req := ReplaceAllUnavailabilitiesRequest{
		UnavailableIDs: []string{"item-1", "item-2", "item-3"}, // Sold out for the day
		HiddenIDs:      []string{"item-4"},                     // Hidden indefinitely
	}

	if err := adapter.ReplaceItemUnavailabilitiesV2(ctx, brandID, siteID, req); err != nil {
		log.Printf("Failed to replace unavailabilities: %v", err)
		return
	}

	log.Println("✓ All unavailabilities updated")
}

// ExampleMarkItemsAvailableAgain demonstrates making sold-out items available
func ExampleMarkItemsAvailableAgain() {
	adapter := NewAdapter(AdapterConfig{
		ClientID:     "your-client-id",
		ClientSecret: "your-client-secret",
		UseSandbox:   false,
	})

	ctx := context.Background()
	brandID := "your-brand-id"
	siteID := "your-site-id"

	// Mark previously unavailable items as available again
	req := UpdateItemUnavailabilitiesRequest{
		ItemUnavailabilities: []ItemUnavailability{
			{
				ItemID: "chicken-breast",
				Status: StatusAvailable, // Back in stock!
			},
		},
	}

	if err := adapter.UpdateItemUnavailabilitiesV2(ctx, brandID, siteID, req); err != nil {
		log.Printf("Failed to mark items available: %v", err)
		return
	}

	log.Println("✓ Items are available again")
}

// ExampleGetCurrentUnavailabilities demonstrates retrieving current unavailabilities
func ExampleGetCurrentUnavailabilities() {
	adapter := NewAdapter(AdapterConfig{
		ClientID:     "your-client-id",
		ClientSecret: "your-client-secret",
		UseSandbox:   false,
	})

	ctx := context.Background()
	brandID := "your-brand-id"
	siteID := "your-site-id"

	// Get currently unavailable items
	unavailabilities, err := adapter.GetItemUnavailabilitiesV2(ctx, brandID, siteID)
	if err != nil {
		log.Printf("Failed to get unavailabilities: %v", err)
		return
	}

	fmt.Printf("Sold out items (%d): %v\n", len(unavailabilities.UnavailableIDs), unavailabilities.UnavailableIDs)
	fmt.Printf("Hidden items (%d): %v\n", len(unavailabilities.HiddenIDs), unavailabilities.HiddenIDs)
}

// ExamplePLUSynchronization demonstrates syncing menu items with POS PLU codes
func ExamplePLUSynchronization() {
	adapter := NewAdapter(AdapterConfig{
		ClientID:     "your-client-id",
		ClientSecret: "your-client-secret",
		UseSandbox:   false,
	})

	ctx := context.Background()
	brandID := "your-brand-id"
	menuID := "your-menu-id"

	// Map menu items to POS PLU codes
	mappings := UpdatePLUsRequest{
		{ItemID: "burger-classic", PLU: "1001"},
		{ItemID: "burger-cheese", PLU: "1002"},
		{ItemID: "fries-regular", PLU: "2001"},
		{ItemID: "fries-large", PLU: "2002"},
		{ItemID: "cola-regular", PLU: "3001"},
	}

	if err := adapter.UpdatePLUs(ctx, brandID, menuID, mappings); err != nil {
		log.Printf("Failed to update PLUs: %v", err)
		return
	}

	log.Println("✓ PLU mappings synchronized")
}

// ExampleV3LargeMenuUpload demonstrates async upload for menus >5MB
func ExampleV3LargeMenuUpload() {
	adapter := NewAdapter(AdapterConfig{
		ClientID:     "your-client-id",
		ClientSecret: "your-client-secret",
		UseSandbox:   false,
	})

	ctx := context.Background()
	brandID := "your-brand-id"
	menuID := "your-menu-id"

	// Step 1: Get presigned S3 URL
	log.Println("Step 1: Getting S3 upload URL...")
	uploadResp, err := adapter.GetMenuUploadURLV3(ctx, brandID, menuID)
	if err != nil {
		log.Printf("Failed to get upload URL: %v", err)
		return
	}

	log.Printf("✓ Got upload URL for menu %s (version: %s)", uploadResp.ID, uploadResp.Version)

	// Step 2: Upload menu directly to S3
	log.Println("Step 2: Uploading menu to S3...")
	menu := &MenuUploadRequest{
		Name: "My Large Menu",
		Menu: MenuContent{
			Categories: []Category{
				// ... your large menu data
			},
			Items: []Item{
				// ... hundreds or thousands of items
			},
		},
		SiteIDs: []string{"site-1", "site-2"},
	}

	if err := adapter.UploadMenuToS3(ctx, uploadResp.UploadURL, menu); err != nil {
		log.Printf("Failed to upload to S3: %v", err)
		return
	}

	log.Println("✓ Menu uploaded to S3")

	// Step 3: Publish the menu to live
	log.Println("Step 3: Publishing menu to live...")
	publishReq := PublishMenuJobRequest{
		Action: JobActionPublishMenuToLive,
		Params: PublishMenuJobParams{
			BrandID: brandID,
			MenuID:  menuID,
			Version: &uploadResp.Version,
		},
	}

	job, err := adapter.PublishMenuJob(ctx, brandID, publishReq)
	if err != nil {
		log.Printf("Failed to create publish job: %v", err)
		return
	}

	log.Printf("✓ Publish job created: %s (status: %s)", job.ID, job.Status)

	// Step 4: Poll job status until complete
	log.Println("Step 4: Waiting for job to complete...")
	for {
		time.Sleep(5 * time.Second)

		jobStatus, err := adapter.GetJobStatus(ctx, brandID, job.ID)
		if err != nil {
			log.Printf("Failed to get job status: %v", err)
			return
		}

		log.Printf("Job status: %s", jobStatus.Status)

		switch jobStatus.Status {
		case JobStatusCompleted:
			log.Println("✓ Menu published successfully!")
			return

		case JobStatusFailed:
			if jobStatus.Error != nil {
				log.Printf("✗ Job failed: %s", *jobStatus.Error)
			} else {
				log.Println("✗ Job failed (no error message)")
			}
			return

		case JobStatusPending, JobStatusRunning:
			// Continue polling
			continue
		}
	}
}

// ExampleMenuV2Retrieval demonstrates getting menu from V2 API
func ExampleMenuV2Retrieval() {
	adapter := NewAdapter(AdapterConfig{
		ClientID:     "your-client-id",
		ClientSecret: "your-client-secret",
		UseSandbox:   false,
	})

	ctx := context.Background()
	brandID := "your-brand-id"
	siteID := "your-site-id"

	// Get menu for a specific site (works with Menu Manager)
	menu, err := adapter.GetMenuV2(ctx, brandID, siteID)
	if err != nil {
		log.Printf("Failed to get menu: %v", err)
		return
	}

	fmt.Printf("Menu: %s\n", menu.Name)
	fmt.Printf("Categories: %d\n", len(menu.Menu.Categories))
	fmt.Printf("Items: %d\n", len(menu.Menu.Items))
	fmt.Printf("Sites: %v\n", menu.SiteIDs)

	// Display categories
	for _, cat := range menu.Menu.Categories {
		fmt.Printf("\nCategory: %s\n", cat.Name)
		fmt.Printf("  Items: %d\n", len(cat.ItemIDs))
	}
}

// ExampleMenuWebhookConfiguration demonstrates configuring menu webhooks
func ExampleMenuWebhookConfiguration() {
	adapter := NewAdapter(AdapterConfig{
		ClientID:     "your-client-id",
		ClientSecret: "your-client-secret",
		UseSandbox:   false,
	})

	ctx := context.Background()

	// Set menu events webhook
	webhookURL := "https://your-server.com/webhooks/deliveroo/menu-events"
	if err := adapter.SetMenuEventsWebhook(ctx, webhookURL); err != nil {
		log.Printf("Failed to set menu webhook: %v", err)
		return
	}

	log.Println("✓ Menu webhook configured")

	// Get current webhook configuration
	config, err := adapter.GetMenuEventsWebhook(ctx)
	if err != nil {
		log.Printf("Failed to get webhook config: %v", err)
		return
	}

	log.Printf("Current webhook URL: %s", config.WebhookURL)
}

// ExampleMenuWebhookServer demonstrates setting up webhook receiver
func ExampleMenuWebhookServer() {
	webhookSecret := "your-webhook-secret"

	// Setup webhook handler
	http.HandleFunc("/webhooks/deliveroo/menu-events", ExampleMenuEventHandler(webhookSecret))

	// Start server
	log.Println("Starting menu webhook server on :8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatal(err)
	}
}

// ExampleIntegratedInventoryManagement demonstrates a complete inventory workflow
func ExampleIntegratedInventoryManagement() {
	adapter := NewAdapter(AdapterConfig{
		ClientID:     "your-client-id",
		ClientSecret: "your-client-secret",
		UseSandbox:   false,
	})

	ctx := context.Background()
	brandID := "your-brand-id"
	siteID := "your-site-id"

	// Simulate daily inventory check at 8 AM
	log.Println("Running morning inventory check...")

	// Items that are low on stock
	lowStockItems := []string{"salmon", "tuna", "avocado"}

	// Items that are out of stock
	outOfStockItems := []string{"lobster", "caviar"}

	// Get current unavailabilities
	current, err := adapter.GetItemUnavailabilitiesV2(ctx, brandID, siteID)
	if err != nil {
		log.Printf("Failed to get current unavailabilities: %v", err)
		return
	}

	log.Printf("Currently unavailable: %v", current.UnavailableIDs)

	// Update unavailabilities based on inventory
	unavailabilities := []ItemUnavailability{}

	// Mark out of stock items as unavailable
	for _, itemID := range outOfStockItems {
		unavailabilities = append(unavailabilities, ItemUnavailability{
			ItemID: itemID,
			Status: StatusUnavailable,
		})
	}

	// Optionally hide low stock items during peak hours (5 PM - 8 PM)
	hour := time.Now().Hour()
	if hour >= 17 && hour <= 20 {
		for _, itemID := range lowStockItems {
			unavailabilities = append(unavailabilities, ItemUnavailability{
				ItemID: itemID,
				Status: StatusHidden,
			})
		}
		log.Println("Peak hours: hiding low-stock items")
	}

	// Update unavailabilities
	if len(unavailabilities) > 0 {
		req := UpdateItemUnavailabilitiesRequest{
			ItemUnavailabilities: unavailabilities,
		}

		if err := adapter.UpdateItemUnavailabilitiesV2(ctx, brandID, siteID, req); err != nil {
			log.Printf("Failed to update unavailabilities: %v", err)
			return
		}

		log.Printf("✓ Updated %d item unavailabilities", len(unavailabilities))
	}

	// At end of day (midnight), clear all daily unavailabilities
	if hour == 0 {
		clearReq := ReplaceAllUnavailabilitiesRequest{
			UnavailableIDs: []string{}, // Clear all sold out items
			HiddenIDs:      []string{}, // Clear all hidden items
		}

		if err := adapter.ReplaceItemUnavailabilitiesV2(ctx, brandID, siteID, clearReq); err != nil {
			log.Printf("Failed to clear unavailabilities: %v", err)
			return
		}

		log.Println("✓ Cleared all daily unavailabilities for new day")
	}
}

// ExampleRealTimeStockSync demonstrates real-time stock synchronization
func ExampleRealTimeStockSync() {
	adapter := NewAdapter(AdapterConfig{
		ClientID:     "your-client-id",
		ClientSecret: "your-client-secret",
		UseSandbox:   false,
	})

	ctx := context.Background()
	brandID := "your-brand-id"
	siteID := "your-site-id"

	// Simulate receiving order that depletes stock
	itemID := "chicken-breast"
	remainingStock := 0 // Sold the last one!

	if remainingStock == 0 {
		// Immediately mark as unavailable
		req := UpdateItemUnavailabilitiesRequest{
			ItemUnavailabilities: []ItemUnavailability{
				{
					ItemID: itemID,
					Status: StatusUnavailable,
				},
			},
		}

		if err := adapter.UpdateItemUnavailabilitiesV2(ctx, brandID, siteID, req); err != nil {
			log.Printf("Failed to mark item unavailable: %v", err)
			return
		}

		log.Printf("✓ %s marked as sold out in real-time", itemID)
	}
}
