package repository

import (
	"context"
	"fmt"
	"time"
	"tsb-service/internal/modules/order/domain"
	"tsb-service/pkg/db"
	"tsb-service/pkg/logging"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
)

type OrderRepository struct {
	pool *db.DBPool
}

func NewOrderRepository(pool *db.DBPool) domain.OrderRepository {
	return &OrderRepository{pool: pool}
}

// Save inserts a new order, creates a Mollie payment, updates the order with payment details,
// and links the order with its product lines.
func (r *OrderRepository) Save(ctx context.Context, o *domain.Order, op *[]domain.OrderProductRaw) (*domain.Order, *[]domain.OrderProductRaw, error) {
	// Begin a transaction.
	tx, err := r.pool.ForContext(ctx).BeginTxx(ctx, nil)
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
	if op != nil && len(*op) > 0 {
		computedTotal := decimal.NewFromInt(0)
		for _, prod := range *op {
			computedTotal = computedTotal.Add(prod.TotalPrice)
		}

		// Add delivery fee if applicable.
		if o.DeliveryFee != nil {
			computedTotal = computedTotal.Add(*o.DeliveryFee)
		}

		// Add discount amount if applicable.
		if o.DiscountAmount != decimal.Zero {
			computedTotal = computedTotal.Sub(o.DiscountAmount)
		}

		o.TotalPrice = computedTotal
	}

	// Insert the order record.
	const orderQuery = `
		INSERT INTO orders (
			user_id, order_status, order_type, is_online_payment,
			discount_amount, delivery_fee, total_price, preferred_ready_time, estimated_ready_time,
			address_id, address_extra, order_note, order_extra, language, coupon_code
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7, $8, $9,
			$10, $11, $12, $13, $14, $15
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
		o.PreferredReadyTime,
		o.EstimatedReadyTime,
		o.AddressID,
		o.AddressExtra,
		o.OrderNote,
		o.OrderExtra,
		o.Language,
		o.CouponCode,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to insert order: %w", err)
	}

	// Update the order with returned values.
	o.ID = inserted.ID
	o.CreatedAt = inserted.CreatedAt
	o.UpdatedAt = inserted.UpdatedAt

	// Insert each order product.
	if op != nil && len(*op) > 0 {
		const orderProductQuery = `
			INSERT INTO order_product (
				order_id, product_id, unit_price, quantity, total_price, product_choice_id
			) VALUES (
				$1, $2, $3, $4, $5, $6
			);
		`
		for _, prod := range *op {
			if _, err = tx.ExecContext(ctx, orderProductQuery,
				o.ID,
				prod.ProductID,
				prod.UnitPrice,
				prod.Quantity,
				prod.TotalPrice,
				prod.ProductChoiceID,
			); err != nil {
				return nil, nil, fmt.Errorf("failed to insert order product: %w", err)
			}
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
		SET order_status = $1, estimated_ready_time = $2, updated_at = CURRENT_TIMESTAMP
		WHERE id = $3;
	`
	if _, err := r.pool.ForContext(ctx).ExecContext(ctx, query, order.OrderStatus, order.EstimatedReadyTime, order.ID); err != nil {
		return fmt.Errorf("failed to update order: %w", err)
	}
	return nil
}

// FindByID retrieves an order by its ID.
func (r *OrderRepository) FindByID(ctx context.Context, orderID uuid.UUID) (*domain.Order, *[]domain.OrderProductRaw, error) {
	query := `
		SELECT *
		FROM orders
		WHERE id = $1
		LIMIT 1
	`

	var order domain.Order

	if err := r.pool.ForContext(ctx).GetContext(ctx, &order, query, orderID); err != nil {
		return nil, nil, fmt.Errorf("failed to query order: %w", err)
	}

	// Fetch order products
	query = `
		SELECT
			op.product_id,
			op.quantity,
			op.unit_price,
			op.total_price,
			op.product_choice_id
		FROM order_product op
		JOIN products p ON op.product_id = p.id
		JOIN product_categories pc ON p.category_id = pc.id
		JOIN product_category_translations pct ON pc.id = pct.product_category_id AND pct.language = 'fr'
		JOIN product_translations pt ON p.id = pt.product_id AND pt.language = 'fr'
		WHERE op.order_id = $1
		ORDER BY p.code ASC, pct.name ASC, pt.name ASC
	`

	var orderProducts []domain.OrderProductRaw

	if err := r.pool.ForContext(ctx).SelectContext(ctx, &orderProducts, query, order.ID); err != nil {
		return nil, nil, fmt.Errorf("failed to query order products: %w", err)
	}

	return &order, &orderProducts, nil
}

func (r *OrderRepository) FindPaginated(ctx context.Context, page int, limit int, userID *uuid.UUID) ([]*domain.Order, error) {
	// Basic pagination safety : ensure page & limit are > 0
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 10
	}

	offset := (page - 1) * limit

	var whereClause string
	var args []interface{}
	placeholderIndex := 1

	// If userID is not nil, add a WHERE condition
	if userID != nil {
		whereClause = fmt.Sprintf("WHERE o.user_id = $%d", placeholderIndex)
		args = append(args, *userID)
		placeholderIndex++
	}

	// Next placeholders for LIMIT and OFFSET
	limitPlaceholder := placeholderIndex
	offsetPlaceholder := placeholderIndex + 1

	// Build the final query
	query := fmt.Sprintf(`
        SELECT
            o.*
        FROM orders o
        %s
        ORDER BY o.created_at DESC
        LIMIT $%d OFFSET $%d
    `, whereClause, limitPlaceholder, offsetPlaceholder)

	// Append the limit and offset arguments
	args = append(args, limit, offset)

	// Execute the query
	var orders []*domain.Order
	if err := r.pool.ForContext(ctx).SelectContext(ctx, &orders, query, args...); err != nil {
		logging.FromContext(ctx).Error("error querying orders", zap.Int("page", page), zap.Int("limit", limit), zap.Any("user_id", userID), zap.Error(err))
		return nil, fmt.Errorf("failed to query orders: %w", err)
	}

	return orders, nil
}

func (r *OrderRepository) FindByOrderIDs(ctx context.Context, orderIDs []string) (map[string][]*domain.OrderProductRaw, error) {
	if len(orderIDs) == 0 {
		// must be []*domain.OrderProductRaw, not []domain.OrderProductRaw
		return make(map[string][]*domain.OrderProductRaw), nil
	}

	// build an IN (…) query with JOIN to products for sorting, expand args with sqlx.In, then rebind for your driver
	query, args, err := sqlx.In(`
        SELECT
            op.order_id,
            op.product_id,
            op.quantity,
            op.unit_price,
            op.total_price,
            op.product_choice_id
        FROM order_product op
        JOIN products p ON op.product_id = p.id
        JOIN product_categories pc ON p.category_id = pc.id
        JOIN product_category_translations pct ON pc.id = pct.product_category_id AND pct.language = 'fr'
        JOIN product_translations pt ON p.id = pt.product_id AND pt.language = 'fr'
        WHERE op.order_id IN (?)
        ORDER BY p.code ASC, pct.name ASC, pt.name ASC
    `, orderIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to build IN query: %w", err)
	}
	query = r.pool.ForContext(ctx).Rebind(query)

	// temp struct to hold each row (including the order_id)
	type rawRow struct {
		OrderID         uuid.UUID       `db:"order_id"`
		ProductID       uuid.UUID       `db:"product_id"`
		Quantity        int64           `db:"quantity"`
		UnitPrice       decimal.Decimal `db:"unit_price"`
		TotalPrice      decimal.Decimal `db:"total_price"`
		ProductChoiceID *uuid.UUID      `db:"product_choice_id"`
	}

	var rows []rawRow
	if err := r.pool.ForContext(ctx).SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("failed to select order products: %w", err)
	}

	// now group into map[string][]*domain.OrderProductRaw
	result := make(map[string][]*domain.OrderProductRaw, len(rows))
	for _, row := range rows {
		op := &domain.OrderProductRaw{
			ProductID:       row.ProductID,
			Quantity:        row.Quantity,
			UnitPrice:       row.UnitPrice,
			TotalPrice:      row.TotalPrice,
			ProductChoiceID: row.ProductChoiceID,
		}
		// key by the string form of the order UUID
		result[row.OrderID.String()] = append(result[row.OrderID.String()], op)
	}

	return result, nil
}

func (r *OrderRepository) FindByUserIDs(ctx context.Context, userIDs []string) (map[string][]*domain.Order, error) {
	// 1) Expand the IN clause
	query, args, err := sqlx.In(`
        SELECT *
        FROM orders
        WHERE user_id IN (?)
        ORDER BY created_at DESC
    `, userIDs)
	if err != nil {
		return nil, err
	}

	// 2) Rebind for the specific driver (?, $1, etc)
	query = r.pool.ForContext(ctx).Rebind(query)

	// 3) Fetch into a slice
	var orders []domain.Order
	if err := r.pool.ForContext(ctx).SelectContext(ctx, &orders, query, args...); err != nil {
		return nil, err
	}

	// 4) Group by user_id
	result := make(map[string][]*domain.Order, len(userIDs))
	for _, o := range orders {
		result[o.UserID.String()] = append(result[o.UserID.String()], &o)
	}

	return result, nil
}

func (r *OrderRepository) InsertStatusHistory(ctx context.Context, orderID uuid.UUID, status domain.OrderStatus) error {
	query := `INSERT INTO order_status_history (order_id, status) VALUES ($1, $2)`
	if _, err := r.pool.ForContext(ctx).ExecContext(ctx, query, orderID, status); err != nil {
		return fmt.Errorf("failed to insert status history: %w", err)
	}
	return nil
}

func (r *OrderRepository) FindStatusHistoryByOrderID(ctx context.Context, orderID uuid.UUID) ([]*domain.OrderStatusHistory, error) {
	query := `SELECT id, order_id, status, changed_at FROM order_status_history WHERE order_id = $1 ORDER BY changed_at ASC`
	var history []*domain.OrderStatusHistory
	if err := r.pool.ForContext(ctx).SelectContext(ctx, &history, query, orderID); err != nil {
		return nil, fmt.Errorf("failed to query status history: %w", err)
	}
	return history, nil
}
