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

// ============================================================================
// Webhook Models
// ============================================================================

// OrderEventType represents the type of order event
type OrderEventType string

const (
	OrderEventNew          OrderEventType = "order.new"
	OrderEventStatusUpdate OrderEventType = "order.status_update"
)

// RiderEventType represents the type of rider event
type RiderEventType string

const (
	RiderEventStatusUpdate RiderEventType = "rider.status_update"
)

// OrderEventWebhook represents an incoming order event webhook
type OrderEventWebhook struct {
	Event OrderEventType     `json:"event"`
	Body  OrderEventBody     `json:"body"`
}

// OrderEventBody contains the order data in the webhook
type OrderEventBody struct {
	Order Order `json:"order"`
}

// RiderEventWebhook represents an incoming rider event webhook
type RiderEventWebhook struct {
	Event RiderEventType `json:"event"`
	Body  RiderEventBody `json:"body"`
}

// RiderEventBody contains rider status information
type RiderEventBody struct {
	OrderID     string       `json:"order_id"`
	StackedWith []string     `json:"stacked_with"`
	Riders      []RiderInfo  `json:"riders"`
}

// RiderStatus represents rider status values
type RiderStatus string

const (
	RiderAssigned              RiderStatus = "rider_assigned"
	RiderArrived               RiderStatus = "rider_arrived"
	RiderConfirmedAtRestaurant RiderStatus = "rider_confirmed_at_restaurant"
	RiderUnassigned            RiderStatus = "rider_unassigned"
	RiderInTransit             RiderStatus = "rider_in_transit"
)

// RiderInfo contains rider information
type RiderInfo struct {
	EstimatedArrivalTime string             `json:"estimated_arrival_time"`
	At                   string             `json:"at"`
	AccuracyInMeters     int                `json:"accuracy_in_meters"`
	Lat                  float64            `json:"lat"`
	Lon                  float64            `json:"lon"`
	FullName             string             `json:"full_name"`
	ContactNumber        string             `json:"contact_number"`
	BridgeCode           string             `json:"bridge_code"`
	BridgeNumber         string             `json:"bridge_number"`
	StatusLog            []RiderStatusLog   `json:"status_log"`
}

// RiderStatusLog represents a rider status change entry
type RiderStatusLog struct {
	At     string      `json:"at"`
	Status RiderStatus `json:"status"`
}

// ============================================================================
// Sync Status Models
// ============================================================================

// SyncStatus represents the sync status of an order
type SyncStatus string

const (
	SyncStatusSucceeded SyncStatus = "succeeded"
	SyncStatusFailed    SyncStatus = "failed"
)

// SyncStatusReason represents why a sync failed
type SyncStatusReason string

const (
	SyncReasonPriceMismatched       SyncStatusReason = "price_mismatched"
	SyncReasonPOSItemIDMismatched   SyncStatusReason = "pos_item_id_mismatched"
	SyncReasonPOSItemIDNotFound     SyncStatusReason = "pos_item_id_not_found"
	SyncReasonItemsOutOfStock       SyncStatusReason = "items_out_of_stock"
	SyncReasonLocationOffline       SyncStatusReason = "location_offline"
	SyncReasonLocationNotSupported  SyncStatusReason = "location_not_supported"
	SyncReasonUnsupportedOrderType  SyncStatusReason = "unsupported_order_type"
	SyncReasonNoWebhookURL          SyncStatusReason = "no_webhook_url"
	SyncReasonWebhookFailed         SyncStatusReason = "webhook_failed"
	SyncReasonTimedOut              SyncStatusReason = "timed_out"
	SyncReasonOther                 SyncStatusReason = "other"
	SyncReasonNoSyncConfirmation    SyncStatusReason = "no_sync_confirmation"
)

// CreateSyncStatusRequest represents the request to create a sync status
type CreateSyncStatusRequest struct {
	Status     SyncStatus       `json:"status"`
	Reason     *SyncStatusReason `json:"reason,omitempty"`
	Notes      *string          `json:"notes,omitempty"`
	OccurredAt time.Time        `json:"occurred_at"`
}

// ============================================================================
// Prep Stage Models
// ============================================================================

// PrepStage represents the preparation stage of an order
type PrepStage string

const (
	PrepStageInKitchen              PrepStage = "in_kitchen"
	PrepStageReadyForCollection     PrepStage = "ready_for_collection"
	PrepStageReadyForCollectionSoon PrepStage = "ready_for_collection_soon"
	PrepStageCollected              PrepStage = "collected"
)

// CreatePrepStageRequest represents the request to create a prep stage
type CreatePrepStageRequest struct {
	Stage      PrepStage `json:"stage"`
	OccurredAt time.Time `json:"occurred_at"`
	Delay      *int      `json:"delay,omitempty"` // Optional: 0, 2, 4, 6, 8, or 10
}

// ============================================================================
// Update Order Models (PATCH /v1/orders/{order_id})
// ============================================================================

// OrderUpdateStatus represents the status to update an order to
type OrderUpdateStatus string

const (
	OrderUpdateAccepted  OrderUpdateStatus = "accepted"
	OrderUpdateRejected  OrderUpdateStatus = "rejected"
	OrderUpdateConfirmed OrderUpdateStatus = "confirmed"
)

// RejectReason represents why an order was rejected
type RejectReason string

const (
	RejectReasonClosingEarly         RejectReason = "closing_early"
	RejectReasonBusy                 RejectReason = "busy"
	RejectReasonIngredientUnavailable RejectReason = "ingredient_unavailable"
	RejectReasonOther                RejectReason = "other"
)

// UpdateOrderRequest represents the request to update an order status
type UpdateOrderRequest struct {
	Status       OrderUpdateStatus `json:"status"`
	RejectReason *RejectReason     `json:"reject_reason,omitempty"`
	Notes        *string           `json:"notes,omitempty"`
}

// ============================================================================
// V2 Orders API Models
// ============================================================================

// GetOrdersV2Request represents query parameters for V2 orders list
type GetOrdersV2Request struct {
	StartDate  *time.Time
	EndDate    *time.Time
	Cursor     *string
	LiveOrders bool
}

// GetOrdersV2Response represents the V2 orders list response
type GetOrdersV2Response struct {
	Orders []Order `json:"orders"`
	Next   *string `json:"next,omitempty"`
}

// ============================================================================
// Webhook Configuration Models
// ============================================================================

// WebhookConfig represents webhook configuration
type WebhookConfig struct {
	WebhookURL string `json:"webhook_url"`
}

// WebhookType represents the type of webhook
type WebhookType string

const (
	WebhookTypePOS             WebhookType = "POS"
	WebhookTypeOrderEvents     WebhookType = "ORDER_EVENTS"
	WebhookTypePOSAndOrderEvents WebhookType = "POS_AND_ORDER_EVENTS"
)

// SiteConfig represents site webhook configuration
type SiteConfig struct {
	LocationID          string      `json:"location_id"`
	Name                string      `json:"name,omitempty"`
	OrdersAPIWebhookType WebhookType `json:"orders_api_webhook_type"`
}

// SitesConfig represents multiple site configurations
type SitesConfig struct {
	Sites []SiteConfig `json:"sites"`
}

// ============================================================================
// Menu API - Item Unavailability Models
// ============================================================================

// UnavailabilityStatus represents the availability status of a menu item
type UnavailabilityStatus string

const (
	StatusAvailable   UnavailabilityStatus = "available"   // Item is visible and orderable
	StatusUnavailable UnavailabilityStatus = "unavailable" // Greyed out, "sold out for the day"
	StatusHidden      UnavailabilityStatus = "hidden"      // Completely hidden from menu
)

// ItemUnavailability represents the unavailability status of a single item
type ItemUnavailability struct {
	ItemID string               `json:"item_id"`
	Status UnavailabilityStatus `json:"status"`
}

// UpdateItemUnavailabilitiesRequest represents request to update individual items
type UpdateItemUnavailabilitiesRequest struct {
	ItemUnavailabilities []ItemUnavailability `json:"item_unavailabilities"`
}

// ReplaceAllUnavailabilitiesRequest represents request to replace all unavailabilities
type ReplaceAllUnavailabilitiesRequest struct {
	UnavailableIDs []string `json:"unavailable_ids"` // Items sold out for the day
	HiddenIDs      []string `json:"hidden_ids"`      // Items hidden indefinitely
}

// GetItemUnavailabilitiesResponse represents response from get unavailabilities
type GetItemUnavailabilitiesResponse struct {
	UnavailableIDs []string `json:"unavailable_ids"`
	HiddenIDs      []string `json:"hidden_ids"`
}

// ============================================================================
// Menu API - PLU (Price Look-Up) Models
// ============================================================================

// PLUMapping represents a mapping between a menu item and a POS PLU code
type PLUMapping struct {
	ItemID string `json:"item_id"` // Menu item ID
	PLU    string `json:"plu"`     // POS system identifier
}

// UpdatePLUsRequest represents request to update PLU mappings
type UpdatePLUsRequest []PLUMapping

// UpdatePLUsResponse represents response from PLU update
type UpdatePLUsResponse struct {
	Status string `json:"status"` // "OK"
}

// ============================================================================
// Menu API - V3 Async Upload Models
// ============================================================================

// MenuUploadURLResponse represents response from V3 menu upload URL request
type MenuUploadURLResponse struct {
	ID        string `json:"id"`         // Menu ID
	UploadURL string `json:"upload_url"` // Presigned S3 URL
	Version   string `json:"version"`    // Menu version
}

// JobAction represents the type of job to execute
type JobAction string

const (
	JobActionPublishMenuToLive JobAction = "publish_menu_to_live"
)

// PublishMenuJobRequest represents request to publish a menu
type PublishMenuJobRequest struct {
	Action JobAction               `json:"action"`
	Params PublishMenuJobParams    `json:"params"`
}

// PublishMenuJobParams represents parameters for menu publish job
type PublishMenuJobParams struct {
	BrandID string  `json:"brand_id"`
	MenuID  string  `json:"menu_id"`
	Version *string `json:"version,omitempty"` // Optional
}

// JobStatus represents the status of an async job
type JobStatus string

const (
	JobStatusPending   JobStatus = "pending"
	JobStatusRunning   JobStatus = "running"
	JobStatusCompleted JobStatus = "completed"
	JobStatusFailed    JobStatus = "failed"
)

// JobResponse represents response from job creation or status check
type JobResponse struct {
	ID     string    `json:"id"`
	Status JobStatus `json:"status"`
	Action JobAction `json:"action"`
	Params struct {
		BrandID string  `json:"brand_id"`
		MenuID  string  `json:"menu_id"`
		Version *string `json:"version,omitempty"`
	} `json:"params"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	Error     *string    `json:"error,omitempty"` // Present if status is failed
}

// MenuV3Response represents V3 menu metadata response
type MenuV3Response struct {
	ID        string `json:"id"`
	Version   string `json:"version"`
	UploadURL string `json:"upload_url,omitempty"`
}

// ============================================================================
// Menu API - Webhook Models
// ============================================================================

// MenuEventType represents the type of menu event
type MenuEventType string

const (
	MenuEventUploadResult MenuEventType = "menu.upload_result"
)

// MenuWebhookEvent represents an incoming menu webhook event
type MenuWebhookEvent struct {
	Event MenuEventType    `json:"event"`
	Body  MenuEventBody    `json:"body"`
}

// MenuEventBody contains the menu event data
type MenuEventBody struct {
	MenuUploadResult MenuUploadResult `json:"menu_upload_result"`
}

// MenuUploadResult represents the result of a menu upload
type MenuUploadResult struct {
	HTTPStatus int             `json:"http_status"` // 200=success, 400=invalid, 500=error
	BrandID    string          `json:"brand_id"`
	MenuID     string          `json:"menu_id"`
	SiteIDs    []string        `json:"site_ids"`
	Errors     []MenuError     `json:"errors,omitempty"`
}

// MenuError represents an error in menu upload
type MenuError struct {
	Code    string  `json:"code"`
	Message string  `json:"message"`
	Field   *string `json:"field,omitempty"`
}