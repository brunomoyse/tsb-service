package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/shopspring/decimal"
	"time"
	"tsb-service/pkg/utils"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"tsb-service/internal/modules/order/domain"
)

type OrderRepository struct {
	db *sqlx.DB
}

func NewOrderRepository(db *sqlx.DB) domain.OrderRepository {
	return &OrderRepository{db: db}
}

// Save inserts a new order, creates a Mollie payment, updates the order with payment details,
// and links the order with its product lines.
func (r *OrderRepository) Save(ctx context.Context, o *domain.Order, op *[]domain.OrderProduct) (*domain.Order, *[]domain.OrderProduct, error) {
	// Begin a transaction.
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	// Rollback if something goes wrong.
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	// Calculate the total price of the order from order products.
	computedTotal := decimal.NewFromInt(0)
	for _, prod := range *op {
		computedTotal = computedTotal.Add(prod.TotalPrice)
	}
	o.TotalPrice = computedTotal

	// Marshal OrderExtras to JSON (for the order_extra column).
	var orderExtraJSON []byte
	if o.OrderExtra != nil {
		orderExtraJSON, err = json.Marshal(o.OrderExtra)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to marshal order extras: %w", err)
		}
	} else {
		// If there are no extras, use "null" (or "[]" if you prefer an empty array)
		orderExtraJSON = []byte("null")
	}

	// Insert the order record.
	const orderQuery = `
		INSERT INTO orders (
			user_id, order_status, order_type, is_online_payment, 
			discount_amount, delivery_fee, total_price, estimated_ready_time, 
			address_id, address_extra, extra_comment, order_extra
		) VALUES (
			$1, $2, $3, $4, 
			$5, $6, $7, $8, 
			$9, $10, $11, $12
		)
		RETURNING id, created_at, updated_at;
	`

	// Execute the query.
	var inserted struct {
		ID        uuid.UUID `db:"id"`
		CreatedAt time.Time `db:"created_at"`
		UpdatedAt time.Time `db:"updated_at"`
	}
	err = tx.GetContext(ctx, &inserted, orderQuery,
		o.UserID,
		o.OrderStatus,
		o.OrderType,
		o.IsOnlinePayment,
		o.DiscountAmount,
		o.DeliveryFee,
		o.TotalPrice,
		o.EstimatedReadyTime,
		o.AddressID,
		o.AddressExtra,
		o.ExtraComment,
		string(orderExtraJSON), // convert []byte to string
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to insert order: %w", err)
	}

	// Update the order with returned values.
	o.ID = inserted.ID
	o.CreatedAt = inserted.CreatedAt
	o.UpdatedAt = inserted.UpdatedAt

	// Insert each order product.
	const orderProductQuery = `
		INSERT INTO order_product (
			order_id, product_id, unit_price, quantity, total_price
		) VALUES (
			$1, $2, $3, $4, $5
		);
	`
	for _, prod := range *op {
		if _, err = tx.ExecContext(ctx, orderProductQuery,
			o.ID,
			prod.ProductID,
			prod.UnitPrice,
			prod.Quantity,
			prod.TotalPrice,
		); err != nil {
			return nil, nil, fmt.Errorf("failed to insert order product: %w", err)
		}
	}

	// Commit the transaction.
	if err = tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return o, op, nil
}

func (r *OrderRepository) Update(ctx context.Context, order *domain.Order) error {
	query := `
		UPDATE orders
		SET status = $1, updated_at = CURRENT_TIMESTAMP
		WHERE id = $2;
	`
	if _, err := r.db.ExecContext(ctx, query, order.OrderStatus, order.ID); err != nil {
		return fmt.Errorf("failed to update order: %w", err)
	}
	return nil
}

// UpdateStatus updates an order’s status based on the Mollie payment status.
/*
func (r *OrderRepository) UpdatePaymentStatus(ctx context.Context, paymentID string, paymentStatus string) error {
	query := `
		UPDATE orders
		SET status = $1
		WHERE mollie_payment_id = $2
		RETURNING id;
	`
	var orderID uuid.UUID
	var newStatus string
	// @TODO: Add payment status instead of updating the order status
	if paymentStatus == "paid" {
		newStatus = "PAID"
	} else {
		newStatus = "FAILED"
	}
	err := r.db.QueryRowContext(ctx, query, newStatus, paymentID).Scan(&orderID)
	if err != nil {
		return fmt.Errorf("failed to update order status: %v", err)
	}
	return nil
}
*/

// FindByUserID retrieves all orders for a given user.
func (r *OrderRepository) FindByUserID(ctx context.Context, userID uuid.UUID) ([]*domain.Order, error) {
	lang := utils.GetLang(ctx)

	query := `
        SELECT 
            o.id AS order_id,
            o.user_id,
            o.payment_mode,
            o.mollie_payment_id,
            o.mollie_payment_url,
            o.delivery_option,
            o.status,
            o.created_at,
            o.updated_at,
            op.product_id,
            op.quantity,
            COALESCE(pt_user.name, pt_fr.name, pt_zh.name, pt_en.name) AS product_name,
            op.unit_price AS product_unit_price,
            op.total_price AS product_total_price,
            p.code AS product_code,
            COALESCE(pct_user.name, pct_fr.name, pct_zh.name, pct_en.name) AS product_category_name
        FROM orders o
        LEFT JOIN order_product op ON o.id = op.order_id
        LEFT JOIN products p ON op.product_id = p.id
        -- Product name translations with fallback
        LEFT JOIN product_translations pt_user ON p.id = pt_user.product_id AND pt_user.locale = $2
        LEFT JOIN product_translations pt_fr   ON p.id = pt_fr.product_id AND pt_fr.locale = 'fr'
        LEFT JOIN product_translations pt_zh   ON p.id = pt_zh.product_id AND pt_zh.locale = 'zh'
        LEFT JOIN product_translations pt_en   ON p.id = pt_en.product_id AND pt_en.locale = 'en'
        -- Product category translations with fallback
        LEFT JOIN product_category_translations pct_user ON p.category_id = pct_user.product_category_id AND pct_user.locale = $2
        LEFT JOIN product_category_translations pct_fr   ON p.category_id = pct_fr.product_category_id AND pct_fr.locale = 'fr'
        LEFT JOIN product_category_translations pct_zh   ON p.category_id = pct_zh.product_category_id AND pct_zh.locale = 'zh'
        LEFT JOIN product_category_translations pct_en   ON p.category_id = pct_en.product_category_id AND pct_en.locale = 'en'
        WHERE o.user_id = $1
        ORDER BY o.created_at DESC
    `

	var rows []struct {
		OrderID             string           `db:"order_id"`
		UserID              string           `db:"user_id"`
		PaymentMode         string           `db:"payment_mode"`
		MolliePaymentId     *string          `db:"mollie_payment_id"`
		MolliePaymentUrl    *string          `db:"mollie_payment_url"`
		DeliveryOption      string           `db:"delivery_option"`
		Status              string           `db:"status"`
		CreatedAt           time.Time        `db:"created_at"`
		UpdatedAt           time.Time        `db:"updated_at"`
		ProductID           *string          `db:"product_id"`
		Quantity            *int64           `db:"quantity"`
		ProductName         *string          `db:"product_name"`
		ProductUnitPrice    *decimal.Decimal `db:"product_unit_price"`
		ProductTotalPrice   *decimal.Decimal `db:"product_total_price"`
		ProductCode         *string          `db:"product_code"`
		ProductCategoryName *string          `db:"product_category_name"`
	}

	if err := r.db.SelectContext(ctx, &rows, query, userID, lang); err != nil {
		return nil, fmt.Errorf("failed to query orders: %w", err)
	}

	var orders []*domain.Order
	var currentOrder *domain.Order

	for _, row := range rows {
		if currentOrder == nil || currentOrder.ID.String() != row.OrderID {
			ordID, err := uuid.Parse(row.OrderID)
			if err != nil {
				return nil, fmt.Errorf("failed to parse order id: %w", err)
			}
			uID, err := uuid.Parse(row.UserID)
			if err != nil {
				return nil, fmt.Errorf("failed to parse user id: %w", err)
			}

			currentOrder = &domain.Order{
				ID:          ordID,
				UserID:      uID,
				OrderStatus: domain.OrderStatus(row.Status),
				CreatedAt:   row.CreatedAt,
				UpdatedAt:   row.UpdatedAt,
			}
			orders = append(orders, currentOrder)
		}

	}

	return orders, nil
}

// FindByID retrieves an order by its ID.
func (r *OrderRepository) FindByID(ctx context.Context, orderID uuid.UUID) (*domain.Order, error) {
	lang := utils.GetLang(ctx)

	query := `
        SELECT 
            o.id AS order_id,
            o.user_id,
            o.payment_mode,
            o.mollie_payment_id,
            o.mollie_payment_url,
            o.delivery_option,
            o.status,
            o.created_at,
            o.updated_at,
            op.product_id,
            op.quantity,
            COALESCE(pt_user.name, pt_fr.name, pt_zh.name, pt_en.name) AS product_name,
            op.unit_price AS product_unit_price,
            op.total_price AS product_total_price,
            p.code AS product_code,
            COALESCE(pct_user.name, pct_fr.name, pct_zh.name, pct_en.name) AS product_category_name
        FROM orders o
        LEFT JOIN order_product op ON o.id = op.order_id
        LEFT JOIN products p ON op.product_id = p.id
        -- Product translations with fallback
        LEFT JOIN product_translations pt_user ON p.id = pt_user.product_id AND pt_user.locale = $2
        LEFT JOIN product_translations pt_fr   ON p.id = pt_fr.product_id AND pt_fr.locale = 'fr'
        LEFT JOIN product_translations pt_zh   ON p.id = pt_zh.product_id AND pt_zh.locale = 'zh'
        LEFT JOIN product_translations pt_en   ON p.id = pt_en.product_id AND pt_en.locale = 'en'
        -- Product category translations with fallback
        LEFT JOIN product_category_translations pct_user ON p.category_id = pct_user.product_category_id AND pct_user.locale = $2
        LEFT JOIN product_category_translations pct_fr   ON p.category_id = pct_fr.product_category_id AND pct_fr.locale = 'fr'
        LEFT JOIN product_category_translations pct_zh   ON p.category_id = pct_zh.product_category_id AND pct_zh.locale = 'zh'
        LEFT JOIN product_category_translations pct_en   ON p.category_id = pct_en.product_category_id AND pct_en.locale = 'en'
        WHERE o.id = $1
        ORDER BY o.created_at DESC
	`

	var rows []struct {
		OrderID             string           `db:"order_id"`
		UserID              string           `db:"user_id"`
		PaymentMode         string           `db:"payment_mode"`
		MolliePaymentId     *string          `db:"mollie_payment_id"`
		MolliePaymentUrl    *string          `db:"mollie_payment_url"`
		DeliveryOption      string           `db:"delivery_option"`
		Status              string           `db:"status"`
		CreatedAt           time.Time        `db:"created_at"`
		UpdatedAt           time.Time        `db:"updated_at"`
		ProductID           *string          `db:"product_id"`
		Quantity            *int64           `db:"quantity"`
		ProductName         *string          `db:"product_name"`
		ProductUnitPrice    *decimal.Decimal `db:"product_unit_price"`
		ProductTotalPrice   *decimal.Decimal `db:"product_total_price"`
		ProductCode         *string          `db:"product_code"`
		ProductCategoryName *string          `db:"product_category_name"`
	}

	if err := r.db.SelectContext(ctx, &rows, query, orderID, lang); err != nil {
		return nil, fmt.Errorf("failed to query order: %w", err)
	}

	if len(rows) == 0 {
		return nil, fmt.Errorf("order not found")
	}

	var order *domain.Order
	for _, row := range rows {
		// Create the order once.
		if order == nil {
			ordID, err := uuid.Parse(row.OrderID)
			if err != nil {
				return nil, fmt.Errorf("failed to parse order id: %w", err)
			}
			uID, err := uuid.Parse(row.UserID)
			if err != nil {
				return nil, fmt.Errorf("failed to parse user id: %w", err)
			}

			order = &domain.Order{
				ID:        ordID,
				UserID:    uID,
				CreatedAt: row.CreatedAt,
				UpdatedAt: row.UpdatedAt,
			}
		}
	}

	return order, nil
}

func (r *OrderRepository) FindPaginated(ctx context.Context, page int, limit int) ([]*domain.Order, error) {
	lang := utils.GetLang(ctx)

	query := `
        SELECT 
            o.id AS order_id,
            o.user_id,
            o.payment_mode,
            o.mollie_payment_id,
            o.mollie_payment_url,
            o.delivery_option,
            o.status,
            o.created_at,
            o.updated_at,
            op.product_id,
            op.quantity,
            COALESCE(pt_user.name, pt_fr.name, pt_zh.name, pt_en.name) AS product_name,
            op.unit_price AS product_unit_price,
            op.total_price AS product_total_price,
            p.code AS product_code,
            COALESCE(pct_user.name, pct_fr.name, pct_zh.name, pct_en.name) AS product_category_name
        FROM orders o
        LEFT JOIN order_product op ON o.id = op.order_id
        LEFT JOIN products p ON op.product_id = p.id
        -- Product translations with fallback languages
        LEFT JOIN product_translations pt_user ON p.id = pt_user.product_id AND pt_user.locale = $2
        LEFT JOIN product_translations pt_fr   ON p.id = pt_fr.product_id AND pt_fr.locale = 'fr'
        LEFT JOIN product_translations pt_zh   ON p.id = pt_zh.product_id AND pt_zh.locale = 'zh'
        LEFT JOIN product_translations pt_en   ON p.id = pt_en.product_id AND pt_en.locale = 'en'
        -- Category translations with fallback languages
        LEFT JOIN product_category_translations pct_user ON p.category_id = pct_user.product_category_id AND pct_user.locale = $2
        LEFT JOIN product_category_translations pct_fr   ON p.category_id = pct_fr.product_category_id AND pct_fr.locale = 'fr'
        LEFT JOIN product_category_translations pct_zh   ON p.category_id = pct_zh.product_category_id AND pct_zh.locale = 'zh'
        LEFT JOIN product_category_translations pct_en   ON p.category_id = pct_en.product_category_id AND pct_en.locale = 'en'
        ORDER BY o.created_at DESC
        LIMIT $1 OFFSET $1 * ($3 - 1)
    `

	var rows []struct {
		OrderID             string           `db:"order_id"`
		UserID              string           `db:"user_id"`
		PaymentMode         string           `db:"payment_mode"`
		MolliePaymentId     *string          `db:"mollie_payment_id"`
		MolliePaymentUrl    *string          `db:"mollie_payment_url"`
		DeliveryOption      string           `db:"delivery_option"`
		Status              string           `db:"status"`
		CreatedAt           time.Time        `db:"created_at"`
		UpdatedAt           time.Time        `db:"updated_at"`
		ProductID           *string          `db:"product_id"`
		Quantity            *int64           `db:"quantity"`
		ProductName         *string          `db:"product_name"`
		ProductUnitPrice    *decimal.Decimal `db:"product_unit_price"`
		ProductTotalPrice   *decimal.Decimal `db:"product_total_price"`
		ProductCode         *string          `db:"product_code"`
		ProductCategoryName *string          `db:"product_category_name"`
	}

	if err := r.db.SelectContext(ctx, &rows, query, limit, lang, page); err != nil {
		return nil, fmt.Errorf("failed to query orders: %w", err)
	}

	var orders []*domain.Order
	var currentOrder *domain.Order

	for _, row := range rows {
		if currentOrder == nil || currentOrder.ID.String() != row.OrderID {
			ordID, err := uuid.Parse(row.OrderID)
			if err != nil {
				return nil, fmt.Errorf("failed to parse order id: %w", err)
			}
			uID, err := uuid.Parse(row.UserID)
			if err != nil {
				return nil, fmt.Errorf("failed to parse user id: %w", err)
			}

			currentOrder = &domain.Order{
				ID:        ordID,
				UserID:    uID,
				CreatedAt: row.CreatedAt,
				UpdatedAt: row.UpdatedAt,
			}
			orders = append(orders, currentOrder)
		}
	}

	return orders, nil
}
