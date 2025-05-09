package domain

import (
	"encoding/json"
	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/shopspring/decimal"
	"time"

	"github.com/google/uuid"
)

type MolliePayment struct {
	ID                              uuid.UUID          `db:"id" json:"id"`
	Resource                        *string            `db:"resource" json:"resource,omitempty"`
	MolliePaymentID                 string             `db:"mollie_payment_id" json:"molliePaymentId"`
	Status                          mollie.OrderStatus `db:"status" json:"status"`
	Description                     *string            `db:"description" json:"description,omitempty"`
	CancelURL                       *string            `db:"cancel_url" json:"cancelUrl,omitempty"`
	WebhookURL                      *string            `db:"webhook_url" json:"webhookUrl,omitempty"`
	CountryCode                     *string            `db:"country_code" json:"countryCode,omitempty"`
	RestrictPaymentMethodsToCountry *string            `db:"restrict_payment_methods_to_country" json:"restrictPaymentMethodsToCountry,omitempty"`
	ProfileID                       *string            `db:"profile_id" json:"profileId,omitempty"`
	SettlementID                    *string            `db:"settlement_id" json:"settlementId,omitempty"`
	OrderID                         uuid.UUID          `db:"order_id" json:"orderId"`
	IsCancelable                    bool               `db:"is_cancelable" json:"isCancelable"`
	Mode                            *string            `db:"mode" json:"mode,omitempty"`
	Locale                          *string            `db:"locale" json:"locale,omitempty"`
	Method                          *string            `db:"method" json:"method,omitempty"`
	Metadata                        json.RawMessage    `db:"metadata" json:"metadata,omitempty"`
	Links                           json.RawMessage    `db:"links" json:"links,omitempty"`
	CreatedAt                       time.Time          `db:"created_at" json:"createdAt"`
	AuthorizedAt                    *time.Time         `db:"authorized_at" json:"authorizedAt,omitempty"`
	PaidAt                          *time.Time         `db:"paid_at" json:"paidAt,omitempty"`
	CanceledAt                      *time.Time         `db:"canceled_at" json:"canceledAt,omitempty"`
	ExpiresAt                       *time.Time         `db:"expires_at" json:"expiresAt,omitempty"`
	ExpiredAt                       *time.Time         `db:"expired_at" json:"expiredAt,omitempty"`
	FailedAt                        *time.Time         `db:"failed_at" json:"failedAt,omitempty"`
	Amount                          decimal.Decimal    `db:"amount" json:"amount"`
	AmountRefunded                  decimal.Decimal    `db:"amount_refunded" json:"amountRefunded"`
	AmountRemaining                 decimal.Decimal    `db:"amount_remaining" json:"amountRemaining"`
	AmountCaptured                  decimal.Decimal    `db:"amount_captured" json:"amountCaptured"`
	AmountChargedBack               decimal.Decimal    `db:"amount_charged_back" json:"amountChargedBack"`
	SettlementAmount                decimal.Decimal    `db:"settlement_amount" json:"settlementAmount"`
}

type PaymentLinks struct {
	Checkout struct {
		Href string `json:"href"`
		Type string `json:"type"`
	} `json:"checkout,omitempty"`

	Self struct {
		Href string `json:"href"`
		Type string `json:"type"`
	} `json:"self,omitempty"`
}
