package repository

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"tsb-service/pkg/utils"

	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
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
func (r *OrderRepository) Save(ctx context.Context, client *mollie.Client, order *domain.Order) (*domain.Order, error) {
	// Begin a transaction using sqlx.
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	// Ensure rollback on error.
	defer func() {
		if err != nil {
			err := tx.Rollback()
			if err != nil {
				return
			}
		}
	}()

	// Insert the order without payment details.
	const insertQuery = `
		INSERT INTO orders (user_id, payment_mode, status, delivery_option)
		VALUES ($1, $2, $3, $4)
		RETURNING id;
	`

	var orderID string
	if err = tx.GetContext(ctx, &orderID, insertQuery, order.UserID, order.PaymentMode, order.Status, order.DeliveryOption); err != nil {
		return nil, fmt.Errorf("failed to insert new order: %w", err)
	}
	order.ID, err = uuid.Parse(orderID)
	if err != nil {
		return nil, fmt.Errorf("failed to parse order id: %w", err)
	}

	if order.PaymentMode != nil && *order.PaymentMode == domain.PaymentModeOnline {
		err := handleOnlinePayment(ctx, tx, client, order)
		if err != nil {
			return nil, err
		}
	}

	// Link the order with its product lines.
	if err = linkOrderProduct(ctx, tx, order.ID, order.Products); err != nil {
		return nil, fmt.Errorf("failed to link order with product lines: %w", err)
	}

	// Commit the transaction.
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return r.FindByID(ctx, order.ID)
}

func (r *OrderRepository) Update(ctx context.Context, order *domain.Order) error {
	query := `
		UPDATE orders
		SET status = $1, updated_at = CURRENT_TIMESTAMP
		WHERE id = $2;
	`
	if _, err := r.db.ExecContext(ctx, query, order.Status, order.ID); err != nil {
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
		OrderID             string    `db:"order_id"`
		UserID              string    `db:"user_id"`
		PaymentMode         string    `db:"payment_mode"`
		MolliePaymentId     *string   `db:"mollie_payment_id"`
		MolliePaymentUrl    *string   `db:"mollie_payment_url"`
		DeliveryOption      string    `db:"delivery_option"`
		Status              string    `db:"status"`
		CreatedAt           time.Time `db:"created_at"`
		UpdatedAt           time.Time `db:"updated_at"`
		ProductID           *string   `db:"product_id"`
		Quantity            *int64    `db:"quantity"`
		ProductName         *string   `db:"product_name"`
		ProductUnitPrice    *float64  `db:"product_unit_price"`
		ProductTotalPrice   *float64  `db:"product_total_price"`
		ProductCode         *string   `db:"product_code"`
		ProductCategoryName *string   `db:"product_category_name"`
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
				ID:               ordID,
				UserID:           uID,
				PaymentMode:      (*domain.PaymentMode)(&row.PaymentMode),
				MolliePaymentId:  row.MolliePaymentId,
				MolliePaymentUrl: row.MolliePaymentUrl,
				DeliveryOption:   domain.DeliveryOption(row.DeliveryOption),
				Status:           domain.OrderStatus(row.Status),
				CreatedAt:        row.CreatedAt,
				UpdatedAt:        row.UpdatedAt,
				Products:         []domain.PaymentLine{},
			}
			orders = append(orders, currentOrder)
		}

		if row.ProductID != nil {
			prodID, err := uuid.Parse(*row.ProductID)
			if err != nil {
				return nil, fmt.Errorf("failed to parse product id: %w", err)
			}

			currentOrder.Products = append(currentOrder.Products, domain.PaymentLine{
				Product: domain.Product{
					ID:           prodID,
					Code:         *row.ProductCode,
					CategoryName: *row.ProductCategoryName,
					Name:         *row.ProductName,
				},
				Quantity:   int(*row.Quantity),
				UnitPrice:  *row.ProductUnitPrice,
				TotalPrice: *row.ProductTotalPrice,
			})
		}
	}

	return orders, nil
}

// FindByID retrieves an order by its ID.
func (r *OrderRepository) FindByID(ctx context.Context, orderId uuid.UUID) (*domain.Order, error) {
	query := `
		SELECT 
			id, 
			user_id, 
			payment_mode, 
			mollie_payment_id, 
			mollie_payment_url, 
			delivery_option,
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
		OrderID             string    `db:"order_id"`
		UserID              string    `db:"user_id"`
		PaymentMode         string    `db:"payment_mode"`
		MolliePaymentId     *string   `db:"mollie_payment_id"`
		MolliePaymentUrl    *string   `db:"mollie_payment_url"`
		DeliveryOption      string    `db:"delivery_option"`
		Status              string    `db:"status"`
		CreatedAt           time.Time `db:"created_at"`
		UpdatedAt           time.Time `db:"updated_at"`
		ProductID           *string   `db:"product_id"`
		Quantity            *int64    `db:"quantity"`
		ProductName         *string   `db:"product_name"`
		ProductUnitPrice    *float64  `db:"product_unit_price"`
		ProductTotalPrice   *float64  `db:"product_total_price"`
		ProductCode         *string   `db:"product_code"`
		ProductCategoryName *string   `db:"product_category_name"`
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
				ID:               ordID,
				UserID:           uID,
				PaymentMode:      (*domain.PaymentMode)(&row.PaymentMode),
				MolliePaymentId:  row.MolliePaymentId,
				MolliePaymentUrl: row.MolliePaymentUrl,
				DeliveryOption:   domain.DeliveryOption(row.DeliveryOption),
				Status:           domain.OrderStatus(row.Status),
				CreatedAt:        row.CreatedAt,
				UpdatedAt:        row.UpdatedAt,
				Products:         []domain.PaymentLine{},
			}
			orders = append(orders, currentOrder)
		}

		// Append product if exists
		if row.ProductID != nil {
			prodID, err := uuid.Parse(*row.ProductID)
			if err != nil {
				return nil, fmt.Errorf("failed to parse product id: %w", err)
			}

			currentOrder.Products = append(currentOrder.Products, domain.PaymentLine{
				Product: domain.Product{
					ID:           prodID,
					Code:         *row.ProductCode,
					CategoryName: *row.ProductCategoryName,
					Name:         *row.ProductName,
				},
				Quantity:   int(*row.Quantity),
				UnitPrice:  *row.ProductUnitPrice,
				TotalPrice: *row.ProductTotalPrice,
			})
		}
	}

	return orders, nil
}

func (r *OrderRepository) OrderFillPrices(ctx context.Context, order *domain.Order) (*domain.Order, error) {
	for i, line := range order.Products {
		query := `
			SELECT 
				id, 
				price
			FROM 
				products
			WHERE 
				id = $1;
		`

		var product struct {
			ID    uuid.UUID `db:"id"`
			Price float64   `db:"price"`
		}

		if err := r.db.GetContext(ctx, &product, query, line.Product.ID); err != nil {
			return nil, fmt.Errorf("failed to get product price: %w", err)
		}

		order.Products[i].UnitPrice = product.Price
		order.Products[i].TotalPrice = product.Price * float64(line.Quantity)
	}

	return order, nil
}

func handleOnlinePayment(ctx context.Context, tx *sqlx.Tx, client *mollie.Client, ord *domain.Order) error {
	// Create the Mollie payment using the order details.
	payment, err := createMolliePayment(ctx, tx, client, ord)
	if err != nil || payment == nil {
		return fmt.Errorf("failed to create Mollie payment: %w", err)
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
	if _, err = tx.ExecContext(ctx, updateQuery, ord.MolliePaymentId, ord.MolliePaymentUrl, ord.ID); err != nil {
		return fmt.Errorf("failed to update order with Mollie payment details: %w", err)
	}

	return nil
}

// createMolliePayment creates a Mollie payment
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

	webhookUrl := os.Getenv("MOLLIE_WEBHOOK_URL")
	if webhookUrl == "" {
		return nil, fmt.Errorf("MOLLIE_WEBHOOK_URL is required")
	}

	redirectEndpoint := appBaseUrl + "/order-completed/" + ord.ID.String()

	// Determine locale based on user language.
	locale := mollie.Locale("fr_FR")
	if lang == "en" || lang == "zh" {
		locale = "en_GB"
	}

	// Construct the payment request.
	paymentRequest := mollie.CreatePayment{
		Amount: &mollie.Amount{
			Value:    amount,
			Currency: "EUR",
		},
		Description: "Tokyo Sushi Bar - " + generateOrderReference(ord.ID),
		RedirectURL: redirectEndpoint,
		WebhookURL:  webhookUrl,
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

func getMolliePaymentLines(ctx context.Context, tx *sqlx.Tx, ord *domain.Order) ([]mollie.PaymentLines, error) {
	// Default language to "fr".
	lang := "fr"
	if l, ok := ctx.Value("lang").(string); ok && l != "" {
		lang = l
	}

	if len(ord.Products) == 0 {
		return nil, fmt.Errorf("no products found in order")
	}

	// Collect product IDs from the order.
	productIDs := make([]uuid.UUID, len(ord.Products))
	for i, line := range ord.Products {
		productIDs[i] = line.Product.ID
	}

	// Build the query using sqlx.In.
	query := `
		SELECT 
			p.id, 
			pt.name, 
			p.price
		FROM 
			products p
		INNER JOIN 
			product_translations pt ON p.id = pt.product_id
		WHERE 
			p.id IN (?)
			AND pt.locale = ?
			AND p.is_available = true
	`
	query, args, err := sqlx.In(query, productIDs, lang)
	if err != nil {
		return nil, fmt.Errorf("preparing query: %w", err)
	}
	query = tx.Rebind(query)

	// Define an inline type to match the query result.
	type productRow struct {
		ID    uuid.UUID `db:"id"`
		Name  string    `db:"name"`
		Price float64   `db:"price"`
	}
	var rows []productRow
	if err := tx.SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("querying products for payment lines failed: %w", err)
	}

	// Build a lookup map.
	productMap := make(map[uuid.UUID]productRow, len(rows))
	for _, p := range rows {
		productMap[p.ID] = p
	}

	// Construct the Mollie payment lines.
	var paymentLines []mollie.PaymentLines
	for _, line := range ord.Products {
		prod, ok := productMap[line.Product.ID]
		if !ok {
			return nil, fmt.Errorf("product %s not found", line.Product.ID)
		}
		unitPriceStr := strconv.FormatFloat(prod.Price, 'f', 2, 64)
		totalAmountStr := strconv.FormatFloat(prod.Price*float64(line.Quantity), 'f', 2, 64)
		paymentLines = append(paymentLines, mollie.PaymentLines{
			Description:  prod.Name,
			Quantity:     line.Quantity,
			QuantityUnit: "pcs",
			UnitPrice:    &mollie.Amount{Value: unitPriceStr, Currency: "EUR"},
			TotalAmount:  &mollie.Amount{Value: totalAmountStr, Currency: "EUR"},
		})
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
		return fmt.Errorf("no products found in order")
	}

	const fieldsPerRow = 5
	placeholders := make([]string, len(productLines))
	args := make([]interface{}, 0, len(productLines)*fieldsPerRow)

	for i, line := range productLines {
		placeholders[i] = fmt.Sprintf("($%d, $%d, $%d, $%d, $%d)",
			i*fieldsPerRow+1, i*fieldsPerRow+2, i*fieldsPerRow+3, i*fieldsPerRow+4, i*fieldsPerRow+5)
		args = append(args, orderId, line.Product.ID, line.Quantity, line.UnitPrice, line.TotalPrice)
	}

	query := "INSERT INTO order_product (order_id, product_id, quantity, unit_price, total_price) VALUES " +
		strings.Join(placeholders, ", ")

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
