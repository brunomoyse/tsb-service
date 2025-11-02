package deliveroo

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// WebhookHandler provides utilities for handling Deliveroo webhooks
type WebhookHandler struct {
	webhookSecret string
}

// NewWebhookHandler creates a new webhook handler
func NewWebhookHandler(webhookSecret string) *WebhookHandler {
	return &WebhookHandler{
		webhookSecret: webhookSecret,
	}
}

// VerifySignature verifies the HMAC-SHA256 signature of a webhook request
func (h *WebhookHandler) VerifySignature(payload []byte, sequenceGUID, receivedSignature string) bool {
	// Compute HMAC-SHA256 using webhook secret and sequence GUID as key
	mac := hmac.New(sha256.New, []byte(h.webhookSecret+sequenceGUID))
	mac.Write(payload)
	expectedSignature := hex.EncodeToString(mac.Sum(nil))

	// Constant-time comparison to prevent timing attacks
	return hmac.Equal([]byte(expectedSignature), []byte(receivedSignature))
}

// ParseOrderEvent parses an order event webhook from an HTTP request
func (h *WebhookHandler) ParseOrderEvent(r *http.Request) (*OrderEventWebhook, error) {
	// Read headers
	sequenceGUID := r.Header.Get("x-deliveroo-sequence-guid")
	signature := r.Header.Get("x-deliveroo-hmac-sha256")
	payloadType := r.Header.Get("x-deliveroo-payload-type")
	webhookVersion := r.Header.Get("x-deliveroo-webhook-version")

	if sequenceGUID == "" || signature == "" {
		return nil, fmt.Errorf("missing required webhook headers")
	}

	// Read body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read request body: %w", err)
	}
	defer r.Body.Close()

	// Verify signature
	if !h.VerifySignature(body, sequenceGUID, signature) {
		return nil, fmt.Errorf("invalid webhook signature")
	}

	// Parse the webhook payload
	var webhook OrderEventWebhook
	if err := json.Unmarshal(body, &webhook); err != nil {
		return nil, fmt.Errorf("failed to parse webhook payload: %w", err)
	}

	// Log metadata for debugging (optional)
	_ = payloadType      // e.g., "event/order.new" or "event/order.status_update"
	_ = webhookVersion   // e.g., "1"

	return &webhook, nil
}

// ParseRiderEvent parses a rider event webhook from an HTTP request
func (h *WebhookHandler) ParseRiderEvent(r *http.Request) (*RiderEventWebhook, error) {
	// Read headers
	sequenceGUID := r.Header.Get("x-deliveroo-sequence-guid")
	signature := r.Header.Get("x-deliveroo-hmac-sha256")
	payloadType := r.Header.Get("x-deliveroo-payload-type")
	webhookVersion := r.Header.Get("x-deliveroo-webhook-version")

	if sequenceGUID == "" || signature == "" {
		return nil, fmt.Errorf("missing required webhook headers")
	}

	// Read body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read request body: %w", err)
	}
	defer r.Body.Close()

	// Verify signature
	if !h.VerifySignature(body, sequenceGUID, signature) {
		return nil, fmt.Errorf("invalid webhook signature")
	}

	// Parse the webhook payload
	var webhook RiderEventWebhook
	if err := json.Unmarshal(body, &webhook); err != nil {
		return nil, fmt.Errorf("failed to parse webhook payload: %w", err)
	}

	// Log metadata for debugging (optional)
	_ = payloadType      // e.g., "event/rider.status_update"
	_ = webhookVersion   // e.g., "1"

	return &webhook, nil
}

// Example HTTP handler function for order events
func ExampleOrderEventHandler(webhookSecret string) http.HandlerFunc {
	handler := NewWebhookHandler(webhookSecret)

	return func(w http.ResponseWriter, r *http.Request) {
		// Parse the webhook
		event, err := handler.ParseOrderEvent(r)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to parse webhook: %v", err), http.StatusBadRequest)
			return
		}

		// Process the event
		switch event.Event {
		case OrderEventNew:
			// Handle new order
			order := event.Body.Order
			fmt.Printf("New order received: %s (Display ID: %s)\n", order.ID, order.DisplayID)

			// TODO: Send order to your POS system
			// TODO: Call CreateSyncStatus to confirm receipt

		case OrderEventStatusUpdate:
			// Handle status update
			order := event.Body.Order
			fmt.Printf("Order %s status updated to: %s\n", order.DisplayID, order.Status)

			// TODO: Update order status in your system
		}

		// Return 200 OK to acknowledge receipt
		w.WriteHeader(http.StatusOK)
	}
}

// Example HTTP handler function for rider events
func ExampleRiderEventHandler(webhookSecret string) http.HandlerFunc {
	handler := NewWebhookHandler(webhookSecret)

	return func(w http.ResponseWriter, r *http.Request) {
		// Parse the webhook
		event, err := handler.ParseRiderEvent(r)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to parse webhook: %v", err), http.StatusBadRequest)
			return
		}

		// Process the event
		fmt.Printf("Rider event for order %s\n", event.Body.OrderID)

		for _, rider := range event.Body.Riders {
			// Get latest status
			if len(rider.StatusLog) > 0 {
				latestStatus := rider.StatusLog[len(rider.StatusLog)-1]
				fmt.Printf("Rider status: %s at %s\n", latestStatus.Status, latestStatus.At)

				switch latestStatus.Status {
				case RiderAssigned:
					fmt.Printf("Rider assigned. ETA: %s\n", rider.EstimatedArrivalTime)
				case RiderArrived:
					fmt.Println("Rider has arrived at restaurant")
				case RiderConfirmedAtRestaurant:
					fmt.Println("Rider confirmed arrival and ready to collect")
				case RiderInTransit:
					fmt.Println("Rider is delivering to customer")
				case RiderUnassigned:
					fmt.Println("Rider was unassigned")
				}
			}
		}

		// Return 200 OK to acknowledge receipt
		w.WriteHeader(http.StatusOK)
	}
}

// ParseMenuEvent parses a menu event webhook from an HTTP request
func (h *WebhookHandler) ParseMenuEvent(r *http.Request) (*MenuWebhookEvent, error) {
	// Read headers
	sequenceGUID := r.Header.Get("x-deliveroo-sequence-guid")
	signature := r.Header.Get("x-deliveroo-hmac-sha256")

	if sequenceGUID == "" || signature == "" {
		return nil, fmt.Errorf("missing required webhook headers")
	}

	// Read body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read request body: %w", err)
	}
	defer r.Body.Close()

	// Verify signature
	if !h.VerifySignature(body, sequenceGUID, signature) {
		return nil, fmt.Errorf("invalid webhook signature")
	}

	// Parse the webhook payload
	var webhook MenuWebhookEvent
	if err := json.Unmarshal(body, &webhook); err != nil {
		return nil, fmt.Errorf("failed to parse webhook payload: %w", err)
	}

	return &webhook, nil
}

// Example HTTP handler function for menu events
func ExampleMenuEventHandler(webhookSecret string) http.HandlerFunc {
	handler := NewWebhookHandler(webhookSecret)

	return func(w http.ResponseWriter, r *http.Request) {
		// Parse the webhook
		event, err := handler.ParseMenuEvent(r)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to parse webhook: %v", err), http.StatusBadRequest)
			return
		}

		// Process the event
		result := event.Body.MenuUploadResult

		if result.HTTPStatus == 200 {
			fmt.Printf("✓ Menu upload successful for menu %s (brand: %s)\n",
				result.MenuID, result.BrandID)
			fmt.Printf("  Applied to sites: %v\n", result.SiteIDs)
		} else {
			fmt.Printf("✗ Menu upload failed for menu %s (brand: %s)\n",
				result.MenuID, result.BrandID)
			fmt.Printf("  HTTP Status: %d\n", result.HTTPStatus)

			if len(result.Errors) > 0 {
				fmt.Println("  Errors:")
				for _, err := range result.Errors {
					if err.Field != nil {
						fmt.Printf("    - [%s] %s (field: %s)\n", err.Code, err.Message, *err.Field)
					} else {
						fmt.Printf("    - [%s] %s\n", err.Code, err.Message)
					}
				}
			}
		}

		// Return 200 OK to acknowledge receipt
		w.WriteHeader(http.StatusOK)
	}
}
