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

type orderRepository struct {
	db *sqlx.DB
}

func NewOrderRepository(db *sqlx.DB) domain.OrderRepository {
	return &orderRepository{db: db}
}

// CreateOrder inserts a new order, creates a Mollie payment, updates the order with payment details,
// and links the order with its product lines.
func (r *orderRepository) Save(ctx context.Context, client *mollie.Client, ord *domain.Order) (*domain.Order, error) {
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

	return ord, nil
}

// UpdateOrderStatus updates an order’s status based on the Mollie payment status.
func (r *orderRepository) UpdateStatus(ctx context.Context, paymentID string, paymentStatus string) error {
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
func (r *orderRepository) FindByUserID(ctx context.Context, userId uuid.UUID) ([]*domain.Order, error) {
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
		WHERE user_id = $1
		ORDER BY created_at DESC;
	`
	var orders []*domain.Order
	err := r.db.SelectContext(ctx, &orders, query, userId)
	if err != nil {
		return nil, fmt.Errorf("failed to query orders: %w", err)
	}
	return orders, nil
}

// GetOrderById retrieves an order by its ID.
func (r *orderRepository) FindByID(ctx context.Context, orderId uuid.UUID) (*domain.Order, error) {
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

	appBaseUrl := os.Getenv("APP_BASE_URL")
	if appBaseUrl == "" {
		return nil, fmt.Errorf("APP_BASE_URL is required")
	}
	apiBaseUrl := os.Getenv("API_BASE_URL")
	if apiBaseUrl == "" {
		return nil, fmt.Errorf("API_BASE_URL is required")
	}

	webhookEndpoint := apiBaseUrl + "/payments/webhook"
	redirectEndpoint := appBaseUrl + "/order-completed/" + ord.ID.String()

	// Determine locale based on userLanguage.
	locale := mollie.Locale("fr_FR")
	if lang == "en" || lang == "zh" {
		locale = mollie.Locale("en_GB")
	}

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

	_, payment, err := client.Payments.Create(ctx, paymentRequest, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create Mollie payment: %w", err)
	}

	return payment, nil
}

// getMolliePaymentLines builds the Mollie payment lines based on the order form.
func getMolliePaymentLines(ctx context.Context, tx *sqlx.Tx, ord *domain.Order) ([]mollie.PaymentLines, error) {
	// Extract user language from context; default to "fr" if not set.
	lang, _ := ctx.Value("lang").(string)

	var paymentLines []mollie.PaymentLines

	// Gather product IDs from the order's product lines.
	productIDs := make([]uuid.UUID, 0, len(ord.Products))
	for _, pl := range ord.Products {
		productIDs = append(productIDs, pl.Product.ID)
	}
	if len(productIDs) == 0 {
		return nil, fmt.Errorf("no products found in order")
	}

	// Build placeholders and arguments for the IN clause.
	placeholders := make([]string, 0, len(productIDs))
	args := make([]interface{}, 0, len(productIDs)+1)
	for i, id := range productIDs {
		placeholders = append(placeholders, fmt.Sprintf("$%d", i+1))
		args = append(args, id)
	}
	// Append the order language as the final argument.
	args = append(args, lang)

	// Build the query.
	// Note: We assume the language column in product_translations matches the order's Language.
	// Also, p.is_active must be true.
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

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying products for payment lines failed: %w", err)
	}
	defer rows.Close()

	var products []domain.Product
	for rows.Next() {
		var product domain.Product
		if err := rows.Scan(&product.ID, &product.Name, &product.Price); err != nil {
			return nil, fmt.Errorf("failed to scan product: %w", err)
		}
		products = append(products, product)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating product rows: %w", err)
	}

	// Build payment lines by matching each order product line with the retrieved product info.
	for _, pl := range ord.Products {
		var prod domain.Product
		for _, p := range products {
			if p.ID == pl.Product.ID {
				prod = p
				break
			}
		}
		productUnitPrice := strconv.FormatFloat(prod.Price, 'f', 2, 64)
		totalLineAmount := strconv.FormatFloat(prod.Price*float64(pl.Quantity), 'f', 2, 64)
		paymentLine := mollie.PaymentLines{
			Description:  prod.Name,
			Quantity:     pl.Quantity,
			QuantityUnit: "pcs",
			UnitPrice:    &mollie.Amount{Value: productUnitPrice, Currency: "EUR"},
			TotalAmount:  &mollie.Amount{Value: totalLineAmount, Currency: "EUR"},
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
