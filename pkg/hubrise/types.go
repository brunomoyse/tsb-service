package hubrise

import (
	"encoding/json"
	"fmt"
	"time"
)

// This file holds the wire types of the HubRise API. They were fixed from the API documentation
// and from the recorded exchanges of the moyun-pos integration (pkg/hubrise/hubrisetest/fixtures),
// which a contract test decodes strictly into these types: a field HubRise sends that is not
// modelled here fails that test. Optional fields are omitted from requests when empty; a JSON null
// in a response decodes to the zero value.

// OrderStatus is the status of a HubRise order.
type OrderStatus string

// The order statuses (docs/hubrise/03-orders.md).
const (
	StatusNew                OrderStatus = "new"
	StatusReceived           OrderStatus = "received"
	StatusAccepted           OrderStatus = "accepted"
	StatusInPreparation      OrderStatus = "in_preparation"
	StatusAwaitingCollection OrderStatus = "awaiting_collection"
	StatusInDelivery         OrderStatus = "in_delivery"
	StatusCompleted          OrderStatus = "completed"
	StatusRejected           OrderStatus = "rejected"
	StatusCancelled          OrderStatus = "cancelled"
	StatusDeliveryFailed     OrderStatus = "delivery_failed"
	// StatusAwaitingShipment is the deprecated alias of StatusAwaitingCollection.
	StatusAwaitingShipment OrderStatus = "awaiting_shipment"
)

// Valid reports whether s is a documented status.
func (s OrderStatus) Valid() bool {
	switch s {
	case StatusNew, StatusReceived, StatusAccepted, StatusInPreparation, StatusAwaitingCollection,
		StatusInDelivery, StatusCompleted, StatusRejected, StatusCancelled, StatusDeliveryFailed,
		StatusAwaitingShipment:
		return true
	default:
		return false
	}
}

// Canonical maps the deprecated awaiting_shipment to awaiting_collection and returns every other
// status unchanged.
func (s OrderStatus) Canonical() OrderStatus {
	if s == StatusAwaitingShipment {
		return StatusAwaitingCollection
	}
	return s
}

// Final reports whether the status ends the life of an order.
func (s OrderStatus) Final() bool {
	switch s {
	case StatusCompleted, StatusRejected, StatusCancelled, StatusDeliveryFailed:
		return true
	default:
		return false
	}
}

// ServiceType is how the customer gets the order.
type ServiceType string

// The service types.
const (
	ServiceDelivery   ServiceType = "delivery"
	ServiceCollection ServiceType = "collection"
	ServiceEatIn      ServiceType = "eat_in"
)

// Bool returns a pointer to b, for the optional boolean fields.
func Bool(b bool) *bool { return &b }

// Time returns a pointer to t, for the optional time fields.
func Time(t time.Time) *time.Time { return &t }

// Order is a HubRise order, used both as the body of a create and as the response of every order
// call. Response only fields (id, total, created_at...) are omitted from requests when empty.
type Order struct {
	ID         string      `json:"id,omitempty"`
	LocationID string      `json:"location_id,omitempty"`
	Ref        string      `json:"ref,omitempty"`
	PrivateRef string      `json:"private_ref,omitempty"`
	Status     OrderStatus `json:"status,omitempty"`

	ServiceType    ServiceType `json:"service_type,omitempty"`
	ServiceTypeRef string      `json:"service_type_ref,omitempty"`

	CreatedAt      time.Time `json:"created_at,omitzero"`
	CreatedBy      string    `json:"created_by,omitempty"`
	Channel        string    `json:"channel,omitempty"`
	ConnectionName string    `json:"connection_name,omitempty"`

	ExpectedTime       *time.Time `json:"expected_time,omitempty"`
	ExpectedTimePickup bool       `json:"expected_time_pickup,omitempty"`
	Asap               *bool      `json:"asap,omitempty"`
	ConfirmedTime      *time.Time `json:"confirmed_time,omitempty"`
	DriverPickupURL    string     `json:"driver_pickup_url,omitempty"`

	CustomerNotes  string   `json:"customer_notes,omitempty"`
	SellerNotes    string   `json:"seller_notes,omitempty"`
	CollectionCode string   `json:"collection_code,omitempty"`
	CouponCodes    []string `json:"coupon_codes,omitempty"`

	// Total is calculated by HubRise: items + charges - discounts.
	Total              Money `json:"total,omitempty"`
	TotalDiscrepancy   Money `json:"total_discrepancy,omitempty"`
	PaymentDiscrepancy Money `json:"payment_discrepancy,omitempty"`

	Items     []OrderItem          `json:"items,omitempty"`
	Deals     map[string]OrderDeal `json:"deals,omitempty"`
	Discounts []OrderDiscount      `json:"discounts,omitempty"`
	Charges   []OrderCharge        `json:"charges,omitempty"`
	Payments  []OrderPayment       `json:"payments,omitempty"`

	// CustomerID, CustomerListID with CustomerPrivateRef, and Customer (a guest) are mutually
	// exclusive on a create.
	CustomerID         string    `json:"customer_id,omitempty"`
	CustomerListID     string    `json:"customer_list_id,omitempty"`
	CustomerPrivateRef string    `json:"customer_private_ref,omitempty"`
	Customer           *Customer `json:"customer,omitempty"`

	LoyaltyOperations []LoyaltyOperation `json:"loyalty_operations,omitempty"`

	Delivery       json.RawMessage `json:"delivery,omitempty"`
	DeliveryQuotes json.RawMessage `json:"delivery_quotes,omitempty"`
	CustomFields   json.RawMessage `json:"custom_fields,omitempty"`
}

// OrderItem is a line of an order.
type OrderItem struct {
	ID          string `json:"id,omitempty"`
	PrivateRef  string `json:"private_ref,omitempty"`
	ProductName string `json:"product_name,omitempty"`
	SkuName     string `json:"sku_name,omitempty"`
	SkuRef      string `json:"sku_ref,omitempty"`
	// Price is the unit price, excluding options.
	Price Money `json:"price,omitempty"`
	// Quantity is a decimal string ("2", "1.0").
	Quantity string `json:"quantity,omitempty"`
	Subtotal Money  `json:"subtotal,omitempty"`
	// TaxRate is a percent decimal between 0 and 100 ("6", not 0.06).
	TaxRate       string        `json:"tax_rate,omitempty"`
	Subset        string        `json:"subset,omitempty"`
	CustomerNotes string        `json:"customer_notes,omitempty"`
	PointsEarned  string        `json:"points_earned,omitempty"`
	PointsUsed    string        `json:"points_used,omitempty"`
	Options       []OrderOption `json:"options,omitempty"`
	DealLine      *DealLine     `json:"deal_line,omitempty"`
	Deleted       bool          `json:"deleted,omitempty"`
}

// OrderOption is an option chosen on an item. An option chosen several times per unit is sent as
// several objects with Quantity 1.
type OrderOption struct {
	OptionListName string `json:"option_list_name"`
	Name           string `json:"name"`
	Ref            string `json:"ref,omitempty"`
	Price          Money  `json:"price,omitempty"`
	// Quantity per unit of the item, 1 when absent or null on the wire.
	Quantity int  `json:"quantity"`
	Removed  bool `json:"removed,omitempty"`
}

// MarshalJSON sends a zero quantity as the documented default 1.
func (o OrderOption) MarshalJSON() ([]byte, error) {
	type plain OrderOption
	if o.Quantity == 0 {
		o.Quantity = 1
	}
	return json.Marshal(plain(o))
}

// UnmarshalJSON reads a missing or null quantity as 1.
func (o *OrderOption) UnmarshalJSON(b []byte) error {
	type plain OrderOption
	aux := struct {
		*plain
		Quantity *int `json:"quantity"`
	}{plain: (*plain)(o)}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	o.Quantity = 1
	if aux.Quantity != nil {
		o.Quantity = *aux.Quantity
	}
	return nil
}

// DealLine ties an item to a line of a deal.
type DealLine struct {
	DealKey       string `json:"deal_key,omitempty"`
	Position      *int   `json:"position,omitempty"`
	Label         string `json:"label,omitempty"`
	PricingEffect string `json:"pricing_effect,omitempty"`
	PricingValue  string `json:"pricing_value,omitempty"`
}

// OrderDeal is an entry of the order's deal map.
type OrderDeal struct {
	Name string `json:"name"`
	Ref  string `json:"ref,omitempty"`
}

// OrderDiscount is an order level discount; PriceOff is a positive amount.
type OrderDiscount struct {
	ID         string `json:"id,omitempty"`
	PrivateRef string `json:"private_ref,omitempty"`
	Name       string `json:"name,omitempty"`
	Ref        string `json:"ref,omitempty"`
	PriceOff   Money  `json:"price_off,omitempty"`
	Deleted    bool   `json:"deleted,omitempty"`
	// PricingEffect and PricingValue are deprecated, only read.
	PricingEffect string `json:"pricing_effect,omitempty"`
	PricingValue  string `json:"pricing_value,omitempty"`
}

// OrderCharge is a fee or surcharge (delivery, online payment fee, rounding).
type OrderCharge struct {
	ID         string `json:"id,omitempty"`
	PrivateRef string `json:"private_ref,omitempty"`
	Name       string `json:"name,omitempty"`
	Ref        string `json:"ref,omitempty"`
	Price      Money  `json:"price,omitempty"`
	TaxRate    string `json:"tax_rate,omitempty"`
	Deleted    bool   `json:"deleted,omitempty"`
	// Type, ChargeType, ChargeRef and ChargePrice are deprecated, only read.
	Type        string `json:"type,omitempty"`
	ChargeType  string `json:"charge_type,omitempty"`
	ChargeRef   string `json:"charge_ref,omitempty"`
	ChargePrice Money  `json:"charge_price,omitempty"`
}

// OrderPayment is a payment on an order. Payments omitted means unpaid.
type OrderPayment struct {
	ID         string          `json:"id,omitempty"`
	PrivateRef string          `json:"private_ref,omitempty"`
	Name       string          `json:"name,omitempty"`
	Ref        string          `json:"ref,omitempty"`
	Amount     Money           `json:"amount,omitempty"`
	Info       json.RawMessage `json:"info,omitempty"`
	Deleted    bool            `json:"deleted,omitempty"`
	// Type is deprecated, only read.
	Type string `json:"type,omitempty"`
}

// LoyaltyOperation adds or removes loyalty points of a customer linked order.
type LoyaltyOperation struct {
	Ref    string `json:"ref,omitempty"`
	Name   string `json:"name,omitempty"`
	Delta  string `json:"delta,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Customer is a customer: the guest object of an order, or a customer of a customer list.
type Customer struct {
	ID             string `json:"id,omitempty"`
	CustomerListID string `json:"customer_list_id,omitempty"`
	Anonymised     bool   `json:"anonymised,omitempty"`
	PrivateRef     string `json:"private_ref,omitempty"`
	Email          string `json:"email,omitempty"`
	FirstName      string `json:"first_name,omitempty"`
	LastName       string `json:"last_name,omitempty"`
	Gender         string `json:"gender,omitempty"`
	BirthDate      string `json:"birth_date,omitempty"`
	CompanyName    string `json:"company_name,omitempty"`
	Phone          string `json:"phone,omitempty"`
	PhoneAccess    string `json:"phone_access_code,omitempty"`
	Address1       string `json:"address_1,omitempty"`
	Address2       string `json:"address_2,omitempty"`
	PostalCode     string `json:"postal_code,omitempty"`
	City           string `json:"city,omitempty"`
	State          string `json:"state,omitempty"`
	// Country is an ISO 3166-1 alpha-2 code.
	Country        string `json:"country,omitempty"`
	Latitude       string `json:"latitude,omitempty"`
	Longitude      string `json:"longitude,omitempty"`
	DeliveryNotes  string `json:"delivery_notes,omitempty"`
	SMSMarketing   bool   `json:"sms_marketing,omitempty"`
	EmailMarketing bool   `json:"email_marketing,omitempty"`

	// Response only.
	CreatedAt      time.Time       `json:"created_at,omitzero"`
	NbOrders       int             `json:"nb_orders,omitempty"`
	OrderTotal     Money           `json:"order_total,omitempty"`
	FirstOrderDate *time.Time      `json:"first_order_date,omitempty"`
	LastOrderDate  *time.Time      `json:"last_order_date,omitempty"`
	LoyaltyCards   json.RawMessage `json:"loyalty_cards,omitempty"`
	CustomFields   json.RawMessage `json:"custom_fields,omitempty"`
}

// OrderPatch is the body of PATCH /location/orders/:id. Only the fields set are sent. For the
// collections (items, discounts, charges, payments): an element with an ID and Deleted true
// deletes it, an element without an ID adds it, an element with an ID and a PrivateRef sets the
// private ref; HubRise does not modify elements in place.
type OrderPatch struct {
	Status          OrderStatus `json:"status,omitempty"`
	ConfirmedTime   *time.Time  `json:"confirmed_time,omitempty"`
	DriverPickupURL string      `json:"driver_pickup_url,omitempty"`
	SellerNotes     string      `json:"seller_notes,omitempty"`
	CollectionCode  string      `json:"collection_code,omitempty"`
	PrivateRef      string      `json:"private_ref,omitempty"`

	Items     []OrderItem     `json:"items,omitempty"`
	Discounts []OrderDiscount `json:"discounts,omitempty"`
	Charges   []OrderCharge   `json:"charges,omitempty"`
	Payments  []OrderPayment  `json:"payments,omitempty"`

	CustomFields json.RawMessage `json:"custom_fields,omitempty"`
}

// OrderFilter selects orders in a list call.
type OrderFilter struct {
	PrivateRef string
	Status     OrderStatus
	CreatedBy  string
	CustomerID string
	// After is an inclusive lower bound on the creation time, Before an exclusive upper bound.
	After, Before time.Time
	// Count is the page size (default and maximum 100).
	Count int
}

// Page is one page of a paginated list.
type Page[T any] struct {
	Items []T
	// NextCursor is the value of the X-Cursor-Next header, empty on the last page.
	NextCursor string
}

// Location is a HubRise location (GET /location).
type Location struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	AccountID      string `json:"account_id,omitempty"`
	CatalogID      string `json:"catalog_id,omitempty"`
	CustomerListID string `json:"customer_list_id,omitempty"`
	Country        string `json:"country,omitempty"`
	Timezone       struct {
		Name string `json:"name,omitempty"`
	} `json:"timezone"`
}

// Catalog is a HubRise catalog (GET /catalogs/:id).
type Catalog struct {
	ID         string       `json:"id"`
	LocationID string       `json:"location_id,omitempty"`
	AccountID  string       `json:"account_id,omitempty"`
	Name       string       `json:"name"`
	CreatedAt  time.Time    `json:"created_at,omitzero"`
	Data       *CatalogData `json:"data,omitempty"`
}

// CatalogData is the content of a catalog.
type CatalogData struct {
	Variants    []Variant    `json:"variants,omitempty"`
	Categories  []Category   `json:"categories,omitempty"`
	Products    []Product    `json:"products,omitempty"`
	OptionLists []OptionList `json:"option_lists,omitempty"`
	Deals       []Deal       `json:"deals,omitempty"`
	Discounts   []Discount   `json:"discounts,omitempty"`
	Charges     []Charge     `json:"charges,omitempty"`
}

// UnmarshalJSON also reads the key "options_lists" that one example of the HubRise documentation
// uses for "option_lists".
func (d *CatalogData) UnmarshalJSON(b []byte) error {
	type plain CatalogData
	aux := struct {
		*plain
		OptionsLists []OptionList `json:"options_lists"`
	}{plain: (*plain)(d)}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	if len(d.OptionLists) == 0 {
		d.OptionLists = aux.OptionsLists
	}
	return nil
}

// Variant is a catalog variant (a price list such as "Uber Eats & Deliveroo").
type Variant struct {
	ID   string `json:"id,omitempty"`
	Ref  string `json:"ref"`
	Name string `json:"name"`
}

// Category is a catalog category; the tree is expressed by ParentRef (uploads) or ParentID
// (responses).
type Category struct {
	ID          string   `json:"id,omitempty"`
	Ref         string   `json:"ref"`
	ParentRef   string   `json:"parent_ref,omitempty"`
	ParentID    string   `json:"parent_id,omitempty"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	ImageIDs    []string `json:"image_ids,omitempty"`
}

// Product is a catalog product, a group of SKUs.
type Product struct {
	ID          string   `json:"id,omitempty"`
	Ref         string   `json:"ref,omitempty"`
	CategoryRef string   `json:"category_ref,omitempty"`
	CategoryID  string   `json:"category_id,omitempty"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	// Nutrition is kept raw: the integration does not use it.
	Nutrition json.RawMessage `json:"nutrition,omitempty"`
	TaxRate   *TaxRate        `json:"tax_rate,omitempty"`
	ImageIDs  []string        `json:"image_ids,omitempty"`
	// ImagesIDs is how the documentation's retrieve example spells image_ids.
	ImagesIDs []string `json:"images_ids,omitempty"`
	Skus      []Sku    `json:"skus"`
}

// TaxRate is the tax rate of a product per service type, percent decimals ("6", "12.0").
type TaxRate struct {
	Delivery   string `json:"delivery,omitempty"`
	Collection string `json:"collection,omitempty"`
	EatIn      string `json:"eat_in,omitempty"`
}

// Sku is a sellable variant of a product; orders carry its Ref.
type Sku struct {
	ID        string `json:"id,omitempty"`
	ProductID string `json:"product_id,omitempty"`
	Ref       string `json:"ref,omitempty"`
	Name      string `json:"name,omitempty"`

	Restrictions   *Restrictions   `json:"restrictions,omitempty"`
	Price          Money           `json:"price"`
	PriceOverrides []PriceOverride `json:"price_overrides,omitempty"`

	// OptionListRefs is how an upload names the option lists, OptionListIDs how a response does.
	OptionListRefs []string `json:"option_list_refs,omitempty"`
	OptionListIDs  []string `json:"option_list_ids,omitempty"`

	Tags     []string `json:"tags,omitempty"`
	Barcodes []string `json:"barcodes,omitempty"`
	// CustomFields holds the conventions of the owner's POS: code, vat_category, translations.
	CustomFields json.RawMessage `json:"custom_fields,omitempty"`
	Weight       string          `json:"weight,omitempty"`
	Volume       string          `json:"volume,omitempty"`
	NbServings   int             `json:"nb_servings,omitempty"`
}

// Restrictions are the availability conditions of a SKU, option, deal, discount or charge.
type Restrictions struct {
	// Enabled false takes the element off sale; absent means enabled.
	Enabled         *bool    `json:"enabled,omitempty"`
	VariantRefs     []string `json:"variant_refs,omitempty"`
	DOW             string   `json:"dow,omitempty"`
	StartTime       string   `json:"start_time,omitempty"`
	EndTime         string   `json:"end_time,omitempty"`
	StartDate       string   `json:"start_date,omitempty"`
	EndDate         string   `json:"end_date,omitempty"`
	ServiceTypes    []string `json:"service_types,omitempty"`
	ServiceTypeRefs []string `json:"service_type_refs,omitempty"`
	MinOrderAmount  Money    `json:"min_order_amount,omitempty"`
	MaxPerOrder     *int     `json:"max_per_order,omitempty"`
	MaxPerCustomer  *int     `json:"max_per_customer,omitempty"`
}

// IsEnabled reports whether the element is on sale: only an explicit enabled=false disables it.
// It is safe on a nil Restrictions.
func (r *Restrictions) IsEnabled() bool {
	return r == nil || r.Enabled == nil || *r.Enabled
}

// PriceOverride is a price rule of a SKU or option; the last matching rule wins.
type PriceOverride struct {
	VariantRefs     []string `json:"variant_refs,omitempty"`
	DOW             string   `json:"dow,omitempty"`
	StartTime       string   `json:"start_time,omitempty"`
	EndTime         string   `json:"end_time,omitempty"`
	StartDate       string   `json:"start_date,omitempty"`
	EndDate         string   `json:"end_date,omitempty"`
	ServiceTypes    []string `json:"service_types,omitempty"`
	ServiceTypeRefs []string `json:"service_type_refs,omitempty"`
	Price           Money    `json:"price"`
}

// OptionList is a list of options shared by SKUs. MaxSelections nil means unlimited.
type OptionList struct {
	ID                string   `json:"id,omitempty"`
	Ref               string   `json:"ref"`
	Name              string   `json:"name"`
	Type              string   `json:"type,omitempty"`
	MinSelections     int      `json:"min_selections"`
	MaxSelections     *int     `json:"max_selections"`
	MultipleSelection bool     `json:"multiple_selection"`
	Tags              []string `json:"tags,omitempty"`
	Options           []Option `json:"options"`
}

// Option is one choice of an option list; orders carry its Ref.
type Option struct {
	ID             string          `json:"id,omitempty"`
	OptionListID   string          `json:"option_list_id,omitempty"`
	Ref            string          `json:"ref,omitempty"`
	Name           string          `json:"name"`
	Restrictions   *Restrictions   `json:"restrictions,omitempty"`
	Price          Money           `json:"price,omitempty"`
	PriceOverrides []PriceOverride `json:"price_overrides,omitempty"`
	Default        bool            `json:"default,omitempty"`
	Tags           []string        `json:"tags,omitempty"`
	Nutrition      json.RawMessage `json:"nutrition,omitempty"`
	Weight         string          `json:"weight,omitempty"`
	Volume         string          `json:"volume,omitempty"`
}

// Deal is a catalog deal (not imported by the integration, decoded for completeness).
type Deal struct {
	ID           string            `json:"id,omitempty"`
	Ref          string            `json:"ref,omitempty"`
	CategoryRef  string            `json:"category_ref,omitempty"`
	CategoryID   string            `json:"category_id,omitempty"`
	Name         string            `json:"name"`
	Description  string            `json:"description,omitempty"`
	Restrictions *Restrictions     `json:"restrictions,omitempty"`
	CouponCodes  []string          `json:"coupon_codes,omitempty"`
	Tags         []string          `json:"tags,omitempty"`
	ImageIDs     []string          `json:"image_ids,omitempty"`
	Lines        []CatalogDealLine `json:"lines,omitempty"`
}

// CatalogDealLine is a line of a catalog deal.
type CatalogDealLine struct {
	Label         string           `json:"label,omitempty"`
	Skus          []CatalogDealSku `json:"skus,omitempty"`
	PricingEffect string           `json:"pricing_effect,omitempty"`
	PricingValue  string           `json:"pricing_value,omitempty"`
}

// CatalogDealSku is an eligible SKU of a deal line.
type CatalogDealSku struct {
	Ref         string `json:"ref"`
	ExtraCharge Money  `json:"extra_charge,omitempty"`
}

// Discount is a catalog discount (decoded for completeness).
type Discount struct {
	ID            string        `json:"id,omitempty"`
	Ref           string        `json:"ref,omitempty"`
	Name          string        `json:"name"`
	Description   string        `json:"description,omitempty"`
	Restrictions  *Restrictions `json:"restrictions,omitempty"`
	CouponCodes   []string      `json:"coupon_codes,omitempty"`
	PricingEffect string        `json:"pricing_effect,omitempty"`
	PricingValue  string        `json:"pricing_value,omitempty"`
	ImageIDs      []string      `json:"image_ids,omitempty"`
}

// Charge is a catalog charge; Type is delivery, payment_fee, tip, tax or other.
type Charge struct {
	ID           string        `json:"id,omitempty"`
	Ref          string        `json:"ref,omitempty"`
	Name         string        `json:"name"`
	Type         string        `json:"type"`
	Price        Money         `json:"price,omitempty"`
	Restrictions *Restrictions `json:"restrictions,omitempty"`
}

// Image is the metadata of a catalog image.
type Image struct {
	ID                   string `json:"id"`
	Type                 string `json:"type"`
	Size                 int    `json:"size"`
	MD5                  string `json:"md5"`
	PrivateRef           string `json:"private_ref,omitempty"`
	SecondsBeforeRemoval int    `json:"seconds_before_removal,omitempty"`
}

// ImageData is a downloaded catalog image.
type ImageData struct {
	ContentType string
	Data        []byte
}

// InventoryEntry is the stock of one SKU or option. Stock "0" is sold out, ExpiresAt (only with
// stock "0") is when it becomes available again; on a PATCH or PUT a nil Stock removes the entry
// (unlimited stock), which is why Stock is always written, as null when nil.
type InventoryEntry struct {
	SkuRef    string     `json:"sku_ref,omitempty"`
	OptionRef string     `json:"option_ref,omitempty"`
	Stock     *string    `json:"stock"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// TargetRef returns the ref the entry is about, whichever of sku_ref and option_ref is set.
func (e InventoryEntry) TargetRef() string {
	if e.SkuRef != "" {
		return e.SkuRef
	}
	return e.OptionRef
}

// IsSoldOut reports whether the entry is a stock of exactly zero.
func (e InventoryEntry) IsSoldOut() bool {
	if e.Stock == nil {
		return false
	}
	d, err := ParseStock(*e.Stock)
	return err == nil && d.IsZero()
}

// SoldOutSku is the entry marking a SKU sold out, until expires when not nil.
func SoldOutSku(ref string, expires *time.Time) InventoryEntry {
	zero := "0"
	return InventoryEntry{SkuRef: ref, Stock: &zero, ExpiresAt: expires}
}

// AvailableSku is the entry removing a SKU from the inventory (unlimited stock again).
func AvailableSku(ref string) InventoryEntry { return InventoryEntry{SkuRef: ref} }

// Callback is the callback registration of a connection. An empty URL registers a passive callback
// (events are only listed by GET /callback/events).
type Callback struct {
	URL    string
	Events map[string][]string
}

type callbackWire struct {
	URL    *string             `json:"url"`
	Events map[string][]string `json:"events"`
}

// MarshalJSON writes an empty URL as null.
func (c Callback) MarshalJSON() ([]byte, error) {
	w := callbackWire{Events: c.Events}
	if c.URL != "" {
		w.URL = &c.URL
	}
	if w.Events == nil {
		w.Events = map[string][]string{}
	}
	return json.Marshal(w)
}

// UnmarshalJSON reads a null URL as empty.
func (c *Callback) UnmarshalJSON(b []byte) error {
	var w callbackWire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	c.URL, c.Events = "", w.Events
	if w.URL != nil {
		c.URL = *w.URL
	}
	return nil
}

// ValidCallbackEvent reports whether the integration may register for resource.event. These are
// the combinations it uses; HubRise answers 422 to a combination it does not know, and the mock
// does too.
func ValidCallbackEvent(resource, event string) bool {
	switch resource {
	case "order":
		return event == "create" || event == "update"
	case "catalog":
		return event == "create" || event == "update" || event == "delete"
	case "inventory":
		return event == "update" || event == "patch"
	default:
		return false
	}
}

// Event is a callback event: the body HubRise POSTs to an active callback, and an element of
// GET /callback/events (which has no state fields).
type Event struct {
	ID           string    `json:"id"`
	ResourceType string    `json:"resource_type"`
	EventType    string    `json:"event_type"`
	CreatedAt    time.Time `json:"created_at,omitzero"`

	// The ids of the resource and of its parents.
	AccountID      string `json:"account_id,omitempty"`
	LocationID     string `json:"location_id,omitempty"`
	OrderID        string `json:"order_id,omitempty"`
	CatalogID      string `json:"catalog_id,omitempty"`
	CustomerID     string `json:"customer_id,omitempty"`
	CustomerListID string `json:"customer_list_id,omitempty"`

	// PreviousState and NewState are carried by order events; catalog events have none, and
	// inventory events carry StateChange on a patch.
	PreviousState json.RawMessage `json:"previous_state,omitempty"`
	NewState      json.RawMessage `json:"new_state,omitempty"`
	StateChange   json.RawMessage `json:"state_change,omitempty"`
}

// ParseEvent decodes the body of a callback request.
func ParseEvent(body []byte) (*Event, error) {
	var e Event
	if err := json.Unmarshal(body, &e); err != nil {
		return nil, fmt.Errorf("hubrise: decode event: %w", err)
	}
	if e.ID == "" || e.ResourceType == "" || e.EventType == "" {
		return nil, fmt.Errorf("hubrise: event without id, resource_type or event_type")
	}
	return &e, nil
}

// Is reports whether the event is resource.event.
func (e *Event) Is(resource, event string) bool {
	return e.ResourceType == resource && e.EventType == event
}

// NewOrder decodes the new_state of an order event.
func (e *Event) NewOrder() (*Order, error) {
	if len(e.NewState) == 0 {
		return nil, fmt.Errorf("hubrise: event %s has no new_state", e.ID)
	}
	var o Order
	if err := json.Unmarshal(e.NewState, &o); err != nil {
		return nil, fmt.Errorf("hubrise: decode new_state of event %s: %w", e.ID, err)
	}
	return &o, nil
}
