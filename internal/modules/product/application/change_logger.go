package application

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"go.uber.org/zap"

	"tsb-service/pkg/logging"
)

// CatalogPushTrigger is the interface the hubrise_webshop module
// implements to be notified when the menu catalog changes. We keep it
// in the product module so the product service can depend on an
// interface, not on the concrete hubrise_webshop module (dependency
// inversion — product does not import hubrise_webshop).
type CatalogPushTrigger interface {
	// TriggerPush schedules an async catalog push after a menu mutation
	// has been committed. Implementations are expected to debounce.
	TriggerPush(ctx context.Context)
}

// NoopCatalogPushTrigger is the default when HubRise integration is
// disabled or not configured yet.
type NoopCatalogPushTrigger struct{}

func (NoopCatalogPushTrigger) TriggerPush(_ context.Context) {}

// MenuChangeOperation classifies a menu change entry.
type MenuChangeOperation string

const (
	MenuChangeCreate MenuChangeOperation = "create"
	MenuChangeUpdate MenuChangeOperation = "update"
	MenuChangeDelete MenuChangeOperation = "delete"
)

// MenuEntityType identifies the entity a change row describes.
type MenuEntityType string

const (
	MenuEntityProduct                    MenuEntityType = "product"
	MenuEntityCategory                   MenuEntityType = "product_category"
	MenuEntityChoice                     MenuEntityType = "product_choice"
	MenuEntityProductTranslation         MenuEntityType = "product_translation"
	MenuEntityCategoryTranslation        MenuEntityType = "product_category_translation"
	MenuEntityProductChoiceTranslation   MenuEntityType = "product_choice_translation"
)

// MenuChangeLogger records mutations to the menu catalog and bumps
// the menu_catalog_version_seq Postgres sequence in the same transaction.
//
// Call LogChange inside the same sqlx.Tx used for the data mutation.
// After the transaction commits successfully, the caller should also
// call Trigger.TriggerPush to schedule an async HubRise catalog push.
type MenuChangeLogger struct {
	Trigger CatalogPushTrigger
}

// NewMenuChangeLogger returns a logger wired with the given trigger.
func NewMenuChangeLogger(trigger CatalogPushTrigger) *MenuChangeLogger {
	if trigger == nil {
		trigger = NoopCatalogPushTrigger{}
	}
	return &MenuChangeLogger{Trigger: trigger}
}

// LogChange inserts a menu_change_log row within the provided
// transaction, obtains a new catalog version via the Postgres sequence,
// and returns that version. The caller is responsible for committing
// the transaction.
func (l *MenuChangeLogger) LogChange(
	ctx context.Context,
	tx *sqlx.Tx,
	entityType MenuEntityType,
	entityID uuid.UUID,
	operation MenuChangeOperation,
	before any,
	after any,
) (int64, error) {
	var version int64
	if err := tx.QueryRowxContext(ctx, "SELECT nextval('menu_catalog_version_seq')").Scan(&version); err != nil {
		return 0, err
	}

	beforeJSON, err := marshalNullable(before)
	if err != nil {
		return 0, err
	}
	afterJSON, err := marshalNullable(after)
	if err != nil {
		return 0, err
	}

	changedBy := sql.NullString{}
	if uid := ctxUserID(ctx); uid != "" {
		changedBy.String = uid
		changedBy.Valid = true
	}

	const insertSQL = `
		INSERT INTO menu_change_log (changed_by, entity_type, entity_id, operation, before_json, after_json, catalog_version)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`
	if _, err := tx.ExecContext(ctx, insertSQL,
		nullStringOrNil(changedBy),
		string(entityType),
		entityID.String(),
		string(operation),
		beforeJSON,
		afterJSON,
		version,
	); err != nil {
		return 0, err
	}

	return version, nil
}

// AfterCommit notifies the push trigger and logs any error. Call after
// successful Commit() of the transaction where LogChange was invoked.
func (l *MenuChangeLogger) AfterCommit(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			logging.FromContext(ctx).Error("catalog push trigger panicked", zap.Any("panic", r))
		}
	}()
	if l.Trigger != nil {
		l.Trigger.TriggerPush(ctx)
	}
}

func marshalNullable(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	return json.Marshal(v)
}

func nullStringOrNil(s sql.NullString) any {
	if !s.Valid {
		return nil
	}
	return s.String
}

// ctxUserID extracts the current user id from context if present.
// We use a best-effort lookup: if no user (e.g. background job) the
// changed_by column stays NULL.
func ctxUserID(ctx context.Context) string {
	if v := ctx.Value("userID"); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
