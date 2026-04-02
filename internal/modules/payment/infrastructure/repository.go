package infrastructure

import (
	"context"
	"fmt"
	"time"
	"tsb-service/internal/modules/payment/domain"
	"tsb-service/pkg/db"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/shopspring/decimal"
)

type PaymentRepository struct {
	pool *db.DBPool
}

func NewPaymentRepository(pool *db.DBPool) domain.PaymentRepository {
	return &PaymentRepository{
		pool: pool,
	}
}

// Save inserts a domain MolliePayment into the database.
// The caller (application service) is responsible for mapping external Mollie data to the domain struct.
func (r *PaymentRepository) Save(ctx context.Context, payment *domain.MolliePayment) error {
	var err error

	tx, err := r.pool.ForContext(ctx).BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

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
		payment.Resource,
		payment.MolliePaymentID,
		payment.Status,
		payment.Description,
		payment.CancelURL,
		payment.WebhookURL,
		payment.CountryCode,
		payment.RestrictPaymentMethodsToCountry,
		payment.ProfileID,
		payment.SettlementID,
		payment.OrderID,
		payment.IsCancelable,
		payment.Mode,
		payment.Locale,
		payment.Method,
		string(payment.Metadata),
		string(payment.Links),
		payment.CreatedAt,
		payment.AuthorizedAt,
		payment.PaidAt,
		payment.CanceledAt,
		payment.ExpiresAt,
		payment.ExpiredAt,
		payment.FailedAt,
		payment.Amount,
		payment.AmountRefunded,
		payment.AmountRemaining,
		payment.AmountCaptured,
		payment.AmountChargedBack,
		payment.SettlementAmount,
	)
	if err != nil {
		return fmt.Errorf("failed to insert mollie payment: %w", err)
	}

	payment.ID = inserted.ID
	payment.CreatedAt = inserted.CreatedAt

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

func (r *PaymentRepository) MarkAsRefund(ctx context.Context, externalPaymentID string, refundedAmount decimal.Decimal) error {
	const query = `
		UPDATE mollie_payments
		SET amount_refunded = $1
		WHERE mollie_payment_id = $2;
	`

	_, err := r.pool.ForContext(ctx).ExecContext(ctx, query, refundedAmount, externalPaymentID)
	if err != nil {
		return fmt.Errorf("failed to mark payment as refunded: %w", err)
	}

	return nil
}

// RefreshStatus updates the payment status and all associated timestamps.
func (r *PaymentRepository) RefreshStatus(ctx context.Context, externalPaymentID string, update *domain.PaymentStatusUpdate) (*uuid.UUID, error) {
	const query = `
		UPDATE mollie_payments
		SET status = $1,
		    paid_at = $2,
		    authorized_at = $3,
		    canceled_at = $4,
		    expired_at = $5,
		    failed_at = $6
		WHERE mollie_payment_id = $7
		RETURNING order_id;
	`

	var orderID uuid.UUID
	err := r.pool.ForContext(ctx).GetContext(ctx, &orderID, query,
		update.Status,
		update.PaidAt,
		update.AuthorizedAt,
		update.CanceledAt,
		update.ExpiredAt,
		update.FailedAt,
		externalPaymentID,
	)

	if err != nil {
		return nil, fmt.Errorf("failed to update payment status: %w", err)
	}

	return &orderID, nil
}

func (r *PaymentRepository) UpdateStatusByOrderID(ctx context.Context, orderID uuid.UUID, status domain.PaymentStatus) (*domain.MolliePayment, error) {
	const query = `
		UPDATE mollie_payments
		SET status = $1
		WHERE order_id = $2
		RETURNING *;
	`

	var payment domain.MolliePayment
	err := r.pool.ForContext(ctx).GetContext(ctx, &payment, query, status, orderID)
	if err != nil {
		return nil, fmt.Errorf("failed to update payment status by order ID: %w", err)
	}

	return &payment, nil
}

func (r *PaymentRepository) FindByOrderID(ctx context.Context, orderID uuid.UUID) (*domain.MolliePayment, error) {
	const query = `
		SELECT *
		FROM mollie_payments
		WHERE order_id = $1
		LIMIT 1;
	`

	var payment domain.MolliePayment
	err := r.pool.ForContext(ctx).GetContext(ctx, &payment, query, orderID)
	if err != nil {
		return nil, fmt.Errorf("failed to find payment by order ID: %w", err)
	}

	return &payment, nil
}

func (r *PaymentRepository) FindByExternalID(ctx context.Context, paymentID string) (*domain.MolliePayment, error) {
	const query = `
		SELECT *
		FROM mollie_payments
		WHERE mollie_payment_id = $1
		LIMIT 1;
	`

	var payment domain.MolliePayment
	err := r.pool.ForContext(ctx).GetContext(ctx, &payment, query, paymentID)
	if err != nil {
		return nil, fmt.Errorf("failed to find payment by ID: %w", err)
	}

	return &payment, nil
}

func (r *PaymentRepository) FindByOrderIDs(ctx context.Context, orderIDs []string) (map[string][]*domain.MolliePayment, error) {
	const query = `
		SELECT *
		FROM mollie_payments
		WHERE order_id = ANY($1::uuid[])
	`

	var payments []*domain.MolliePayment
	err := r.pool.ForContext(ctx).SelectContext(ctx, &payments, query, pq.Array(orderIDs))
	if err != nil {
		return nil, fmt.Errorf("failed to find payments by order IDs: %w", err)
	}

	paymentsMap := make(map[string][]*domain.MolliePayment)
	for _, payment := range payments {
		paymentsMap[payment.OrderID.String()] = append(paymentsMap[payment.OrderID.String()], payment)
	}

	return paymentsMap, nil
}
