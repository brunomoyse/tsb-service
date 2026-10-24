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
	auto_refund_on_cancel, import_images, publish_catalog, publish_images, catalog_language, variant_ref, channel_name,
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
	err := r.inTx(ctx, "connect", func(tx store) error {
		if err := tx.get(ctx, "connect", &c, `
		INSERT INTO hubrise_connection (id, access_token_enc, account_id, account_name, location_id, location_name,
			catalog_id, catalog_name, customer_list_id, customer_list_name, scope, push_orders_since, relink_required_at)
		VALUES (TRUE, $1, NULLIF($2, ''), NULLIF($3, ''), NULLIF($4, ''), NULLIF($5, ''),
			NULLIF($6, ''), NULLIF($7, ''), NULLIF($8, ''), NULLIF($9, ''), NULLIF($10, ''), $11, $12)
		ON CONFLICT (id) DO UPDATE SET
			access_token_enc = EXCLUDED.access_token_enc,
			account_id = EXCLUDED.account_id, account_name = EXCLUDED.account_name,
			location_id = EXCLUDED.location_id, location_name = EXCLUDED.location_name,
			catalog_id = EXCLUDED.catalog_id, catalog_name = EXCLUDED.catalog_name,
			customer_list_id = EXCLUDED.customer_list_id, customer_list_name = EXCLUDED.customer_list_name,
			scope = EXCLUDED.scope,
			push_orders_since = COALESCE(EXCLUDED.push_orders_since, hubrise_connection.push_orders_since),
			enabled = CASE WHEN $12::timestamptz IS NULL THEN hubrise_connection.enabled ELSE FALSE END,
			sync_catalog = CASE WHEN $12::timestamptz IS NULL THEN hubrise_connection.sync_catalog ELSE FALSE END,
			sync_inventory = CASE WHEN $12::timestamptz IS NULL THEN hubrise_connection.sync_inventory ELSE FALSE END,
			relink_required_at = COALESCE($12::timestamptz, hubrise_connection.relink_required_at),
			auth_error_at = NULL, last_error = NULL, updated_at = now()
		RETURNING `+connectionColumns,
			p.AccessTokenEnc, p.AccountID, p.AccountName, p.LocationID, p.LocationName,
			p.CatalogID, p.CatalogName, p.CustomerListID, p.CustomerListName, p.Scope, since, p.RelinkRequiredAt); err != nil {
			return err
		}
		// The publisher follows the connection (amendment A15): a reconnect clears the errors that a new
		// authorization can fix (a missing scope, a revoked token, a deleted catalog, an account catalog),
		// and a connection to another location or catalog starts over with a fresh first publish
		// confirmation and forgets what was published to the old catalog.
		if p.RelinkRequiredAt != nil {
			if err := tx.exec(ctx, "reset publisher on relink", resetPublisherSQL, time.Now()); err != nil {
				return err
			}
			return tx.exec(ctx, "forget published items on relink", `DELETE FROM hubrise_published_items`)
		}
		return tx.exec(ctx, "clear publisher errors on reconnect", `
			UPDATE hubrise_catalog_publish SET blocked_at = NULL, last_error = NULL, last_error_code = NULL, last_error_at = NULL,
				attempts = 0, updated_at = now()
			WHERE last_error_code IN ('WRITE_SCOPE_MISSING', 'AUTH', 'CATALOG_NOT_FOUND', 'ACCOUNT_CATALOG')`)
	})
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
	err := r.inTx(ctx, "update settings", func(tx store) error {
		// The row is locked first: the two catalog directions are mutually exclusive, and the check and
		// the update must not be overtaken by a concurrent change of the other one.
		var cur struct {
			Sync    bool `db:"sync_catalog"`
			Publish bool `db:"publish_catalog"`
		}
		if err := tx.get(ctx, "lock connection", &cur, `SELECT sync_catalog, publish_catalog FROM hubrise_connection FOR UPDATE`); err != nil {
			return err
		}
		sync, publish := cur.Sync, cur.Publish
		if p.SyncCatalog != nil {
			sync = *p.SyncCatalog
		}
		if p.PublishCatalog != nil {
			publish = *p.PublishCatalog
		}
		if sync && publish {
			return domain.ErrDirectionConflict
		}
		return tx.get(ctx, "update settings", &c, `
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
			publish_images = COALESCE($16, publish_images),
			publish_catalog = COALESCE($17, publish_catalog),
			updated_at = now()
		RETURNING `+connectionColumns,
			p.Enabled, p.PushOrders, p.SyncStatus, p.SyncInventory, p.SyncCatalog, p.SyncCustomers,
			p.CatalogAutoImport, p.AutoRefundOnCancel, p.ImportImages, p.CatalogLanguage,
			p.VariantRef, p.ChannelName, p.DeliveryChargeRef, p.PaymentFeeChargeRef, p.PushOrdersSince,
			p.PublishImages, p.PublishCatalog)
	})
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// resetPublisherSQL returns the publisher to "awaiting first publish" at $1.
const resetPublisherSQL = `
	UPDATE hubrise_catalog_publish SET
		first_publish_confirmed_at = NULL, change_seq = change_seq + 1, dirty_since = $1, last_change_at = $1,
		next_attempt_at = NULL, attempts = 0, blocked_at = NULL, external_change_at = NULL,
		last_error = NULL, last_error_code = NULL, last_error_at = NULL,
		last_published_local_hash = NULL, last_published_remote_hash = NULL,
		lease_holder = NULL, leased_until = NULL, updated_at = $1`

// clearPublisherSQL clears the dirty flag and the warnings when the direction leaves PUBLISH ($1 = now).
const clearPublisherSQL = `
	UPDATE hubrise_catalog_publish SET
		dirty_since = NULL, next_attempt_at = NULL, attempts = 0, blocked_at = NULL, external_change_at = NULL,
		last_error = NULL, last_error_code = NULL, last_error_at = NULL,
		last_published_local_hash = NULL, last_published_remote_hash = NULL,
		lease_holder = NULL, leased_until = NULL, updated_at = $1`

func (r *connectionRepository) ChangeCatalogDirection(ctx context.Context, to domain.CatalogDirection, now time.Time, guard domain.DirectionGuard) (*domain.Connection, domain.CatalogDirection, error) {
	if !to.Valid() {
		return nil, "", &domain.InvalidSettingError{Field: "catalogDirection", Message: "unknown direction"}
	}
	var out domain.Connection
	var before domain.CatalogDirection
	err := r.inTx(ctx, "change catalog direction", func(tx store) error {
		var cur domain.Connection
		if err := tx.get(ctx, "lock connection", &cur, `SELECT `+connectionColumns+` FROM hubrise_connection FOR UPDATE`); err != nil {
			return err
		}
		var state domain.PublishState
		if err := tx.get(ctx, "lock publisher", &state, `SELECT `+publishStateColumns+` FROM hubrise_catalog_publish FOR UPDATE`); err != nil {
			return err
		}
		before = cur.CatalogDirection()
		if guard != nil {
			if err := guard(ctx, &cur, &state); err != nil {
				return err
			}
		}
		sync, publish, auto := false, false, "catalog_auto_import"
		switch to {
		case domain.DirectionImport:
			sync = true
		case domain.DirectionPublish:
			publish, auto = true, "FALSE"
		}
		if err := tx.get(ctx, "change catalog direction", &out, `
			UPDATE hubrise_connection SET sync_catalog = $1, publish_catalog = $2,
				catalog_auto_import = `+auto+`,
				catalog_update_available_at = CASE WHEN $2 THEN NULL ELSE catalog_update_available_at END,
				updated_at = now()
			RETURNING `+connectionColumns, sync, publish); err != nil {
			return err
		}
		// Entering or leaving PUBLISH: what was published describes a menu the other direction may
		// since have changed, so the published items go with the hashes.
		var reset string
		switch {
		case to == domain.DirectionPublish && before != domain.DirectionPublish:
			reset = resetPublisherSQL
		case to != domain.DirectionPublish && before == domain.DirectionPublish:
			reset = clearPublisherSQL
		default:
			return nil
		}
		if err := tx.exec(ctx, "reset publisher", reset, now); err != nil {
			return err
		}
		return tx.exec(ctx, "forget published items on direction change", `DELETE FROM hubrise_published_items`)
	})
	if err != nil {
		return nil, "", err
	}
	return &out, before, nil
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

func (r *connectionRepository) ResetInventoryBaseline(ctx context.Context) error {
	return r.mustUpdate(ctx, "reset inventory baseline",
		`UPDATE hubrise_connection SET inventory_baseline_at = NULL, updated_at = now()`)
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
