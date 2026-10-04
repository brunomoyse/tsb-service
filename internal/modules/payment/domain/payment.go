package domain

import (
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// PaymentStatus represents the status of a Mollie payment (domain-owned, no SDK dependency).
type PaymentStatus string

const (
	PaymentStatusOpen       PaymentStatus = "open"
	PaymentStatusCanceled   PaymentStatus = "canceled"
	PaymentStatusPending    PaymentStatus = "pending"
	PaymentStatusAuthorized PaymentStatus = "authorized"
	PaymentStatusExpired    PaymentStatus = "expired"
	PaymentStatusFailed     PaymentStatus = "failed"
	PaymentStatusPaid       PaymentStatus = "paid"
)

// ErrPaymentNotRefundable means a paid payment cannot be refunded through Mollie: Mollie reports no
// refundable amount for it (vouchers, gift cards and other methods without API refunds, or an expired
// refund window) or refuses the refund. Retrying cannot help; the customer has to be refunded by
// other means. Returned wrapped, test with errors.Is.
var ErrPaymentNotRefundable = errors.New("payment cannot be refunded through Mollie")

// PaymentStatuses lists every status a payment can have (Mollie's payment statuses).
var PaymentStatuses = []PaymentStatus{
	PaymentStatusOpen, PaymentStatusCanceled, PaymentStatusPending, PaymentStatusAuthorized,
	PaymentStatusExpired, PaymentStatusFailed, PaymentStatusPaid,
}

// IsValid reports whether s is one of the known payment statuses (exact, lower-case match).
func (s PaymentStatus) IsValid() bool {
	return slices.Contains(PaymentStatuses, s)
}

// PaymentStatusUpdate carries the fields to update when refreshing a payment's status from Mollie.
type PaymentStatusUpdate struct {
	Status       PaymentStatus
	PaidAt       *time.Time
	AuthorizedAt *time.Time
	CanceledAt   *time.Time
	ExpiredAt    *time.Time
	FailedAt     *time.Time
}

// CancelSettlement is the outcome of undoing the payment of an order that staff cancelled.
type CancelSettlement struct {
	// Refunded is the amount refunded by this settlement (zero when it refunded nothing, e.g. because
	// an earlier attempt already did).
	Refunded decimal.Decimal
	// TotalRefunded is everything the customer has got back for this payment once the settlement is
	// through, including what an earlier attempt or staff in the Mollie dashboard refunded.
	TotalRefunded decimal.Decimal
	// StatusUpdate is the payment status to record once the cancelled order is saved (see
	// PaymentService.PersistPaymentStatus), nil when there is nothing to record. It is returned
	// rather than written by the settlement: recording "canceled" before the order is saved would
	// make Mollie's own canceled webhook look "already processed" and leave an order that could not
	// be saved active with a payment that can no longer be paid.
	StatusUpdate *PaymentStatusUpdate
}

// RefundNotice is the amount to tell the customer they got back, zero when there is nothing to tell.
// It is what this settlement refunded or, when it refunded nothing because a previous attempt did and
// then failed to save the order (so the customer was never told), everything refunded so far.
func (c CancelSettlement) RefundNotice() decimal.Decimal {
	if c.Refunded.IsPositive() {
		return c.Refunded
	}
	return c.TotalRefunded
}

type MolliePayment struct {
	ID                              uuid.UUID       `db:"id" json:"id"`
	Resource                        *string         `db:"resource" json:"resource,omitempty"`
	MolliePaymentID                 string          `db:"mollie_payment_id" json:"molliePaymentId"`
	Status                          PaymentStatus   `db:"status" json:"status"`
	Description                     *string         `db:"description" json:"description,omitempty"`
	CancelURL                       *string         `db:"cancel_url" json:"cancelUrl,omitempty"`
	WebhookURL                      *string         `db:"webhook_url" json:"webhookUrl,omitempty"`
	CountryCode                     *string         `db:"country_code" json:"countryCode,omitempty"`
	RestrictPaymentMethodsToCountry *string         `db:"restrict_payment_methods_to_country" json:"restrictPaymentMethodsToCountry,omitempty"`
	ProfileID                       *string         `db:"profile_id" json:"profileId,omitempty"`
	SettlementID                    *string         `db:"settlement_id" json:"settlementId,omitempty"`
	OrderID                         uuid.UUID       `db:"order_id" json:"orderId"`
	IsCancelable                    bool            `db:"is_cancelable" json:"isCancelable"`
	Mode                            *string         `db:"mode" json:"mode,omitempty"`
	Locale                          *string         `db:"locale" json:"locale,omitempty"`
	Method                          *string         `db:"method" json:"method,omitempty"`
	Metadata                        json.RawMessage `db:"metadata" json:"metadata,omitempty"`
	Links                           json.RawMessage `db:"links" json:"links,omitempty"`
	CreatedAt                       time.Time       `db:"created_at" json:"createdAt"`
	AuthorizedAt                    *time.Time      `db:"authorized_at" json:"authorizedAt,omitempty"`
	PaidAt                          *time.Time      `db:"paid_at" json:"paidAt,omitempty"`
	CanceledAt                      *time.Time      `db:"canceled_at" json:"canceledAt,omitempty"`
	ExpiresAt                       *time.Time      `db:"expires_at" json:"expiresAt,omitempty"`
	ExpiredAt                       *time.Time      `db:"expired_at" json:"expiredAt,omitempty"`
	FailedAt                        *time.Time      `db:"failed_at" json:"failedAt,omitempty"`
	Amount                          decimal.Decimal `db:"amount" json:"amount"`
	AmountRefunded                  decimal.Decimal `db:"amount_refunded" json:"amountRefunded"`
	AmountRemaining                 decimal.Decimal `db:"amount_remaining" json:"amountRemaining"`
	AmountCaptured                  decimal.Decimal `db:"amount_captured" json:"amountCaptured"`
	AmountChargedBack               decimal.Decimal `db:"amount_charged_back" json:"amountChargedBack"`
	SettlementAmount                decimal.Decimal `db:"settlement_amount" json:"settlementAmount"`
}

type PaymentLinks struct {
	Checkout struct {
		Href string `json:"href"`
		Type string `json:"type"`
	} `json:"checkout"`

	Self struct {
		Href string `json:"href"`
		Type string `json:"type"`
	} `json:"self"`
}
