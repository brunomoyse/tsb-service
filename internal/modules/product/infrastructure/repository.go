package infrastructure

import (
	"context"
	"errors"
	"fmt"
	"github.com/gosimple/slug"
	"github.com/lib/pq"
	"github.com/shopspring/decimal"
	"regexp"
	"sort"
	"strconv"
	"time"
	"tsb-service/pkg/utils"

	"github.com/jmoiron/sqlx"

	"tsb-service/internal/modules/product/domain"

	"github.com/google/uuid"
)

type ProductRepository struct {
	db *sqlx.DB
}

func NewProductRepository(db *sqlx.DB) domain.ProductRepository {
	return &ProductRepository{db: db}
}

// Create inserts a product and its translations.
func (r *ProductRepository) Create(ctx context.Context, product *domain.Product) (err error) {
	// Begin a transaction.
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
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

	var frenchName string
	for _, t := range product.Translations {
		if t.Language == "fr" && t.Name != "" {
			frenchName = t.Name
			break
		}
	}

	// If we have a French name, generate a new slug.
	if frenchName != "" {
		var frenchCategoryName string
		// Query the product_category_translations table for the French name.
		queryCategory := `
		SELECT name
		FROM product_category_translations 
		WHERE product_category_id = $1 
		  AND locale = 'fr'
	`
		err = tx.QueryRowContext(ctx, queryCategory, product.CategoryID.String()).Scan(&frenchCategoryName)
		if err != nil {
			return fmt.Errorf("failed to fetch category translation name: %w", err)
		}

		// Create a slug by concatenating the French category name and the French product name.
		newSlug := slug.MakeLang(frenchCategoryName+" "+frenchName, "fr")
		// Update the product slug.
		product.Slug = &newSlug
	}

	// Insert the product.
	query := `
		INSERT INTO products (id, price, code, slug, piece_count, is_visible, is_available, is_halal, is_vegan, category_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`
	_, err = tx.ExecContext(ctx, query,
		product.ID.String(),
		product.Price,
		product.Code,
		product.Slug,
		product.PieceCount,
		product.IsVisible,
		product.IsAvailable,
		product.IsHalal,
		product.IsVegan,
		product.CategoryID.String(),
	)
	if err != nil {
		return fmt.Errorf("failed to insert product: %w", err)
	}

	// Insert each translation.
	translationQuery := `
		INSERT INTO product_translations (id, product_id, locale, name, description)
		VALUES ($1, $2, $3, $4, $5)
	`
	for _, t := range product.Translations {
		translationID := uuid.New().String()
		_, err = tx.ExecContext(ctx, translationQuery,
			translationID,
			product.ID.String(),
			t.Language,
			t.Name,
			t.Description,
		)
		if err != nil {
			return fmt.Errorf("failed to insert product translation: %w", err)
		}
	}

	// Commit the transaction.
	return tx.Commit()
}

// Update modifies a product and its translations.
func (r *ProductRepository) Update(ctx context.Context, product *domain.Product) error {
	// Begin a transaction.
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// Ensure a rollback happens if there's an error.
	defer func() {
		if err != nil {
			err := tx.Rollback()
			if err != nil {
				return
			}
		}
	}()

	// Check if a French translation with a non-empty name is provided.
	var frenchName string
	for _, t := range product.Translations {
		if t.Language == "fr" && t.Name != "" {
			frenchName = t.Name
			break
		}
	}

	// If we have a French name, generate a new slug.
	if frenchName != "" {
		var frenchCategoryName string
		// Query the product_category_translations table for the French name.
		queryCategory := `
		SELECT name 
		FROM product_category_translations 
		WHERE product_category_id = $1 
		  AND locale = 'fr'
	`
		err = tx.QueryRowContext(ctx, queryCategory, product.CategoryID.String()).Scan(&frenchCategoryName)
		if err != nil {
			return fmt.Errorf("failed to fetch category translation name: %w", err)
		}

		// Create a slug by concatenating the French category name and the French product name.
		newSlug := slug.MakeLang(frenchCategoryName+" "+frenchName, "fr")
		// Update the product slug.
		product.Slug = &newSlug
	}

	// Update the main product fields.
	updateQuery := `
		UPDATE products
		SET price = $2,
		    code = $3,
		    slug = $4,
		    piece_count = $5,
		    is_visible = $6,
		    is_available = $7,
		    is_halal = $8,
		    is_vegan = $9,
		    category_id = $10
		WHERE id = $1
	`
	_, err = tx.ExecContext(ctx, updateQuery,
		product.ID.String(),
		product.Price,
		product.Code,
		product.Slug,
		product.PieceCount,
		product.IsVisible,
		product.IsAvailable,
		product.IsHalal,
		product.IsVegan,
		product.CategoryID.String(),
	)
	if err != nil {
		return fmt.Errorf("failed to update product: %w", err)
	}

	// Upsert translations for the provided locales.
	// This query inserts a new translation, or if a conflict on (product_id, locale) occurs,
	// updates the name and description.
	upsertQuery := `
		INSERT INTO product_translations (product_id, locale, name, description)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (product_id, locale)
		DO UPDATE SET
		    name = EXCLUDED.name,
		    description = EXCLUDED.description
	`
	for _, t := range product.Translations {
		_, err = tx.ExecContext(ctx, upsertQuery,
			product.ID.String(),
			t.Language,
			t.Name,
			t.Description,
		)
		if err != nil {
			return fmt.Errorf("failed to upsert translation for locale %s: %w", t.Language, err)
		}
	}

	// Commit the transaction.
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}
	return nil
}

// FindByID retrieves a product by its ID.
func (r *ProductRepository) FindByID(ctx context.Context, id string) (*domain.Product, error) {
	query := `
        SELECT 
            p.id,
            p.price,
            p.code,
            p.slug,
			p.piece_count,
            p.is_visible,
            p.is_available,
            p.is_halal,
            p.is_vegan,
            p.category_id,
            p.created_at,
            p.updated_at,
            t.locale,
            t.name,
            t.description
        FROM products p
        LEFT JOIN product_translations t ON p.id = t.product_id
        WHERE p.id = $1;
    `
	products, err := r.queryProducts(ctx, query, id)
	if err != nil {
		return nil, err
	}
	if len(products) == 0 {
		return nil, errors.New("product not found")
	}
	return products[0], nil
}

// FindAll retrieves all products.
func (r *ProductRepository) FindAll(ctx context.Context) ([]*domain.Product, error) {
	query := `
        SELECT 
            p.id,
            p.price,
            p.code,
            p.slug,
			p.piece_count,
            p.is_visible,
            p.is_available,
            p.is_halal,
            p.is_vegan,
            p.category_id,
            p.created_at,
            p.updated_at,
            t.locale,
            t.name,
            t.description
        FROM products p
        LEFT JOIN product_translations t ON p.id = t.product_id
        ORDER BY p.code;
    `
	return r.queryProducts(ctx, query)
}

func (r *ProductRepository) FindByIDs(ctx context.Context, productIDs []string) ([]*domain.ProductOrderDetails, error) {
	lang := utils.GetLang(ctx)

	query := `
		SELECT 
			p.id,
			p.code,
			p.price,
			pct.name AS category_name,
			pt.name AS name
		FROM products p
		LEFT JOIN product_translations pt ON p.id = pt.product_id
		LEFT JOIN product_category_translations pct ON p.category_id = pct.product_category_id
		WHERE p.id = ANY($1)
		AND pt.locale = $2
		AND pct.locale = $2
		ORDER BY p.code;
	`
	var products []*domain.ProductOrderDetails
	err := r.db.SelectContext(ctx, &products, query, pq.Array(productIDs), lang)
	if err != nil {
		return nil, err
	}
	return products, nil
}

// FindByCategoryID retrieves products filtered by a specific category ID.
func (r *ProductRepository) FindByCategoryID(ctx context.Context, categoryID string) ([]*domain.Product, error) {
	query := `
        SELECT 
            p.id,
            p.price,
            p.code,
            p.slug,
			p.piece_count,
            p.is_visible,
            p.is_available,
            p.is_halal,
            p.is_vegan,
            p.category_id,
            p.created_at,
            p.updated_at,
            t.locale,
            t.name,
            t.description
        FROM products p
        LEFT JOIN product_translations t ON p.id = t.product_id
        WHERE p.category_id = $1;
    `
	return r.queryProducts(ctx, query, categoryID)
}

// FindAllCategories retrieves all categories and their translations.
func (r *ProductRepository) FindAllCategories(ctx context.Context) ([]*domain.Category, error) {
	query := `
        SELECT 
            c.id,
            c.order,
            t.locale,
            t.name
        FROM product_categories c
        LEFT JOIN product_category_translations t ON c.id = t.product_category_id;
    `
	// Use QueryxContext to get sqlx.Rows.
	rows, err := r.db.QueryxContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func(rows *sqlx.Rows) {
		err := rows.Close()
		if err != nil {
			return
		}
	}(rows)

	// Define a temporary struct that uses pointers for nullable columns.
	type categoryRow struct {
		ID     string  `db:"id"`
		Order  int     `db:"order"`
		Locale *string `db:"locale"`
		Name   *string `db:"name"`
	}

	// Use a map to group rows by category ID.
	categoriesMap := make(map[string]*domain.Category)
	for rows.Next() {
		var row categoryRow
		if err := rows.StructScan(&row); err != nil {
			return nil, err
		}

		// Check if the category has already been added.
		cat, exists := categoriesMap[row.ID]
		if !exists {
			categoryID, err := uuid.Parse(row.ID)
			if err != nil {
				return nil, err
			}
			cat = &domain.Category{
				ID:           categoryID,
				Order:        row.Order,
				Translations: []domain.Translation{},
			}
			categoriesMap[row.ID] = cat
		}

		// Append a translation if both locale and name are non-nil.
		if row.Locale != nil && row.Name != nil {
			translation := domain.Translation{
				Language: *row.Locale,
				Name:     *row.Name,
			}
			cat.Translations = append(cat.Translations, translation)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Convert the map to a slice.
	var categories []*domain.Category
	for _, cat := range categoriesMap {
		categories = append(categories, cat)
	}

	// Sort the slice by the Order field.
	sort.Slice(categories, func(i, j int) bool {
		return categories[i].Order < categories[j].Order
	})

	return categories, nil
}

// queryProducts is a helper method that executes the given query with optional arguments,
// groups the rows by product, and sorts the final slice.
func (r *ProductRepository) queryProducts(ctx context.Context, query string, args ...any) ([]*domain.Product, error) {
	// Use QueryxContext from sqlx.
	rows, err := r.db.QueryxContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func(rows *sqlx.Rows) {
		err := rows.Close()
		if err != nil {

		}
	}(rows)

	// Define a helper struct for scanning rows.
	type productRow struct {
		ID               string          `db:"id"`
		Price            decimal.Decimal `db:"price"`
		Code             *string         `db:"code"`
		Slug             *string         `db:"slug"`
		PieceCount       *int            `db:"piece_count"`
		IsVisible        bool            `db:"is_visible"`
		IsAvailable      bool            `db:"is_available"`
		IsHalal          bool            `db:"is_halal"`
		IsVegan          bool            `db:"is_vegan"`
		CategoryID       string          `db:"category_id"`
		CreatedAt        time.Time       `db:"created_at"`
		UpdatedAt        time.Time       `db:"updated_at"`
		Locale           *string         `db:"locale"`
		TransName        *string         `db:"name"`
		TransDescription *string         `db:"description"`
	}

	// Group rows by product ID.
	productsMap := make(map[string]*domain.Product)
	for rows.Next() {
		var row productRow
		if err := rows.StructScan(&row); err != nil {
			return nil, err
		}

		prod, exists := productsMap[row.ID]
		if !exists {
			productID, err := uuid.Parse(row.ID)
			if err != nil {
				return nil, err
			}
			categoryID, err := uuid.Parse(row.CategoryID)
			if err != nil {
				return nil, err
			}
			prod = &domain.Product{
				ID:           productID,
				Price:        row.Price,
				PieceCount:   row.PieceCount,
				IsVisible:    row.IsVisible,
				IsAvailable:  row.IsAvailable,
				IsHalal:      row.IsHalal,
				IsVegan:      row.IsVegan,
				CategoryID:   categoryID,
				CreatedAt:    row.CreatedAt,
				UpdatedAt:    row.UpdatedAt,
				Translations: []domain.Translation{},
			}
			// Check if available.
			if row.Code != nil {
				prod.Code = row.Code
			}
			if row.Slug != nil {
				prod.Slug = row.Slug
			}
			if row.PieceCount != nil {
				prod.PieceCount = row.PieceCount
			}

			productsMap[row.ID] = prod
		}

		// Append translation if available.
		if row.Locale != nil && row.TransName != nil {
			trans := domain.Translation{
				Language: *row.Locale,
				Name:     *row.TransName,
			}
			if row.TransDescription != nil {
				trans.Description = row.TransDescription
			}
			prod.Translations = append(prod.Translations, trans)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Convert the map to a slice.
	products := make([]*domain.Product, 0, len(productsMap))
	for _, p := range productsMap {
		products = append(products, p)
	}

	// Sorting logic mimicking the desired SQL order.
	alphaRegexp := regexp.MustCompile(`^[A-Za-z]+`)
	numRegexp := regexp.MustCompile(`[0-9]+`)

	// Helper to extract French translation name or fallback.
	getFrenchName := func(translations []domain.Translation) string {
		for _, t := range translations {
			if t.Language == "fr" {
				return t.Name
			}
		}
		if len(translations) > 0 {
			return translations[0].Name
		}
		return ""
	}

	sort.Slice(products, func(i, j int) bool {
		// Retrieve product codes, defaulting to empty string.
		codeA := ""
		if products[i].Code != nil {
			codeA = *products[i].Code
		}
		codeB := ""
		if products[j].Code != nil {
			codeB = *products[j].Code
		}

		// Extract the alphabetical parts.
		alphaA := alphaRegexp.FindString(codeA)
		alphaB := alphaRegexp.FindString(codeB)
		if alphaA != alphaB {
			return alphaA < alphaB
		}

		// Extract numeric parts.
		numAStr := numRegexp.FindString(codeA)
		numBStr := numRegexp.FindString(codeB)
		numA, numB := 0, 0
		if numAStr != "" {
			if n, err := strconv.Atoi(numAStr); err == nil {
				numA = n
			}
		}
		if numBStr != "" {
			if n, err := strconv.Atoi(numBStr); err == nil {
				numB = n
			}
		}
		if numA != numB {
			return numA < numB
		}

		// Finally, compare using the French translation's name.
		tnameA := getFrenchName(products[i].Translations)
		tnameB := getFrenchName(products[j].Translations)
		return tnameA < tnameB
	})

	return products, nil
}
