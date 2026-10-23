package infrastructure

import (
	"context"
	"time"

	"github.com/jmoiron/sqlx"

	"tsb-service/internal/modules/hubrise/domain"
)

// connectionColumns lists every mapped column (the table also has the singleton id, which has no
// field, so SELECT * would not scan).
const connectionColumns = `enabled, access_token_enc, account_id, account_name, location_id, location_name,
	catalog_id, catalog_name, customer_list_id, customer_list_name, scope,
	push_orders, sync_status, sync_inventory, sync_catalog, sync_customers, catalog_auto_import,
	auto_refund_on_cancel, import_images, catalog_language, variant_ref, channel_name,
	delivery_charge_ref, payment_fee_charge_ref, catalog_hash, catalog_preview_hash, catalog_preview_at,
	catalog_update_available_at, push_orders_since, inventory_baseline_at, relink_required_at,
	last_catalog_sync_at, last_event_at, auth_error_at, last_error, callback_registered_at,
	created_at, updated_at`

type connectionRepository struct{ store }

// NewConnectionRepository returns the repository of the single hubrise_connection row.
func NewConnectionRepository(db *sqlx.DB) domain.ConnectionRepository {
	return &connectionRepository{newStore(db)}
}

func (r *connectionRepository) Get(ctx context.Context) (*domain.Connection, error) {
	var c domain.Connection
	if err := r.get(ctx, "get connection", &c, `SELECT `+connectionColumns+` FROM hubrise_connection`); err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *connectionRepository) Connect(ctx context.Context, p domain.ConnectParams) (*domain.Connection, error) {
	var since any
	if !p.PushOrdersSince.IsZero() {
		since = p.PushOrdersSince
	}
	var c domain.Connection
	err := r.get(ctx, "connect", &c, `
		INSERT INTO hubrise_connection (id, access_token_enc, account_id, account_name, location_id, location_name,
			catalog_id, catalog_name, customer_list_id, customer_list_name, scope, push_orders_since)
		VALUES (TRUE, $1, NULLIF($2, ''), NULLIF($3, ''), NULLIF($4, ''), NULLIF($5, ''),
			NULLIF($6, ''), NULLIF($7, ''), NULLIF($8, ''), NULLIF($9, ''), NULLIF($10, ''), $11)
		ON CONFLICT (id) DO UPDATE SET
			access_token_enc = EXCLUDED.access_token_enc,
			account_id = EXCLUDED.account_id, account_name = EXCLUDED.account_name,
			location_id = EXCLUDED.location_id, location_name = EXCLUDED.location_name,
			catalog_id = EXCLUDED.catalog_id, catalog_name = EXCLUDED.catalog_name,
			customer_list_id = EXCLUDED.customer_list_id, customer_list_name = EXCLUDED.customer_list_name,
			scope = EXCLUDED.scope,
			push_orders_since = COALESCE(EXCLUDED.push_orders_since, hubrise_connection.push_orders_since),
			auth_error_at = NULL, last_error = NULL, updated_at = now()
		RETURNING `+connectionColumns,
		p.AccessTokenEnc, p.AccountID, p.AccountName, p.LocationID, p.LocationName,
		p.CatalogID, p.CatalogName, p.CustomerListID, p.CustomerListName, p.Scope, since)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *connectionRepository) Disconnect(ctx context.Context) error {
	return r.mustUpdate(ctx, "disconnect", `
		UPDATE hubrise_connection SET access_token_enc = NULL, enabled = FALSE, callback_registered_at = NULL,
			auth_error_at = NULL, updated_at = now()`)
}

func (r *connectionRepository) UpdateSettings(ctx context.Context, p domain.SettingsPatch) (*domain.Connection, error) {
	var c domain.Connection
	err := r.get(ctx, "update settings", &c, `
		UPDATE hubrise_connection SET
			enabled = COALESCE($1, enabled),
			push_orders = COALESCE($2, push_orders),
			sync_status = COALESCE($3, sync_status),
			sync_inventory = COALESCE($4, sync_inventory),
			sync_catalog = COALESCE($5, sync_catalog),
			sync_customers = COALESCE($6, sync_customers),
			catalog_auto_import = COALESCE($7, catalog_auto_import),
			auto_refund_on_cancel = COALESCE($8, auto_refund_on_cancel),
			import_images = COALESCE($9, import_images),
			catalog_language = COALESCE($10, catalog_language),
			variant_ref = CASE WHEN $11::text IS NULL THEN variant_ref ELSE NULLIF($11::text, '') END,
			channel_name = CASE WHEN $12::text IS NULL THEN channel_name ELSE NULLIF($12::text, '') END,
			delivery_charge_ref = CASE WHEN $13::text IS NULL THEN delivery_charge_ref ELSE NULLIF($13::text, '') END,
			payment_fee_charge_ref = CASE WHEN $14::text IS NULL THEN payment_fee_charge_ref ELSE NULLIF($14::text, '') END,
			push_orders_since = COALESCE($15, push_orders_since),
			updated_at = now()
		RETURNING `+connectionColumns,
		p.Enabled, p.PushOrders, p.SyncStatus, p.SyncInventory, p.SyncCatalog, p.SyncCustomers,
		p.CatalogAutoImport, p.AutoRefundOnCancel, p.ImportImages, p.CatalogLanguage,
		p.VariantRef, p.ChannelName, p.DeliveryChargeRef, p.PaymentFeeChargeRef, p.PushOrdersSince)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *connectionRepository) RecordAuthError(ctx context.Context, at time.Time, message string) error {
	return r.mustUpdate(ctx, "record auth error",
		`UPDATE hubrise_connection SET auth_error_at = $1, last_error = $2, updated_at = now()`, at, message)
}

func (r *connectionRepository) ClearAuthError(ctx context.Context) error {
	return r.mustUpdate(ctx, "clear auth error",
		`UPDATE hubrise_connection SET auth_error_at = NULL, last_error = NULL, updated_at = now()`)
}

func (r *connectionRepository) RecordError(ctx context.Context, message string) error {
	return r.mustUpdate(ctx, "record error",
		`UPDATE hubrise_connection SET last_error = NULLIF($1, ''), updated_at = now()`, message)
}

func (r *connectionRepository) TouchLastEvent(ctx context.Context, at time.Time) error {
	return r.mustUpdate(ctx, "touch last event",
		`UPDATE hubrise_connection SET last_event_at = $1, updated_at = now()`, at)
}

func (r *connectionRepository) SetCallbackRegistered(ctx context.Context, at *time.Time) error {
	return r.mustUpdate(ctx, "set callback registered",
		`UPDATE hubrise_connection SET callback_registered_at = $1, updated_at = now()`, at)
}

func (r *connectionRepository) RecordCatalogSync(ctx context.Context, hash string, at time.Time) error {
	return r.mustUpdate(ctx, "record catalog sync",
		`UPDATE hubrise_connection SET catalog_hash = $1, last_catalog_sync_at = $2,
			catalog_update_available_at = NULL, updated_at = now()`, hash, at)
}

func (r *connectionRepository) RecordCatalogPreview(ctx context.Context, hash string, at time.Time) error {
	return r.mustUpdate(ctx, "record catalog preview",
		`UPDATE hubrise_connection SET catalog_preview_hash = $1, catalog_preview_at = $2, updated_at = now()`, hash, at)
}

func (r *connectionRepository) SetCatalogUpdateAvailable(ctx context.Context, at *time.Time) error {
	return r.mustUpdate(ctx, "set catalog update available",
		`UPDATE hubrise_connection SET catalog_update_available_at = $1, updated_at = now()`, at)
}

func (r *connectionRepository) SetInventoryBaseline(ctx context.Context, at time.Time) error {
	return r.mustUpdate(ctx, "set inventory baseline",
		`UPDATE hubrise_connection SET inventory_baseline_at = $1, updated_at = now()`, at)
}

func (r *connectionRepository) SetRelinkRequired(ctx context.Context, at *time.Time) error {
	return r.mustUpdate(ctx, "set relink required",
		`UPDATE hubrise_connection SET relink_required_at = $1, updated_at = now()`, at)
}

// mustUpdate runs an UPDATE of the single row and returns ErrNotFound when there is none.
func (r *connectionRepository) mustUpdate(ctx context.Context, what, query string, args ...any) error {
	ok, err := r.affected(ctx, what, query, args...)
	if err != nil {
		return err
	}
	if !ok {
		return domain.ErrNotFound
	}
	return nil
}
