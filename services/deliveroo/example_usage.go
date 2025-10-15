package deliveroo

import (
	"context"
	"log"
	"time"
)

// ExampleOrderWorkflow demonstrates a complete order processing workflow
func ExampleOrderWorkflow() {
	// Initialize the adapter
	adapter := NewAdapter(AdapterConfig{
		ClientID:     "your-client-id",
		ClientSecret: "your-client-secret",
		UseSandbox:   false,
	})

	ctx := context.Background()
	outletID := "your-outlet-id"

	// Step 1: List new placed orders
	log.Println("Fetching new orders...")
	orders, err := adapter.ListOrders(ctx, OrderStatusPlaced, nil, outletID)
	if err != nil {
		log.Fatalf("Failed to list orders: %v", err)
	}

	log.Printf("Found %d new orders", len(orders))

	// Step 2: Process each order
	for _, order := range orders {
		log.Printf("\nProcessing order %s (Display ID: %s)", order.ID, order.DisplayID)
		log.Printf("  Total: %d %s", order.TotalPrice.Fractional, order.TotalPrice.CurrencyCode)
		log.Printf("  Fulfillment: %s", order.FulfillmentType)

		// Step 3: Acknowledge order receipt
		log.Printf("  Acknowledging order...")
		if err := adapter.AcknowledgeOrder(ctx, order.ID); err != nil {
			log.Printf("  Failed to acknowledge: %v", err)
			continue
		}

		// Step 4: Calculate preparation time (example logic)
		prepMinutes := calculatePrepTime(order)
		log.Printf("  Estimated prep time: %d minutes", prepMinutes)

		// Step 5: Accept the order
		log.Printf("  Accepting order...")
		if err := adapter.AcceptOrder(ctx, order.ID, prepMinutes); err != nil {
			log.Printf("  Failed to accept: %v", err)
			continue
		}

		log.Printf("  ✓ Order %s accepted successfully", order.DisplayID)

		// In a real implementation, you would:
		// - Send order to kitchen system
		// - Track preparation progress
		// - Update status when ready
	}
}

// ExampleUpdateOrderStatus demonstrates updating order status
func ExampleUpdateOrderStatus() {
	adapter := NewAdapter(AdapterConfig{
		ClientID:     "your-client-id",
		ClientSecret: "your-client-secret",
		UseSandbox:   false,
	})

	ctx := context.Background()
	orderID := "order-id-123"

	// Mark order as ready for pickup
	readyAt := time.Now()
	err := adapter.UpdateOrderStatus(
		ctx,
		orderID,
		OrderStatusConfirmed,
		&readyAt,
		nil,
	)
	if err != nil {
		log.Fatalf("Failed to update status: %v", err)
	}

	log.Printf("Order %s marked as ready", orderID)

	// Later, mark as picked up
	pickupAt := time.Now()
	err = adapter.UpdateOrderStatus(
		ctx,
		orderID,
		OrderStatusDelivered,
		nil,
		&pickupAt,
	)
	if err != nil {
		log.Fatalf("Failed to update status: %v", err)
	}

	log.Printf("Order %s marked as delivered", orderID)
}

// ExampleMenuSync demonstrates menu synchronization
func ExampleMenuSync() {
	adapter := NewAdapter(AdapterConfig{
		ClientID:     "your-client-id",
		ClientSecret: "your-client-secret",
		UseSandbox:   false,
	})

	ctx := context.Background()
	brandID := "your-brand-id"
	menuID := "your-menu-id"

	// Create a sample menu
	menu := &MenuUploadRequest{
		Name: "Tokyo Sushi Bar Menu",
		Menu: MenuContent{
			Categories: []Category{
				{
					ID: "sushi-rolls",
					Name: map[string]string{
						"en": "Sushi Rolls",
						"fr": "Makis",
						"zh": "寿司卷",
					},
					Description: map[string]string{
						"en": "Traditional and creative sushi rolls",
						"fr": "Makis traditionnels et créatifs",
						"zh": "传统和创意寿司卷",
					},
					ItemIDs: []string{"california-roll", "salmon-roll"},
				},
				{
					ID: "drinks",
					Name: map[string]string{
						"en": "Drinks",
						"fr": "Boissons",
						"zh": "饮料",
					},
					ItemIDs: []string{"green-tea", "sake"},
				},
			},
			Items: []Item{
				{
					ID: "california-roll",
					Name: map[string]string{
						"en": "California Roll",
						"fr": "California Maki",
						"zh": "加州卷",
					},
					Description: map[string]string{
						"en": "Crab, avocado, cucumber",
						"fr": "Crabe, avocat, concombre",
						"zh": "蟹肉、鳄梨、黄瓜",
					},
					OperationalName:           "CALIFORNIA_ROLL",
					PriceInfo:                 PriceInfo{Price: 850}, // €8.50
					TaxRate:                   "20",
					ContainsAlcohol:           false,
					Type:                      "ITEM",
					Allergies:                 []string{"shellfish"},
					Diets:                     []string{},
					IsEligibleAsReplacement:   true,
					IsEligibleForSubstitution: true,
				},
				{
					ID: "salmon-roll",
					Name: map[string]string{
						"en": "Salmon Roll",
						"fr": "Maki Saumon",
						"zh": "三文鱼卷",
					},
					Description: map[string]string{
						"en": "Fresh salmon with rice",
						"fr": "Saumon frais avec riz",
						"zh": "新鲜三文鱼配米饭",
					},
					OperationalName:           "SALMON_ROLL",
					PriceInfo:                 PriceInfo{Price: 750},
					TaxRate:                   "20",
					ContainsAlcohol:           false,
					Type:                      "ITEM",
					Allergies:                 []string{"fish"},
					Diets:                     []string{},
					IsEligibleAsReplacement:   true,
					IsEligibleForSubstitution: true,
				},
				{
					ID: "green-tea",
					Name: map[string]string{
						"en": "Green Tea",
						"fr": "Thé Vert",
						"zh": "绿茶",
					},
					OperationalName:           "GREEN_TEA",
					PriceInfo:                 PriceInfo{Price: 250},
					TaxRate:                   "20",
					ContainsAlcohol:           false,
					Type:                      "ITEM",
					Allergies:                 []string{},
					Diets:                     []string{"vegan"},
					IsEligibleAsReplacement:   true,
					IsEligibleForSubstitution: true,
				},
				{
					ID: "sake",
					Name: map[string]string{
						"en": "Sake",
						"fr": "Saké",
						"zh": "清酒",
					},
					Description: map[string]string{
						"en": "Traditional Japanese rice wine",
						"fr": "Vin de riz japonais traditionnel",
						"zh": "传统日本米酒",
					},
					OperationalName:           "SAKE",
					PriceInfo:                 PriceInfo{Price: 450},
					TaxRate:                   "20",
					ContainsAlcohol:           true,
					Type:                      "ITEM",
					Classifications:           []string{"alcohol_product"},
					Allergies:                 []string{},
					Diets:                     []string{},
					IsEligibleAsReplacement:   true,
					IsEligibleForSubstitution: true,
				},
			},
		},
		SiteIDs: []string{"site-123"},
	}

	// Push menu to Deliveroo
	log.Println("Uploading menu to Deliveroo...")
	err := adapter.PushMenu(ctx, brandID, menuID, menu)
	if err != nil {
		log.Fatalf("Failed to upload menu: %v", err)
	}

	log.Printf("✓ Menu uploaded successfully!")
	log.Printf("  - %d categories", len(menu.Menu.Categories))
	log.Printf("  - %d items", len(menu.Menu.Items))
}

// ExampleRetrieveMenu demonstrates pulling current menu from Deliveroo
func ExampleRetrieveMenu() {
	adapter := NewAdapter(AdapterConfig{
		ClientID:     "your-client-id",
		ClientSecret: "your-client-secret",
		UseSandbox:   false,
	})

	ctx := context.Background()
	brandID := "your-brand-id"
	menuID := "your-menu-id"

	// Pull current menu
	log.Println("Retrieving menu from Deliveroo...")
	menu, err := adapter.PullMenu(ctx, brandID, menuID)
	if err != nil {
		log.Fatalf("Failed to retrieve menu: %v", err)
	}

	log.Printf("✓ Menu retrieved: %s", menu.Name)
	log.Printf("  - %d categories", len(menu.Menu.Categories))
	log.Printf("  - %d items", len(menu.Menu.Items))

	// Display categories
	for _, cat := range menu.Menu.Categories {
		log.Printf("  Category: %v (%d items)", cat.Name, len(cat.ItemIDs))
	}
}

// ExampleFilterOrders demonstrates various order filtering options
func ExampleFilterOrders() {
	adapter := NewAdapter(AdapterConfig{
		ClientID:     "your-client-id",
		ClientSecret: "your-client-secret",
		UseSandbox:   false,
	})

	ctx := context.Background()

	// Get all orders
	log.Println("Fetching all orders...")
	allOrders, err := adapter.ListOrders(ctx, "", nil, "")
	if err != nil {
		log.Fatalf("Failed to list orders: %v", err)
	}
	log.Printf("Total orders: %d", len(allOrders))

	// Get only placed orders
	log.Println("\nFetching placed orders...")
	placedOrders, err := adapter.ListOrders(ctx, OrderStatusPlaced, nil, "")
	if err != nil {
		log.Fatalf("Failed to list placed orders: %v", err)
	}
	log.Printf("Placed orders: %d", len(placedOrders))

	// Get orders from last hour
	log.Println("\nFetching recent orders...")
	since := time.Now().Add(-1 * time.Hour)
	recentOrders, err := adapter.ListOrders(ctx, "", &since, "")
	if err != nil {
		log.Fatalf("Failed to list recent orders: %v", err)
	}
	log.Printf("Orders from last hour: %d", len(recentOrders))

	// Get orders for specific outlet
	log.Println("\nFetching orders for outlet...")
	outletOrders, err := adapter.ListOrders(ctx, "", nil, "outlet-123")
	if err != nil {
		log.Fatalf("Failed to list outlet orders: %v", err)
	}
	log.Printf("Outlet orders: %d", len(outletOrders))
}

// ExampleOrderDetails demonstrates accessing order information
func ExampleOrderDetails() {
	adapter := NewAdapter(AdapterConfig{
		ClientID:     "your-client-id",
		ClientSecret: "your-client-secret",
		UseSandbox:   false,
	})

	ctx := context.Background()

	orders, err := adapter.ListOrders(ctx, OrderStatusPlaced, nil, "")
	if err != nil {
		log.Fatalf("Failed to list orders: %v", err)
	}

	for _, order := range orders {
		log.Printf("\n=== Order %s ===", order.DisplayID)
		log.Printf("ID: %s", order.ID)
		log.Printf("Status: %s", order.Status)
		log.Printf("ASAP: %v", order.ASAP)
		log.Printf("Fulfillment: %s", order.FulfillmentType)

		// Pricing information
		log.Printf("\nPricing:")
		log.Printf("  Subtotal: %d %s",
			order.Subtotal.Fractional,
			order.Subtotal.CurrencyCode)
		log.Printf("  Total: %d %s",
			order.TotalPrice.Fractional,
			order.TotalPrice.CurrencyCode)

		// Delivery information
		if order.Delivery != nil {
			log.Printf("\nDelivery:")
			log.Printf("  Fee: %d %s",
				order.Delivery.DeliveryFee.Fractional,
				order.Delivery.DeliveryFee.CurrencyCode)

			if order.Delivery.Address != nil {
				addr := order.Delivery.Address
				log.Printf("  Address: %s %s, %s %s",
					addr.Street, addr.Number,
					addr.PostalCode, addr.City)
			}
		}

		// Customer information
		if order.Customer != nil {
			log.Printf("\nCustomer:")
			log.Printf("  Name: %s", order.Customer.FirstName)
			if order.Customer.ContactNumber != "" {
				log.Printf("  Contact: %s (code: %s)",
					order.Customer.ContactNumber,
					order.Customer.ContactAccessCode)
			}
		}

		// Items
		log.Printf("\nItems:")
		for _, item := range order.Items {
			log.Printf("  - %s x%d @ %d %s",
				item.Name,
				item.Quantity,
				item.UnitPrice.Fractional,
				item.UnitPrice.CurrencyCode)

			// Modifiers
			for _, mod := range item.Modifiers {
				log.Printf("    + %s", mod.Name)
			}
		}

		// Notes
		if order.OrderNotes != "" {
			log.Printf("\nNotes: %s", order.OrderNotes)
		}

		// Timing
		log.Printf("\nTiming:")
		log.Printf("  Start preparing at: %s", order.StartPreparingAt)
		log.Printf("  Prepare for: %s", order.PrepareFor)
	}
}

// calculatePrepTime is a helper function to estimate preparation time
func calculatePrepTime(order Order) int {
	// Simple example: 5 minutes per item + 10 base minutes
	itemCount := 0
	for _, item := range order.Items {
		itemCount += item.Quantity
	}

	prepTime := 10 + (itemCount * 5)

	// Add extra time for delivery orders
	if order.FulfillmentType == FulfillmentDeliveroo {
		prepTime += 5
	}

	return prepTime
}