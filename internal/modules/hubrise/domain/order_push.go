package domain

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Codes stored in hubrise_orders.needs_attention. They are an API of the dashboard: add, never
// rename.
const (
	// AttentionUnlinkedItem: an item or option of the order has no HubRise ref, so the EPOS may
	// ignore it.
	AttentionUnlinkedItem = "UNLINKED_ITEM"
	// AttentionTotalMismatch: the total HubRise calculated differs from the local total.
	AttentionTotalMismatch = "TOTAL_MISMATCH"
	// AttentionPushParked: the order could not be pushed (HubRise refused it, or it grew too old).
	AttentionPushParked = "PUSH_PARKED"
	// AttentionNotAccepted: the order is still "new" at HubRise after the delay.
	AttentionNotAccepted = "NOT_ACCEPTED"
	// AttentionRemoteMissing: HubRise no longer knows the order.
	AttentionRemoteMissing = "REMOTE_MISSING"
	// AttentionRemoteCancelledPaid: the order was cancelled or rejected at HubRise but it was paid
	// online and the connection does not refund by itself. Staff cancel it in the dashboard, which
	// refunds the customer.
	AttentionRemoteCancelledPaid = "REMOTE_CANCELLED_PAID"
	// AttentionRemoteCancelFailed: the order was cancelled at HubRise and the shop could not cancel
	// it (the payment cannot be refunded through Mollie, or the cancel keeps failing).
	AttentionRemoteCancelFailed = "REMOTE_CANCEL_FAILED"
	// AttentionDeliveryFailed: the delivery failed according to HubRise. Staff decide what happens
	// to the order and to the payment.
	AttentionDeliveryFailed = "DELIVERY_FAILED"
	// AttentionStatusConflict: HubRise moved an order the shop had already finished. The shop keeps
	// its status.
	AttentionStatusConflict = "STATUS_CONFLICT"
)

// Local order statuses, as stored in orders.order_status. The hubrise module does not import the
// order module, so it names the values it relies on here.
const (
	LocalPending        = "PENDING"
	LocalConfirmed      = "CONFIRMED"
	LocalPreparing      = "PREPARING"
	LocalAwaitingPickUp = "AWAITING_PICK_UP"
	LocalPickedUp       = "PICKED_UP"
	LocalOutForDelivery = "OUT_FOR_DELIVERY"
	LocalDelivered      = "DELIVERED"
	LocalCancelled      = "CANCELLED"
	LocalFailed         = "FAILED"
)

// Local order types, as stored in orders.order_type.
const (
	TypePickUp   = "PICKUP"
	TypeDelivery = "DELIVERY"
)

// Channels an order can come from (orders.channel). NULL on old orders reads as web.
const (
	ChannelWeb = "web"
	ChannelApp = "app"
)

// IsTerminalLocal reports whether a local status ends the life of an order.
func IsTerminalLocal(status string) bool {
	switch status {
	case LocalPickedUp, LocalDelivered, LocalCancelled, LocalFailed:
		return true
	}
	return false
}

// PayloadVersion is the version of the order_create payload stored in the outbox.
const PayloadVersion = 1

// CreatePayload is the part of an order_create that is frozen when the order becomes actionable:
// the items with their names and base prices, the charges and discounts, the customer and the notes,
// so a price edit seconds later cannot change what the kitchen sees. The parts that must be current
// at send time are NOT here: the SKU and option refs (the link table, an adoption done meanwhile is
// picked up), the status, the expected time check and the charge refs.
type CreatePayload struct {
	Version int `json:"v"`
	// Ref is the short human reference of the order.
	Ref string `json:"ref"`
	// OrderChannel is orders.channel ("web", "app", or empty for an old order).
	OrderChannel string `json:"order_channel,omitempty"`
	Online       bool   `json:"online"`
	// ServiceType is the local order type (PICKUP or DELIVERY).
	ServiceType string `json:"service_type"`
	// PreferredReadyTime is the time the customer asked for; nil means as soon as possible.
	PreferredReadyTime *time.Time `json:"preferred_ready_time,omitempty"`
	CustomerNotes      string     `json:"customer_notes,omitempty"`
	CouponCode         string     `json:"coupon_code,omitempty"`
	// Total is the local total_price; HubRise's calculated total must equal it.
	Total           decimal.Decimal  `json:"total"`
	MolliePaymentID string           `json:"mollie_payment_id,omitempty"`
	UserID          uuid.UUID        `json:"user_id"`
	Customer        *PayloadCustomer `json:"customer,omitempty"`
	Items           []PayloadItem    `json:"items"`
	// Charges and Discounts include the rounding line that closes the gap to Total.
	Charges   []PayloadAdjustment `json:"charges,omitempty"`
	Discounts []PayloadAdjustment `json:"discounts,omitempty"`
}

// PayloadCustomer is the customer data of an order.
type PayloadCustomer struct {
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
	Email     string `json:"email,omitempty"`
	Phone     string `json:"phone,omitempty"`
	// The delivery address, for a delivery order only.
	Address1      string `json:"address_1,omitempty"`
	PostalCode    string `json:"postal_code,omitempty"`
	City          string `json:"city,omitempty"`
	Latitude      string `json:"latitude,omitempty"`
	Longitude     string `json:"longitude,omitempty"`
	DeliveryNotes string `json:"delivery_notes,omitempty"`
}

// PayloadItem is one line of the order as HubRise will get it. Quantity is the number of units; the
// options are per unit, and an option chosen several times per unit appears several times.
type PayloadItem struct {
	ProductID   uuid.UUID       `json:"product_id"`
	ProductName string          `json:"product_name"`
	Price       decimal.Decimal `json:"price"`
	Quantity    int             `json:"quantity"`
	// TaxRate is a percent decimal ("6").
	TaxRate string          `json:"tax_rate,omitempty"`
	Options []PayloadOption `json:"options,omitempty"`
}

// PayloadOption is one option of one unit of an item.
type PayloadOption struct {
	GroupID  uuid.UUID       `json:"group_id"`
	ChoiceID uuid.UUID       `json:"choice_id"`
	ListName string          `json:"list_name"`
	Name     string          `json:"name"`
	Price    decimal.Decimal `json:"price"`
}

// Kinds of charges and discounts a payload can carry. The worker maps them to refs at send time.
const (
	AdjustmentDelivery   = "delivery"
	AdjustmentPaymentFee = "payment_fee"
	AdjustmentTakeaway   = "takeaway"
	AdjustmentCoupon     = "coupon"
	AdjustmentRounding   = "rounding"
)

// PayloadAdjustment is a charge or a discount; Amount is positive in both cases.
type PayloadAdjustment struct {
	Kind   string          `json:"kind"`
	Name   string          `json:"name"`
	Ref    string          `json:"ref,omitempty"`
	Amount decimal.Decimal `json:"amount"`
}

// StatusPayload is the payload of an order_status row: the local status the transition led to,
// frozen when it happened.
type StatusPayload struct {
	Status string `json:"status"`
}

// Marshal encodes the payload for the outbox.
func (p CreatePayload) Marshal() (json.RawMessage, error) { return json.Marshal(p) }

// OrderSnapshot is everything the push payload needs to know about an order, read from the shop's
// own tables by an OrderSource.
type OrderSnapshot struct {
	OrderID            uuid.UUID
	UserID             uuid.UUID
	Status             string
	Type               string
	IsOnlinePayment    bool
	IsTest             bool
	Channel            string
	CreatedAt          time.Time
	PreferredReadyTime *time.Time
	Note               string
	Extras             []OrderExtra
	CouponCode         string
	CashAmount         *decimal.Decimal
	DeliveryFee        decimal.Decimal
	TransactionFee     decimal.Decimal
	TakeawayDiscount   decimal.Decimal
	CouponDiscount     decimal.Decimal
	TotalPrice         decimal.Decimal
	// MolliePaymentID is the external id of the order's Mollie payment, empty when it has none.
	MolliePaymentID string

	StreetName   string
	HouseNumber  string
	BoxNumber    string
	Postcode     string
	Municipality string
	AddressLat   *float64
	AddressLng   *float64
	AddressExtra string
	FirstName    string
	LastName     string
	Email        string
	Phone        string
	Lines        []SnapshotLine
}

// OrderExtra is a chosen extra of the order (chopsticks, sauces).
type OrderExtra struct {
	Name    string   `json:"name"`
	Options []string `json:"options,omitempty"`
}

// SnapshotLine is one order line with the CURRENT catalog data next to the stored figures.
type SnapshotLine struct {
	ProductID   uuid.UUID
	ProductName string
	// BasePrice is the product's price today.
	BasePrice decimal.Decimal
	Quantity  int64
	// TotalPrice is what the customer was charged for the line (the authority).
	TotalPrice decimal.Decimal
	// VatRate is the stored percent (6.00).
	VatRate    decimal.Decimal
	Selections []SnapshotSelection
}

// SnapshotSelection is a chosen option of a line. Quantity is the total for the whole line.
type SnapshotSelection struct {
	GroupID   uuid.UUID
	ChoiceID  uuid.UUID
	GroupName string
	Name      string
	Modifier  decimal.Decimal
	Quantity  int
}

// OrderState is the current state of an order, read at send time.
type OrderState struct {
	Status string
	Type   string
	Total  decimal.Decimal
	// OnlinePaid is true when a Mollie payment of the order was paid: cancelling the order moves
	// money.
	OnlinePaid         bool
	EstimatedReadyTime *time.Time
}

// OpenLink is a pushed, still open order seen by the reconciler.
type OpenLink struct {
	OrderID          uuid.UUID
	HubriseOrderID   string
	LocalStatus      string
	LocalTotal       decimal.Decimal
	LastRemoteStatus string
	NeedsAttention   string
	PushedAt         *time.Time
}

// StatusPair maps a local status to the HubRise status that mirrors it.
type StatusPair struct {
	Local  string
	Remote string
}

// OrderSource reads the shop's orders for the integration. The hubrise module owns no order data:
// the implementation reads the shop's tables, so the module needs no import of the order module.
type OrderSource interface {
	// Snapshot returns the order with its lines; ErrNotFound when it does not exist.
	Snapshot(ctx context.Context, orderID uuid.UUID) (*OrderSnapshot, error)
	// State returns the current status of an order; ErrNotFound when it does not exist.
	State(ctx context.Context, orderID uuid.UUID) (*OrderState, error)
	// OpenUnpushed lists the actionable open orders created at or after createdAfter that have no
	// order link: not a test order, not terminal, and, for an online order, paid. Oldest first.
	OpenUnpushed(ctx context.Context, createdAfter time.Time, limit int) ([]uuid.UUID, error)
	// StatusMismatches lists the pushed orders whose local status is not what the last remote
	// status says, according to pairs, that have no queued, sending or parked status or create row
	// and whose remote status is not final at HubRise.
	StatusMismatches(ctx context.Context, pairs []StatusPair, limit int) ([]OpenLink, error)
	// RefreshCandidates lists open pushed orders, the ones read from HubRise longest ago first.
	RefreshCandidates(ctx context.Context, limit int) ([]OpenLink, error)
	// StuckNew lists open pushed orders still "new" at HubRise since before the cutoff that staff
	// were not told about yet (see OrderLinkRepository.MarkStuckAlerted) and that carry no flag
	// other than a lesser one (UNLINKED_ITEM, TOTAL_MISMATCH) or NOT_ACCEPTED itself (flagged, alert
	// still to go out).
	StuckNew(ctx context.Context, pushedBefore time.Time, limit int) ([]OpenLink, error)
}
