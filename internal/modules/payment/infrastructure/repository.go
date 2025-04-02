package infrastructure

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
	"time"
	"tsb-service/internal/modules/payment/domain"
)

type PaymentRepository struct {
	db *sqlx.DB
}

func NewPaymentRepository(db *sqlx.DB) domain.PaymentRepository {
	return &PaymentRepository{
		db: db,
	}
}

// Save converts a Mollie payment object to your domain type and inserts it into the mollie_payments table.
func (r *PaymentRepository) Save(ctx context.Context, external mollie.Payment, orderID uuid.UUID) (*domain.MolliePayment, error) {
	var err error

	// Convert external monetary fields (which are strings) into decimals.
	amount, err := decimal.NewFromString(external.Amount.Value)
	if err != nil {
		return nil, fmt.Errorf("failed to convert amount: %w", err)
	}

	amountRefunded := decimal.Zero
	if external.AmountRefunded != nil {
		amountRefunded, err = decimal.NewFromString(external.AmountRefunded.Value)
		if err != nil {
			return nil, fmt.Errorf("failed to convert amountRefunded: %w", err)
		}
	}

	amountRemaining := decimal.Zero
	if external.AmountRemaining != nil {
		amountRemaining, err = decimal.NewFromString(external.AmountRemaining.Value)
		if err != nil {
			return nil, fmt.Errorf("failed to convert amountRemaining: %w", err)
		}
	}

	amountCaptured := decimal.Zero
	if external.AmountCaptured != nil {
		amountCaptured, err = decimal.NewFromString(external.AmountCaptured.Value)
		if err != nil {
			return nil, fmt.Errorf("failed to convert amountCaptured: %w", err)
		}
	}

	amountChargedBack := decimal.Zero
	if external.AmountChargedBack != nil {
		amountChargedBack, err = decimal.NewFromString(external.AmountChargedBack.Value)
		if err != nil {
			return nil, fmt.Errorf("failed to convert amountChargedBack: %w", err)
		}
	}

	settlementAmount := decimal.Zero
	if external.SettlementAmount != nil {
		settlementAmount, err = decimal.NewFromString(external.SettlementAmount.Value)
		if err != nil {
			return nil, fmt.Errorf("failed to convert settlementAmount: %w", err)
		}
	}

	// Marshal Metadata into a JSON string.
	var metadataJSON string
	if external.Metadata != nil {
		raw, err := json.Marshal(external.Metadata)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal metadata: %w", err)
		}
		metadataJSON = string(raw)
	} else {
		metadataJSON = "null"
	}

	// Marshal Links into a JSON string.
	raw, err := json.Marshal(external.Links)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal links: %w", err)
	}
	linksJSON := string(raw)

	// Build the domain MolliePayment object.
	domainPayment := &domain.MolliePayment{
		Resource:                        &external.Resource,
		MolliePaymentID:                 external.ID,
		Status:                          external.Status,
		Description:                     &external.Description,
		CancelURL:                       &external.CancelURL,
		WebhookURL:                      &external.WebhookURL,
		CountryCode:                     &external.CountryCode,
		RestrictPaymentMethodsToCountry: &external.RestrictPaymentMethodsToCountry,
		ProfileID:                       &external.ProfileID,
		SettlementID:                    &external.SettlementID,
		OrderID:                         orderID,
		IsCancelable:                    external.IsCancelable,
		Mode:                            nil, // fill in if needed
		Locale:                          nil, // fill in if needed
		Method:                          nil, // fill in if needed
		// We store the JSON strings as []byte if your columns are JSONB in DB
		Metadata:          []byte(metadataJSON),
		Links:             []byte(linksJSON),
		CreatedAt:         time.Now(), // or external.CreatedAt if you want Mollie's creation date
		AuthorizedAt:      external.AuthorizedAt,
		PaidAt:            external.PaidAt,
		CanceledAt:        external.CanceledAt,
		ExpiresAt:         external.ExpiresAt,
		ExpiredAt:         external.ExpiredAt,
		FailedAt:          external.FailedAt,
		Amount:            amount,
		AmountRefunded:    amountRefunded,
		AmountRemaining:   amountRemaining,
		AmountCaptured:    amountCaptured,
		AmountChargedBack: amountChargedBack,
		SettlementAmount:  settlementAmount,
	}

	// Begin a transaction.
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	// Insert the domainPayment into the database.
	// Ensure the columns match your DB schema exactly.
	const query = `
        INSERT INTO mollie_payments (
            resource, mollie_payment_id, status, description, cancel_url,
            webhook_url, country_code, restrict_payment_methods_to_country,
            profile_id, settlement_id, order_id, is_cancelable, mode, locale, method,
            metadata, links, created_at, authorized_at, paid_at, canceled_at, expires_at,
            expired_at, failed_at, amount, amount_refunded, amount_remaining, amount_captured,
            amount_charged_back, settlement_amount
        ) VALUES (
            $1, $2, $3, $4, $5,
            $6, $7, $8,
            $9, $10, $11, $12, $13, $14, $15,
            $16, $17, $18, $19, $20, $21, $22,
            $23, $24, $25, $26, $27, $28, $29, $30
        )
        RETURNING id, created_at;
    `

	var inserted struct {
		ID        uuid.UUID `db:"id"`
		CreatedAt time.Time `db:"created_at"`
	}
	err = tx.GetContext(ctx, &inserted, query,
		domainPayment.Resource,
		domainPayment.MolliePaymentID,
		domainPayment.Status,
		domainPayment.Description,
		domainPayment.CancelURL,
		domainPayment.WebhookURL,
		domainPayment.CountryCode,
		domainPayment.RestrictPaymentMethodsToCountry,
		domainPayment.ProfileID,
		domainPayment.SettlementID,
		domainPayment.OrderID,
		domainPayment.IsCancelable,
		domainPayment.Mode,
		domainPayment.Locale,
		domainPayment.Method,
		string(domainPayment.Metadata), // pass JSON strings to JSONB columns
		string(domainPayment.Links),
		domainPayment.CreatedAt,
		domainPayment.AuthorizedAt,
		domainPayment.PaidAt,
		domainPayment.CanceledAt,
		domainPayment.ExpiresAt,
		domainPayment.ExpiredAt,
		domainPayment.FailedAt,
		domainPayment.Amount,
		domainPayment.AmountRefunded,
		domainPayment.AmountRemaining,
		domainPayment.AmountCaptured,
		domainPayment.AmountChargedBack,
		domainPayment.SettlementAmount,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to insert mollie payment: %w", err)
	}

	// Assign the auto-generated values back to the domain object.
	domainPayment.ID = inserted.ID
	domainPayment.CreatedAt = inserted.CreatedAt

	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return domainPayment, nil
}
