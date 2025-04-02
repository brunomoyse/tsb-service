package infrastructure

/*
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
			p.price,
			p.code,
			pct.name AS category_name
		FROM
			products p
		INNER JOIN
			product_translations pt ON p.id = pt.product_id
		INNER JOIN
			product_category_translations pct ON p.category_id = pct.product_category_id
		WHERE
			p.id IN (?)
			AND pt.locale = 'fr'
		  	AND pct.locale = 'fr'
			AND p.is_available = true
	`
	query, args, err := sqlx.In(query, productIDs)
	if err != nil {
		return nil, fmt.Errorf("preparing query: %w", err)
	}
	query = tx.Rebind(query)

	// Define an inline type to match the query result.
	type productRow struct {
		ID           uuid.UUID `db:"id"`
		Name         string    `db:"name"`
		Price        decimal.Decimal   `db:"price"`
		Code         *string   `db:"code"`
		CategoryName string    `db:"category_name"`
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
		totalAmountStr := strconv.FormatFloat(prod.Price*decimal.Decimal(line.Quantity), 'f', 2, 64)
		var description string
		if prod.Code != nil && *prod.Code != "" {
			description = fmt.Sprintf("%s - %s %s", *prod.Code, prod.CategoryName, prod.Name)
		} else {
			description = fmt.Sprintf("%s %s", prod.CategoryName, prod.Name)
		}
		paymentLines = append(paymentLines, mollie.PaymentLines{
			Description:  description,
			Quantity:     line.Quantity,
			QuantityUnit: "pcs",
			UnitPrice:    &mollie.Amount{Value: unitPriceStr, Currency: "EUR"},
			TotalAmount:  &mollie.Amount{Value: totalAmountStr, Currency: "EUR"},
		})
	}
	return paymentLines, nil
}
*/
