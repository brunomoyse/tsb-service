package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// EntityType is the kind of local row a catalog link points at.
type EntityType string

const (
	EntityCategory    EntityType = "category"
	EntityProduct     EntityType = "product"
	EntityChoiceGroup EntityType = "choice_group"
	EntityChoice      EntityType = "choice"
)

// Valid reports whether t is one of the known entity types.
func (t EntityType) Valid() bool {
	switch t {
	case EntityCategory, EntityProduct, EntityChoiceGroup, EntityChoice:
		return true
	}
	return false
}

// LinkOrigin says how a link came to be.
type LinkOrigin string

const (
	OriginImport  LinkOrigin = "import"  // created by the importer together with the local row
	OriginAdopted LinkOrigin = "adopted" // matched to a row that existed before the integration
	OriginManual  LinkOrigin = "manual"  // set by hand in the dashboard
	// OriginPublished is a link the catalog publisher created for a new local item, with a ref the
	// shop generated.
	OriginPublished LinkOrigin = "published"
)

// CatalogLink ties one HubRise catalog entry to one local row. Exactly the id matching EntityType
// is set (the table enforces it). Keys are: category = ref; product = SKU ref; choice group =
// "skuRef|listRef"; choice = "skuRef|listRef|optionRef".
type CatalogLink struct {
	ID         int64      `db:"id"`
	EntityType EntityType `db:"entity_type"`

	CategoryID    *uuid.UUID `db:"category_id"`
	ProductID     *uuid.UUID `db:"product_id"`
	ChoiceGroupID *uuid.UUID `db:"choice_group_id"`
	ChoiceID      *uuid.UUID `db:"choice_id"`

	HubriseKey  string     `db:"hubrise_key"`
	HubriseRef  string     `db:"hubrise_ref"`
	HubriseID   *string    `db:"hubrise_id"`
	ContentHash string     `db:"content_hash"`
	Origin      LinkOrigin `db:"origin"`

	// Stock is the last stock figure HubRise reported (display only, no stock accounting).
	Stock *decimal.Decimal `db:"stock"`
	// InventorySoldOut is true only while the current unavailability was caused by HubRise
	// inventory. The system never restores availability it did not take away.
	InventorySoldOut  bool       `db:"inventory_sold_out"`
	SoldOutUntil      *time.Time `db:"sold_out_until"`
	CatalogDisabled   bool       `db:"catalog_disabled"`
	InventorySyncedAt *time.Time `db:"inventory_synced_at"`
	ImageMD5          *string    `db:"image_md5"`
	RetiredAt         *time.Time `db:"retired_at"`

	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

// LocalID returns the id of the local row, or the nil UUID when the link names none.
func (l *CatalogLink) LocalID() uuid.UUID {
	var id *uuid.UUID
	switch l.EntityType {
	case EntityCategory:
		id = l.CategoryID
	case EntityProduct:
		id = l.ProductID
	case EntityChoiceGroup:
		id = l.ChoiceGroupID
	case EntityChoice:
		id = l.ChoiceID
	}
	if id == nil {
		return uuid.Nil
	}
	return *id
}

// SetLocalID points the link at the local row of its entity type and clears the other ids.
func (l *CatalogLink) SetLocalID(id uuid.UUID) {
	l.CategoryID, l.ProductID, l.ChoiceGroupID, l.ChoiceID = nil, nil, nil, nil
	switch l.EntityType {
	case EntityCategory:
		l.CategoryID = &id
	case EntityProduct:
		l.ProductID = &id
	case EntityChoiceGroup:
		l.ChoiceGroupID = &id
	case EntityChoice:
		l.ChoiceID = &id
	}
}

// Validate checks the invariants the table also enforces, so a bad link fails before the query.
func (l *CatalogLink) Validate() error {
	if !l.EntityType.Valid() || l.HubriseKey == "" || l.HubriseRef == "" || l.LocalID() == uuid.Nil {
		return ErrInvalidLink
	}
	n := 0
	for _, id := range []*uuid.UUID{l.CategoryID, l.ProductID, l.ChoiceGroupID, l.ChoiceID} {
		if id != nil {
			n++
		}
	}
	if n != 1 {
		return ErrInvalidLink
	}
	switch l.Origin {
	case OriginImport, OriginAdopted, OriginManual, OriginPublished:
		return nil
	}
	return ErrInvalidLink
}

// InventoryState is what an inventory sync records on a link.
type InventoryState struct {
	Stock        *decimal.Decimal
	SoldOut      bool
	SoldOutUntil *time.Time
	SyncedAt     time.Time
}

// CatalogLinkRepository stores the links.
type CatalogLinkRepository interface {
	// Upsert creates the link or, when (entity type, key) exists, updates its target, ref, ids,
	// hash, origin and retirement. Inventory state is untouched (see UpdateInventory). The stored
	// row is written back into l.
	Upsert(ctx context.Context, l *CatalogLink) error
	// FindByKey returns ErrNotFound when no link has the key.
	FindByKey(ctx context.Context, t EntityType, key string) (*CatalogLink, error)
	// FindByLocal returns the link of a local row, or ErrNotFound.
	FindByLocal(ctx context.Context, t EntityType, localID uuid.UUID) (*CatalogLink, error)
	// ListByType lists the links of an entity type ordered by id; retired ones only on request.
	ListByType(ctx context.Context, t EntityType, includeRetired bool) ([]CatalogLink, error)
	// ListByRef lists the links of an entity type whose ref is ref (every choice of one option).
	ListByRef(ctx context.Context, t EntityType, ref string) ([]CatalogLink, error)
	// Count counts the links; retired ones only on request.
	Count(ctx context.Context, includeRetired bool) (int, error)
	// SetRetired stamps (or with nil clears) the retirement of a link.
	SetRetired(ctx context.Context, id int64, at *time.Time) error
	// SetCatalogDisabled records whether HubRise restrictions disable the entry.
	SetCatalogDisabled(ctx context.Context, id int64, disabled bool) error
	// SetImageMD5 records the MD5 of the image imported for the entry.
	SetImageMD5(ctx context.Context, id int64, md5 string) error
	// UpdateInventory records the result of an inventory sync.
	UpdateInventory(ctx context.Context, id int64, s InventoryState) error
	// RecordStock stores the stock figure and the expiry HubRise reported, without touching the
	// sold-out provenance flag (which only TakeOver, Release and the shop's own toggles change).
	RecordStock(ctx context.Context, id int64, stock *decimal.Decimal, soldOutUntil *time.Time, at time.Time) error
	// ListSoldOutExpired lists the non retired links that inventory sold out and whose expiry has
	// passed at now.
	ListSoldOutExpired(ctx context.Context, now time.Time) ([]CatalogLink, error)
	// ClearSoldOut clears the sold-out provenance of a link: the shop now owns that state.
	ClearSoldOut(ctx context.Context, id int64) error
	// Rekey changes the key and ref of a link (the shared option list split of the publisher).
	Rekey(ctx context.Context, id int64, key, ref string) error
	// SetHubriseID refreshes the cached HubRise id of the link of the entry (ids change on every PUT
	// of the catalog). A key without a link is ignored.
	SetHubriseID(ctx context.Context, t EntityType, key, hubriseID string) error
	// Delete removes one link (the local row stays).
	Delete(ctx context.Context, id int64) error
	// DeleteAll removes every link (the local rows stay) and returns how many.
	DeleteAll(ctx context.Context) (int64, error)
	// LinkedProductIDs returns, among ids, the products that have a live (not retired) link: the
	// ones HubRise manages when catalog sync is on.
	LinkedProductIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]bool, error)
	// CountUnlinkedProducts counts the visible products that have no live link: the ones an order
	// would carry without a SKU ref.
	CountUnlinkedProducts(ctx context.Context) (int, error)
}
