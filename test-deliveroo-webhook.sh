#!/bin/bash

# ============================================================================
# Deliveroo Webhook Test Script
# ============================================================================
# This script simulates a Deliveroo webhook for testing the order integration
# Usage: ./test-deliveroo-webhook.sh [event_type]
# Events: new, status_update, rider
# ============================================================================

set -e

# ============================================================================
# Configuration
# ============================================================================

# Webhook URL (change to your deployed URL if needed)
WEBHOOK_URL="${WEBHOOK_URL:-http://localhost:8080/api/v1/webhooks/deliveroo/orders}"

# Webhook secret (must match DELIVEROO_WEBHOOK_SECRET in .env)
WEBHOOK_SECRET="${DELIVEROO_WEBHOOK_SECRET:-your-webhook-secret-here}"

# Event type (default: new order)
EVENT_TYPE="${1:-new}"

# ============================================================================
# Color output
# ============================================================================
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# ============================================================================
# Helper Functions
# ============================================================================

log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

# ============================================================================
# Generate timestamps
# ============================================================================

# Current timestamp
NOW=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

# 30 minutes from now (prepare_for time)
if [[ "$OSTYPE" == "darwin"* ]]; then
    # macOS
    PREPARE_FOR=$(date -u -v+30M +"%Y-%m-%dT%H:%M:%SZ")
else
    # Linux
    PREPARE_FOR=$(date -u -d "+30 minutes" +"%Y-%m-%dT%H:%M:%SZ")
fi

# Generate unique IDs
TIMESTAMP=$(date +%s)
ORDER_ID="gb:test-order-${TIMESTAMP}"
ORDER_NUMBER="TEST-${TIMESTAMP}"
DISPLAY_ID="T$(date +%H%M%S)"

# ============================================================================
# Generate Payloads
# ============================================================================

generate_new_order_payload() {
    cat <<EOF
{
  "event": "order.new",
  "body": {
    "order": {
      "id": "${ORDER_ID}",
      "order_number": "${ORDER_NUMBER}",
      "location_id": "test-location-123",
      "brand_id": "test-brand-456",
      "display_id": "${DISPLAY_ID}",
      "status": "placed",
      "status_log": [
        {
          "at": "${NOW}",
          "status": "placed"
        }
      ],
      "fulfillment_type": "deliveroo",
      "order_notes": "Test order - Ring the doorbell please",
      "cutlery_notes": "No cutlery needed",
      "asap": true,
      "prepare_for": "${PREPARE_FOR}",
      "start_preparing_at": "${NOW}",
      "table_number": "",
      "subtotal": {
        "fractional": 2500,
        "currency_code": "EUR"
      },
      "total_price": {
        "fractional": 2790,
        "currency_code": "EUR"
      },
      "partner_order_subtotal": {
        "fractional": 2500,
        "currency_code": "EUR"
      },
      "partner_order_total": {
        "fractional": 2790,
        "currency_code": "EUR"
      },
      "offer_discount": {
        "fractional": 0,
        "currency_code": "EUR"
      },
      "cash_due": {
        "fractional": 0,
        "currency_code": "EUR"
      },
      "bag_fee": {
        "fractional": 0,
        "currency_code": "EUR"
      },
      "surcharge": {
        "fractional": 0,
        "currency_code": "EUR"
      },
      "fee_breakdown": [],
      "items": [
        {
          "pos_item_id": null,
          "name": "Margherita Pizza",
          "operational_name": "PIZZA_MARGHERITA",
          "unit_price": {
            "fractional": 1250,
            "currency_code": "EUR"
          },
          "total_price": {
            "fractional": 2500,
            "currency_code": "EUR"
          },
          "quantity": 2,
          "type": "ITEM",
          "modifier_groups": [
            {
              "name": "Extra Toppings",
              "modifiers": [
                {
                  "pos_modifier_id": null,
                  "name": "Extra Cheese",
                  "quantity": 1,
                  "unit_price": {
                    "fractional": 0,
                    "currency_code": "EUR"
                  },
                  "total_price": {
                    "fractional": 0,
                    "currency_code": "EUR"
                  }
                }
              ]
            }
          ]
        }
      ],
      "delivery": {
        "delivery_fee": {
          "fractional": 290,
          "currency_code": "EUR"
        },
        "address": {
          "street": "Avenue Louise",
          "number": "123",
          "postal_code": "1000",
          "city": "Brussels",
          "address_line_1": "123 Avenue Louise",
          "address_line_2": "Apartment 4B",
          "latitude": 50.8503,
          "longitude": 4.3517
        },
        "estimated_delivery_at": null
      },
      "customer": {
        "first_name": "Jean",
        "last_name": "Dupont",
        "phone_number": "+32499123456"
      },
      "promotions": [],
      "remake_details": null,
      "is_tabletless": false,
      "meal_cards": []
    }
  }
}
EOF
}

generate_status_update_payload() {
    cat <<EOF
{
  "event": "order.status_update",
  "body": {
    "order": {
      "id": "gb:test-order-existing",
      "order_number": "TEST-EXISTING",
      "location_id": "test-location-123",
      "brand_id": "test-brand-456",
      "display_id": "TEXIST",
      "status": "accepted",
      "status_log": [
        {
          "at": "${NOW}",
          "status": "accepted"
        }
      ],
      "fulfillment_type": "deliveroo",
      "order_notes": "",
      "cutlery_notes": "",
      "asap": true,
      "prepare_for": "${PREPARE_FOR}",
      "start_preparing_at": "${NOW}",
      "table_number": "",
      "subtotal": {
        "fractional": 1500,
        "currency_code": "EUR"
      },
      "total_price": {
        "fractional": 1790,
        "currency_code": "EUR"
      },
      "partner_order_subtotal": {
        "fractional": 1500,
        "currency_code": "EUR"
      },
      "partner_order_total": {
        "fractional": 1790,
        "currency_code": "EUR"
      },
      "offer_discount": {
        "fractional": 0,
        "currency_code": "EUR"
      },
      "cash_due": {
        "fractional": 0,
        "currency_code": "EUR"
      },
      "bag_fee": {
        "fractional": 0,
        "currency_code": "EUR"
      },
      "surcharge": {
        "fractional": 0,
        "currency_code": "EUR"
      },
      "fee_breakdown": [],
      "items": [
        {
          "name": "Caesar Salad",
          "operational_name": "SALAD_CAESAR",
          "unit_price": {
            "fractional": 1500,
            "currency_code": "EUR"
          },
          "total_price": {
            "fractional": 1500,
            "currency_code": "EUR"
          },
          "quantity": 1,
          "type": "ITEM",
          "modifier_groups": []
        }
      ],
      "delivery": {
        "delivery_fee": {
          "fractional": 290,
          "currency_code": "EUR"
        },
        "address": {
          "street": "Rue de la Loi",
          "number": "42",
          "postal_code": "1040",
          "city": "Brussels",
          "address_line_1": "42 Rue de la Loi",
          "address_line_2": "",
          "latitude": 50.8467,
          "longitude": 4.3686
        }
      },
      "customer": {
        "first_name": "Marie",
        "last_name": "Martin",
        "phone_number": "+32487654321"
      },
      "is_tabletless": false
    }
  }
}
EOF
}

generate_rider_update_payload() {
    cat <<EOF
{
  "event": "rider.status_update",
  "body": {
    "order_id": "gb:test-order-existing",
    "riders": [
      {
        "estimated_arrival_time": "${PREPARE_FOR}",
        "at": "${NOW}",
        "accuracy_in_meters": 50,
        "lat": 50.8503,
        "lon": 4.3517,
        "full_name": "Pierre Dubois",
        "contact_number": "+32471234567",
        "bridge_code": "",
        "bridge_number": "",
        "status_log": [
          {
            "at": "${NOW}",
            "status": "rider_assigned"
          }
        ]
      }
    ]
  }
}
EOF
}

# ============================================================================
# Main Logic
# ============================================================================

log_info "Deliveroo Webhook Test Script"
echo ""

# Check if openssl is available
if ! command -v openssl &> /dev/null; then
    log_error "openssl is required but not installed. Please install it first."
    exit 1
fi

# Generate payload based on event type
case "$EVENT_TYPE" in
    new)
        log_info "Event Type: New Order (order.new)"
        PAYLOAD=$(generate_new_order_payload)
        ;;
    status_update|status)
        log_info "Event Type: Status Update (order.status_update)"
        PAYLOAD=$(generate_status_update_payload)
        WEBHOOK_URL="${WEBHOOK_URL}"
        ;;
    rider)
        log_info "Event Type: Rider Update (rider.status_update)"
        PAYLOAD=$(generate_rider_update_payload)
        WEBHOOK_URL="${WEBHOOK_URL/orders/riders}"
        ;;
    *)
        log_error "Invalid event type: $EVENT_TYPE"
        echo ""
        echo "Usage: $0 [event_type]"
        echo "Event types:"
        echo "  new           - Send a new order webhook (default)"
        echo "  status_update - Send an order status update"
        echo "  rider         - Send a rider status update"
        exit 1
        ;;
esac

# Generate sequence GUID
if command -v uuidgen &> /dev/null; then
    SEQUENCE_GUID=$(uuidgen)
else
    SEQUENCE_GUID="$(cat /proc/sys/kernel/random/uuid 2>/dev/null || echo "test-guid-$(date +%s)")"
fi

log_info "Generating HMAC signature..."

# Generate HMAC-SHA256 signature
# Backend uses: HMAC-SHA256(payload, webhook_secret + sequence_guid) encoded as hex
HMAC_KEY="${WEBHOOK_SECRET}${SEQUENCE_GUID}"
SIGNATURE=$(echo -n "${PAYLOAD}" | openssl dgst -sha256 -hmac "${HMAC_KEY}" | sed 's/^.* //')

# Display request details
echo ""
log_info "Request Details:"
echo "  Webhook URL:    ${WEBHOOK_URL}"
echo "  Order ID:       ${ORDER_ID}"
echo "  Display ID:     ${DISPLAY_ID}"
echo "  Sequence GUID:  ${SEQUENCE_GUID}"
echo "  Signature:      ${SIGNATURE:0:40}..."
echo ""

# Send webhook
log_info "Sending webhook..."
echo ""

# Create a temporary file to store the response
TEMP_RESPONSE=$(mktemp)
TEMP_HEADERS=$(mktemp)

# Send webhook and capture response
HTTP_CODE=$(curl -s -w "%{http_code}" -o "$TEMP_RESPONSE" -X POST "${WEBHOOK_URL}" \
  -H "Content-Type: application/json" \
  -H "x-deliveroo-hmac-sha256: ${SIGNATURE}" \
  -H "x-deliveroo-sequence-guid: ${SEQUENCE_GUID}" \
  -d "${PAYLOAD}")

# Read response body
RESPONSE_BODY=$(cat "$TEMP_RESPONSE")

# Clean up temp files
rm -f "$TEMP_RESPONSE" "$TEMP_HEADERS"

echo ""
log_info "Response:"
echo "  HTTP Status: ${HTTP_CODE}"
echo "  Body: ${RESPONSE_BODY}"
echo ""

# Check response
if [ "$HTTP_CODE" -eq 200 ]; then
    log_success "✓ Webhook delivered successfully!"
    echo ""
    log_info "Next steps:"
    echo "  1. Check backend logs for order processing"
    echo "  2. Query GraphQL to see the order:"
    echo ""
    echo "     query {"
    echo "       orders(page: 1, limit: 10) {"
    echo "         id"
    echo "         source"
    echo "         platformOrderId"
    echo "         orderStatus"
    echo "       }"
    echo "     }"
    echo ""
    echo "  3. Subscribe to real-time updates:"
    echo ""
    echo "     subscription {"
    echo "       platformOrderUpdates(source: DELIVEROO, restaurantId: \"your-uuid\") {"
    echo "         displayId"
    echo "         status"
    echo "       }"
    echo "     }"
    echo ""
elif [ "$HTTP_CODE" -eq 400 ]; then
    log_error "✗ Bad Request - Signature verification likely failed"
    echo ""
    log_warn "Troubleshooting:"
    echo "  1. Check DELIVEROO_WEBHOOK_SECRET in .env matches the secret in this script"
    echo "  2. Current secret: ${WEBHOOK_SECRET}"
    echo "  3. Set environment variable: export DELIVEROO_WEBHOOK_SECRET='your-secret'"
    echo ""
    exit 1
elif [ "$HTTP_CODE" -eq 404 ]; then
    log_error "✗ Not Found - Webhook endpoint not available"
    echo ""
    log_warn "Troubleshooting:"
    echo "  1. Make sure the application is running: go run cmd/app/main.go"
    echo "  2. Check the webhook URL: ${WEBHOOK_URL}"
    echo "  3. Set custom URL: export WEBHOOK_URL='http://your-server:8080/api/v1/webhooks/deliveroo/orders'"
    echo ""
    exit 1
else
    log_error "✗ Unexpected response code: ${HTTP_CODE}"
    echo ""
    exit 1
fi

exit 0
