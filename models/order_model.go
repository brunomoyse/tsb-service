package models

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"tsb-service/config"

	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/google/uuid"
)

// Order struct to represent an order
type Order struct {
	ID               uuid.UUID         `json:"id"`
	CreatedAt        time.Time         `json:"createdAt"`
	UpdatedAt        *time.Time        `json:"updatedAt"`
	UserId           uuid.UUID         `json:"userId"`
	PaymentMode      *OrderPaymentMode `json:"paymentMode"`
	MolliePaymentId  *string           `json:"molliePaymentId"`
	MolliePaymentUrl *string           `json:"molliePaymentUrl"`
	Status           OrderStatus       `json:"status"`
	// ShippingAddress  *mollie.Address       `json:"shipping_address"`
}

// OrderStatus represents the status of an order
type OrderStatus string

const (
	OrderStatusOpen       OrderStatus = "OPEN"
	OrderStatusCanceled   OrderStatus = "CANCELED"
	OrderStatusPending    OrderStatus = "PENDING"
	OrderStatusAuthorized OrderStatus = "AUTHORIZED"
	OrderStatusExpired    OrderStatus = "EXPIRED"
	OrderStatusFailed     OrderStatus = "FAILED"
	OrderStatusPaid       OrderStatus = "PAID"
)

// OrderPaymentMode represents the payment mode for an order
type OrderPaymentMode string

const (
	PaymentModeCash     OrderPaymentMode = "CASH"
	PaymentModeOnline   OrderPaymentMode = "ONLINE"
	PaymentModeTerminal OrderPaymentMode = "TERMINAL"
)

type CreateOrderForm struct {
	// ShippingAddress *mollie.Address `json:"shipping_address"`
	ProductsLines []ProductLine `json:"products"`
}

type ProductLine struct {
	ProductId uuid.UUID `json:"productId"`
	Quantity  int       `json:"quantity"`
}

// CreateOrder creates a new order in the database and then updates it with Mollie payment details
func CreateOrder(client *mollie.Client, form CreateOrderForm, currentUserLang string, currentUserId uuid.UUID) (*Order, error) {
	// Insert the order into the database without Mollie payment details
	var order Order
	status := OrderStatus(OrderStatusOpen)
	paymentMode := OrderPaymentMode(PaymentModeOnline)

	// Insert query
	query := `
	INSERT INTO orders (user_id, payment_mode, status, created_at)
	VALUES ($1, $2, $3, NOW())
	RETURNING id, user_id, payment_mode, status, created_at, updated_at;
	`

	// Execute the insert query and scan the results into the Order struct
	err := config.DB.QueryRow(query, currentUserId, paymentMode, status).Scan(
		&order.ID,
		&order.UserId,
		&order.PaymentMode,
		&order.Status,
		&order.CreatedAt,
		&order.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to insert new order: %v", err)
	}

	// Create the Mollie payment after the order has been inserted
	payment, err := CreateMolliePayment(client, form, order.ID, currentUserLang)
	if err != nil || payment == nil {
		return nil, fmt.Errorf("failed to create Mollie payment: %v", err)
	}

	// Update the order with Mollie payment details
	order.MolliePaymentId = &(payment.ID)
	order.MolliePaymentUrl = &(payment.Links.Checkout.Href)

	// Update the order in the database with Mollie payment details
	updateQuery := `
	UPDATE orders
	SET mollie_payment_id = $1, mollie_payment_url = $2, updated_at = NOW()
	WHERE id = $3
	RETURNING id, mollie_payment_id, mollie_payment_url, updated_at;
	`

	// Execute the update query and scan the updated values back into the Order struct
	err = config.DB.QueryRow(updateQuery, order.MolliePaymentId, order.MolliePaymentUrl, order.ID).Scan(
		&order.ID,
		&order.MolliePaymentId,
		&order.MolliePaymentUrl,
		&order.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to update order with Mollie payment details: %v", err)
	}

	// Link the order with the products
	err = LinkOrderProduct(order.ID, form.ProductsLines)
	if err != nil {
		return nil, fmt.Errorf("failed to link the new order with products: %v", err)
	}

	// Return the complete order object
	return &order, nil
}

// CreateMolliePayment creates a Mollie payment
func CreateMolliePayment(client *mollie.Client, form CreateOrderForm, orderId uuid.UUID, currentUserLang string) (*mollie.Payment, error) {
	// Get the product lines
	paymentLines, err := GetMolliePaymentLines(form, currentUserLang)
	if err != nil {
		return nil, fmt.Errorf("failed to get payment lines: %v", err)
	}

	// Get the total amount
	amount, err := GetTotalAmount(paymentLines)
	if err != nil {
		return nil, fmt.Errorf("failed to get total payment amount: %v", err)
	}

	appBaseUrl := os.Getenv("APP_BASE_URL")
	if appBaseUrl == "" {
		return nil, fmt.Errorf("APP_BASE_URL is required")
	}

	webhookEndpoint := appBaseUrl + "payments/webhook"
	redirectEndpoint := appBaseUrl + "order-completed/" + orderId.String()

	locale := mollie.Locale("fr_FR")

	if currentUserLang == "en" || currentUserLang == "zh" {
		locale = mollie.Locale("en_GB")
	}

	// Create a Mollie payment request
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

	// Call the Payments.Create function
	ctx := context.Background()
	_, payment, err := client.Payments.Create(ctx, paymentRequest, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create Mollie payment: %v", err)
	}

	return payment, nil
}

func GetMolliePaymentLines(form CreateOrderForm, currentUserLang string) ([]mollie.PaymentLines, error) {
	// Create a slice of Mollie payment lines
	var paymentLines []mollie.PaymentLines

	// Get all the product ids based on the product lines
	productIds := make([]uuid.UUID, 0)
	for _, productLine := range form.ProductsLines {
		productIds = append(productIds, productLine.ProductId)
	}

	// If no products, return an error
	if len(productIds) == 0 {
		return nil, fmt.Errorf("no products found in order")
	}

	// Build a dynamic query with placeholders for the product IDs
	placeholders := []string{}
	args := []interface{}{}
	for i, id := range productIds {
		placeholders = append(placeholders, fmt.Sprintf("$%d", i+1))
		args = append(args, id)
	}

	// Add the locale to the args
	args = append(args, currentUserLang)

	// Build the final query
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

	// Execute the query and scan the products
	rows, err := config.DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Scan the products
	var products []ProductInfo
	for rows.Next() {
		var product ProductInfo
		err := rows.Scan(&product.ID, &product.Name, &product.Price)
		if err != nil {
			return nil, fmt.Errorf("failed to scan product: %v", err)
		}

		products = append(products, product)
	}

	// Loop through the products and create a Mollie payment line
	for _, productLine := range form.ProductsLines {
		// Get the product based on the product ID
		var product ProductInfo

		// Find the matching product from the scanned products
		for _, p := range products {
			if p.ID == productLine.ProductId {
				product = p
				break
			}
		}

		productUnitPrice := strconv.FormatFloat(product.Price, 'f', 2, 64)
		totalLineAmount := strconv.FormatFloat(product.Price*float64(productLine.Quantity), 'f', 2, 64)

		// Create a new payment line
		paymentLine := mollie.PaymentLines{
			Description:  product.Name,
			Quantity:     productLine.Quantity,
			QuantityUnit: "pcs",
			UnitPrice:    &mollie.Amount{Value: productUnitPrice, Currency: "EUR"},
			TotalAmount:  &mollie.Amount{Value: totalLineAmount, Currency: "EUR"},
		}

		// Append the payment line to the slice
		paymentLines = append(paymentLines, paymentLine)
	}

	return paymentLines, nil
}

func GetTotalAmount(paymentLines []mollie.PaymentLines) (string, error) {
	totalAmount := 0.0

	for _, line := range paymentLines {
		totalLineValue, err := strconv.ParseFloat(line.TotalAmount.Value, 64)
		if err != nil {
			return "", fmt.Errorf("failed to parse float: %v", err)
		}
		totalAmount += totalLineValue
	}

	return strconv.FormatFloat(totalAmount, 'f', 2, 64), nil
}

func LinkOrderProduct(orderId uuid.UUID, productLines []ProductLine) error {
	// Build the SQL query dynamically
	query := "INSERT INTO order_product (order_id, product_id, quantity) VALUES "
	values := []interface{}{}

	// Dynamically build placeholders for each product (e.g., ($1, $2, $3), ($4, $5, $6), ...)
	valueStrings := []string{}
	for i, productLine := range productLines {
		// Placeholders like ($1, $2, $3), ($4, $5, $6), ...
		valueStrings = append(valueStrings, fmt.Sprintf("($%d, $%d, $%d)", i*3+1, i*3+2, i*3+3))
		values = append(values, orderId, productLine.ProductId, productLine.Quantity)
	}

	// Join all placeholders with commas
	query += strings.Join(valueStrings, ", ")

	// Execute the query
	_, err := config.DB.Exec(query, values...)
	if err != nil {
		return fmt.Errorf("failed to insert order products: %v", err)
	}

	return nil
}

// generateOrderReference generates a user-friendly order reference
func generateOrderReference(orderID uuid.UUID) string {
	// Get the current date in YYYY format
	currentDate := time.Now().Format("20060102")

	// Take the first 8 characters of the order UUID
	shortUUID := strings.ToUpper(orderID.String()[:8])

	// Format the order reference as: TSB-YYYYMMDD-UUID (first 8 chars of UUID)
	orderReference := fmt.Sprintf("#%s-%s", currentDate, shortUUID)

	return orderReference
}

func GetOrdersForUser(userId uuid.UUID) ([]Order, error) {
	// Query the database for all orders for the user
	query := `
	SELECT 
		id, user_id, payment_mode, mollie_payment_id, mollie_payment_url, status, created_at, updated_at
	FROM 
		orders
	WHERE 
		user_id = $1
	ORDER BY 
		created_at DESC;
	`

	// Execute the query
	rows, err := config.DB.Query(query, userId)
	if err != nil {
		return nil, fmt.Errorf("failed to query orders: %v", err)
	}
	defer rows.Close()

	// Create a slice of orders
	var orders []Order

	// Loop through the rows and scan the results into the Order struct
	for rows.Next() {
		var order Order
		err := rows.Scan(
			&order.ID,
			&order.UserId,
			&order.PaymentMode,
			&order.MolliePaymentId,
			&order.MolliePaymentUrl,
			&order.Status,
			&order.CreatedAt,
			&order.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan order: %v", err)
		}

		// Append the order to the slice
		orders = append(orders, order)
	}

	return orders, nil
}

func UpdateOrderStatus(paymentID string, paymentStatus string) error {
	// Update the order status in the database
	query := `
	UPDATE orders
	SET status = $1
	WHERE mollie_payment_id = $2
	RETURNING id;
	`

	// Init order status var
	var orderStatus string

	if paymentStatus == "paid" {
		orderStatus = "PAID"
	} else {
		orderStatus = "FAILED"
	}

	// Execute the query
	var orderID uuid.UUID

	err := config.DB.QueryRow(query, orderStatus, paymentID).Scan(&orderID)
	if err != nil {
		return fmt.Errorf("failed to update order status: %v", err)
	}

	return nil
}
