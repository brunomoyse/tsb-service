package domain

import (
	"cmp"
	"encoding/json"
	"time"
	"tsb-service/pkg/types"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type OrderStatus string
type OrderType string
type OrderCancellationReason string

const (
	OrderStatusPending        OrderStatus = "PENDING"
	OrderStatusConfirmed      OrderStatus = "CONFIRMED"
	OrderStatusPreparing      OrderStatus = "PREPARING"
	OrderStatusAwaitingUp     OrderStatus = "AWAITING_PICK_UP"
	OrderStatusPickedUp       OrderStatus = "PICKED_UP"
	OrderStatusOutForDelivery OrderStatus = "OUT_FOR_DELIVERY"
	OrderStatusDelivered      OrderStatus = "DELIVERED"
	OrderStatusCanceled       OrderStatus = "CANCELLED"
	OrderStatusFailed         OrderStatus = "FAILED"
)

const (
	OrderTypeDelivery OrderType = "DELIVERY"
	OrderTypePickUp   OrderType = "PICKUP"
)

const (
	OrderCancellationReasonOutOfStock    OrderCancellationReason = "OUT_OF_STOCK"
	OrderCancellationReasonKitchenClosed OrderCancellationReason = "KITCHEN_CLOSED"
	OrderCancellationReasonDeliveryArea  OrderCancellationReason = "DELIVERY_AREA"
	OrderCancellationReasonOther         OrderCancellationReason = "OTHER"
)

type Order struct {
	ID                 uuid.UUID        `db:"id" json:"id"`
	CreatedAt          time.Time        `db:"created_at" json:"createdAt"`
	UpdatedAt          time.Time        `db:"updated_at" json:"updatedAt"`
	UserID             uuid.UUID        `db:"user_id" json:"userId"`
	OrderStatus        OrderStatus      `db:"order_status" json:"orderStatus"`
	OrderType          OrderType        `db:"order_type" json:"orderType"`
	IsOnlinePayment    bool             `db:"is_online_payment" json:"isOnlinePayment"`
	PaymentID          *uuid.UUID       `db:"payment_id" json:"paymentId,omitempty"`
	TakeawayDiscount   decimal.Decimal  `db:"takeaway_discount" json:"takeawayDiscount"`
	CouponDiscount     decimal.Decimal  `db:"coupon_discount" json:"couponDiscount"`
	DeliveryFee        *decimal.Decimal `db:"delivery_fee" json:"deliveryFee,omitempty"`
	TotalPrice         decimal.Decimal  `db:"total_price" json:"totalPrice"`
	PreferredReadyTime *time.Time       `db:"preferred_ready_time" json:"preferredReadyTime,omitempty"`
	EstimatedReadyTime *time.Time       `db:"estimated_ready_time" json:"estimatedReadyTime,omitempty"`
	AddressID          *string          `db:"address_id" json:"addressId,omitempty"`
	AddressExtra       *string          `db:"address_extra" json:"addressExtra,omitempty"`
	OrderNote          *string          `db:"order_note" json:"orderNote,omitempty"`
	OrderExtra         types.NullableJSON     `db:"order_extra" json:"orderExtras,omitempty"`
	Language           string           `db:"language" json:"language"`
	CouponCode         *string          `db:"coupon_code" json:"couponCode,omitempty"`
	// Denormalized address fields (snapshot at order time)
	StreetID         *string  `db:"street_id" json:"streetId,omitempty"`
	StreetName       *string  `db:"street_name" json:"streetName,omitempty"`
	HouseNumber      *string  `db:"house_number" json:"houseNumber,omitempty"`
	BoxNumber        *string  `db:"box_number" json:"boxNumber,omitempty"`
	MunicipalityName *string  `db:"municipality_name" json:"municipalityName,omitempty"`
	Postcode         *string  `db:"postcode" json:"postcode,omitempty"`
	AddressDistance  *float64 `db:"address_distance" json:"addressDistance,omitempty"`
	IsManualAddress  bool     `db:"is_manual_address" json:"isManualAddress"`
	AddressPlaceID   *string  `db:"address_place_id" json:"addressPlaceId,omitempty"`
	AddressLat       *float64 `db:"address_lat" json:"addressLat,omitempty"`
	AddressLng       *float64 `db:"address_lng" json:"addressLng,omitempty"`
	CancellationReason *OrderCancellationReason `db:"cancellation_reason" json:"cancellationReason,omitempty"`
}

type OrderStatusHistory struct {
	ID        uuid.UUID   `db:"id" json:"id"`
	OrderID   uuid.UUID   `db:"order_id" json:"orderId"`
	Status    OrderStatus `db:"status" json:"status"`
	ChangedAt time.Time   `db:"changed_at" json:"changedAt"`
}

type OrderExtra struct {
	// Name of the extra, e.g., "chopsticks" or "sauce".
	Name string `json:"name"`
	// Options for this extra, e.g., for sauce: ["sweet"], ["salty"] or ["both"].
	Options []string `json:"options,omitempty"`
}

type OrderProductRaw struct {
	ProductID       uuid.UUID       `db:"product_id" json:"productId"`
	Quantity        int64           `db:"quantity" json:"quantity"`
	UnitPrice       decimal.Decimal `db:"unit_price" json:"unitPrice"`
	TotalPrice      decimal.Decimal `db:"total_price" json:"totalPrice"`
	ProductChoiceID *uuid.UUID      `db:"product_choice_id" json:"productChoiceId,omitempty"`
}

type OrderProduct struct {
	Product    Product         `json:"product"`
	Quantity   int64           `json:"quantity"`
	UnitPrice  decimal.Decimal `json:"unitPrice"`
	TotalPrice decimal.Decimal `json:"totalPrice"`
}

type Product struct {
	ID           uuid.UUID `json:"id"`
	Code         *string   `json:"code"`
	CategoryName string    `json:"categoryName"`
	Name         string    `json:"name"`
}

// CustomerStatsRow holds aggregated order statistics for a single customer.
type CustomerStatsRow struct {
	UserID         uuid.UUID       `db:"user_id"`
	FirstName      string          `db:"first_name"`
	LastName       string          `db:"last_name"`
	Email          string          `db:"email"`
	PhoneNumber    *string         `db:"phone_number"`
	RegisteredAt   time.Time       `db:"registered_at"`
	TotalOrders    int             `db:"total_orders"`
	TotalAmount    decimal.Decimal `db:"total_amount"`
	AverageAmount  decimal.Decimal `db:"average_amount"`
	FirstOrderDate time.Time       `db:"first_order_date"`
	LastOrderDate  time.Time       `db:"last_order_date"`
	DeliveryCount  int             `db:"delivery_count"`
	PickupCount    int             `db:"pickup_count"`
}

// NewOrder is a constructor function that creates a new Order domain object.
// Prices will be set later in the service layer.
// DiscountAmount returns the total discount (takeaway + coupon).
func (o *Order) DiscountAmount() decimal.Decimal {
	return o.TakeawayDiscount.Add(o.CouponDiscount)
}

// AddressSnapshot holds the denormalized address fields for an order.
type AddressSnapshot struct {
	StreetID         *string
	StreetName       *string
	HouseNumber      *string
	BoxNumber        *string
	MunicipalityName *string
	Postcode         *string
	Distance         *float64
	PlaceID          *string
	Lat              *float64
	Lng              *float64
	IsManual         bool
}

func NewOrder(
	userID uuid.UUID,
	orderType OrderType,
	isOnlinePayment bool,
	addressID *string,
	addressExtra *string,
	orderNote *string,
	preferredReadyTime *time.Time,
	orderExtra []OrderExtra,
	deliveryFee *decimal.Decimal,
	takeawayDiscount decimal.Decimal,
	couponDiscount decimal.Decimal,
	language string,
	addrSnapshot *AddressSnapshot,
) *Order {
	var orderExtraJSON types.NullableJSON
	if len(orderExtra) > 0 {
		jsonBytes, _ := json.Marshal(orderExtra)
		orderExtraJSON = types.NullableJSON(jsonBytes)
	}

	language = cmp.Or(language, "fr")

	o := &Order{
		ID:                 uuid.Nil,
		UserID:             userID,
		OrderStatus:        OrderStatusPending,
		OrderType:          orderType,
		IsOnlinePayment:    isOnlinePayment,
		AddressID:          addressID,
		AddressExtra:       addressExtra,
		OrderNote:          orderNote,
		PreferredReadyTime: preferredReadyTime,
		OrderExtra:         orderExtraJSON,
		DeliveryFee:        deliveryFee,
		TakeawayDiscount:   takeawayDiscount,
		CouponDiscount:     couponDiscount,
		Language:           language,
	}

	if addrSnapshot != nil {
		o.StreetID = addrSnapshot.StreetID
		o.StreetName = addrSnapshot.StreetName
		o.HouseNumber = addrSnapshot.HouseNumber
		o.BoxNumber = addrSnapshot.BoxNumber
		o.MunicipalityName = addrSnapshot.MunicipalityName
		o.Postcode = addrSnapshot.Postcode
		o.AddressDistance = addrSnapshot.Distance
		o.AddressPlaceID = addrSnapshot.PlaceID
		o.AddressLat = addrSnapshot.Lat
		o.AddressLng = addrSnapshot.Lng
		o.IsManualAddress = addrSnapshot.IsManual
	}

	return o
}

