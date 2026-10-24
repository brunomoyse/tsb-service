package domain

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
)

// InventoryPayloadVersion is the version of the payload of an inventory outbox row.
const InventoryPayloadVersion = 1

// InventoryDedupeKey is the dedupe key of the outbox rows about one SKU: a pending row is replaced
// by a newer one, a row already being sent is never merged into.
func InventoryDedupeKey(skuRef string) string { return "inv:sku:" + skuRef }

// InventoryPayload is what an inventory outbox row carries. It names the product and its SKU only:
// whether the PATCH sells the SKU out or makes it available again is decided at send time from the
// product's state then, so a row that waited out an outage never sends a stale answer.
type InventoryPayload struct {
	Version   int       `json:"v"`
	ProductID uuid.UUID `json:"product_id"`
	SkuRef    string    `json:"sku_ref"`
}

// Marshal encodes the payload for the outbox (it carries the current version).
func (p InventoryPayload) Marshal() json.RawMessage {
	p.Version = InventoryPayloadVersion
	b, _ := json.Marshal(p) // a uuid, a string and an int always encode
	return b
}

// Availability is the availability of a local row that inventory can switch.
type Availability struct {
	Available bool
	// ProductID is the product itself, or the product a choice belongs to.
	ProductID uuid.UUID
}

// InventoryChange is the result of taking over or releasing a sold-out state.
type InventoryChange struct {
	// Done is false when nothing was done: the link was already in the wanted ownership state (it
	// was taken over or released by someone else in between).
	Done bool
	// Changed reports that the availability of the local row flipped, which is what the shop shows.
	Changed bool
	// ProductID is the product to announce, or the product a choice belongs to.
	ProductID uuid.UUID
}

// AvailabilityStore reads and switches the availability flags of the local rows inventory drives
// (products.is_available, product_choices.is_available), together with the sold-out provenance of
// their links. Taking over and releasing are atomic and compare-and-set on the provenance flag: two
// replicas, or an inventory sync racing a staff toggle, cannot both win.
type AvailabilityStore interface {
	// Availability returns the availability of the local rows of an entity type (product or choice)
	// among ids; ids that do not exist are absent from the result.
	Availability(ctx context.Context, t EntityType, ids []uuid.UUID) (map[uuid.UUID]Availability, error)
	// TakeOver marks the link as sold out by inventory (the provenance flag, the stock, the expiry
	// in st) and makes the local row unavailable, in one transaction. It does nothing (Done false)
	// when the link already owns the sold-out state, or when the local row is unavailable although
	// HubRise does not disable it (staff sold it out by hand since it was read): that stays theirs.
	TakeOver(ctx context.Context, link *CatalogLink, st InventoryState) (InventoryChange, error)
	// Release gives a sold-out link back (clears the provenance flag and the expiry, stores the stock
	// in st) and makes the local row available again unless HubRise restrictions disable it, in one
	// transaction. It does nothing (Done false) when the link does not own the sold-out state: the
	// shop took it over meanwhile.
	Release(ctx context.Context, link *CatalogLink, st InventoryState) (InventoryChange, error)
}

// InventoryEventResource is the resource type of inventory callback events.
const InventoryEventResource = "inventory"

// CatalogEventResource is the resource type of catalog callback events.
const CatalogEventResource = "catalog"
