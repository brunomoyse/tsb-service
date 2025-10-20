package deliveroo

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"
)

// This file contains complete examples of order workflows using the Deliveroo API

// ExampleCompleteOrderWorkflow demonstrates the complete lifecycle of processing a Deliveroo order
func ExampleCompleteOrderWorkflow() {
	// Initialize adapter
	adapter := NewAdapter(AdapterConfig{
		ClientID:     "your-client-id",
		ClientSecret: "your-client-secret",
		UseSandbox:   false,
	})

	ctx := context.Background()
	orderID := "gb:07fbb82a-ceb5-43ec-123f-18af89e3380a"

	// Step 1: Receive order via webhook (see ExampleOrderEventHandler in webhooks.go)
	// When webhook is received, you get the full order data

	// Step 2: Send sync status to confirm order was received in POS
	syncReq := CreateSyncStatusRequest{
		Status:     SyncStatusSucceeded,
		OccurredAt: time.Now(),
	}
	if err := adapter.CreateSyncStatus(ctx, orderID, syncReq); err != nil {
		log.Printf("Failed to create sync status: %v", err)
		// If failed, send failed status with reason
		reason := SyncReasonWebhookFailed
		notes := "POS system unavailable"
		failedReq := CreateSyncStatusRequest{
			Status:     SyncStatusFailed,
			Reason:     &reason,
			Notes:      &notes,
			OccurredAt: time.Now(),
		}
		adapter.CreateSyncStatus(ctx, orderID, failedReq)
		return
	}
	log.Println("✓ Sync status sent successfully")

	// Step 3: Accept the order using V1 Update Order endpoint
	acceptReq := UpdateOrderRequest{
		Status: OrderUpdateAccepted,
	}
	if err := adapter.UpdateOrder(ctx, orderID, acceptReq); err != nil {
		log.Printf("Failed to accept order: %v", err)
		return
	}
	log.Println("✓ Order accepted")

	// Step 4: Update prep stage to "in kitchen"
	prepReq := CreatePrepStageRequest{
		Stage:      PrepStageInKitchen,
		OccurredAt: time.Now(),
	}
	if err := adapter.CreatePrepStage(ctx, orderID, prepReq); err != nil {
		log.Printf("Failed to update prep stage: %v", err)
	}
	log.Println("✓ Order is now in kitchen")

	// Simulate cooking time
	time.Sleep(5 * time.Second)

	// Step 5: Update prep stage to "ready for collection soon"
	prepReq.Stage = PrepStageReadyForCollectionSoon
	prepReq.OccurredAt = time.Now()
	if err := adapter.CreatePrepStage(ctx, orderID, prepReq); err != nil {
		log.Printf("Failed to update prep stage: %v", err)
	}
	log.Println("✓ Order will be ready soon")

	// Step 6: Update prep stage to "ready for collection"
	prepReq.Stage = PrepStageReadyForCollection
	prepReq.OccurredAt = time.Now()
	if err := adapter.CreatePrepStage(ctx, orderID, prepReq); err != nil {
		log.Printf("Failed to update prep stage: %v", err)
	}
	log.Println("✓ Order is ready for collection")

	// Step 7: Mark as collected when rider picks up
	prepReq.Stage = PrepStageCollected
	prepReq.OccurredAt = time.Now()
	if err := adapter.CreatePrepStage(ctx, orderID, prepReq); err != nil {
		log.Printf("Failed to update prep stage: %v", err)
	}
	log.Println("✓ Order collected by rider")
}

// ExampleRejectOrder demonstrates how to reject an order
func ExampleRejectOrder() {
	adapter := NewAdapter(AdapterConfig{
		ClientID:     "your-client-id",
		ClientSecret: "your-client-secret",
		UseSandbox:   false,
	})

	ctx := context.Background()
	orderID := "gb:07fbb82a-ceb5-43ec-123f-18af89e3380a"

	// Reject the order with reason
	reason := RejectReasonIngredientUnavailable
	notes := "Out of chicken breast"
	rejectReq := UpdateOrderRequest{
		Status:       OrderUpdateRejected,
		RejectReason: &reason,
		Notes:        &notes,
	}

	if err := adapter.UpdateOrder(ctx, orderID, rejectReq); err != nil {
		log.Printf("Failed to reject order: %v", err)
		return
	}

	log.Println("✓ Order rejected successfully")
}

// ExampleScheduledOrderWorkflow demonstrates handling a scheduled order
func ExampleScheduledOrderWorkflow() {
	adapter := NewAdapter(AdapterConfig{
		ClientID:     "your-client-id",
		ClientSecret: "your-client-secret",
		UseSandbox:   false,
	})

	ctx := context.Background()
	orderID := "gb:07fbb82a-ceb5-43ec-123f-18af89e3380a"

	// Step 1: Accept the scheduled order
	acceptReq := UpdateOrderRequest{
		Status: OrderUpdateAccepted,
	}
	if err := adapter.UpdateOrder(ctx, orderID, acceptReq); err != nil {
		log.Printf("Failed to accept order: %v", err)
		return
	}
	log.Println("✓ Scheduled order accepted")

	// Step 2: Wait until confirm_at time, then confirm the order
	// (In real implementation, you'd check order.ConfirmAt and wait)
	time.Sleep(10 * time.Second) // Simulate waiting

	confirmReq := UpdateOrderRequest{
		Status: OrderUpdateConfirmed,
	}
	if err := adapter.UpdateOrder(ctx, orderID, confirmReq); err != nil {
		log.Printf("Failed to confirm order: %v", err)
		return
	}
	log.Println("✓ Scheduled order confirmed - starting preparation")

	// Continue with normal prep stages...
}

// ExampleGetOrdersV2 demonstrates retrieving orders with pagination
func ExampleGetOrdersV2() {
	adapter := NewAdapter(AdapterConfig{
		ClientID:     "your-client-id",
		ClientSecret: "your-client-secret",
		UseSandbox:   false,
	})

	ctx := context.Background()
	brandID := "your-brand-id"
	restaurantID := "your-restaurant-id"

	// Get orders from the last 7 days
	startDate := time.Now().AddDate(0, 0, -7)

	req := GetOrdersV2Request{
		StartDate:  &startDate,
		LiveOrders: false,
	}

	// First page
	resp, err := adapter.GetOrdersV2(ctx, brandID, restaurantID, req)
	if err != nil {
		log.Printf("Failed to get orders: %v", err)
		return
	}

	log.Printf("Retrieved %d orders\n", len(resp.Orders))

	for _, order := range resp.Orders {
		fmt.Printf("Order %s - Status: %s - Total: %d %s\n",
			order.DisplayID,
			order.Status,
			order.TotalPrice.Fractional,
			order.TotalPrice.CurrencyCode)
	}

	// Get next page if available
	if resp.Next != nil {
		req.Cursor = resp.Next
		resp, err = adapter.GetOrdersV2(ctx, brandID, restaurantID, req)
		if err != nil {
			log.Printf("Failed to get next page: %v", err)
			return
		}
		log.Printf("Retrieved %d more orders from next page\n", len(resp.Orders))
	}
}

// ExampleGetLiveOrders demonstrates retrieving only live (active) orders
func ExampleGetLiveOrders() {
	adapter := NewAdapter(AdapterConfig{
		ClientID:     "your-client-id",
		ClientSecret: "your-client-secret",
		UseSandbox:   false,
	})

	ctx := context.Background()
	brandID := "your-brand-id"
	restaurantID := "your-restaurant-id"

	// Get only live orders (placed, accepted, or confirmed and not yet collected)
	req := GetOrdersV2Request{
		LiveOrders: true,
	}

	resp, err := adapter.GetOrdersV2(ctx, brandID, restaurantID, req)
	if err != nil {
		log.Printf("Failed to get live orders: %v", err)
		return
	}

	log.Printf("You have %d active orders\n", len(resp.Orders))

	for _, order := range resp.Orders {
		fmt.Printf("Active Order %s - Status: %s - Prepare for: %s\n",
			order.DisplayID,
			order.Status,
			order.PrepareFor.Format(time.RFC3339))
	}
}

// ExampleWebhookConfiguration demonstrates managing webhook settings
func ExampleWebhookConfiguration() {
	adapter := NewAdapter(AdapterConfig{
		ClientID:     "your-client-id",
		ClientSecret: "your-client-secret",
		UseSandbox:   false,
	})

	ctx := context.Background()

	// Set order events webhook
	webhookURL := "https://your-server.com/webhooks/deliveroo/order-events"
	if err := adapter.SetOrderEventsWebhook(ctx, webhookURL); err != nil {
		log.Printf("Failed to set order events webhook: %v", err)
		return
	}
	log.Println("✓ Order events webhook configured")

	// Set rider events webhook
	riderWebhookURL := "https://your-server.com/webhooks/deliveroo/rider-events"
	if err := adapter.SetRiderEventsWebhook(ctx, riderWebhookURL); err != nil {
		log.Printf("Failed to set rider events webhook: %v", err)
		return
	}
	log.Println("✓ Rider events webhook configured")

	// Get current webhook configuration
	config, err := adapter.GetOrderEventsWebhook(ctx)
	if err != nil {
		log.Printf("Failed to get webhook config: %v", err)
		return
	}
	log.Printf("Current webhook URL: %s\n", config.WebhookURL)
}

// ExampleSitesConfiguration demonstrates managing site webhook settings
func ExampleSitesConfiguration() {
	adapter := NewAdapter(AdapterConfig{
		ClientID:     "your-client-id",
		ClientSecret: "your-client-secret",
		UseSandbox:   false,
	})

	ctx := context.Background()
	brandID := "your-brand-id"

	// Get current sites configuration
	config, err := adapter.GetSitesConfig(ctx, brandID)
	if err != nil {
		log.Printf("Failed to get sites config: %v", err)
		return
	}

	log.Printf("Current configuration for %d sites:\n", len(config.Sites))
	for _, site := range config.Sites {
		fmt.Printf("  Site %s (%s): %s\n", site.LocationID, site.Name, site.OrdersAPIWebhookType)
	}

	// Update sites to use Order Events webhook
	newConfig := SitesConfig{
		Sites: []SiteConfig{
			{
				LocationID:           "site-123",
				OrdersAPIWebhookType: WebhookTypeOrderEvents,
			},
			{
				LocationID:           "site-456",
				OrdersAPIWebhookType: WebhookTypePOSAndOrderEvents,
			},
		},
	}

	if err := adapter.SetSitesConfig(ctx, brandID, newConfig); err != nil {
		log.Printf("Failed to set sites config: %v", err)
		return
	}
	log.Println("✓ Sites configuration updated")
}

// ExampleWebhookServer demonstrates setting up an HTTP server to receive webhooks
func ExampleWebhookServer() {
	webhookSecret := "your-webhook-secret"

	// Setup routes
	http.HandleFunc("/webhooks/deliveroo/order-events", ExampleOrderEventHandler(webhookSecret))
	http.HandleFunc("/webhooks/deliveroo/rider-events", ExampleRiderEventHandler(webhookSecret))

	// Start server
	log.Println("Starting webhook server on :8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatal(err)
	}
}

// ExamplePrepStageWithDelay demonstrates requesting additional prep time
func ExamplePrepStageWithDelay() {
	adapter := NewAdapter(AdapterConfig{
		ClientID:     "your-client-id",
		ClientSecret: "your-client-secret",
		UseSandbox:   false,
	})

	ctx := context.Background()
	orderID := "gb:07fbb82a-ceb5-43ec-123f-18af89e3380a"

	// Request 10 minutes additional prep time when starting to cook
	delay := 10
	prepReq := CreatePrepStageRequest{
		Stage:      PrepStageInKitchen,
		OccurredAt: time.Now(),
		Delay:      &delay, // Can be 0, 2, 4, 6, 8, or 10
	}

	if err := adapter.CreatePrepStage(ctx, orderID, prepReq); err != nil {
		log.Printf("Failed to update prep stage: %v", err)
		return
	}

	log.Println("✓ Order in kitchen with 10 minute delay requested")
}

// ExampleMonitorOrderStatus demonstrates monitoring an order's status
func ExampleMonitorOrderStatus() {
	adapter := NewAdapter(AdapterConfig{
		ClientID:     "your-client-id",
		ClientSecret: "your-client-secret",
		UseSandbox:   false,
	})

	ctx := context.Background()
	orderID := "gb:07fbb82a-ceb5-43ec-123f-18af89e3380a"

	// Poll order status every 30 seconds
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		order, err := adapter.GetOrderV2(ctx, orderID)
		if err != nil {
			log.Printf("Failed to get order: %v", err)
			continue
		}

		log.Printf("Order %s - Current Status: %s\n", order.DisplayID, order.Status)

		// Show status history
		if len(order.StatusLog) > 0 {
			latestStatus := order.StatusLog[len(order.StatusLog)-1]
			log.Printf("  Last updated: %s at %s\n", latestStatus.Status, latestStatus.At)
		}

		// Stop monitoring when order is completed
		if order.Status == OrderStatusDelivered || order.Status == OrderStatusCanceled {
			log.Println("Order completed, stopping monitor")
			break
		}
	}
}
