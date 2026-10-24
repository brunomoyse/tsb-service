package domain

import (
	"slices"

	"github.com/google/uuid"
)

// Action is what an import does with one entry.
type Action string

const (
	// ActionCreate creates the local row and its link.
	ActionCreate Action = "create"
	// ActionUpdate brings a linked local row up to date.
	ActionUpdate Action = "update"
	// ActionLink adopts an existing local row: the link is created, and in a full import the
	// fields HubRise owns are updated.
	ActionLink Action = "link"
	// ActionRetire hides what HubRise no longer has; nothing is deleted.
	ActionRetire Action = "retire"
	// ActionRevive undoes a retirement because the entry is back in the catalog.
	ActionRevive Action = "revive"
)

// RetireBatch is the batch key of the retirements.
const RetireBatch = "~retire"

// Field names a Change lists in Fields, for the preview.
const (
	FieldPrice      = "price"
	FieldCategory   = "category"
	FieldVat        = "vat_category"
	FieldCode       = "code"
	FieldVisibility = "visibility"
	FieldAvailable  = "availability"
	FieldNames      = "names"
	FieldLimits     = "limits"
	FieldOrder      = "sort"
	FieldImage      = "image"
	// FieldContent: only the HubRise content changed (a tag, a flag), no field the shop shows.
	FieldContent = "content"
)

// Change is one planned step. Exactly one of Category, Product, Group and Choice is set, for
// create, update, link and revive; a retire carries none.
type Change struct {
	Entity EntityType
	Action Action
	// Key is the HubRise key of the entry (ref, SKU ref, "sku|list", "sku|list|option").
	Key  string
	Name string
	// ParentKey is the key of the category (for a product), the product (for a group) or the group
	// (for a choice).
	ParentKey string
	// ProductKey is the key of the product a group or choice belongs to.
	ProductKey string
	// Order is the position of a new category.
	Order int
	// LocalID is the local row of an update, link, retire or revive.
	LocalID uuid.UUID
	// Fields lists what changes, for the preview.
	Fields []string
	// Hash is the content hash the link will carry afterwards.
	Hash string
	// Origin is the origin of a new link.
	Origin LinkOrigin
	// Batch groups the changes that land in one transaction: the category key, or RetireBatch.
	Batch string

	// Disabled is the catalog_disabled state the link will carry (products and choices).
	Disabled bool
	// SetVisible, when set, is the visibility to write (a product on create, adoption, revive, or when
	// HubRise changed its mind about it).
	SetVisible *bool
	// SetAvailable, when set, is the availability to write.
	SetAvailable *bool

	Category *CatalogCategory
	Product  *CatalogProduct
	Group    *CatalogGroup
	Choice   *CatalogChoice
}

// Diff is the result of planning an import: what a FULL import would do.
type Diff struct {
	// CatalogHash is the hash of the neutral catalog the plan was made from.
	CatalogHash string
	Changes     []Change
	Warnings    []Warning
	// Conflicts are matches the planner refused to guess; the entries are neither created nor linked.
	Conflicts []Conflict
	// Unchanged counts the entries that need nothing.
	Unchanged int
	// LinkedProducts counts the non-retired product links before the import: the base of the
	// mass-retire threshold.
	LinkedProducts int
	// RetiringProducts counts the products this import retires.
	RetiringProducts int
	// RetireNeedsConfirm is true when the retirements trip the mass-retire threshold.
	RetireNeedsConfirm bool
}

// Count counts the changes of an entity type and action.
func (d *Diff) Count(entity EntityType, action Action) int {
	n := 0
	for i := range d.Changes {
		if d.Changes[i].Entity == entity && d.Changes[i].Action == action {
			n++
		}
	}
	return n
}

// Batches splits the changes into the transactions of an apply: one per category key in the order
// of the plan, the retirements last. The relative order inside a batch is kept.
func (d *Diff) Batches() [][]Change {
	var order []string
	byKey := map[string][]Change{}
	for _, c := range d.Changes {
		if _, ok := byKey[c.Batch]; !ok {
			order = append(order, c.Batch)
		}
		byKey[c.Batch] = append(byKey[c.Batch], c)
	}
	if i := slices.Index(order, RetireBatch); i >= 0 { // retirements last
		order = append(slices.Delete(order, i, i+1), RetireBatch)
	}
	out := make([][]Change, 0, len(order))
	for _, k := range order {
		out = append(out, byKey[k])
	}
	return out
}

// Retirement thresholds: retiring more than MassRetireShare of the linked products, and at least
// MassRetireMin of them, needs an explicit confirmation.
const (
	MassRetireShare = 0.30
	MassRetireMin   = 5
)

// TripsMassRetire reports whether retiring `retiring` of `linked` non-retired products needs a
// confirmation.
func TripsMassRetire(retiring, linked int) bool {
	return retiring >= MassRetireMin && float64(retiring) > MassRetireShare*float64(linked)
}
