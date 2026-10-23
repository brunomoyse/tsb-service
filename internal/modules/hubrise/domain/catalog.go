package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/shopspring/decimal"
)

// This file is the neutral catalog: what the shop needs to know about a HubRise catalog, already
// reduced to the shape of the local menu (one product per SKU, one choice group per SKU and option
// list, flat categories, one language per name). The mapper builds it from the HubRise wire types,
// the planner compares it with the local menu, the applier writes it. Nothing here knows about the
// HTTP API or the database.

// SyncMode says how far an import goes.
type SyncMode string

const (
	// SyncDryRun computes the diff and writes nothing.
	SyncDryRun SyncMode = "DRY_RUN"
	// SyncLinkOnly matches the HubRise entries with existing local rows and records the links, so
	// orders carry SKU and option refs, without touching any product data.
	SyncLinkOnly SyncMode = "LINK_ONLY"
	// SyncFull adopts, creates, updates and retires.
	SyncFull SyncMode = "FULL"
)

// Valid reports whether m is one of the three modes.
func (m SyncMode) Valid() bool {
	switch m {
	case SyncDryRun, SyncLinkOnly, SyncFull:
		return true
	}
	return false
}

// VAT categories the catalog can carry (the values of products.vat_category).
const (
	VatFood       = "food"
	VatBeverage   = "beverage"
	VatZeroRated  = "zero_rated"
	VatOutOfScope = "out_of_scope"
)

// ValidVatCategory reports whether v is a VAT category of the shop.
func ValidVatCategory(v string) bool {
	switch v {
	case VatFood, VatBeverage, VatZeroRated, VatOutOfScope:
		return true
	}
	return false
}

// Text is a name with an optional description in one language.
type Text struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// CatalogCategory is a HubRise category that holds products.
type CatalogCategory struct {
	// Key is the HubRise ref.
	Key       string `json:"key"`
	HubriseID string `json:"id,omitempty"`
	// Names by language: the catalog language comes from the category name, the other languages
	// from the description convention of the owner's POS.
	Names map[string]string `json:"names"`
	// Order is the position in the depth-first walk of the catalog's category tree.
	Order int `json:"order"`
}

// CatalogProduct is one SKU, which becomes one local product.
type CatalogProduct struct {
	// Key is the SKU ref.
	Key         string `json:"key"`
	HubriseID   string `json:"id,omitempty"`
	CategoryKey string `json:"category"`
	// Texts by language. The catalog language is the product name (plus the SKU name); the other
	// languages come from the SKU custom fields.
	Texts map[string]Text `json:"texts"`
	// Price is the SKU price after the price overrides of the configured variant.
	Price decimal.Decimal `json:"price"`
	// Code is the menu code ("A2") from the SKU custom fields, empty when there is none.
	Code string `json:"code,omitempty"`
	// VatCategory is empty when it cannot be derived (the product is then created hidden).
	VatCategory string `json:"vat,omitempty"`
	// Visible says whether HubRise wants the product shown (not hidden by a tag or a restriction,
	// and with a known VAT category).
	Visible bool `json:"visible"`
	// Disabled is restrictions.enabled = false: off sale.
	Disabled   bool `json:"disabled,omitempty"`
	Halal      bool `json:"halal,omitempty"`
	Vegetarian bool `json:"vegetarian,omitempty"`
	Spicy      bool `json:"spicy,omitempty"`
	// Discountable is whether the pickup discount applies; it only seeds a new product.
	Discountable bool `json:"discountable"`
	// ImageID is the HubRise image of the product, empty when there is none.
	ImageID string         `json:"image,omitempty"`
	Groups  []CatalogGroup `json:"groups,omitempty"`
}

// CatalogGroup is an option list of one SKU, which becomes one local choice group.
type CatalogGroup struct {
	// Key is "skuRef|listRef".
	Key       string            `json:"key"`
	ListRef   string            `json:"ref"`
	HubriseID string            `json:"id,omitempty"`
	Names     map[string]string `json:"names"`
	Min       int               `json:"min"`
	Max       int               `json:"max"`
	Sort      int               `json:"sort"`
	Choices   []CatalogChoice   `json:"choices"`
}

// CatalogChoice is an option of one option list of one SKU, which becomes one local choice.
type CatalogChoice struct {
	// Key is "skuRef|listRef|optionRef", Ref the option ref (what inventory names).
	Key       string            `json:"key"`
	Ref       string            `json:"ref"`
	HubriseID string            `json:"id,omitempty"`
	Names     map[string]string `json:"names"`
	// PriceModifier is the option price, never negative.
	PriceModifier decimal.Decimal `json:"price"`
	Sort          int             `json:"sort"`
	// Disabled is restrictions.enabled = false: the choice cannot be selected.
	Disabled bool `json:"disabled,omitempty"`
}

// Catalog is the neutral form of a HubRise catalog.
type Catalog struct {
	// Language is the catalog language, the language HubRise names are written in.
	Language   string            `json:"language"`
	Categories []CatalogCategory `json:"categories"`
	Products   []CatalogProduct  `json:"products"`
	// Warnings are what the mapper could not or would not import; not part of the hash.
	Warnings []Warning `json:"-"`
}

// Hash is the content hash of the catalog as the shop sees it: it changes when something the
// import would write changes, and not when HubRise data the import ignores (deals, discounts,
// nutrition) does.
func (c *Catalog) Hash() string { return hashJSON(c) }

// ProductHash is the content hash stored on a product link. It covers every input of the product
// itself (its option groups have their own links and hashes), and records whether HubRise wanted
// the product visible, so a later import can tell a change on the HubRise side from a visibility
// decision the shop took by hand.
func (p *CatalogProduct) ProductHash() string {
	cp := *p
	cp.Groups = nil
	cp.Visible = false
	suffix := ":v0"
	if p.Visible {
		suffix = ":v1"
	}
	return hashJSON(cp) + suffix
}

// HashVisible reads the visibility out of a stored ProductHash; known is false for a hash that
// does not carry it (no link yet, or a link of another entity).
func HashVisible(storedHash string) (visible, known bool) {
	switch {
	case strings.HasSuffix(storedHash, ":v1"):
		return true, true
	case strings.HasSuffix(storedHash, ":v0"):
		return false, true
	}
	return false, false
}

// Hash is the content hash stored on a category link.
func (c *CatalogCategory) Hash() string { return hashJSON(c) }

// Hash is the content hash stored on a choice group link; the choices have their own.
func (g *CatalogGroup) Hash() string {
	cp := *g
	cp.Choices = nil
	return hashJSON(cp)
}

// Hash is the content hash stored on a choice link.
func (c *CatalogChoice) Hash() string { return hashJSON(c) }

func hashJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil { // the types above only hold strings, numbers, bools and maps of strings
		panic("hubrise: hash catalog: " + err.Error())
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Warning is something the import noticed and did not silently absorb.
type Warning struct {
	Code    WarningCode `json:"code"`
	Key     string      `json:"key,omitempty"`
	Message string      `json:"message"`
}

// WarningCode classifies a Warning.
type WarningCode string

const (
	// WarnVATUnmapped: no VAT category could be derived, the product is hidden.
	WarnVATUnmapped WarningCode = "VAT_UNMAPPED"
	// WarnPriceWindow: a price override depends on a time or date window, which is ignored.
	WarnPriceWindow WarningCode = "PRICE_WINDOW_IGNORED"
	// WarnRestrictionIgnored: a time, day or customer restriction is not modelled and is ignored.
	WarnRestrictionIgnored WarningCode = "RESTRICTION_IGNORED"
	// WarnNegativePrice: an option price below zero was raised to zero.
	WarnNegativePrice WarningCode = "NEGATIVE_OPTION_PRICE"
	// WarnInvalidPrice: a price could not be read, the entry is skipped.
	WarnInvalidPrice WarningCode = "INVALID_PRICE"
	// WarnMissingRef: an entry without a ref cannot be linked, it is skipped.
	WarnMissingRef WarningCode = "MISSING_REF"
	// WarnDuplicateRef: a ref used twice, the second use is skipped.
	WarnDuplicateRef WarningCode = "DUPLICATE_REF"
	// WarnEmptyOptionList: an option list without options is skipped.
	WarnEmptyOptionList WarningCode = "EMPTY_OPTION_LIST"
	// WarnSelectionLimits: min and max selections were inconsistent and were adjusted.
	WarnSelectionLimits WarningCode = "SELECTION_LIMITS_ADJUSTED"
	// WarnUnknownOptionList: a SKU names an option list the catalog does not have.
	WarnUnknownOptionList WarningCode = "UNKNOWN_OPTION_LIST"
	// WarnUnknownCategory: a product names a category the catalog does not have, it is skipped.
	WarnUnknownCategory WarningCode = "UNKNOWN_CATEGORY"
	// WarnImage: an image could not be imported.
	WarnImage WarningCode = "IMAGE_FAILED"
)

// Conflict is an entry the planner refused to guess: the match is ambiguous.
type Conflict struct {
	Entity EntityType `json:"entity"`
	Key    string     `json:"key"`
	Name   string     `json:"name"`
	Reason string     `json:"reason"`
	// Candidates are the names (or codes) of the local rows that matched.
	Candidates []string `json:"candidates,omitempty"`
}
