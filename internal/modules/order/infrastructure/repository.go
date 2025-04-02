package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
	"log"
	"time"
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
func (r *OrderRepository) Save(ctx context.Context, o *domain.Order, op *[]domain.OrderProductRaw) (*domain.Order, *[]domain.OrderProductRaw, error) {
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

// FindByID retrieves an order by its ID.
func (r *OrderRepository) FindByID(ctx context.Context, orderID uuid.UUID) (*domain.Order, *[]domain.OrderProductRaw, error) {
	query := `
		SELECT *
		FROM orders
		WHERE id = $1
		LIMIT 1
	`

	var order domain.Order

	if err := r.db.GetContext(ctx, &order, query, orderID); err != nil {
		return nil, nil, fmt.Errorf("failed to query order: %w", err)
	}

	// Fetch order products
	query = `
		SELECT 
			op.product_id,
			op.quantity,
			op.unit_price,
			op.total_price
		FROM order_product op
		WHERE op.order_id = $1
	`

	var orderProducts []domain.OrderProductRaw

	if err := r.db.SelectContext(ctx, &orderProducts, query, order.ID); err != nil {
		return nil, nil, fmt.Errorf("failed to query order products: %w", err)
	}

	return &order, &orderProducts, nil
}

func (r *OrderRepository) FindPaginated(ctx context.Context, page int, limit int, userID *uuid.UUID) ([]*domain.Order, error) {
	// Basic pagination safety: ensure page & limit are > 0
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
	if err := r.db.SelectContext(ctx, &orders, query, args...); err != nil {
		log.Printf("Error querying orders (page=%d, limit=%d, userID=%v): %v", page, limit, userID, err)
		return nil, fmt.Errorf("failed to query orders: %w", err)
	}

	return orders, nil
}

func (r *OrderRepository) FindByOrderIDs(ctx context.Context, orderIDs []uuid.UUID) (map[uuid.UUID][]domain.OrderProductRaw, error) {
	if len(orderIDs) == 0 {
		// Return an empty map if nothing was requested
		return make(map[uuid.UUID][]domain.OrderProductRaw), nil
	}

	// Build a query with an IN clause using sqlx.In for parameter expansion
	query, args, err := sqlx.In(`
        SELECT 
            order_id,
            product_id,
            quantity,
            unit_price,
            total_price
        FROM order_product
        WHERE order_id IN (?)
    `, orderIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to build IN query: %w", err)
	}

	// Rebind the query for the current driver
	query = r.db.Rebind(query)

	// Define a struct that matches the selected columns
	var rows []struct {
		OrderID    uuid.UUID       `db:"order_id"`
		ProductID  uuid.UUID       `db:"product_id"`
		Quantity   int64           `db:"quantity"`
		UnitPrice  decimal.Decimal `db:"unit_price"`
		TotalPrice decimal.Decimal `db:"total_price"`
	}

	// Execute the query
	if err := r.db.SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("failed to select order products: %w", err)
	}

	// Initialize the result map
	productsByOrder := make(map[uuid.UUID][]domain.OrderProductRaw)

	// Populate the map: orderID -> slice of OrderProductRaw
	for _, row := range rows {
		op := domain.OrderProductRaw{
			ProductID:  row.ProductID,
			Quantity:   row.Quantity,
			UnitPrice:  row.UnitPrice,
			TotalPrice: row.TotalPrice,
		}
		productsByOrder[row.OrderID] = append(productsByOrder[row.OrderID], op)
	}

	return productsByOrder, nil
}
