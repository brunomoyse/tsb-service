package repository

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"tsb-service/internal/order"

	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/google/uuid"
)

// OrderRepository defines the DB operations for orders.
type OrderRepository interface {
	CreateOrder(ctx context.Context, client *mollie.Client, form order.CreateOrderForm, currentUserLang string, currentUserId uuid.UUID) (*order.Order, error)
	GetOrdersForUser(ctx context.Context, userId uuid.UUID) ([]order.Order, error)
	GetOrderById(ctx context.Context, orderId uuid.UUID) (*order.Order, error)
	UpdateOrderStatus(ctx context.Context, paymentID string, paymentStatus string) error
}

type orderRepository struct {
	db *sql.DB
}

// NewOrderRepository creates a new OrderRepository instance.
func NewOrderRepository(db *sql.DB) OrderRepository {
	return &orderRepository{db: db}
}

// CreateOrder inserts a new order, creates a Mollie payment, updates the order with payment details,
// and links the order with its product lines.
func (r *orderRepository) CreateOrder(ctx context.Context, client *mollie.Client, form order.CreateOrderForm, currentUserLang string, currentUserId uuid.UUID) (*order.Order, error) {
	var ord order.Order
	status := order.OrderStatus(order.OrderStatusOpen)
	paymentMode := order.OrderPaymentMode(order.PaymentModeOnline)

	// Insert order without payment details.
	query := `
		INSERT INTO orders (user_id, payment_mode, status, created_at)
		VALUES ($1, $2, $3, NOW())
		RETURNING id, user_id, payment_mode, status, created_at, updated_at;
	`
	err := r.db.QueryRowContext(ctx, query, currentUserId, paymentMode, status).Scan(
		&ord.ID,
		&ord.UserId,
		&ord.PaymentMode,
		&ord.Status,
		&ord.CreatedAt,
		&ord.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to insert new order: %v", err)
	}

	// Create the Mollie payment.
	payment, err := createMolliePayment(ctx, r.db, client, form, ord.ID, currentUserLang)
	if err != nil || payment == nil {
		return nil, fmt.Errorf("failed to create Mollie payment: %v", err)
	}

	ord.MolliePaymentId = &(payment.ID)
	ord.MolliePaymentUrl = &(payment.Links.Checkout.Href)

	// Update the order with Mollie payment details.
	updateQuery := `
		UPDATE orders
		SET mollie_payment_id = $1, mollie_payment_url = $2, updated_at = NOW()
		WHERE id = $3
		RETURNING id, mollie_payment_id, mollie_payment_url, updated_at;
	`
	err = r.db.QueryRowContext(ctx, updateQuery, ord.MolliePaymentId, ord.MolliePaymentUrl, ord.ID).Scan(
		&ord.ID,
		&ord.MolliePaymentId,
		&ord.MolliePaymentUrl,
		&ord.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to update order with Mollie payment details: %v", err)
	}

	// Link the order with product lines.
	err = linkOrderProduct(ctx, r.db, ord.ID, form.ProductsLines)
	if err != nil {
		return nil, fmt.Errorf("failed to link the new order with products: %v", err)
	}

	return &ord, nil
}

// createMolliePayment creates a Mollie payment for an order.
func createMolliePayment(ctx context.Context, db *sql.DB, client *mollie.Client, form order.CreateOrderForm, orderId uuid.UUID, currentUserLang string) (*mollie.Payment, error) {
	paymentLines, err := getMolliePaymentLines(ctx, db, form, currentUserLang)
	if err != nil {
		return nil, fmt.Errorf("failed to get payment lines: %v", err)
	}

	amount, err := getTotalAmount(paymentLines)
	if err != nil {
		return nil, fmt.Errorf("failed to get total payment amount: %v", err)
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
	redirectEndpoint := appBaseUrl + "/order-completed/" + orderId.String()

	locale := mollie.Locale("fr_FR")
	if currentUserLang == "en" || currentUserLang == "zh" {
		locale = mollie.Locale("en_GB")
	}

	paymentRequest := mollie.CreatePayment{
		Amount: &mollie.Amount{
			Value:    amount,
			Currency: "EUR",
		},
		Description: "Tokyo Sushi Bar - " + generateOrderReference(orderId),
		RedirectURL: redirectEndpoint,
		WebhookURL:  webhookEndpoint,
		Locale:      locale,
	}

	_, payment, err := client.Payments.Create(ctx, paymentRequest, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create Mollie payment: %v", err)
	}

	return payment, nil
}

// getMolliePaymentLines builds the Mollie payment lines based on the order form.
func getMolliePaymentLines(ctx context.Context, db *sql.DB, form order.CreateOrderForm, currentUserLang string) ([]mollie.PaymentLines, error) {
	var paymentLines []mollie.PaymentLines

	// Gather product IDs from the form.
	productIds := make([]uuid.UUID, 0)
	for _, pl := range form.ProductsLines {
		productIds = append(productIds, pl.Product.ID)
	}
	if len(productIds) == 0 {
		return nil, fmt.Errorf("no products found in order")
	}

	placeholders := []string{}
	args := []interface{}{}
	for i, id := range productIds {
		placeholders = append(placeholders, fmt.Sprintf("$%d", i+1))
		args = append(args, id)
	}
	// Append the current user language.
	args = append(args, currentUserLang)
	query := fmt.Sprintf(`
		SELECT 
			p.id, pt.name, p.price
		FROM 
			products p 
		INNER JOIN 
			product_translations pt ON p.id = pt.product_id
		WHERE 
			p.id IN (%s)
			AND pt.locale = $%d
			AND p.is_active = true
	`, strings.Join(placeholders, ","), len(args))

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var products []order.ProductInfo
	for rows.Next() {
		var product order.ProductInfo
		if err := rows.Scan(&product.ID, &product.Name, &product.Price); err != nil {
			return nil, fmt.Errorf("failed to scan product: %v", err)
		}
		products = append(products, product)
	}

	// Build payment lines from the product lines.
	for _, pl := range form.ProductsLines {
		var prod order.ProductInfo
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
func linkOrderProduct(ctx context.Context, db *sql.DB, orderId uuid.UUID, productLines []order.ProductLine) error {
	query := "INSERT INTO order_product (order_id, product_id, quantity) VALUES "
	values := []interface{}{}
	valueStrings := []string{}
	for i, pl := range productLines {
		valueStrings = append(valueStrings, fmt.Sprintf("($%d, $%d, $%d)", i*3+1, i*3+2, i*3+3))
		values = append(values, orderId, pl.Product.ID, pl.Quantity)
	}
	query += strings.Join(valueStrings, ", ")
	_, err := db.ExecContext(ctx, query, values...)
	if err != nil {
		return fmt.Errorf("failed to insert order products: %v", err)
	}
	return nil
}

// generateOrderReference generates a reference string for the order.
func generateOrderReference(orderID uuid.UUID) string {
	currentDate := time.Now().Format("20060102")
	shortUUID := strings.ToUpper(orderID.String()[:8])
	return fmt.Sprintf("#%s-%s", currentDate, shortUUID)
}

// GetOrdersForUser retrieves all orders for a given user.
func (r *orderRepository) GetOrdersForUser(ctx context.Context, userId uuid.UUID) ([]order.Order, error) {
	query := `
		SELECT id, user_id, payment_mode, mollie_payment_id, mollie_payment_url, status, created_at, updated_at
		FROM orders
		WHERE user_id = $1
		ORDER BY created_at DESC;
	`
	rows, err := r.db.QueryContext(ctx, query, userId)
	if err != nil {
		return nil, fmt.Errorf("failed to query orders: %v", err)
	}
	defer rows.Close()

	var orders []order.Order
	for rows.Next() {
		var ord order.Order
		if err := rows.Scan(
			&ord.ID,
			&ord.UserId,
			&ord.PaymentMode,
			&ord.MolliePaymentId,
			&ord.MolliePaymentUrl,
			&ord.Status,
			&ord.CreatedAt,
			&ord.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan order: %v", err)
		}
		orders = append(orders, ord)
	}
	return orders, nil
}

// GetOrderById retrieves an order by its ID.
func (r *orderRepository) GetOrderById(ctx context.Context, orderId uuid.UUID) (*order.Order, error) {
	query := `
		SELECT id, user_id, payment_mode, mollie_payment_id, mollie_payment_url, status, created_at, updated_at
		FROM orders
		WHERE id = $1;
	`
	var ord order.Order
	err := r.db.QueryRowContext(ctx, query, orderId).Scan(
		&ord.ID,
		&ord.UserId,
		&ord.PaymentMode,
		&ord.MolliePaymentId,
		&ord.MolliePaymentUrl,
		&ord.Status,
		&ord.CreatedAt,
		&ord.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query order: %v", err)
	}
	return &ord, nil
}

// UpdateOrderStatus updates an order’s status based on the Mollie payment status.
func (r *orderRepository) UpdateOrderStatus(ctx context.Context, paymentID string, paymentStatus string) error {
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
