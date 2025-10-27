# Deliveroo Webhook Testing Guide

## Quick Start

### 1. Set Your Webhook Secret (Required!)

**IMPORTANT**: You must set the `DELIVEROO_WEBHOOK_SECRET` environment variable to match your `.env` file:

```bash
# Get the secret from your .env file
grep DELIVEROO_WEBHOOK_SECRET .env

# Export it (required!)
export DELIVEROO_WEBHOOK_SECRET="your-webhook-secret-from-env-file"
```

Without this, you'll get a 400 error with "Invalid webhook signature or payload".

### 2. Make Sure Your App is Running

```bash
go run cmd/app/main.go
```

You should see in the logs:
```
Deliveroo webhook endpoints registered at /api/v1/webhooks/deliveroo
```

### 3. Run the Test Script

```bash
# Set the webhook secret first (see step 1)
export DELIVEROO_WEBHOOK_SECRET="your-webhook-secret-from-env-file"

# Test a new order (default)
./test-deliveroo-webhook.sh

# Test an order status update
./test-deliveroo-webhook.sh status_update

# Test a rider update
./test-deliveroo-webhook.sh rider
```

## Expected Output

### Success (HTTP 200)
```
[INFO] Deliveroo Webhook Test Script

[INFO] Event Type: New Order (order.new)
[INFO] Generating HMAC signature...

[INFO] Request Details:
  Webhook URL:    http://localhost:8080/api/v1/webhooks/deliveroo/orders
  Order ID:       gb:test-order-1729877234
  Display ID:     T185714
  Sequence GUID:  A5B3C2D1-E4F3-4321-8765-123456789ABC
  Signature:      Xj8yK3mN9pQ2rT5vW8xZ1...

[INFO] Sending webhook...

[INFO] Response:
  HTTP Status: 200
  Body: {"status":"received"}

[SUCCESS] ✓ Webhook delivered successfully!

[INFO] Next steps:
  1. Check backend logs for order processing
  2. Query GraphQL to see the order
  3. Subscribe to real-time updates
```

## Backend Logs to Check

When the webhook is received, you should see:

```
Received new Deliveroo order: gb:test-order-1729877234 (Display ID: T185714)
Created platform order with ID: 3f8a9b7c-5d4e-2f1a-9b8c-7d6e5f4a3b2c
```

## Test GraphQL Subscription

Open your GraphQL client and subscribe:

```graphql
subscription {
  platformOrderUpdates(source: DELIVEROO, restaurantId: "your-restaurant-uuid") {
    platformOrderId
    displayId
    status
    totalPrice {
      fractional
      currencyCode
    }
    items {
      name
      quantity
    }
    customer {
      firstName
      phoneNumber
    }
  }
}
```

You should immediately receive the test order!

## Query Created Orders

```graphql
query {
  orders(page: 1, limit: 10) {
    id
    source
    orderStatus
    totalPrice
    platformOrderId
    platformData
  }
}
```

## Advanced Usage

### Custom Webhook URL

```bash
export WEBHOOK_URL="https://your-domain.com/api/v1/webhooks/deliveroo/orders"
./test-deliveroo-webhook.sh
```

### Test with ngrok

```bash
# Terminal 1: Start your app
go run cmd/app/main.go

# Terminal 2: Start ngrok
ngrok http 8080

# Terminal 3: Test with ngrok URL
export WEBHOOK_URL="https://abc123.ngrok.io/api/v1/webhooks/deliveroo/orders"
./test-deliveroo-webhook.sh
```

## Troubleshooting

### Error: HTTP 400 - Bad Request

**Problem**: Signature verification failed (most common issue!)

**Root Cause**: The `DELIVEROO_WEBHOOK_SECRET` environment variable is not set or doesn't match your `.env` file.

**Solution**:
```bash
# Check what's in your .env file
grep DELIVEROO_WEBHOOK_SECRET .env

# Copy the value and export it (including quotes!)
export DELIVEROO_WEBHOOK_SECRET="AtqcNmLL6mgCZVMjs_nlj9g6t1QXzTGZLL1mHYt8QKuZyEEomSoJi6EMnLvRbbd6ATcXj5ab8pWrvN_CPRklLgC"

# Now run the script
./test-deliveroo-webhook.sh
```

**Note**: The script uses the default value "your-webhook-secret-here" if the environment variable is not set, which will always fail signature verification.

### Error: HTTP 404 - Not Found

**Problem**: Webhook endpoint not available

**Solutions**:
1. Check app is running: `curl http://localhost:8080/api/v1/up`
2. Check Deliveroo is configured (check startup logs)
3. Verify webhook route is registered

### Error: Connection Refused

**Problem**: App not running

**Solution**:
```bash
go run cmd/app/main.go
```

### No Order in GraphQL

**Problem**: Order was created but not visible

**Check**:
1. Database migration applied?
   ```sql
   SELECT column_name FROM information_schema.columns
   WHERE table_name = 'orders'
   AND column_name IN ('source', 'platform_order_id', 'platform_data');
   ```

2. Check backend logs for errors

3. Query directly by platform_order_id:
   ```graphql
   query {
     orders(page: 1, limit: 100) {
       platformOrderId
       source
     }
   }
   ```

## Test Scenarios

### Scenario 1: New Delivery Order
```bash
./test-deliveroo-webhook.sh new
# Creates: Margherita Pizza x2 + Delivery
```

### Scenario 2: Order Status Changed
```bash
# First create an order, then update it
./test-deliveroo-webhook.sh status_update
# Updates existing order to "accepted"
```

### Scenario 3: Rider Assigned
```bash
./test-deliveroo-webhook.sh rider
# Rider "Pierre Dubois" assigned to order
```

## Script Configuration

Edit the script to customize:

```bash
# Line 16: Webhook URL
WEBHOOK_URL="${WEBHOOK_URL:-http://localhost:8080/api/v1/webhooks/deliveroo/orders}"

# Line 19: Webhook Secret
WEBHOOK_SECRET="${DELIVEROO_WEBHOOK_SECRET:-your-webhook-secret-here}"

# Customize order data in generate_new_order_payload() function
```

## Multiple Orders Testing

```bash
# Send 5 test orders
for i in {1..5}; do
  ./test-deliveroo-webhook.sh
  sleep 2
done
```

## Clean Up Test Orders

```sql
-- Delete test orders from database
DELETE FROM orders
WHERE platform_order_id LIKE 'gb:test-%';
```

## Next Steps

1. ✅ Test webhook delivery
2. ✅ Verify order creation
3. ✅ Test GraphQL subscription
4. ✅ Test accept/reject mutations
5. 🚀 Deploy to staging
6. 🚀 Configure real Deliveroo webhooks
