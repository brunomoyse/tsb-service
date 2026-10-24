package domain

import (
	"context"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Sources of a translation row (the value of the source column).
const (
	// SourceManual is a translation typed in the dashboard: the import never overwrites it.
	SourceManual = "manual"
	// SourceHubrise is a translation the import wrote: the import may refresh it.
	SourceHubrise = "hubrise"
)

// LocalText is a translation row of the local menu.
type LocalText struct {
	Name        string
	Description string
	Source      string
}

// LocalCategory is a category of the local menu.
type LocalCategory struct {
	ID    uuid.UUID
	Slug  string
	Order int
	Names map[string]LocalText
}

// LocalProduct is a product of the local menu.
type LocalProduct struct {
	ID          uuid.UUID
	CategoryID  uuid.UUID
	Code        string
	Price       decimal.Decimal
	VatCategory string
	Visible     bool
	Available   bool
	Texts       map[string]LocalText
}

// LocalGroup is a choice group of the local menu.
type LocalGroup struct {
	ID        uuid.UUID
	ProductID uuid.UUID
	Min, Max  int
	Sort      int
	Names     map[string]LocalText
}

// LocalChoice is a choice of the local menu.
type LocalChoice struct {
	ID            uuid.UUID
	ProductID     uuid.UUID
	GroupID       uuid.UUID
	PriceModifier decimal.Decimal
	Sort          int
	Unavailable   bool
	Names         map[string]LocalText
}

// LocalSnapshot is the whole local menu, read for planning an import.
type LocalSnapshot struct {
	Categories []LocalCategory
	Products   []LocalProduct
	Groups     []LocalGroup
	Choices    []LocalChoice
}

// TextTarget names the translation table a text is written to.
type TextTarget string

const (
	TextCategory TextTarget = "category"
	TextProduct  TextTarget = "product"
	TextGroup    TextTarget = "group"
	TextChoice   TextTarget = "choice"
)

// TextWrite is one translation the import wants in place.
type TextWrite struct {
	Language    string
	Name        string
	Description string
}

// NewCategory creates a category. SlugBase is the text the unique public slug is made of; it is
// generated once and never regenerated, because slugs are public URLs.
type NewCategory struct {
	Order    int
	SlugBase string
}

// NewProduct creates a product written by the import. It is not subject to the dashboard rule
// that a visible product needs three translations.
type NewProduct struct {
	CategoryID   uuid.UUID
	Price        decimal.Decimal
	Code         string // empty = none
	VatCategory  string
	Visible      bool
	Available    bool
	Halal        bool
	Vegetarian   bool
	Spicy        bool
	Discountable bool
	// SlugName is the product name; the slug is the category's name plus this, made unique, and is
	// generated once.
	SlugName string
}

// ProductPatch changes the fields of a product the import owns; nil leaves a field alone. A code is
// never blanked: there is no way to ask for it.
type ProductPatch struct {
	CategoryID  *uuid.UUID
	Price       *decimal.Decimal
	Code        *string
	VatCategory *string
	Visible     *bool
	Available   *bool
}

// NewGroup creates a choice group.
type NewGroup struct {
	ProductID uuid.UUID
	Min, Max  int
	Sort      int
}

// GroupPatch changes a choice group; nil leaves a field alone.
type GroupPatch struct {
	Min, Max, Sort *int
}

// NewChoice creates a choice.
type NewChoice struct {
	ProductID     uuid.UUID
	GroupID       uuid.UUID
	PriceModifier decimal.Decimal
	Sort          int
	Unavailable   bool
}

// ChoicePatch changes a choice; nil leaves a field alone.
type ChoicePatch struct {
	PriceModifier *decimal.Decimal
	Sort          *int
	Unavailable   *bool
}

// CatalogTx is the write side of an import batch: one transaction, holding the import's advisory
// lock. The batch either lands whole or not at all. Nothing in it ever deletes a product, a choice
// or a choice group (order lines reference them): retirement flips flags.
type CatalogTx interface {
	// Links is the link repository bound to this transaction.
	Links() CatalogLinkRepository

	CreateCategory(ctx context.Context, c NewCategory) (uuid.UUID, error)
	CreateProduct(ctx context.Context, p NewProduct) (uuid.UUID, error)
	PatchProduct(ctx context.Context, id uuid.UUID, p ProductPatch) error
	CreateGroup(ctx context.Context, g NewGroup) (uuid.UUID, error)
	PatchGroup(ctx context.Context, id uuid.UUID, p GroupPatch) error
	CreateChoice(ctx context.Context, c NewChoice) (uuid.UUID, error)
	PatchChoice(ctx context.Context, id uuid.UUID, p ChoicePatch) error

	// UpsertTexts writes the translations that are missing or were written by the import. A row a
	// human edited (source manual) is never overwritten, and a language not named is never touched.
	UpsertTexts(ctx context.Context, target TextTarget, ownerID uuid.UUID, texts []TextWrite) error
}

// LocalCatalogRepository reads and writes the local menu for the import. It is the importer's own
// repository: it writes the product tables directly, with the rules the import needs (no
// three-translation visibility rule, slugs generated once, translation sources), and it is the
// only code that does.
type LocalCatalogRepository interface {
	// Snapshot reads the whole menu.
	Snapshot(ctx context.Context) (*LocalSnapshot, error)
	// SnapshotWithLinks reads the menu and every catalog link (retired ones too) in one
	// repeatable-read transaction: what the catalog publisher plans from.
	SnapshotWithLinks(ctx context.Context) (*LocalSnapshot, []CatalogLink, error)
	// InBatch runs fn in one transaction after taking the import's advisory lock
	// (pg_advisory_xact_lock), so two instances never import at the same time. fn's error rolls the
	// batch back.
	InBatch(ctx context.Context, fn func(ctx context.Context, tx CatalogTx) error) error
}
