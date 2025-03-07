package repository

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"tsb-service/internal/modules/order/domain"

	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

type OrderRepository struct {
	db *sqlx.DB
}

func NewOrderRepository(db *sqlx.DB) domain.OrderRepository {
	return &OrderRepository{db: db}
}

// CreateOrder inserts a new order, creates a Mollie payment, updates the order with payment details,
// and links the order with its product lines.
func (r *OrderRepository) Save(ctx context.Context, client *mollie.Client, ord *domain.Order) (*domain.Order, error) {
	// Begin a transaction using sqlx.
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	// Ensure rollback on error.
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	// Insert the order without payment details.
	// Postgres will generate created_at and updated_at automatically.
	const insertQuery = `
		INSERT INTO orders (user_id, payment_mode, status)
		VALUES ($1, $2, $3)
		RETURNING id;
	`

	var orderID string
	if err = tx.GetContext(ctx, &orderID, insertQuery, ord.UserID, ord.PaymentMode, ord.Status); err != nil {
		return nil, fmt.Errorf("failed to insert new order: %w", err)
	}
	ord.ID, err = uuid.Parse(orderID)
	if err != nil {
		return nil, fmt.Errorf("failed to parse order id: %w", err)
	}

	// Create the Mollie payment using the order details.
	// Assumes createMolliePayment accepts a transaction and an order.
	payment, err := createMolliePayment(ctx, tx, client, ord)
	if err != nil || payment == nil {
		return nil, fmt.Errorf("failed to create Mollie payment: %w", err)
	}

	// Update the order struct with Mollie payment details.
	ord.MolliePaymentId = &payment.ID
	ord.MolliePaymentUrl = &payment.Links.Checkout.Href

	// Update the order record with payment details.
	const updateQuery = `
		UPDATE orders
		SET mollie_payment_id = $1, mollie_payment_url = $2, updated_at = NOW()
		WHERE id = $3;
	`
	if _, err = tx.ExecContext(ctx, updateQuery, ord.MolliePaymentId, ord.MolliePaymentUrl, orderID); err != nil {
		return nil, fmt.Errorf("failed to update order with Mollie payment details: %w", err)
	}

	// Link the order with its product lines.
	// Assumes ord.Products contains the necessary details.
	if err = linkOrderProduct(ctx, tx, ord.ID, ord.Products); err != nil {
		return nil, fmt.Errorf("failed to link order with product lines: %w", err)
	}

	// Commit the transaction.
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return r.FindByID(ctx, ord.ID)
}

// UpdateOrderStatus updates an order’s status based on the Mollie payment status.
func (r *OrderRepository) UpdateStatus(ctx context.Context, paymentID string, paymentStatus string) error {
	query := `
		UPDATE orders
		SET status = $1
		WHERE mollie_payment_id = $2
		RETURNING id;
	`
	var orderID uuid.UUID
	var newStatus string
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

// GetOrdersForUser retrieves all orders for a given user.
func (r *OrderRepository) FindByUserID(ctx context.Context, userID uuid.UUID) ([]*domain.Order, error) {
	// Retrieve user language from context, default to "fr"
	lang, ok := ctx.Value("lang").(string)
	if !ok || lang == "" {
		lang = "fr"
	}

	query := `
		SELECT 
			o.id AS order_id,
			o.user_id,
			o.payment_mode,
			o.mollie_payment_id,
			o.mollie_payment_url,
			o.status,
			o.created_at,
			o.updated_at,
			op.product_id,
			op.quantity,
			pt.name AS product_name,
			p.price AS product_price
		FROM orders o
		LEFT JOIN order_product op ON o.id = op.order_id
		LEFT JOIN products p ON op.product_id = p.id
		LEFT JOIN product_translations pt ON p.id = pt.product_id AND pt.locale = $2
		WHERE o.user_id = $1
		ORDER BY o.created_at DESC;
	`

	// Define a helper struct for scanning rows.
	type orderRow struct {
		OrderID          string    `db:"order_id"`
		UserID           string    `db:"user_id"`
		PaymentMode      string    `db:"payment_mode"`
		MolliePaymentId  *string   `db:"mollie_payment_id"`
		MolliePaymentUrl *string   `db:"mollie_payment_url"`
		Status           string    `db:"status"`
		CreatedAt        time.Time `db:"created_at"`
		UpdatedAt        time.Time `db:"updated_at"`
		ProductID        *string   `db:"product_id"`
		Quantity         *int64    `db:"quantity"`
		ProductName      *string   `db:"product_name"`
		ProductPrice     *float64  `db:"product_price"`
	}

	rows, err := r.db.QueryxContext(ctx, query, userID, lang)
	if err != nil {
		return nil, fmt.Errorf("failed to query orders: %w", err)
	}
	defer rows.Close()

	// Group rows by order ID.
	ordersMap := make(map[string]*domain.Order)
	for rows.Next() {
		var row orderRow
		if err := rows.StructScan(&row); err != nil {
			return nil, fmt.Errorf("failed to scan order row: %w", err)
		}

		order, exists := ordersMap[row.OrderID]
		if !exists {
			ordID, err := uuid.Parse(row.OrderID)
			if err != nil {
				return nil, fmt.Errorf("failed to parse order id: %w", err)
			}
			uID, err := uuid.Parse(row.UserID)
			if err != nil {
				return nil, fmt.Errorf("failed to parse user id: %w", err)
			}

			order = &domain.Order{
				ID:               ordID,
				UserID:           uID,
				PaymentMode:      (*domain.PaymentMode)(&row.PaymentMode), // adapt as needed
				MolliePaymentId:  row.MolliePaymentId,
				MolliePaymentUrl: row.MolliePaymentUrl,
				Status:           domain.OrderStatus(row.Status),
				CreatedAt:        row.CreatedAt,
				UpdatedAt:        row.UpdatedAt,
				Products:         []domain.PaymentLine{},
			}
			ordersMap[row.OrderID] = order
		}

		// If product info exists, add it to the order.
		if row.ProductID != nil && row.Quantity != nil && row.ProductName != nil && row.ProductPrice != nil {
			prodID, err := uuid.Parse(*row.ProductID)
			if err != nil {
				return nil, fmt.Errorf("failed to parse product id: %w", err)
			}
			line := domain.PaymentLine{
				Product: domain.Product{
					ID:    prodID,
					Name:  *row.ProductName,
					Price: *row.ProductPrice,
				},
				Quantity: int(*row.Quantity),
			}
			order.Products = append(order.Products, line)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating rows: %w", err)
	}

	// Convert the map to a slice.
	orders := make([]*domain.Order, 0, len(ordersMap))
	for _, o := range ordersMap {
		orders = append(orders, o)
	}
	return orders, nil
}

// GetOrderById retrieves an order by its ID.
func (r *OrderRepository) FindByID(ctx context.Context, orderId uuid.UUID) (*domain.Order, error) {
	query := `
		SELECT 
			id, 
			user_id, 
			payment_mode, 
			mollie_payment_id, 
			mollie_payment_url, 
			status, 
			created_at, 
			updated_at
		FROM orders
		WHERE id = $1;
	`
	var ord domain.Order
	err := r.db.GetContext(ctx, &ord, query, orderId)
	if err != nil {
		return nil, fmt.Errorf("failed to query order: %w", err)
	}
	return &ord, nil
}

// createMolliePayment creates a Mollie payment for an domain.
func createMolliePayment(ctx context.Context, tx *sqlx.Tx, client *mollie.Client, ord *domain.Order) (*mollie.Payment, error) {
	// Extract user language from context; default to "fr" if not set.
	lang, _ := ctx.Value("lang").(string)

	// Generate payment lines using the order's product lines.
	paymentLines, err := getMolliePaymentLines(ctx, tx, ord)
	if err != nil {
		return nil, fmt.Errorf("failed to get payment lines: %w", err)
	}

	amount, err := getTotalAmount(paymentLines)
	if err != nil {
		return nil, fmt.Errorf("failed to get total payment amount: %w", err)
	}

	// Retrieve base URLs from environment variables.
	appBaseUrl := os.Getenv("APP_BASE_URL")
	if appBaseUrl == "" {
		return nil, fmt.Errorf("APP_BASE_URL is required")
	}
	// @TODO: Uncomment when in production.
	//apiBaseUrl := os.Getenv("API_BASE_URL")
	//if apiBaseUrl == "" {
	//	return nil, fmt.Errorf("API_BASE_URL is required")
	//}

	// Build webhook and redirect endpoints.
	//webhookEndpoint := apiBaseUrl + "/payments/webhook"
	//redirectEndpoint := appBaseUrl + "/order-completed/" + ord.ID.String()

	webhookEndpoint := "https://nuagemagique.dev/payments/webhook"
	redirectEndpoint := "https://nuagemagique.dev/order-completed/" + ord.ID.String()

	// Determine locale based on user language.
	locale := mollie.Locale("fr_FR")
	if lang == "en" || lang == "zh" {
		locale = "en_GB"
	}

	// Debug: Print payment lines (print contents, not the address).
	fmt.Println(paymentLines)

	// Construct the payment request.
	paymentRequest := mollie.CreatePayment{
		Amount: &mollie.Amount{
			Value:    amount,
			Currency: "EUR",
		},
		Description: "Tokyo Sushi Bar - " + generateOrderReference(ord.ID),
		RedirectURL: redirectEndpoint,
		WebhookURL:  webhookEndpoint,
		Locale:      locale,
		Lines:       paymentLines,
	}

	// Create the payment via the Mollie client.
	_, payment, err := client.Payments.Create(ctx, paymentRequest, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create Mollie payment: %w", err)
	}

	return payment, nil
}

// getMolliePaymentLines builds the Mollie payment lines based on the order form.
func getMolliePaymentLines(ctx context.Context, tx *sqlx.Tx, ord *domain.Order) ([]mollie.PaymentLines, error) {
	// Extract user language from context; default to "fr" if not set.
	lang, ok := ctx.Value("lang").(string)
	if !ok || lang == "" {
		lang = "fr"
	}

	// Gather product IDs from the order's product lines.
	productIDs := make([]uuid.UUID, 0, len(ord.Products))
	for _, pl := range ord.Products {
		productIDs = append(productIDs, pl.Product.ID)
	}
	if len(productIDs) == 0 {
		return nil, fmt.Errorf("no products found in order")
	}

	// Build placeholders and arguments for the IN clause.
	placeholders := make([]string, len(productIDs))
	args := make([]interface{}, 0, len(productIDs)+1)
	for i, id := range productIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args = append(args, id)
	}
	// Append the language as the final argument.
	args = append(args, lang)

	// Build the query.
	// Note: The final parameter (language) is at position len(args)
	query := fmt.Sprintf(`
		SELECT 
			p.id, 
			pt.name, 
			p.price
		FROM 
			products p
		INNER JOIN 
			product_translations pt ON p.id = pt.product_id
		WHERE 
			p.id IN (%s)
			AND pt.locale = $%d
			AND p.is_active = true
	`, strings.Join(placeholders, ","), len(args))

	// Use SQLX's SelectContext to retrieve products.
	var products []domain.Product
	if err := tx.SelectContext(ctx, &products, query, args...); err != nil {
		return nil, fmt.Errorf("querying products for payment lines failed: %w", err)
	}

	// Build a map for quick product lookup by ID.
	prodMap := make(map[uuid.UUID]domain.Product, len(products))
	for _, p := range products {
		prodMap[p.ID] = p
	}

	// Construct payment lines by matching each order product with the retrieved product info.
	paymentLines := make([]mollie.PaymentLines, 0, len(ord.Products))
	for _, pl := range ord.Products {
		prod, found := prodMap[pl.Product.ID]
		if !found {
			return nil, fmt.Errorf("product %s not found", pl.Product.ID)
		}

		unitPriceStr := strconv.FormatFloat(prod.Price, 'f', 2, 64)
		totalAmountStr := strconv.FormatFloat(prod.Price*float64(pl.Quantity), 'f', 2, 64)
		paymentLine := mollie.PaymentLines{
			Description:  prod.Name,
			Quantity:     pl.Quantity,
			QuantityUnit: "pcs",
			UnitPrice:    &mollie.Amount{Value: unitPriceStr, Currency: "EUR"},
			TotalAmount:  &mollie.Amount{Value: totalAmountStr, Currency: "EUR"},
		}
		paymentLines = append(paymentLines, paymentLine)
	}

	return paymentLines, nil
}

// getTotalAmount calculates the total amount for the payment lines.
func getTotalAmount(paymentLines []mollie.PaymentLines) (string, error) {
	totalAmount := 0.0
	for _, line := range paymentLines {
		value, err := strconv.ParseFloat(line.TotalAmount.Value, 64)
		if err != nil {
			return "", fmt.Errorf("failed to parse float: %v", err)
		}
		totalAmount += value
	}
	return strconv.FormatFloat(totalAmount, 'f', 2, 64), nil
}

// linkOrderProduct creates the order-product relationships in the DB.
func linkOrderProduct(ctx context.Context, tx *sqlx.Tx, orderId uuid.UUID, productLines []domain.PaymentLine) error {
	if len(productLines) == 0 {
		return nil
	}

	placeholders := make([]string, len(productLines))
	args := make([]interface{}, 0, len(productLines)*3)
	for i, pl := range productLines {
		placeholders[i] = fmt.Sprintf("($%d, $%d, $%d)", i*3+1, i*3+2, i*3+3)
		args = append(args, orderId, pl.Product.ID, pl.Quantity)
	}

	query := "INSERT INTO order_product (order_id, product_id, quantity) VALUES " + strings.Join(placeholders, ", ")
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("failed to insert order products: %w", err)
	}
	return nil
}

// generateOrderReference generates a reference string for the domain.
func generateOrderReference(orderID uuid.UUID) string {
	currentDate := time.Now().Format("20060102")
	shortUUID := strings.ToUpper(orderID.String()[:8])
	return fmt.Sprintf("#%s-%s", currentDate, shortUUID)
}
