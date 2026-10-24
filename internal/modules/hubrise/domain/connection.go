package domain

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Catalog languages the importer can read HubRise names in (the languages of the shop).
const (
	LanguageFR = "fr"
	LanguageEN = "en"
	LanguageNL = "nl"
	LanguageZH = "zh"
)

// Default charge refs sent with an order when the settings name none.
const (
	DefaultDeliveryChargeRef   = "DELIVERY"
	DefaultPaymentFeeChargeRef = "PAYMENT_FEE"
)

// Connection is the single hubrise_connection row: the link of this installation to one HubRise
// location, with its runtime switches. Absence of the row, or an empty AccessTokenEnc, means
// standalone mode.
type Connection struct {
	Enabled bool `db:"enabled"`
	// AccessTokenEnc is the sealed access token (see TokenCodec); nil when disconnected. It never
	// leaves the backend: no GraphQL field and no log carries it.
	AccessTokenEnc []byte `db:"access_token_enc" json:"-"`

	AccountID        *string `db:"account_id"`
	AccountName      *string `db:"account_name"`
	LocationID       *string `db:"location_id"`
	LocationName     *string `db:"location_name"`
	CatalogID        *string `db:"catalog_id"`
	CatalogName      *string `db:"catalog_name"`
	CustomerListID   *string `db:"customer_list_id"`
	CustomerListName *string `db:"customer_list_name"`
	Scope            *string `db:"scope"`

	PushOrders         bool `db:"push_orders"`
	SyncStatus         bool `db:"sync_status"`
	SyncInventory      bool `db:"sync_inventory"`
	SyncCatalog        bool `db:"sync_catalog"`
	SyncCustomers      bool `db:"sync_customers"`
	CatalogAutoImport  bool `db:"catalog_auto_import"`
	AutoRefundOnCancel bool `db:"auto_refund_on_cancel"`
	ImportImages       bool `db:"import_images"`
	// PublishCatalog is the catalog direction PUBLISH: the dashboard owns the menu and every edit is
	// published to HubRise. It is never true together with SyncCatalog.
	PublishCatalog bool `db:"publish_catalog"`
	// PublishImages uploads the product images with the published catalog.
	PublishImages bool `db:"publish_images"`

	CatalogLanguage     string  `db:"catalog_language"`
	VariantRef          *string `db:"variant_ref"`
	ChannelName         *string `db:"channel_name"`
	DeliveryChargeRef   *string `db:"delivery_charge_ref"`
	PaymentFeeChargeRef *string `db:"payment_fee_charge_ref"`

	CatalogHash              *string    `db:"catalog_hash"`
	CatalogPreviewHash       *string    `db:"catalog_preview_hash"`
	CatalogPreviewAt         *time.Time `db:"catalog_preview_at"`
	CatalogUpdateAvailableAt *time.Time `db:"catalog_update_available_at"`
	PushOrdersSince          *time.Time `db:"push_orders_since"`
	InventoryBaselineAt      *time.Time `db:"inventory_baseline_at"`
	RelinkRequiredAt         *time.Time `db:"relink_required_at"`
	LastCatalogSyncAt        *time.Time `db:"last_catalog_sync_at"`
	LastEventAt              *time.Time `db:"last_event_at"`
	AuthErrorAt              *time.Time `db:"auth_error_at"`
	LastError                *string    `db:"last_error"`
	CallbackRegisteredAt     *time.Time `db:"callback_registered_at"`

	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

// CatalogDirection says who owns the menu.
type CatalogDirection string

const (
	// DirectionNone: nobody does; links only.
	DirectionNone CatalogDirection = "NONE"
	// DirectionImport: HubRise owns the menu (sync_catalog).
	DirectionImport CatalogDirection = "IMPORT"
	// DirectionPublish: the dashboard owns the menu (publish_catalog).
	DirectionPublish CatalogDirection = "PUBLISH"
)

// Valid reports whether d is one of the three directions.
func (d CatalogDirection) Valid() bool {
	return d == DirectionNone || d == DirectionImport || d == DirectionPublish
}

// CatalogDirection derives the direction from the two switches. Both set is impossible (one UPDATE
// keeps them apart); if it ever happened PUBLISH wins, which writes nothing the shop did not
// decide.
func (c *Connection) CatalogDirection() CatalogDirection {
	switch {
	case c == nil:
		return DirectionNone
	case c.PublishCatalog:
		return DirectionPublish
	case c.SyncCatalog:
		return DirectionImport
	}
	return DirectionNone
}

// HasWriteScope reports whether the scope stored at the last authorization grants catalog.write
// (inside location[...] or account[...], or through the catalog.* wildcard). Connections made
// before the dashboard-owned menu existed do not have it and must reconnect before they can
// publish.
func (c *Connection) HasWriteScope() bool {
	return c != nil && c.Scope != nil && HasWriteScope(*c.Scope)
}

// catalogWildcard is the scope entry that grants every catalog permission.
const catalogWildcard = "catalog." + "*"

// HasWriteScope reports whether a scope string grants catalog.write.
func HasWriteScope(scope string) bool {
	inner := scope
	if open := strings.Index(scope, "["); open >= 0 {
		inner = scope[open+1:]
		if end := strings.LastIndex(inner, "]"); end >= 0 {
			inner = inner[:end]
		}
	}
	for _, p := range strings.FieldsFunc(inner, func(r rune) bool { return r == ',' || r == ' ' }) {
		if p == "catalog.write" || p == catalogWildcard {
			return true
		}
	}
	return false
}

// Connected reports whether an access token is stored.
func (c *Connection) Connected() bool { return c != nil && len(c.AccessTokenEnc) > 0 }

// Active reports whether the integration may talk to HubRise right now: connected, switched on
// and not stopped by an authorization error or a pending relink.
func (c *Connection) Active() bool {
	return c.Connected() && c.Enabled && c.AuthErrorAt == nil && c.RelinkRequiredAt == nil
}

// AccessToken opens the stored token. It is meant to be called only when building the API client.
// A token that cannot be opened is reported as ErrTokenUnreadable; the codec's own error is
// attached, and never carries the token.
func (c *Connection) AccessToken(codec TokenCodec) (Redacted, error) {
	if !c.Connected() {
		return "", ErrNotConnected
	}
	plain, err := codec.Decrypt(c.AccessTokenEnc)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrTokenUnreadable, err)
	}
	return Redacted(plain), nil
}

// DeliveryChargeRefOrDefault is the ref of the delivery fee charge.
func (c *Connection) DeliveryChargeRefOrDefault() string {
	return refOrDefault(c.DeliveryChargeRef, DefaultDeliveryChargeRef)
}

// PaymentFeeChargeRefOrDefault is the ref of the online payment fee charge.
func (c *Connection) PaymentFeeChargeRefOrDefault() string {
	return refOrDefault(c.PaymentFeeChargeRef, DefaultPaymentFeeChargeRef)
}

func refOrDefault(ref *string, def string) string {
	if ref == nil || *ref == "" {
		return def
	}
	return *ref
}

// ConnectParams is what the OAuth callback learns from the token response.
type ConnectParams struct {
	AccessTokenEnc   []byte
	AccountID        string
	AccountName      string
	LocationID       string
	LocationName     string
	CatalogID        string
	CatalogName      string
	CustomerListID   string
	CustomerListName string
	Scope            string
	// PushOrdersSince stamps the moment order pushing becomes effective (see Connection).
	PushOrdersSince time.Time
	// RelinkRequiredAt, when set, is stored in the same statement as the new ids and switches the
	// integration and the catalog and inventory sync off: the connection names another location or
	// catalog than the existing links were made for, so nothing may be pushed with them even for a
	// moment. Nil keeps the flag as it is.
	RelinkRequiredAt *time.Time
}

// SettingsPatch changes settings; nil fields stay as they are. For the text options an empty
// string clears the value.
type SettingsPatch struct {
	Enabled            *bool
	PushOrders         *bool
	SyncStatus         *bool
	SyncInventory      *bool
	SyncCatalog        *bool
	SyncCustomers      *bool
	CatalogAutoImport  *bool
	AutoRefundOnCancel *bool
	ImportImages       *bool
	// PublishImages uploads product images with the published catalog. PublishCatalog is the
	// catalog direction, changed through ConnectionRepository.ChangeCatalogDirection only; UpdateSettings
	// refuses to turn SyncCatalog and PublishCatalog on together.
	PublishImages  *bool
	PublishCatalog *bool

	CatalogLanguage     *string
	VariantRef          *string
	ChannelName         *string
	DeliveryChargeRef   *string
	PaymentFeeChargeRef *string

	// PushOrdersSince, when set, replaces the push_orders_since stamp.
	PushOrdersSince *time.Time
}

// ConnectionRepository stores the single connection row.
type ConnectionRepository interface {
	// Get returns the row, or ErrNotFound when the installation never connected.
	Get(ctx context.Context) (*Connection, error)
	// Connect creates the row or re-authorizes it (new token and ids, auth error and relink flag
	// cleared). The settings of an existing row are kept, except that a RelinkRequiredAt in p
	// stamps the flag and switches the integration off atomically with the new ids.
	Connect(ctx context.Context, p ConnectParams) (*Connection, error)
	// Disconnect removes the token and switches the integration off; the row, its settings and
	// ids stay. It returns ErrNotFound when there is no row.
	Disconnect(ctx context.Context) error
	// UpdateSettings applies the patch and returns the new row (ErrNotFound without a row). It reads
	// the row FOR UPDATE and returns ErrDirectionConflict instead of leaving sync_catalog and
	// publish_catalog both on.
	UpdateSettings(ctx context.Context, p SettingsPatch) (*Connection, error)
	// ChangeCatalogDirection switches the catalog direction atomically: it locks the connection and
	// the publisher row, calls guard with both (an error from it rolls everything back), and then
	// sets sync_catalog, catalog_auto_import, publish_catalog and the publisher row in one
	// transaction. Moving to PUBLISH clears the import flags and resets the publisher to "awaiting
	// first publish"; leaving it clears the publisher's dirty flag and warnings. It returns the new
	// row and the direction it had before.
	ChangeCatalogDirection(ctx context.Context, to CatalogDirection, now time.Time, guard DirectionGuard) (*Connection, CatalogDirection, error)

	// RecordAuthError pauses the pipeline: auth_error_at and last_error.
	RecordAuthError(ctx context.Context, at time.Time, message string) error
	// ClearAuthError lifts the pause.
	ClearAuthError(ctx context.Context) error
	// RecordError stores a non-fatal error message (empty clears it).
	RecordError(ctx context.Context, message string) error
	// TouchLastEvent stamps the receipt of a callback event.
	TouchLastEvent(ctx context.Context, at time.Time) error
	// SetCallbackRegistered stamps (or with nil clears) the registration of our callback.
	SetCallbackRegistered(ctx context.Context, at *time.Time) error
	// RecordCatalogSync stores the hash of the catalog that was just imported.
	RecordCatalogSync(ctx context.Context, hash string, at time.Time) error
	// RecordCatalogPreview stores the hash of a successful dry run.
	RecordCatalogPreview(ctx context.Context, hash string, at time.Time) error
	// SetCatalogUpdateAvailable stamps (or with nil clears) "HubRise has a newer catalog".
	SetCatalogUpdateAvailable(ctx context.Context, at *time.Time) error
	// SetInventoryBaseline stamps the end of the first-enable inventory baseline.
	SetInventoryBaseline(ctx context.Context, at time.Time) error
	// ResetInventoryBaseline forgets the baseline, so the next sync pushes the shop's sold-out
	// products again (inventory sync was switched off while the shop kept toggling availability).
	ResetInventoryBaseline(ctx context.Context) error
	// SetRelinkRequired stamps (or with nil clears) the "re-authorized against another
	// location" flag.
	SetRelinkRequired(ctx context.Context, at *time.Time) error
}

// OAuthStateRepository stores the one-time OAuth state nonces.
type OAuthStateRepository interface {
	Create(ctx context.Context, state string, adminUserID uuid.UUID, expiresAt time.Time) error
	// Consume deletes the state and returns the admin who started it; ok is false when the state
	// is unknown, already used or expired.
	Consume(ctx context.Context, state string, now time.Time) (adminUserID uuid.UUID, ok bool, err error)
	// PurgeExpired deletes expired states and returns how many.
	PurgeExpired(ctx context.Context, now time.Time) (int64, error)
}

// LeaseRepository arbitrates the singleton background jobs between replicas.
type LeaseRepository interface {
	// Acquire takes the lease when it is free, expired or already ours (a renewal).
	Acquire(ctx context.Context, name, holder string, now time.Time, ttl time.Duration) (bool, error)
	// Release gives the lease back if holder still owns it.
	Release(ctx context.Context, name, holder string) error
}

// DirectionGuard runs inside ChangeCatalogDirection with the connection and the publisher row
// locked, so a check made there cannot be overtaken by a concurrent change.
type DirectionGuard func(ctx context.Context, conn *Connection, state *PublishState) error
