// Code generated by github.com/99designs/gqlgen, DO NOT EDIT.

package model

import (
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/google/uuid"
)

type Address struct {
	ID               string  `json:"id"`
	Postcode         string  `json:"postcode"`
	MunicipalityName string  `json:"municipalityName"`
	StreetName       string  `json:"streetName"`
	HouseNumber      string  `json:"houseNumber"`
	BoxNumber        *string `json:"boxNumber,omitempty"`
	Distance         float64 `json:"distance"`
}

type Order struct {
	ID                 uuid.UUID       `json:"id"`
	CreatedAt          time.Time       `json:"createdAt"`
	UpdatedAt          time.Time       `json:"updatedAt"`
	Status             OrderStatusEnum `json:"status"`
	Type               OrderTypeEnum   `json:"type"`
	IsOnlinePayment    bool            `json:"isOnlinePayment"`
	DiscountAmount     string          `json:"discountAmount"`
	DeliveryFee        string          `json:"deliveryFee"`
	TotalPrice         string          `json:"totalPrice"`
	EstimatedReadyTime *time.Time      `json:"estimatedReadyTime,omitempty"`
	AddressExtra       *string         `json:"addressExtra,omitempty"`
	OrderNote          *string         `json:"orderNote,omitempty"`
	OrderExtra         map[string]any  `json:"orderExtra,omitempty"`
	Address            *Address        `json:"address,omitempty"`
	Customer           *User           `json:"customer"`
	Payment            *Payment        `json:"payment,omitempty"`
	Items              []*OrderItem    `json:"items"`
}

type OrderItem struct {
	Product    *Product  `json:"product"`
	ProductID  uuid.UUID `json:"productID"`
	UnitPrice  string    `json:"unitPrice"`
	Quantity   int       `json:"quantity"`
	TotalPrice string    `json:"totalPrice"`
}

type Payment struct {
	ID                              uuid.UUID      `json:"id"`
	Resource                        *string        `json:"resource,omitempty"`
	MolliePaymentID                 string         `json:"mollie_payment_id"`
	Status                          string         `json:"status"`
	Description                     *string        `json:"description,omitempty"`
	CancelURL                       *string        `json:"cancel_url,omitempty"`
	WebhookURL                      *string        `json:"webhook_url,omitempty"`
	CountryCode                     *string        `json:"country_code,omitempty"`
	RestrictPaymentMethodsToCountry *string        `json:"restrict_payment_methods_to_country,omitempty"`
	ProfileID                       *string        `json:"profile_id,omitempty"`
	SettlementID                    *string        `json:"settlement_id,omitempty"`
	OrderID                         uuid.UUID      `json:"order_id"`
	IsCancelable                    bool           `json:"is_cancelable"`
	Mode                            *string        `json:"mode,omitempty"`
	Locale                          *string        `json:"locale,omitempty"`
	Method                          *string        `json:"method,omitempty"`
	Metadata                        map[string]any `json:"metadata,omitempty"`
	Links                           map[string]any `json:"links,omitempty"`
	CreatedAt                       time.Time      `json:"created_at"`
	AuthorizedAt                    *time.Time     `json:"authorized_at,omitempty"`
	PaidAt                          *time.Time     `json:"paid_at,omitempty"`
	CanceledAt                      *time.Time     `json:"canceled_at,omitempty"`
	ExpiresAt                       *time.Time     `json:"expires_at,omitempty"`
	ExpiredAt                       *time.Time     `json:"expired_at,omitempty"`
	FailedAt                        *time.Time     `json:"failed_at,omitempty"`
	Amount                          *float64       `json:"amount,omitempty"`
	AmountRefunded                  *float64       `json:"amount_refunded,omitempty"`
	AmountRemaining                 *float64       `json:"amount_remaining,omitempty"`
	AmountCaptured                  *float64       `json:"amount_captured,omitempty"`
	AmountChargedBack               *float64       `json:"amount_charged_back,omitempty"`
	SettlementAmount                *float64       `json:"settlement_amount,omitempty"`
}

type Product struct {
	ID           uuid.UUID        `json:"id"`
	CreatedAt    time.Time        `json:"createdAt"`
	Price        string           `json:"price"`
	Code         *string          `json:"code,omitempty"`
	Slug         string           `json:"slug"`
	PieceCount   *int             `json:"pieceCount,omitempty"`
	IsVisible    bool             `json:"isVisible"`
	IsAvailable  bool             `json:"isAvailable"`
	IsHalal      bool             `json:"isHalal"`
	IsVegan      bool             `json:"isVegan"`
	Name         string           `json:"name"`
	Description  *string          `json:"description,omitempty"`
	Category     *ProductCategory `json:"category"`
	Translations []*Translation   `json:"translations"`
}

type ProductCategory struct {
	ID           uuid.UUID      `json:"id"`
	Order        int            `json:"order"`
	Name         string         `json:"name"`
	Products     []*Product     `json:"products"`
	Translations []*Translation `json:"translations"`
}

type Query struct {
}

type Street struct {
	ID               string `json:"id"`
	StreetName       string `json:"streetName"`
	MunicipalityName string `json:"municipalityName"`
	Postcode         string `json:"postcode"`
}

type Translation struct {
	Language    string  `json:"language"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
}

type User struct {
	ID          uuid.UUID `json:"id"`
	Email       string    `json:"email"`
	FirstName   string    `json:"firstName"`
	LastName    string    `json:"lastName"`
	PhoneNumber *string   `json:"phoneNumber,omitempty"`
	Address     *Address  `json:"address,omitempty"`
	Orders      []*Order  `json:"orders,omitempty"`
}

type OrderStatusEnum string

const (
	OrderStatusEnumPending    OrderStatusEnum = "PENDING"
	OrderStatusEnumProcessing OrderStatusEnum = "PROCESSING"
	OrderStatusEnumCompleted  OrderStatusEnum = "COMPLETED"
	OrderStatusEnumCanceled   OrderStatusEnum = "CANCELED"
)

var AllOrderStatusEnum = []OrderStatusEnum{
	OrderStatusEnumPending,
	OrderStatusEnumProcessing,
	OrderStatusEnumCompleted,
	OrderStatusEnumCanceled,
}

func (e OrderStatusEnum) IsValid() bool {
	switch e {
	case OrderStatusEnumPending, OrderStatusEnumProcessing, OrderStatusEnumCompleted, OrderStatusEnumCanceled:
		return true
	}
	return false
}

func (e OrderStatusEnum) String() string {
	return string(e)
}

func (e *OrderStatusEnum) UnmarshalGQL(v any) error {
	str, ok := v.(string)
	if !ok {
		return fmt.Errorf("enums must be strings")
	}

	*e = OrderStatusEnum(str)
	if !e.IsValid() {
		return fmt.Errorf("%s is not a valid OrderStatusEnum", str)
	}
	return nil
}

func (e OrderStatusEnum) MarshalGQL(w io.Writer) {
	fmt.Fprint(w, strconv.Quote(e.String()))
}

type OrderTypeEnum string

const (
	OrderTypeEnumDelivery OrderTypeEnum = "DELIVERY"
	OrderTypeEnumPickup   OrderTypeEnum = "PICKUP"
)

var AllOrderTypeEnum = []OrderTypeEnum{
	OrderTypeEnumDelivery,
	OrderTypeEnumPickup,
}

func (e OrderTypeEnum) IsValid() bool {
	switch e {
	case OrderTypeEnumDelivery, OrderTypeEnumPickup:
		return true
	}
	return false
}

func (e OrderTypeEnum) String() string {
	return string(e)
}

func (e *OrderTypeEnum) UnmarshalGQL(v any) error {
	str, ok := v.(string)
	if !ok {
		return fmt.Errorf("enums must be strings")
	}

	*e = OrderTypeEnum(str)
	if !e.IsValid() {
		return fmt.Errorf("%s is not a valid OrderTypeEnum", str)
	}
	return nil
}

func (e OrderTypeEnum) MarshalGQL(w io.Writer) {
	fmt.Fprint(w, strconv.Quote(e.String()))
}
