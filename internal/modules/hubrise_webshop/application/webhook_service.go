package application

import (
	"context"
	"encoding/json"

	"go.uber.org/zap"

	"tsb-service/internal/modules/hubrise_webshop/domain"
	"tsb-service/pkg/logging"
	"tsb-service/pkg/pubsub"
)

// WebhookService processes incoming HubRise callback events.
type WebhookService struct {
	eventRepo domain.WebhookEventRepository
	broker    *pubsub.Broker
}

// NewWebhookService constructs a webhook service.
func NewWebhookService(eventRepo domain.WebhookEventRepository, broker *pubsub.Broker) *WebhookService {
	return &WebhookService{eventRepo: eventRepo, broker: broker}
}

// Event is the shape of a single HubRise callback event.
type Event struct {
	ID            string          `json:"id"`
	ResourceType  string          `json:"resource_type"`
	EventType     string          `json:"event_type"`
	CreatedAt     string          `json:"created_at"`
	NewState      json.RawMessage `json:"new_state,omitempty"`
	PreviousState json.RawMessage `json:"previous_state,omitempty"`
}

// Process stores the event idempotently and dispatches it to domain
// handlers. Returns nil if the event was a duplicate (already stored).
func (s *WebhookService) Process(ctx context.Context, raw []byte) error {
	var ev Event
	if err := json.Unmarshal(raw, &ev); err != nil {
		return err
	}

	isNew, err := s.eventRepo.Insert(ctx, ev.ID, ClientName, ev.ResourceType, ev.EventType, raw)
	if err != nil {
		return err
	}
	if !isNew {
		return nil
	}

	logger := logging.FromContext(ctx).With(
		zap.String("event_id", ev.ID),
		zap.String("resource", ev.ResourceType),
		zap.String("type", ev.EventType),
	)

	switch {
	case ev.ResourceType == "order" && (ev.EventType == "update" || ev.EventType == "create"):
		// Publish a generic notification to the in-memory broker — the
		// order resolver / subscription layer can then propagate this
		// to GraphQL subscribers.
		if s.broker != nil {
			s.broker.Publish("hubriseOrderEvent", raw)
		}
	case ev.ResourceType == "catalog":
		logger.Debug("received catalog event (no-op on webshop side)")
	default:
		logger.Debug("ignoring unhandled event")
	}

	if err := s.eventRepo.MarkProcessed(ctx, ev.ID); err != nil {
		return err
	}
	return nil
}
