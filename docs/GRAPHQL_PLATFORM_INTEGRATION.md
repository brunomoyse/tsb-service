# GraphQL Platform Integration Guide

This guide explains how to use the GraphQL API to interact with third-party delivery platforms (Deliveroo, Uber Eats).

## Overview

All orders in the system now have an `OrderSource` field that indicates where they came from:
- `TOKYO` - Direct orders from your webshop
- `DELIVEROO` - Orders from Deliveroo
- `UBER` - Orders from Uber Eats

## Order Type Extension

The existing `Order` type has been extended with platform-specific data:

```graphql
type Order {
  # ... existing fields ...

  # New fields
  source: OrderSource!           # Where the order came from
  platformData: PlatformOrder    # Platform-specific details (null for TOKYO orders)
}
```

## Platform Order Management

### Query Platform Orders

Get orders from a specific platform with optional filtering:

```graphql
query GetDeliverooOrders {
  platformOrders(
    source: DELIVEROO
    brandId: "your-brand-id"
    restaurantId: "your-restaurant-id"
    filter: {
      liveOrdersOnly: true
      startDate: "2025-01-01T00:00:00Z"
    }
  ) {
    orders {
      platformOrderId
      displayId
      status
      fulfillmentType
      totalPrice {
        fractional
        currencyCode
      }
      items {
        name
        quantity
        totalPrice {
          fractional
          currencyCode
        }
      }
      customer {
        firstName
        contactNumber
      }
    }
    nextCursor
    hasMore
  }
}
```

### Get Single Platform Order

```graphql
query GetSingleOrder {
  platformOrder(
    source: DELIVEROO
    orderId: "order-uuid-here"
  ) {
    displayId
    status
    prepareFor
    startPreparingAt
    items {
      name
      operationalName
      quantity
    }
  }
}
```

### Accept Platform Order

```graphql
mutation AcceptOrder {
  acceptPlatformOrder(
    input: {
      source: DELIVEROO
      orderId: "order-uuid-here"
      notes: "Accepted - starting preparation"
    }
  ) {
    displayId
    status
  }
}
```

### Reject Platform Order

```graphql
mutation RejectOrder {
  rejectPlatformOrder(
    input: {
      source: DELIVEROO
      orderId: "order-uuid-here"
      reason: "ingredient_unavailable"
      notes: "Out of chicken breast"
    }
  ) {
    displayId
    status
  }
}
```

### Update Preparation Stage

```graphql
mutation UpdatePrepStage {
  updatePlatformOrderPrepStage(
    input: {
      source: DELIVEROO
      orderId: "order-uuid-here"
      stage: READY_FOR_COLLECTION
      delay: 5  # Optional: request 5 more minutes
    }
  ) {
    displayId
    status
  }
}
```

Available prep stages:
- `IN_KITCHEN` - Order is being prepared
- `READY_FOR_COLLECTION_SOON` - Almost ready
- `READY_FOR_COLLECTION` - Ready for pickup
- `COLLECTED` - Rider has collected

## Menu Management

### Get Item Unavailabilities

```graphql
query GetUnavailableItems {
  itemUnavailabilities(
    source: DELIVEROO
    brandId: "your-brand-id"
    siteId: "your-site-id"
  ) {
    unavailableIds  # Sold out for the day
    hiddenIds       # Completely hidden
  }
}
```

### Mark Items Sold Out

```graphql
mutation MarkSoldOut {
  markItemsSoldOut(
    source: DELIVEROO
    brandId: "your-brand-id"
    siteId: "your-site-id"
    itemIds: ["item-1-uuid", "item-2-uuid"]
  ) {
    unavailableIds
    hiddenIds
  }
}
```

### Mark Items Available Again

```graphql
mutation MarkAvailable {
  markItemsAvailable(
    source: DELIVEROO
    brandId: "your-brand-id"
    siteId: "your-site-id"
    itemIds: ["item-1-uuid", "item-2-uuid"]
  ) {
    unavailableIds
    hiddenIds
  }
}
```

### Update Item Availability (Granular Control)

```graphql
mutation UpdateAvailability {
  updateItemAvailabilities(
    source: DELIVEROO
    brandId: "your-brand-id"
    siteId: "your-site-id"
    items: [
      { itemId: "item-1-uuid", status: UNAVAILABLE }
      { itemId: "item-2-uuid", status: HIDDEN }
      { itemId: "item-3-uuid", status: AVAILABLE }
    ]
  ) {
    unavailableIds
    hiddenIds
  }
}
```

Item availability statuses:
- `AVAILABLE` - Item is visible and orderable
- `UNAVAILABLE` - Item is greyed out ("sold out for the day")
- `HIDDEN` - Item is completely hidden from menu

### Replace All Unavailabilities

Clear all previous unavailabilities and set new ones:

```graphql
mutation ReplaceAll {
  replaceAllUnavailabilities(
    source: DELIVEROO
    brandId: "your-brand-id"
    siteId: "your-site-id"
    input: {
      unavailableIds: ["item-1-uuid", "item-2-uuid"]
      hiddenIds: ["item-3-uuid"]
    }
  ) {
    unavailableIds
    hiddenIds
  }
}
```

### Sync Menu to Platform

Push your local menu to the platform:

```graphql
mutation SyncMenu {
  syncMenuToPlatform(
    source: DELIVEROO
    brandId: "your-brand-id"
    menuId: "your-menu-id"
  )
}
```

### Update PLU Mappings

Map menu items to POS PLU codes:

```graphql
mutation UpdatePLUs {
  updatePLUs(
    source: DELIVEROO
    brandId: "your-brand-id"
    menuId: "your-menu-id"
    input: {
      mappings: [
        { itemId: "item-1-uuid", plu: "1001" }
        { itemId: "item-2-uuid", plu: "1002" }
      ]
    }
  )
}
```

## Real-Time Subscriptions

### Subscribe to Platform Order Updates

Receive real-time updates when orders are created or status changes:

```graphql
subscription OrderUpdates {
  platformOrderUpdates(
    source: DELIVEROO
    restaurantId: "your-restaurant-id"
  ) {
    displayId
    status
    items {
      name
      quantity
    }
  }
}
```

### Subscribe to Rider Updates

Get notified when riders are assigned, arrive, or status changes:

```graphql
subscription RiderUpdates {
  platformRiderUpdates(
    source: DELIVEROO
    restaurantId: "your-restaurant-id"
  ) {
    orderId
    riders {
      fullName
      contactNumber
      estimatedArrivalTime
      statusLog {
        at
        status
      }
    }
  }
}
```

## Error Handling

All mutations and queries will return descriptive errors:

```graphql
{
  "errors": [
    {
      "message": "deliveroo service not configured",
      "path": ["acceptPlatformOrder"]
    }
  ]
}
```

Common errors:
- "deliveroo service not configured" - Deliveroo credentials not set in environment
- "Uber Eats integration not yet implemented" - Uber Eats support coming soon
- "brandID and restaurantID are required" - Missing required parameters

## Adding Uber Eats Support

The schema is designed to support Uber Eats with minimal changes:

1. Implement Uber Eats adapter methods
2. Add switch cases in resolvers for `model.OrderSourceUber`
3. Update subscription handlers for Uber Eats events

All GraphQL queries and mutations already accept `source: UBER` parameter.

## Notes

- All admin mutations require `@admin` directive (admin JWT token)
- Subscriptions require `@auth` directive (valid JWT token)
- Platform-specific IDs are UUIDs
- Times are in RFC3339 format
- Monetary amounts use fractional units (cents)
