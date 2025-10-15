# Deliveroo Integration

A comprehensive Go client for Deliveroo Developer APIs with built-in retry logic, OAuth token caching, and idempotency support.

## Features

- **Complete API Coverage**: Orders, Menu, and Authentication APIs
- **Automatic Retry**: Exponential backoff for 429 (rate limit) and 5xx errors
- **OAuth Token Caching**: Automatic token refresh with expiry management
- **Idempotency**: Automatic idempotency key generation for safe retries
- **Thread-Safe**: Concurrent request handling with proper synchronization
- **Sandbox Support**: Easy toggle between production and sandbox environments

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

#### Order Management

##### ListOrders
List orders with optional filtering.

```go
orders, err := adapter.ListOrders(ctx, status, since, outletID)
```

Parameters:
- `ctx`: Context
- `status`: Filter by order status (e.g., `OrderStatusPlaced`, `OrderStatusAccepted`)
- `since`: Filter orders after this time (optional, pass `nil` for all)
- `outletID`: Filter by outlet/location ID (optional, pass `""` for all)

Returns: `[]Order, error`

##### AcknowledgeOrder
Acknowledge receipt of an order.

```go
err := adapter.AcknowledgeOrder(ctx, orderID)
```

Parameters:
- `ctx`: Context
- `orderID`: The unique order ID

Returns: `error`

##### AcceptOrder
Accept an order with preparation time.

```go
err := adapter.AcceptOrder(ctx, orderID, prepMinutes)
```

Parameters:
- `ctx`: Context
- `orderID`: The unique order ID
- `prepMinutes`: Preparation time in minutes

Returns: `error`

##### UpdateOrderStatus
Update the status of an order.

```go
err := adapter.UpdateOrderStatus(ctx, orderID, status, readyAt, pickupAt)
```

Parameters:
- `ctx`: Context
- `orderID`: The unique order ID
- `status`: New order status
- `readyAt`: Time when order is ready (optional, pass `nil`)
- `pickupAt`: Time when order was picked up (optional, pass `nil`)

Returns: `error`

#### Menu Management

##### PullMenu
Retrieve the current menu from Deliveroo.

```go
menu, err := adapter.PullMenu(ctx, brandID, menuID)
```

Parameters:
- `ctx`: Context
- `brandID`: Your brand ID
- `menuID`: The menu ID

Returns: `*MenuUploadRequest, error`

##### PushMenu
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