package infrastructure

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/shopspring/decimal"

	"tsb-service/internal/modules/hubrise/domain"
)

const catalogLinkColumns = `id, entity_type, category_id, product_id, choice_group_id, choice_id, hubrise_key,
	hubrise_ref, hubrise_id, content_hash, origin, stock, inventory_sold_out, sold_out_until, catalog_disabled,
	inventory_synced_at, image_md5, retired_at, created_at, updated_at`

type catalogLinkRepository struct{ store }

// NewCatalogLinkRepository returns the store of HubRise catalog links.
func NewCatalogLinkRepository(db *sqlx.DB) domain.CatalogLinkRepository {
	return &catalogLinkRepository{newStore(db)}
}

// NewCatalogLinkRepositoryTx returns the same repository bound to a transaction, for the import
// batches that write links together with the menu rows.
func NewCatalogLinkRepositoryTx(tx *sqlx.Tx) domain.CatalogLinkRepository {
	return &catalogLinkRepository{newTxStore(tx)}
}

func (r *catalogLinkRepository) Upsert(ctx context.Context, l *domain.CatalogLink) error {
	if err := l.Validate(); err != nil {
		return err
	}
	var out domain.CatalogLink
	err := r.get(ctx, "upsert catalog link", &out, `
		INSERT INTO hubrise_catalog_links (entity_type, category_id, product_id, choice_group_id, choice_id,
			hubrise_key, hubrise_ref, hubrise_id, content_hash, origin, image_md5, retired_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (entity_type, hubrise_key) DO UPDATE SET
			category_id = EXCLUDED.category_id, product_id = EXCLUDED.product_id,
			choice_group_id = EXCLUDED.choice_group_id, choice_id = EXCLUDED.choice_id,
			hubrise_ref = EXCLUDED.hubrise_ref, hubrise_id = EXCLUDED.hubrise_id,
			content_hash = EXCLUDED.content_hash, origin = EXCLUDED.origin,
			image_md5 = EXCLUDED.image_md5, retired_at = EXCLUDED.retired_at, updated_at = now()
		RETURNING `+catalogLinkColumns,
		l.EntityType, l.CategoryID, l.ProductID, l.ChoiceGroupID, l.ChoiceID,
		l.HubriseKey, l.HubriseRef, l.HubriseID, l.ContentHash, l.Origin, l.ImageMD5, l.RetiredAt)
	if err != nil {
		return err
	}
	*l = out
	return nil
}

func (r *catalogLinkRepository) FindByKey(ctx context.Context, t domain.EntityType, key string) (*domain.CatalogLink, error) {
	var l domain.CatalogLink
	if err := r.get(ctx, "find catalog link by key", &l,
		`SELECT `+catalogLinkColumns+` FROM hubrise_catalog_links WHERE entity_type = $1 AND hubrise_key = $2`, t, key); err != nil {
		return nil, err
	}
	return &l, nil
}

// localColumns maps an entity type to the column holding its local id. The names are constants,
// never user input.
var localColumns = map[domain.EntityType]string{
	domain.EntityCategory:    "category_id",
	domain.EntityProduct:     "product_id",
	domain.EntityChoiceGroup: "choice_group_id",
	domain.EntityChoice:      "choice_id",
}

func (r *catalogLinkRepository) FindByLocal(ctx context.Context, t domain.EntityType, localID uuid.UUID) (*domain.CatalogLink, error) {
	col, ok := localColumns[t]
	if !ok {
		return nil, domain.ErrInvalidLink
	}
	var l domain.CatalogLink
	if err := r.get(ctx, "find catalog link by local row", &l,
		`SELECT `+catalogLinkColumns+` FROM hubrise_catalog_links WHERE entity_type = $1 AND `+col+` = $2`, t, localID); err != nil {
		return nil, err
	}
	return &l, nil
}

func (r *catalogLinkRepository) ListByType(ctx context.Context, t domain.EntityType, includeRetired bool) ([]domain.CatalogLink, error) {
	links := []domain.CatalogLink{}
	err := r.sel(ctx, "list catalog links", &links,
		`SELECT `+catalogLinkColumns+` FROM hubrise_catalog_links
		 WHERE entity_type = $1 AND ($2 OR retired_at IS NULL) ORDER BY id`, t, includeRetired)
	return links, err
}

func (r *catalogLinkRepository) ListByRef(ctx context.Context, t domain.EntityType, ref string) ([]domain.CatalogLink, error) {
	links := []domain.CatalogLink{}
	err := r.sel(ctx, "list catalog links by ref", &links,
		`SELECT `+catalogLinkColumns+` FROM hubrise_catalog_links WHERE entity_type = $1 AND hubrise_ref = $2 ORDER BY id`, t, ref)
	return links, err
}

func (r *catalogLinkRepository) Count(ctx context.Context, includeRetired bool) (int, error) {
	var n int
	err := r.get(ctx, "count catalog links", &n,
		`SELECT count(*) FROM hubrise_catalog_links WHERE $1 OR retired_at IS NULL`, includeRetired)
	return n, err
}

func (r *catalogLinkRepository) SetRetired(ctx context.Context, id int64, at *time.Time) error {
	return r.mustChange(ctx, "set catalog link retired",
		`UPDATE hubrise_catalog_links SET retired_at = $2, updated_at = now() WHERE id = $1`, id, at)
}

func (r *catalogLinkRepository) SetCatalogDisabled(ctx context.Context, id int64, disabled bool) error {
	return r.mustChange(ctx, "set catalog link disabled",
		`UPDATE hubrise_catalog_links SET catalog_disabled = $2, updated_at = now() WHERE id = $1`, id, disabled)
}

func (r *catalogLinkRepository) SetImageMD5(ctx context.Context, id int64, md5 string) error {
	return r.mustChange(ctx, "set catalog link image md5",
		`UPDATE hubrise_catalog_links SET image_md5 = $2, updated_at = now() WHERE id = $1`, id, md5)
}

func (r *catalogLinkRepository) UpdateInventory(ctx context.Context, id int64, s domain.InventoryState) error {
	return r.mustChange(ctx, "update catalog link inventory", `
		UPDATE hubrise_catalog_links SET stock = $2, inventory_sold_out = $3, sold_out_until = $4,
			inventory_synced_at = $5, updated_at = now() WHERE id = $1`,
		id, s.Stock, s.SoldOut, s.SoldOutUntil, s.SyncedAt)
}

func (r *catalogLinkRepository) RecordStock(ctx context.Context, id int64, stock *decimal.Decimal, soldOutUntil *time.Time, at time.Time) error {
	return r.mustChange(ctx, "record catalog link stock", `
		UPDATE hubrise_catalog_links SET stock = $2, sold_out_until = $3, inventory_synced_at = $4, updated_at = now()
		WHERE id = $1`, id, stock, soldOutUntil, at)
}

func (r *catalogLinkRepository) ListSoldOutExpired(ctx context.Context, now time.Time) ([]domain.CatalogLink, error) {
	links := []domain.CatalogLink{}
	err := r.sel(ctx, "list expired sold out links", &links, `SELECT `+catalogLinkColumns+` FROM hubrise_catalog_links
		WHERE inventory_sold_out AND retired_at IS NULL AND sold_out_until IS NOT NULL AND sold_out_until <= $1
		ORDER BY id`, now)
	return links, err
}

func (r *catalogLinkRepository) ClearSoldOut(ctx context.Context, id int64) error {
	return r.mustChange(ctx, "clear catalog link sold out",
		`UPDATE hubrise_catalog_links SET inventory_sold_out = FALSE, sold_out_until = NULL, updated_at = now() WHERE id = $1`, id)
}

func (r *catalogLinkRepository) Rekey(ctx context.Context, id int64, key, ref string) error {
	if key == "" || ref == "" {
		return domain.ErrInvalidLink
	}
	return r.mustChange(ctx, "rekey catalog link",
		`UPDATE hubrise_catalog_links SET hubrise_key = $2, hubrise_ref = $3, updated_at = now() WHERE id = $1`, id, key, ref)
}

func (r *catalogLinkRepository) SetHubriseID(ctx context.Context, t domain.EntityType, key, hubriseID string) error {
	return r.exec(ctx, "set catalog link hubrise id", `
		UPDATE hubrise_catalog_links SET hubrise_id = NULLIF($3, ''), updated_at = now()
		WHERE entity_type = $1 AND hubrise_key = $2 AND hubrise_id IS DISTINCT FROM NULLIF($3, '')`, t, key, hubriseID)
}

func (r *catalogLinkRepository) Delete(ctx context.Context, id int64) error {
	return r.mustChange(ctx, "delete catalog link", `DELETE FROM hubrise_catalog_links WHERE id = $1`, id)
}

func (r *catalogLinkRepository) DeleteAll(ctx context.Context) (int64, error) {
	return r.execN(ctx, "delete all catalog links", `DELETE FROM hubrise_catalog_links`)
}

func (r *catalogLinkRepository) LinkedProductIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	out := map[uuid.UUID]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	var linked []uuid.UUID
	if err := r.sel(ctx, "list linked products", &linked, `
		SELECT product_id FROM hubrise_catalog_links
		WHERE entity_type = 'product' AND retired_at IS NULL AND product_id = ANY($1)`, pq.Array(ids)); err != nil {
		return nil, err
	}
	for _, id := range linked {
		out[id] = true
	}
	return out, nil
}

func (r *catalogLinkRepository) CountUnlinkedProducts(ctx context.Context) (int, error) {
	var n int
	err := r.get(ctx, "count unlinked products", &n, `
		SELECT count(*) FROM products p
		WHERE p.is_visible AND NOT EXISTS (
			SELECT 1 FROM hubrise_catalog_links l
			WHERE l.entity_type = 'product' AND l.product_id = p.id AND l.retired_at IS NULL)`)
	return n, err
}

// mustChange runs a statement that must hit one link, and returns ErrNotFound otherwise.
func (r *catalogLinkRepository) mustChange(ctx context.Context, what, query string, args ...any) error {
	ok, err := r.affected(ctx, what, query, args...)
	if err != nil {
		return err
	}
	if !ok {
		return domain.ErrNotFound
	}
	return nil
}
