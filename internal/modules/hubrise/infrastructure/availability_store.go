package infrastructure

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"tsb-service/internal/modules/hubrise/domain"
)

type availabilityStore struct{ store }

// NewAvailabilityStore returns the store that switches the availability of products and choices on
// behalf of the inventory sync.
func NewAvailabilityStore(db *sqlx.DB) domain.AvailabilityStore {
	return &availabilityStore{newStore(db)}
}

// availabilityTargets maps an entity type to the table that holds its availability. The names are
// constants, never user input. A choice has no updated_at column.
var availabilityTargets = map[domain.EntityType]struct{ table, productColumn, touched string }{
	domain.EntityProduct: {"products", "id", ", updated_at = now()"},
	domain.EntityChoice:  {"product_choices", "product_id", ""},
}

func (s *availabilityStore) Availability(ctx context.Context, t domain.EntityType, ids []uuid.UUID) (map[uuid.UUID]domain.Availability, error) {
	var query string
	switch t {
	case domain.EntityProduct:
		query = `SELECT id, id AS product_id, is_available FROM products WHERE id = ANY($1)`
	case domain.EntityChoice:
		query = `SELECT id, product_id, is_available FROM product_choices WHERE id = ANY($1)`
	default:
		return nil, domain.ErrInvalidLink
	}
	strs := make([]string, len(ids))
	for i, id := range ids {
		strs[i] = id.String()
	}
	var rows []struct {
		ID        uuid.UUID `db:"id"`
		ProductID uuid.UUID `db:"product_id"`
		Available bool      `db:"is_available"`
	}
	if err := s.sel(ctx, "read availability", &rows, query, pq.Array(strs)); err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]domain.Availability, len(rows))
	for _, r := range rows {
		out[r.ID] = domain.Availability{Available: r.Available, ProductID: r.ProductID}
	}
	return out, nil
}

func (s *availabilityStore) TakeOver(ctx context.Context, link *domain.CatalogLink, st domain.InventoryState) (domain.InventoryChange, error) {
	return s.switchLink(ctx, "take over sold out", link, st, `
		UPDATE hubrise_catalog_links SET inventory_sold_out = TRUE, sold_out_until = $2, stock = $3,
			inventory_synced_at = $4, updated_at = now()
		WHERE id = $1 AND NOT inventory_sold_out RETURNING catalog_disabled`, false)
}

func (s *availabilityStore) Release(ctx context.Context, link *domain.CatalogLink, st domain.InventoryState) (domain.InventoryChange, error) {
	return s.switchLink(ctx, "release sold out", link, st, `
		UPDATE hubrise_catalog_links SET inventory_sold_out = FALSE, sold_out_until = $2, stock = $3,
			inventory_synced_at = $4, updated_at = now()
		WHERE id = $1 AND inventory_sold_out RETURNING catalog_disabled`, true)
}

// switchLink flips the provenance flag with a compare-and-set statement (flip, whose $1 is the link,
// $2 the expiry, $3 the stock and $4 the sync time, and which returns the link's catalog_disabled)
// and, when it won, the availability of the local row, in one transaction. A released row becomes
// available unless HubRise restrictions disable its entry; a taken over one becomes unavailable,
// unless staff sold the row out meanwhile (the take-over is then refused).
func (s *availabilityStore) switchLink(ctx context.Context, what string, link *domain.CatalogLink, st domain.InventoryState, flip string, release bool) (domain.InventoryChange, error) {
	target, ok := availabilityTargets[link.EntityType]
	if !ok {
		return domain.InventoryChange{}, domain.ErrInvalidLink
	}
	fail := func(err error) (domain.InventoryChange, error) {
		return domain.InventoryChange{}, fmt.Errorf("hubrise: %s: %w", what, err)
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return fail(err)
	}
	defer func() { _ = tx.Rollback() }()

	var disabled bool
	err = tx.GetContext(ctx, &disabled, flip, link.ID, st.SoldOutUntil, st.Stock, st.SyncedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.InventoryChange{}, nil // the flag was already in the wanted state
	}
	if err != nil {
		return fail(err)
	}

	// Lock the local row. Sync read it as available some time ago and holds no lock against staff: a
	// row staff sold out by hand in between stays theirs (the provenance flag would otherwise let a
	// later stock or the expiry make it available against their intent). A row HubRise restrictions
	// disable is unavailable for a reason that is taken over with it.
	var row struct {
		Available bool      `db:"is_available"`
		ProductID uuid.UUID `db:"product_id"`
	}
	// The link has a foreign key on the row, so it is there.
	if err = tx.GetContext(ctx, &row, fmt.Sprintf(`SELECT is_available, %s AS product_id FROM %s WHERE id = $1 FOR UPDATE`,
		target.productColumn, target.table), link.LocalID()); err != nil {
		return fail(err)
	}
	if !release && !row.Available && !disabled {
		return domain.InventoryChange{}, nil // the deferred rollback undoes the flip
	}

	res, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET is_available = $2%s WHERE id = $1 AND is_available <> $2`,
		target.table, target.touched), link.LocalID(), release && !disabled)
	if err != nil {
		return fail(err)
	}
	changed, _ := res.RowsAffected() // lib/pq always reports it
	if err := tx.Commit(); err != nil {
		return fail(err)
	}
	return domain.InventoryChange{Done: true, Changed: changed > 0, ProductID: row.ProductID}, nil
}
