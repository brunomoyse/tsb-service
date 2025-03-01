package infrastructure

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/jmoiron/sqlx"

	"tsb-service/internal/modules/product/domain"

	"github.com/google/uuid"
)

// ProductRepositoryImpl implements domain.ProductRepository using a SQL database.
type ProductRepository struct {
	db *sqlx.DB
}

// NewProductRepository creates a new instance of ProductRepositoryImpl.
func NewProductRepository(db *sqlx.DB) domain.ProductRepository {
	return &ProductRepository{db: db}
}

// Save inserts a product and its translations.
func (r *ProductRepository) Save(ctx context.Context, product *domain.Product) (err error) {
	// Begin a transaction.
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// Ensure rollback on error.
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	// Insert the product.
	query := `
		INSERT INTO products (id, price, code, slug, is_active, is_halal, is_vegan, category_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`
	_, err = tx.ExecContext(ctx, query,
		product.ID.String(),
		product.Price,
		product.Code,
		product.Slug,
		product.IsActive,
		product.IsHalal,
		product.IsVegan,
		product.CategoryID.String(),
	)
	if err != nil {
		return fmt.Errorf("failed to insert product: %w", err)
	}

	// Insert each translation.
	translationQuery := `
		INSERT INTO product_translations (id, product_id, language, name, description)
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

// FindByID retrieves a product by its ID.
func (r *ProductRepository) FindByID(ctx context.Context, id string) (*domain.Product, error) {
	query := `
        SELECT 
            p.id,
            p.price,
            p.code,
            p.slug,
            p.is_active,
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
            p.is_active,
            p.is_halal,
            p.is_vegan,
            p.category_id,
            p.created_at,
            p.updated_at,
            t.locale,
            t.name,
            t.description
        FROM products p
        LEFT JOIN product_translations t ON p.id = t.product_id;
    `
	return r.queryProducts(ctx, query)
}

// FindByCategoryID retrieves products filtered by a specific category ID.
func (r *ProductRepository) FindByCategoryID(ctx context.Context, categoryID string) ([]*domain.Product, error) {
	query := `
        SELECT 
            p.id,
            p.price,
            p.code,
            p.slug,
            p.is_active,
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
	defer rows.Close()

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
	defer rows.Close()

	// Define a helper struct for scanning rows.
	type productRow struct {
		ID               string    `db:"id"`
		Price            float64   `db:"price"`
		Code             *string   `db:"code"`
		Slug             *string   `db:"slug"`
		IsActive         bool      `db:"is_active"`
		IsHalal          bool      `db:"is_halal"`
		IsVegan          bool      `db:"is_vegan"`
		CategoryID       string    `db:"category_id"`
		CreatedAt        time.Time `db:"created_at"`
		UpdatedAt        time.Time `db:"updated_at"`
		Locale           *string   `db:"locale"`
		TransName        *string   `db:"name"`
		TransDescription *string   `db:"description"`
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
				IsActive:     row.IsActive,
				IsHalal:      row.IsHalal,
				IsVegan:      row.IsVegan,
				CategoryID:   categoryID,
				CreatedAt:    row.CreatedAt,
				UpdatedAt:    row.UpdatedAt,
				Translations: []domain.Translation{},
			}
			// Set Code and Slug if available.
			if row.Code != nil {
				prod.Code = row.Code
			}
			if row.Slug != nil {
				prod.Slug = row.Slug
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
