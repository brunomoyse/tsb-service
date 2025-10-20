# Deliveroo Integration

A comprehensive Go client for Deliveroo Developer APIs with built-in retry logic, OAuth token caching, and idempotency support.

## Features

### Core Infrastructure
- **Complete Orders API Coverage**: V1 & V2 endpoints with pagination support
- **Webhook Handling**: Secure HMAC-SHA256 signature verification for order and rider events
- **Automatic Retry**: Exponential backoff for 429 (rate limit) and 5xx errors
- **OAuth Token Caching**: Automatic token refresh with expiry management
- **Idempotency**: Automatic idempotency key generation for safe retries
- **Thread-Safe**: Concurrent request handling with proper synchronization
- **Sandbox Support**: Easy toggle between production and sandbox environments

### Orders API
- **Order Retrieval**: Get single orders or paginated lists with filtering (V2)
- **Order Management**: Accept, reject, or confirm orders with reasons
- **Sync Status**: Report POS integration success/failure to Deliveroo
- **Prep Stages**: Real-time kitchen progress updates (in_kitchen → ready_for_collection → collected)
- **Webhook Events**: Receive new orders and status updates in real-time
- **Rider Tracking**: Monitor rider assignment, arrival, and collection

### Menu API
- **Menu Sync**: Upload and retrieve menus with multi-language support
- **Category Management**: Organize items into categories
- **Item Management**: Full product details with modifiers, pricing, and nutritional info

### Webhook Configuration
- **Order Events Webhook**: Configure URL for new orders and status updates
- **Rider Events Webhook**: Configure URL for rider status updates
- **Sites Configuration**: Manage webhook types per site (POS vs Order Events)

## Installation

```bash
go get tsb-service/services/deliveroo
```

## Quick Start

### Basic Setup

```go
package main

import (
    "context"
    "log"
    "tsb-service/services/deliveroo"
)

func main() {
    // Create adapter with configuration
    adapter := deliveroo.NewAdapter(deliveroo.AdapterConfig{
        ClientID:     "your-client-id",
        ClientSecret: "your-client-secret",
        UseSandbox:   false, // Set to true for sandbox
    })

    ctx := context.Background()

    // List orders
    orders, err := adapter.ListOrders(ctx, deliveroo.OrderStatusPlaced, nil, "outlet-123")
    if err != nil {
        log.Fatal(err)
    }

    log.Printf("Found %d orders", len(orders))
}
```

### Using the High-Level Service (for Menu Sync)

```go
package main

import (
    "context"
    "tsb-service/services/deliveroo"
    "tsb-service/internal/modules/product/domain"
)

func main() {
    // Create service for menu synchronization
    service := deliveroo.NewServiceWithConfig(deliveroo.ServiceConfig{
        ClientID:     "your-client-id",
        ClientSecret: "your-client-secret",
        BrandID:      "your-brand-id",
        MenuID:       "your-menu-id",
        OutletID:     "your-outlet-id",
        Currency:     "EUR",
        UseSandbox:   false,
    })

    ctx := context.Background()

    // Sync menu
    var categories []*domain.Category
    var products []*domain.Product
    // ... load your categories and products

    err := service.SyncMenu(ctx, categories, products)
    if err != nil {
        log.Fatal(err)
    }

    // Access the adapter for direct API calls
    adapter := service.GetAdapter()
    orders, _ := adapter.ListOrders(ctx, "", nil, "")
}
```

## API Reference

### DeliverooAdapter Methods

#### Order Management (V1 & V2)

##### ListOrders
List orders with optional filtering (deprecated, use GetOrdersV2).

```go
orders, err := adapter.ListOrders(ctx, status, since, outletID)
```

Parameters:
- `ctx`: Context
- `status`: Filter by order status (e.g., `OrderStatusPlaced`, `OrderStatusAccepted`)
- `since`: Filter orders after this time (optional, pass `nil` for all)
- `outletID`: Filter by outlet/location ID (optional, pass `""` for all)

Returns: `[]Order, error`

##### GetOrderV2
Retrieve a single order by ID (V2 API).

```go
order, err := adapter.GetOrderV2(ctx, orderID)
```

Parameters:
- `ctx`: Context
- `orderID`: The unique order ID (format: `{market}:{uuid}`)

Returns: `*Order, error`

##### GetOrdersV2
Retrieve orders for a restaurant with pagination support (V2 API).

```go
req := GetOrdersV2Request{
    StartDate:  &startDate,
    EndDate:    &endDate,
    LiveOrders: false,
}
resp, err := adapter.GetOrdersV2(ctx, brandID, restaurantID, req)
```

Parameters:
- `ctx`: Context
- `brandID`: Your brand ID
- `restaurantID`: The restaurant/site ID
- `req`: Request with filters (start_date, end_date, cursor, live_orders)

Returns: `*GetOrdersV2Response, error` (includes pagination cursor)

##### UpdateOrder
Accept, reject, or confirm an order (V1 PATCH endpoint).

```go
req := UpdateOrderRequest{
    Status: OrderUpdateAccepted,
}
err := adapter.UpdateOrder(ctx, orderID, req)
```

Parameters:
- `ctx`: Context
- `orderID`: The unique order ID
- `req`: Update request (status: accepted/rejected/confirmed, optional reject_reason and notes)

Returns: `error`

**Note**: This replaces the deprecated `AcceptOrder` and `AcknowledgeOrder` methods for new integrations.

##### AcknowledgeOrder (Deprecated)
Acknowledge receipt of an order.

```go
err := adapter.AcknowledgeOrder(ctx, orderID)
```

##### AcceptOrder (Deprecated)
Accept an order with preparation time.

```go
err := adapter.AcceptOrder(ctx, orderID, prepMinutes)
```

##### UpdateOrderStatus (Deprecated)
Update the status of an order.

```go
err := adapter.UpdateOrderStatus(ctx, orderID, status, readyAt, pickupAt)
```

#### Sync Status

##### CreateSyncStatus
Tell Deliveroo if an order was successfully sent to your POS system.

```go
req := CreateSyncStatusRequest{
    Status:     SyncStatusSucceeded,
    OccurredAt: time.Now(),
}
err := adapter.CreateSyncStatus(ctx, orderID, req)
```

Parameters:
- `ctx`: Context
- `orderID`: The unique order ID
- `req`: Sync status (succeeded/failed, optional reason and notes)

Returns: `error`

**Important**: Must be called within 3 minutes of receiving an order, or Deliveroo will notify staff to manually enter the order.

#### Preparation Stages

##### CreatePrepStage
Update the preparation stage of an order for better tracking.

```go
req := CreatePrepStageRequest{
    Stage:      PrepStageInKitchen,
    OccurredAt: time.Now(),
}
err := adapter.CreatePrepStage(ctx, orderID, req)
```

Parameters:
- `ctx`: Context
- `orderID`: The unique order ID
- `req`: Prep stage (in_kitchen, ready_for_collection_soon, ready_for_collection, collected)

Returns: `error`

**Stages**:
- `PrepStageInKitchen`: Cooking has started
- `PrepStageReadyForCollectionSoon`: Food is max 60s from being ready
- `PrepStageReadyForCollection`: Food is cooked and packaged
- `PrepStageCollected`: Order has been collected

**Optional delay**: Can request 0, 2, 4, 6, 8, or 10 minutes additional prep time with `in_kitchen` stage.

#### Menu Management

##### PullMenu (V1)
Retrieve the current menu from Deliveroo (API-created menus only).

```go
menu, err := adapter.PullMenu(ctx, brandID, menuID)
```

Parameters:
- `ctx`: Context
- `brandID`: Your brand ID
- `menuID`: The menu ID

Returns: `*MenuUploadRequest, error`

##### PushMenu (V1)
Upload/update a menu on Deliveroo.

```go
err := adapter.PushMenu(ctx, brandID, menuID, menu)
```

Parameters:
- `ctx`: Context
- `brandID`: Your brand ID
- `menuID`: The menu ID
- `menu`: Menu data structure

Returns: `error`

**Rate Limits**: 1 req/min per site; 10 req/10s for payloads >5MB
**Size Limit**: 10MB (recommend <9MB for optimal performance)

##### GetMenuV2
Retrieve menu for a specific site (V2 - works with Menu Manager).

```go
menu, err := adapter.GetMenuV2(ctx, brandID, siteID)
```

Parameters:
- `ctx`: Context
- `brandID`: Your brand ID
- `siteID`: Site/location ID

Returns: `*MenuUploadRequest, error`

**Note**: V2 works with both Menu Manager and API-created menus

#### Item Unavailability Management (V2)

##### GetItemUnavailabilitiesV2
Retrieve currently unavailable items for a site.

```go
unavailabilities, err := adapter.GetItemUnavailabilitiesV2(ctx, brandID, siteID)
```

Parameters:
- `ctx`: Context
- `brandID`: Your brand ID
- `siteID`: Site/location ID

Returns: `*GetItemUnavailabilitiesResponse, error`

Response contains:
- `UnavailableIDs`: Items sold out for the day
- `HiddenIDs`: Items hidden indefinitely

##### UpdateItemUnavailabilitiesV2
Update individual item unavailabilities.

```go
req := UpdateItemUnavailabilitiesRequest{
    ItemUnavailabilities: []ItemUnavailability{
        {ItemID: "chicken-breast", Status: StatusUnavailable},
        {ItemID: "salmon", Status: StatusAvailable},
    },
}
err := adapter.UpdateItemUnavailabilitiesV2(ctx, brandID, siteID, req)
```

Parameters:
- `ctx`: Context
- `brandID`: Your brand ID
- `siteID`: Site/location ID
- `req`: Array of item status updates

Returns: `error`

**Statuses**:
- `StatusAvailable`: Item visible and orderable
- `StatusUnavailable`: Greyed out, "sold out for the day"
- `StatusHidden`: Completely hidden from menu

##### ReplaceItemUnavailabilitiesV2
Replace ALL item unavailabilities (bulk update).

```go
req := ReplaceAllUnavailabilitiesRequest{
    UnavailableIDs: []string{"item-1", "item-2"},
    HiddenIDs:      []string{"item-3"},
}
err := adapter.ReplaceItemUnavailabilitiesV2(ctx, brandID, siteID, req)
```

Parameters:
- `ctx`: Context
- `brandID`: Your brand ID
- `siteID`: Site/location ID
- `req`: Complete list of unavailable/hidden items

Returns: `error`

**Warning**: This clears all previous unavailabilities

#### PLU (Price Look-Up) Management

##### UpdatePLUs
Update PLU mappings between menu items and POS system IDs.

```go
mappings := UpdatePLUsRequest{
    {ItemID: "burger-classic", PLU: "1001"},
    {ItemID: "burger-cheese", PLU: "1002"},
}
err := adapter.UpdatePLUs(ctx, brandID, menuID, mappings)
```

Parameters:
- `ctx`: Context
- `brandID`: Your brand ID
- `menuID`: The menu ID
- `mappings`: Array of item ID to PLU code mappings

Returns: `error`

#### V3 Async Upload (Large Menus)

For menus larger than 5MB, use the async upload workflow:

##### GetMenuUploadURLV3
Get presigned S3 URL for uploading large menus.

```go
uploadResp, err := adapter.GetMenuUploadURLV3(ctx, brandID, menuID)
```

Returns: `*MenuUploadURLResponse, error` (includes S3 upload URL)

##### UploadMenuToS3
Upload menu directly to S3.

```go
err := adapter.UploadMenuToS3(ctx, uploadResp.UploadURL, menu)
```

Parameters:
- `ctx`: Context
- `uploadURL`: Presigned S3 URL from GetMenuUploadURLV3
- `menu`: Menu data

Returns: `error`

##### PublishMenuJob
Create job to publish menu to live.

```go
req := PublishMenuJobRequest{
    Action: JobActionPublishMenuToLive,
    Params: PublishMenuJobParams{
        BrandID: brandID,
        MenuID:  menuID,
        Version: &version,
    },
}
job, err := adapter.PublishMenuJob(ctx, brandID, req)
```

Returns: `*JobResponse, error` (includes job ID for tracking)

##### GetJobStatus
Check status of async job.

```go
status, err := adapter.GetJobStatus(ctx, brandID, jobID)
```

Returns: `*JobResponse, error`

**Job Statuses**: `pending`, `running`, `completed`, `failed`

**Complete V3 Workflow**:
1. Get upload URL → 2. Upload to S3 → 3. Publish job → 4. Poll job status

#### Menu Webhooks

##### GetMenuEventsWebhook
Retrieve configured menu events webhook URL.

```go
config, err := adapter.GetMenuEventsWebhook(ctx)
```

Returns: `*WebhookConfig, error`

##### SetMenuEventsWebhook
Configure menu events webhook URL.

```go
err := adapter.SetMenuEventsWebhook(ctx, "https://your-server.com/webhooks/menu-events")
```

Parameters:
- `ctx`: Context
- `webhookURL`: Your webhook endpoint (or empty string to remove)

Returns: `error`

#### Webhook Configuration

##### GetOrderEventsWebhook
Retrieve the current order events webhook URL.

```go
config, err := adapter.GetOrderEventsWebhook(ctx)
```

Returns: `*WebhookConfig, error`

##### SetOrderEventsWebhook
Configure the order events webhook URL.

```go
err := adapter.SetOrderEventsWebhook(ctx, "https://your-server.com/webhooks/order-events")
```

Parameters:
- `ctx`: Context
- `webhookURL`: Your webhook endpoint URL (or empty string to remove)

Returns: `error`

##### GetRiderEventsWebhook
Retrieve the current rider events webhook URL.

```go
config, err := adapter.GetRiderEventsWebhook(ctx)
```

Returns: `*WebhookConfig, error`

##### SetRiderEventsWebhook
Configure the rider events webhook URL.

```go
err := adapter.SetRiderEventsWebhook(ctx, "https://your-server.com/webhooks/rider-events")
```

Parameters:
- `ctx`: Context
- `webhookURL`: Your webhook endpoint URL (or empty string to remove)

Returns: `error`

##### GetSitesConfig
Retrieve webhook configuration for all sites under a brand.

```go
config, err := adapter.GetSitesConfig(ctx, brandID)
```

Parameters:
- `ctx`: Context
- `brandID`: Your brand ID

Returns: `*SitesConfig, error`

##### SetSitesConfig
Configure which webhook type sites should use.

```go
config := SitesConfig{
    Sites: []SiteConfig{
        {
            LocationID:           "site-123",
            OrdersAPIWebhookType: WebhookTypeOrderEvents,
        },
    },
}
err := adapter.SetSitesConfig(ctx, brandID, config)
```

Parameters:
- `ctx`: Context
- `brandID`: Your brand ID
- `config`: Sites configuration

Returns: `error`

**Webhook Types**:
- `WebhookTypePOS`: Legacy POS webhook
- `WebhookTypeOrderEvents`: New Order Events webhook (recommended)
- `WebhookTypePOSAndOrderEvents`: Both webhooks

## Webhook Handling

### Setting Up Webhooks

```go
// Create webhook handler with your secret
webhookHandler := deliveroo.NewWebhookHandler("your-webhook-secret")

// Setup HTTP handler for order events
http.HandleFunc("/webhooks/order-events", func(w http.ResponseWriter, r *http.Request) {
    event, err := webhookHandler.ParseOrderEvent(r)
    if err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }

    // Process the event
    switch event.Event {
    case deliveroo.OrderEventNew:
        // Handle new order
        order := event.Body.Order
        log.Printf("New order: %s", order.DisplayID)

    case deliveroo.OrderEventStatusUpdate:
        // Handle status update
        order := event.Body.Order
        log.Printf("Order %s status: %s", order.DisplayID, order.Status)
    }

    w.WriteHeader(http.StatusOK)
})

// Setup HTTP handler for rider events
http.HandleFunc("/webhooks/rider-events", func(w http.ResponseWriter, r *http.Request) {
    event, err := webhookHandler.ParseRiderEvent(r)
    if err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }

    // Process rider updates
    for _, rider := range event.Body.Riders {
        if len(rider.StatusLog) > 0 {
            status := rider.StatusLog[len(rider.StatusLog)-1]
            log.Printf("Rider %s: %s", event.Body.OrderID, status.Status)
        }
    }

    w.WriteHeader(http.StatusOK)
})
```

### Webhook Security

All webhooks are signed with HMAC-SHA256. The `WebhookHandler` automatically verifies signatures using:
- Header: `x-deliveroo-hmac-sha256` (the signature)
- Header: `x-deliveroo-sequence-guid` (used in signature generation)
- Your webhook secret (provided by Deliveroo)

**Never skip signature verification in production!**

## Order Status Values

```go
OrderStatusPending   = "pending"
OrderStatusPlaced    = "placed"
OrderStatusAccepted  = "accepted"
OrderStatusConfirmed = "confirmed"
OrderStatusRejected  = "rejected"
OrderStatusCanceled  = "canceled"
OrderStatusDelivered = "delivered"
```

## Fulfillment Types

```go
FulfillmentDeliveroo   = "deliveroo"    // Deliveroo rider delivery
FulfillmentRestaurant  = "restaurant"   // Restaurant delivery
FulfillmentCustomer    = "customer"     // Customer pickup
FulfillmentTableService = "table_service" // Table service
FulfillmentAutonomous  = "autonomous"   // Autonomous delivery
```

## Error Handling

The adapter automatically retries on:
- `429 Too Many Requests` (rate limiting)
- `5xx Server Errors`

With exponential backoff:
- Initial backoff: 1 second
- Max backoff: 30 seconds
- Multiplier: 2.0
- Max retries: 3

```go
orders, err := adapter.ListOrders(ctx, "", nil, "")
if err != nil {
    // All retries exhausted or non-retryable error
    log.Printf("Failed to list orders: %v", err)
    return
}
```

## Complete Order Workflow

Here's the recommended end-to-end workflow for processing Deliveroo orders:

### 1. Setup Webhooks

```go
adapter := deliveroo.NewAdapter(deliveroo.AdapterConfig{
    ClientID:     os.Getenv("DELIVEROO_CLIENT_ID"),
    ClientSecret: os.Getenv("DELIVEROO_CLIENT_SECRET"),
    UseSandbox:   false,
})

// Configure webhook URLs
adapter.SetOrderEventsWebhook(ctx, "https://your-server.com/webhooks/order-events")
adapter.SetRiderEventsWebhook(ctx, "https://your-server.com/webhooks/rider-events")
```

### 2. Receive Order via Webhook

```go
webhookHandler := deliveroo.NewWebhookHandler(os.Getenv("DELIVEROO_WEBHOOK_SECRET"))

http.HandleFunc("/webhooks/order-events", func(w http.ResponseWriter, r *http.Request) {
    event, err := webhookHandler.ParseOrderEvent(r)
    if err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }

    if event.Event == deliveroo.OrderEventNew {
        order := event.Body.Order

        // Process order asynchronously
        go processNewOrder(adapter, order)
    }

    w.WriteHeader(http.StatusOK)
})
```

### 3. Process the Order

```go
func processNewOrder(adapter *deliveroo.DeliverooAdapter, order deliveroo.Order) {
    ctx := context.Background()

    // Step 1: Send to POS system
    err := sendToPOS(order)

    // Step 2: Report sync status
    if err != nil {
        // Failed to send to POS
        reason := deliveroo.SyncReasonWebhookFailed
        notes := err.Error()
        adapter.CreateSyncStatus(ctx, order.ID, deliveroo.CreateSyncStatusRequest{
            Status:     deliveroo.SyncStatusFailed,
            Reason:     &reason,
            Notes:      &notes,
            OccurredAt: time.Now(),
        })
        return
    }

    // Successfully sent to POS
    adapter.CreateSyncStatus(ctx, order.ID, deliveroo.CreateSyncStatusRequest{
        Status:     deliveroo.SyncStatusSucceeded,
        OccurredAt: time.Now(),
    })

    // Step 3: Accept the order
    adapter.UpdateOrder(ctx, order.ID, deliveroo.UpdateOrderRequest{
        Status: deliveroo.OrderUpdateAccepted,
    })

    // Step 4: Update prep stages as cooking progresses
    updatePrepStages(adapter, order.ID)
}

func updatePrepStages(adapter *deliveroo.DeliverooAdapter, orderID string) {
    ctx := context.Background()

    // Cooking started
    adapter.CreatePrepStage(ctx, orderID, deliveroo.CreatePrepStageRequest{
        Stage:      deliveroo.PrepStageInKitchen,
        OccurredAt: time.Now(),
    })

    // Wait for food to be almost ready (this would be event-driven in real app)
    time.Sleep(15 * time.Minute)

    // Almost ready (60 seconds out)
    adapter.CreatePrepStage(ctx, orderID, deliveroo.CreatePrepStageRequest{
        Stage:      deliveroo.PrepStageReadyForCollectionSoon,
        OccurredAt: time.Now(),
    })

    time.Sleep(1 * time.Minute)

    // Ready for pickup
    adapter.CreatePrepStage(ctx, orderID, deliveroo.CreatePrepStageRequest{
        Stage:      deliveroo.PrepStageReadyForCollection,
        OccurredAt: time.Now(),
    })
}
```

### 4. Track Rider Status

```go
http.HandleFunc("/webhooks/rider-events", func(w http.ResponseWriter, r *http.Request) {
    event, err := webhookHandler.ParseRiderEvent(r)
    if err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }

    for _, rider := range event.Body.Riders {
        if len(rider.StatusLog) > 0 {
            status := rider.StatusLog[len(rider.StatusLog)-1]

            switch status.Status {
            case deliveroo.RiderAssigned:
                log.Printf("Rider assigned to order %s, ETA: %s",
                    event.Body.OrderID, rider.EstimatedArrivalTime)

            case deliveroo.RiderArrived:
                log.Printf("Rider arrived for order %s", event.Body.OrderID)
                // Notify kitchen to prepare for handoff

            case deliveroo.RiderConfirmedAtRestaurant:
                log.Printf("Rider ready to collect order %s", event.Body.OrderID)
                // Mark as collected
                adapter.CreatePrepStage(ctx, event.Body.OrderID,
                    deliveroo.CreatePrepStageRequest{
                        Stage:      deliveroo.PrepStageCollected,
                        OccurredAt: time.Now(),
                    })
            }
        }
    }

    w.WriteHeader(http.StatusOK)
})
```

### 5. Handle Scheduled Orders

Scheduled orders require an additional confirmation step:

```go
// When you receive a scheduled order
if !order.ASAP && order.ConfirmAt != nil {
    // Accept the order first
    adapter.UpdateOrder(ctx, order.ID, deliveroo.UpdateOrderRequest{
        Status: deliveroo.OrderUpdateAccepted,
    })

    // Wait until confirm_at time, then confirm
    waitUntil(*order.ConfirmAt)

    adapter.UpdateOrder(ctx, order.ID, deliveroo.UpdateOrderRequest{
        Status: deliveroo.OrderUpdateConfirmed,
    })

    // Now start cooking
    adapter.CreatePrepStage(ctx, order.ID, deliveroo.CreatePrepStageRequest{
        Stage:      deliveroo.PrepStageInKitchen,
        OccurredAt: time.Now(),
    })
}
```

### 6. Reject Orders When Necessary

```go
// If you can't fulfill the order
reason := deliveroo.RejectReasonIngredientUnavailable
notes := "Out of chicken breast"

adapter.UpdateOrder(ctx, order.ID, deliveroo.UpdateOrderRequest{
    Status:       deliveroo.OrderUpdateRejected,
    RejectReason: &reason,
    Notes:        &notes,
})
```

## Menu API Usage Examples

### Daily Stock Management

```go
// Mark items as sold out for the day
req := deliveroo.UpdateItemUnavailabilitiesRequest{
    ItemUnavailabilities: []deliveroo.ItemUnavailability{
        {ItemID: "chicken-breast", Status: deliveroo.StatusUnavailable},
        {ItemID: "salmon", Status: deliveroo.StatusUnavailable},
    },
}
adapter.UpdateItemUnavailabilitiesV2(ctx, brandID, siteID, req)

// Later: mark them available again when restocked
restockReq := deliveroo.UpdateItemUnavailabilitiesRequest{
    ItemUnavailabilities: []deliveroo.ItemUnavailability{
        {ItemID: "chicken-breast", Status: deliveroo.StatusAvailable},
    },
}
adapter.UpdateItemUnavailabilitiesV2(ctx, brandID, siteID, restockReq)
```

### Bulk Unavailability Reset (End of Day)

```go
// Clear all unavailabilities at midnight for new day
req := deliveroo.ReplaceAllUnavailabilitiesRequest{
    UnavailableIDs: []string{}, // Clear all sold out items
    HiddenIDs:      []string{}, // Clear all hidden items
}
adapter.ReplaceItemUnavailabilitiesV2(ctx, brandID, siteID, req)
```

### POS Integration with PLU Codes

```go
// Synchronize menu items with your POS system
mappings := deliveroo.UpdatePLUsRequest{
    {ItemID: "burger-classic", PLU: "1001"},
    {ItemID: "burger-cheese", PLU: "1002"},
    {ItemID: "fries-regular", PLU: "2001"},
}
adapter.UpdatePLUs(ctx, brandID, menuID, mappings)
```

### Large Menu Upload (V3 Async)

```go
// For menus >5MB, use async workflow
// Step 1: Get S3 upload URL
uploadResp, _ := adapter.GetMenuUploadURLV3(ctx, brandID, menuID)

// Step 2: Upload directly to S3
adapter.UploadMenuToS3(ctx, uploadResp.UploadURL, largeMenu)

// Step 3: Publish to live
publishReq := deliveroo.PublishMenuJobRequest{
    Action: deliveroo.JobActionPublishMenuToLive,
    Params: deliveroo.PublishMenuJobParams{
        BrandID: brandID,
        MenuID:  menuID,
        Version: &uploadResp.Version,
    },
}
job, _ := adapter.PublishMenuJob(ctx, brandID, publishReq)

// Step 4: Poll until complete
for {
    time.Sleep(5 * time.Second)
    status, _ := adapter.GetJobStatus(ctx, brandID, job.ID)

    if status.Status == deliveroo.JobStatusCompleted {
        fmt.Println("✓ Menu published!")
        break
    } else if status.Status == deliveroo.JobStatusFailed {
        fmt.Printf("✗ Failed: %s\n", *status.Error)
        break
    }
}
```

### Real-Time Inventory Synchronization

```go
// When an order depletes stock to zero
func onOrderReceived(itemID string, remainingStock int) {
    if remainingStock == 0 {
        // Immediately mark as unavailable
        req := deliveroo.UpdateItemUnavailabilitiesRequest{
            ItemUnavailabilities: []deliveroo.ItemUnavailability{
                {ItemID: itemID, Status: deliveroo.StatusUnavailable},
            },
        }
        adapter.UpdateItemUnavailabilitiesV2(ctx, brandID, siteID, req)
    }
}
```

### Menu Webhook Handling

```go
// Setup webhook to receive menu upload results
http.HandleFunc("/webhooks/menu-events", func(w http.ResponseWriter, r *http.Request) {
    handler := deliveroo.NewWebhookHandler(webhookSecret)
    event, err := handler.ParseMenuEvent(r)
    if err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }

    result := event.Body.MenuUploadResult
    if result.HTTPStatus == 200 {
        log.Printf("✓ Menu %s uploaded successfully", result.MenuID)
    } else {
        log.Printf("✗ Menu upload failed: %v", result.Errors)
    }

    w.WriteHeader(http.StatusOK)
})
```

### Automated Inventory Management

```go
// Daily morning inventory check
func morningInventoryCheck() {
    // Get current unavailabilities
    current, _ := adapter.GetItemUnavailabilitiesV2(ctx, brandID, siteID)

    // Check actual stock levels
    outOfStock := checkInventory() // Your inventory system

    // Update only changed items
    var updates []deliveroo.ItemUnavailability
    for _, itemID := range outOfStock {
        if !contains(current.UnavailableIDs, itemID) {
            updates = append(updates, deliveroo.ItemUnavailability{
                ItemID: itemID,
                Status: deliveroo.StatusUnavailable,
            })
        }
    }

    if len(updates) > 0 {
        req := deliveroo.UpdateItemUnavailabilitiesRequest{
            ItemUnavailabilities: updates,
        }
        adapter.UpdateItemUnavailabilitiesV2(ctx, brandID, siteID, req)
    }
}
```

## Advanced Usage Examples

### Order Workflow

Complete order processing workflow:

```go
// 1. List new orders
orders, err := adapter.ListOrders(ctx, deliveroo.OrderStatusPlaced, nil, outletID)
if err != nil {
    log.Fatal(err)
}

for _, order := range orders {
    // 2. Acknowledge order receipt
    if err := adapter.AcknowledgeOrder(ctx, order.ID); err != nil {
        log.Printf("Failed to acknowledge order %s: %v", order.ID, err)
        continue
    }

    // 3. Accept order with prep time
    prepMinutes := 20
    if err := adapter.AcceptOrder(ctx, order.ID, prepMinutes); err != nil {
        log.Printf("Failed to accept order %s: %v", order.ID, err)
        continue
    }

    log.Printf("Successfully accepted order %s", order.DisplayID)

    // 4. Later, mark order as ready
    readyAt := time.Now()
    err = adapter.UpdateOrderStatus(
        ctx,
        order.ID,
        deliveroo.OrderStatusConfirmed,
        &readyAt,
        nil,
    )
    if err != nil {
        log.Printf("Failed to update order status: %v", err)
    }
}
```

### Menu Synchronization

Sync your menu to Deliveroo:

```go
// Create menu structure
menu := &deliveroo.MenuUploadRequest{
    Name: "My Restaurant Menu",
    Menu: deliveroo.MenuContent{
        Categories: []deliveroo.Category{
            {
                ID:   "cat-1",
                Name: map[string]string{"en": "Appetizers", "fr": "Entrées"},
                Description: map[string]string{
                    "en": "Delicious starters",
                    "fr": "Délicieuses entrées",
                },
                ItemIDs: []string{"item-1", "item-2"},
            },
        },
        Items: []deliveroo.Item{
            {
                ID:   "item-1",
                Name: map[string]string{"en": "Spring Rolls", "fr": "Rouleaux de printemps"},
                Description: map[string]string{
                    "en": "Crispy vegetable rolls",
                    "fr": "Rouleaux de légumes croustillants",
                },
                OperationalName: "SPRING_ROLL",
                PriceInfo: deliveroo.PriceInfo{
                    Price: 650, // €6.50 in cents
                },
                TaxRate:                   "20",
                ContainsAlcohol:           false,
                Type:                      "ITEM",
                Allergies:                 []string{},
                Diets:                     []string{"vegetarian"},
                IsEligibleAsReplacement:   true,
                IsEligibleForSubstitution: true,
            },
        },
    },
    SiteIDs: []string{"site-123", "site-456"},
}

// Push menu to Deliveroo
err := adapter.PushMenu(ctx, brandID, menuID, menu)
if err != nil {
    log.Fatal(err)
}
```

### Filtering Orders by Time

Get orders since a specific time:

```go
// Get orders from the last hour
since := time.Now().Add(-1 * time.Hour)
orders, err := adapter.ListOrders(
    ctx,
    deliveroo.OrderStatusPlaced,
    &since,
    "",
)
```

### Accessing Order Details

```go
for _, order := range orders {
    log.Printf("Order ID: %s", order.DisplayID)
    log.Printf("Status: %s", order.Status)
    log.Printf("Total: %d %s", order.TotalPrice.Fractional, order.TotalPrice.CurrencyCode)
    log.Printf("Fulfillment: %s", order.FulfillmentType)

    // Delivery information (if applicable)
    if order.Delivery != nil && order.Delivery.Address != nil {
        addr := order.Delivery.Address
        log.Printf("Delivery to: %s %s, %s %s",
            addr.Street, addr.Number, addr.PostalCode, addr.City)
    }

    // Order items
    for _, item := range order.Items {
        log.Printf("  - %s x%d @ %d",
            item.Name, item.Quantity, item.UnitPrice.Fractional)
    }

    // Customer info (if available)
    if order.Customer != nil {
        log.Printf("Customer: %s", order.Customer.FirstName)
    }
}
```

## Configuration

### Environment Variables

Recommended environment variable names for configuration:

```bash
DELIVEROO_CLIENT_ID=your-client-id
DELIVEROO_CLIENT_SECRET=your-client-secret
DELIVEROO_BRAND_ID=your-brand-id
DELIVEROO_MENU_ID=your-menu-id
DELIVEROO_OUTLET_ID=your-outlet-id
DELIVEROO_CURRENCY=EUR
DELIVEROO_USE_SANDBOX=false
```

### Retry Configuration

The retry behavior is configured via constants in `adapter.go`:

```go
const (
    MaxRetries        = 3
    InitialBackoff    = 1 * time.Second
    MaxBackoff        = 30 * time.Second
    BackoffMultiplier = 2.0
)
```

### Token Management

OAuth tokens are automatically managed:
- Cached after first retrieval
- Refreshed 1 minute before expiry
- Thread-safe access

## Thread Safety

The adapter is fully thread-safe and can be used concurrently:

```go
var wg sync.WaitGroup
adapter := deliveroo.NewAdapter(config)

for i := 0; i < 10; i++ {
    wg.Add(1)
    go func() {
        defer wg.Done()
        orders, _ := adapter.ListOrders(ctx, "", nil, "")
        // Process orders...
    }()
}

wg.Wait()
```

## Testing

### Using Sandbox Environment

```go
adapter := deliveroo.NewAdapter(deliveroo.AdapterConfig{
    ClientID:     "sandbox-client-id",
    ClientSecret: "sandbox-client-secret",
    UseSandbox:   true, // Use sandbox URLs
})
```

Sandbox URLs:
- Auth: `https://auth-sandbox.developers.deliveroo.com`
- Orders: `https://api-sandbox.developers.deliveroo.com/order`
- Menu: `https://api-sandbox.developers.deliveroo.com/menu`

## Troubleshooting

### Authentication Errors

```go
// Error: "token request failed with status 401"
// Solution: Verify your client ID and secret
```

### Rate Limiting

The adapter automatically handles rate limiting with exponential backoff. If you still encounter issues:
- Reduce request frequency
- Implement request queuing
- Contact Deliveroo support for rate limit increase

### Menu Upload Failures

Common issues:
- Missing required fields (name, price_info, tax_rate)
- Invalid translations (must provide at least one language)
- Incorrect site IDs

## API Limits

- Menu API: 1 request per minute per site
- Large menus (>5MB): 10 requests per 10 seconds per integration
- Request size limit: 10 MB

## Support

For Deliveroo API issues:
- Documentation: https://developers.deliveroo.com/docs
- Support: Contact your Deliveroo integration team

## License

This integration is part of the TSB Service project.