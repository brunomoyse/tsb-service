package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Connection represents the stored OAuth connection for a single
// HubRise client (e.g. the "tsb-webshop" client).
type Connection struct {
	ID             uuid.UUID `db:"id"`
	ClientName     string    `db:"client_name"`
	LocationID     string    `db:"location_id"`
	AccountID      string    `db:"account_id"`
	CatalogID      *string   `db:"catalog_id"`
	CustomerListID *string   `db:"customer_list_id"`
	AccessToken    string    `db:"access_token"`
	Scope          string    `db:"scope"`
	CreatedAt      time.Time `db:"created_at"`
	UpdatedAt      time.Time `db:"updated_at"`
}

// ConnectionRepository persists HubRise OAuth connections.
type ConnectionRepository interface {
	// GetByClient returns the connection for a given client name
	// (e.g. "tsb-webshop"), or nil if not configured yet.
	GetByClient(ctx context.Context, clientName string) (*Connection, error)
	// Upsert inserts or updates a connection by client name.
	Upsert(ctx context.Context, c *Connection) error
	// Delete removes a stored connection.
	Delete(ctx context.Context, clientName string) error
}

// CatalogSyncState tracks the last successful catalog push per client.
type CatalogSyncState struct {
	ClientName        string     `db:"client_name"`
	LastPushedVersion *int64     `db:"last_pushed_version"`
	LastPushedAt      *time.Time `db:"last_pushed_at"`
	LastPushStatus    *string    `db:"last_push_status"`
	LastError         *string    `db:"last_error"`
}

// CatalogSyncStateRepository persists the per-client catalog push state.
type CatalogSyncStateRepository interface {
	Get(ctx context.Context, clientName string) (*CatalogSyncState, error)
	Upsert(ctx context.Context, s *CatalogSyncState) error
}

// WebhookEventRepository provides idempotent storage for callbacks
// received from HubRise.
type WebhookEventRepository interface {
	// Insert stores an event. Returns true if the event was newly
	// inserted, false if it already existed (idempotence).
	Insert(ctx context.Context, id, clientName, resourceType, eventType string, payload []byte) (bool, error)
	// MarkProcessed updates processed_at on a given event.
	MarkProcessed(ctx context.Context, id string) error
	// MarkFailed updates error_msg on a given event.
	MarkFailed(ctx context.Context, id, errMsg string) error
}
