package deliveroo

import "time"

// OrderStatus represents the status of an order
type OrderStatus string

const (
	OrderStatusPending   OrderStatus = "pending"
	OrderStatusPlaced    OrderStatus = "placed"
	OrderStatusAccepted  OrderStatus = "accepted"
	OrderStatusConfirmed OrderStatus = "confirmed"
	OrderStatusRejected  OrderStatus = "rejected"
	OrderStatusCanceled  OrderStatus = "canceled"
	OrderStatusDelivered OrderStatus = "delivered"
)

// FulfillmentType represents who will collect the order
type FulfillmentType string

const (
	FulfillmentDeliveroo   FulfillmentType = "deliveroo"
	FulfillmentRestaurant  FulfillmentType = "restaurant"
	FulfillmentCustomer    FulfillmentType = "customer"
	FulfillmentTableService FulfillmentType = "table_service"
	FulfillmentAutonomous  FulfillmentType = "autonomous"
)

// Order represents a Deliveroo order
type Order struct {
	ID                      string             `json:"id"`
	OrderNumber             string             `json:"order_number"`
	LocationID              string             `json:"location_id"`
	BrandID                 string             `json:"brand_id"`
	DisplayID               string             `json:"display_id"`
	Status                  OrderStatus        `json:"status"`
	StatusLog               []StatusLogItem    `json:"status_log"`
	FulfillmentType         FulfillmentType    `json:"fulfillment_type"`
	OrderNotes              string             `json:"order_notes,omitempty"`
	CutleryNotes            string             `json:"cutlery_notes,omitempty"`
	ASAP                    bool               `json:"asap"`
	PrepareFor              time.Time          `json:"prepare_for"`
	TableNumber             string             `json:"table_number,omitempty"`
	Subtotal                MonetaryAmount     `json:"subtotal"`
	Delivery                *DeliveryDetails   `json:"delivery,omitempty"`
	TotalPrice              MonetaryAmount     `json:"total_price"`
	PartnerOrderSubtotal    MonetaryAmount     `json:"partner_order_subtotal"`
	PartnerOrderTotal       MonetaryAmount     `json:"partner_order_total"`
	OfferDiscount           MonetaryAmount     `json:"offer_discount"`
	CashDue                 MonetaryAmount     `json:"cash_due,omitempty"`
	BagFee                  MonetaryAmount     `json:"bag_fee,omitempty"`
	Surcharge               MonetaryAmount     `json:"surcharge,omitempty"`
	FeeBreakdown            []FeeDescription   `json:"fee_breakdown,omitempty"`
	Items                   []OrderItem        `json:"items"`
	StartPreparingAt        time.Time          `json:"start_preparing_at"`
	ConfirmAt               *time.Time         `json:"confirm_at,omitempty"`
	Promotions              []Promotion        `json:"promotions,omitempty"`
	RemakeDetails           *RemakeDetails     `json:"remake_details,omitempty"`
	IsTabletless            bool               `json:"is_tabletless"`
	MealCards               []MealCard         `json:"meal_cards,omitempty"`
	Customer                *CustomerDetails   `json:"customer,omitempty"`
}

// StatusLogItem represents a status change entry
type StatusLogItem struct {
	At     time.Time   `json:"at"`
	Status OrderStatus `json:"status"`
}

// MonetaryAmount represents a price with currency
type MonetaryAmount struct {
	Fractional   int    `json:"fractional"`
	CurrencyCode string `json:"currency_code"`
}

// DeliveryDetails contains delivery information
type DeliveryDetails struct {
	DeliveryFee        MonetaryAmount   `json:"delivery_fee"`
	Address            *DeliveryAddress `json:"address,omitempty"`
	EstimatedDeliveryAt *time.Time      `json:"estimated_delivery_at,omitempty"`
}

// DeliveryAddress represents a delivery address
type DeliveryAddress struct {
	Street       string  `json:"street"`
	Number       string  `json:"number"`
	PostalCode   string  `json:"postal_code"`
	City         string  `json:"city"`
	AddressLine1 string  `json:"address_line_1"`
	AddressLine2 string  `json:"address_line_2"`
	Latitude     float64 `json:"latitude"`
	Longitude    float64 `json:"longitude"`
}

// FeeDescription describes a fee applied to the order
type FeeDescription struct {
	Type         string         `json:"type"`
	Amount       MonetaryAmount `json:"amount"`
	BundleAmount MonetaryAmount `json:"bundle_amount,omitempty"`
	Quantity     int            `json:"quantity,omitempty"`
}

// OrderItem represents an item in an order
type OrderItem struct {
	PosItemID        *string        `json:"pos_item_id"`
	Name             string         `json:"name"`
	OperationalName  string         `json:"operational_name"`
	UnitPrice        MonetaryAmount `json:"unit_price"`
	TotalPrice       MonetaryAmount `json:"total_price"`
	MenuUnitPrice    MonetaryAmount `json:"menu_unit_price"`
	Quantity         int            `json:"quantity"`
	ItemFees         []ItemFee      `json:"item_fees,omitempty"`
	Modifiers        []OrderItem    `json:"modifiers,omitempty"`
	DiscountAmount   MonetaryAmount `json:"discount_amount,omitempty"`
}

// ItemFee represents a fee applied to an item
type ItemFee struct {
	Type        string         `json:"type"`
	CostPerUnit MonetaryAmount `json:"cost_per_unit"`
}

// Promotion represents a promotion applied to the order
type Promotion struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Amount MonetaryAmount `json:"amount"`
}

// RemakeDetails contains information about a remake order
type RemakeDetails struct {
	ParentOrderID string  `json:"parent_order_id"`
	Fault         string  `json:"fault"`
	OrderCost     float64 `json:"order_cost"`
}

// MealCard represents meal card payment info
type MealCard struct {
	Provider string `json:"provider"`
	Amount   int    `json:"amount"`
}

// CustomerDetails contains customer information
type CustomerDetails struct {
	FirstName              string         `json:"first_name,omitempty"`
	ContactNumber          string         `json:"contact_number,omitempty"`
	ContactAccessCode      string         `json:"contact_access_code,omitempty"`
	OrderFrequencyAtSite   string         `json:"order_frequency_at_site,omitempty"`
	Loyalty                *LoyaltyInfo   `json:"loyalty,omitempty"`
}

// LoyaltyInfo contains loyalty program information
type LoyaltyInfo struct {
	LoyaltyID string `json:"loyalty_id"`
}

// OrdersListResponse represents the response from listing orders
type OrdersListResponse struct {
	Orders []Order `json:"orders"`
	Pagination *Pagination `json:"pagination,omitempty"`
}

// Pagination contains pagination information
type Pagination struct {
	NextCursor string `json:"next_cursor,omitempty"`
	HasMore    bool   `json:"has_more"`
}

// AcknowledgeOrderRequest represents the request to acknowledge an order
type AcknowledgeOrderRequest struct {
	AcknowledgedAt time.Time `json:"acknowledged_at"`
}

// AcceptOrderRequest represents the request to accept an order
type AcceptOrderRequest struct {
	AcceptedAt          time.Time `json:"accepted_at"`
	PreparationMinutes  int       `json:"preparation_minutes"`
}

// UpdateOrderStatusRequest represents the request to update order status
type UpdateOrderStatusRequest struct {
	Status   OrderStatus `json:"status"`
	ReadyAt  *time.Time  `json:"ready_at,omitempty"`
	PickupAt *time.Time  `json:"pickup_at,omitempty"`
}

// MenuUploadRequest represents the menu upload structure
type MenuUploadRequest struct {
	Name    string       `json:"name"`
	Menu    MenuContent  `json:"menu"`
	SiteIDs []string     `json:"site_ids"`
}

// MenuContent contains the complete menu structure
type MenuContent struct {
	Categories    []Category    `json:"categories"`
	Items         []Item        `json:"items"`
	Mealtimes     []Mealtime    `json:"mealtimes,omitempty"`
	Modifiers     []Modifier    `json:"modifiers,omitempty"`
	Experience    string        `json:"experience,omitempty"`
}

// Mealtime represents a time-based menu grouping
type Mealtime struct {
	ID              string                 `json:"id"`
	Name            map[string]string      `json:"name"`
	Description     map[string]string      `json:"description,omitempty"`
	Image           *ImageInfo             `json:"image"`
	CategoryIDs     []string               `json:"category_ids"`
	Schedule        []MealtimeSchedule     `json:"schedule"`
	SEODescription  map[string]string      `json:"seo_description,omitempty"`
}

// MealtimeSchedule defines when a mealtime is available
type MealtimeSchedule struct {
	DayOfWeek   int            `json:"day_of_week"`
	TimePeriods []TimePeriod   `json:"time_periods"`
}

// TimePeriod defines a time range
type TimePeriod struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// Modifier represents a modifier group
type Modifier struct {
	ID           string            `json:"id"`
	Name         map[string]string `json:"name"`
	Description  map[string]string `json:"description,omitempty"`
	ItemIDs      []string          `json:"item_ids"`
	MinSelection int               `json:"min_selection"`
	MaxSelection int               `json:"max_selection"`
	Repeatable   bool              `json:"repeatable"`
}

// ImageInfo contains image URL information
type ImageInfo struct {
	URL string `json:"url,omitempty"`
}

// PriceInfo contains pricing details for an item
type PriceInfo struct {
	Price     int              `json:"price"`
	Overrides []PriceOverride  `json:"overrides,omitempty"`
	Fees      []Fee            `json:"fees,omitempty"`
}

// PriceOverride represents a context-specific price
type PriceOverride struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Price int    `json:"price"`
}

// Fee represents an additional fee
type Fee struct {
	Type   string `json:"type"`
	Amount int    `json:"amount"`
}

// NutritionalInfo contains nutritional information
type NutritionalInfo struct {
	EnergyKcal *EnergyRange `json:"energy_kcal,omitempty"`
	HFSS       *bool        `json:"hfss,omitempty"`
}

// EnergyRange represents a range of calorie values
type EnergyRange struct {
	Low  int `json:"low"`
	High int `json:"high"`
}