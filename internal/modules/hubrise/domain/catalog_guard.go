package domain

import (
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// The field sets below are what the ownership guard compares. They are neutral copies of the
// product, choice group and choice that the shop's edit paths hold, so the HubRise module never
// imports the product module.

// ProductFields are the fields of a product the guard looks at.
type ProductFields struct {
	ID          uuid.UUID
	CategoryID  uuid.UUID
	Price       decimal.Decimal
	Code        string
	VatCategory string
}

// GroupFields are the fields of a choice group the guard looks at. Names holds the translations by
// language.
type GroupFields struct {
	ID        uuid.UUID
	ProductID uuid.UUID
	Min, Max  int
	Sort      int
	Names     map[string]string
}

// ChoiceFields are the fields of a choice the guard looks at.
type ChoiceFields struct {
	ID            uuid.UUID
	ProductID     uuid.UUID
	GroupID       uuid.UUID
	PriceModifier decimal.Decimal
	Sort          int
	Names         map[string]string
}

// Names of the locked fields, as reported in a ManagedError.
const (
	LockedPrice        = "price"
	LockedCategory     = "category"
	LockedVatCategory  = "vatCategory"
	LockedCode         = "code"
	LockedLimits       = "selections"
	LockedSort         = "sortOrder"
	LockedName         = "name"
	LockedPriceMod     = "priceModifier"
	LockedChoiceGroups = "choiceGroups"
	LockedChoices      = "choices"
)

// PendingReviewPrefix starts the connection's last_error while a scheduled catalog import waits for
// a human to confirm a mass retirement.
const PendingReviewPrefix = "CATALOG_PENDING_REVIEW: "

// CatalogPendingReview reports whether a scheduled import is waiting for confirmation.
func (c *Connection) CatalogPendingReview() bool {
	return c != nil && c.LastError != nil && len(*c.LastError) >= len(PendingReviewPrefix) &&
		(*c.LastError)[:len(PendingReviewPrefix)] == PendingReviewPrefix
}
