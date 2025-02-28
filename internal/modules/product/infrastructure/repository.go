package infrastructure

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"time"

	"tsb-service/internal/modules/product/domain"

	"github.com/google/uuid"
)

// ProductRepositoryImpl implements domain.ProductRepository using a SQL database.
type ProductRepositoryImpl struct {
	db *sql.DB
}

// NewProductRepository creates a new instance of ProductRepositoryImpl.
func NewProductRepository(db *sql.DB) domain.ProductRepository {
	return &ProductRepositoryImpl{db: db}
}

// Save inserts a product and its translations.
func (r *ProductRepositoryImpl) Save(ctx context.Context, product *domain.Product) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	_, err = tx.ExecContext(ctx, `
        INSERT INTO products (id, price, code, slug, is_active, is_halal, is_vegan, category_id, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `,
		product.ID.String(),
		product.Price,
		product.Code,
		product.Slug,
		product.IsActive,
		product.IsHalal,
		product.IsVegan,
		product.CategoryID.String(),
		product.CreatedAt,
		product.UpdatedAt,
	)
	if err != nil {
		return err
	}

	for _, t := range product.Translations {
		translationID := uuid.New().String()
		_, err = tx.ExecContext(ctx, `
            INSERT INTO product_translations (id, product_id, language, name, description)
            VALUES (?, ?, ?, ?, ?)
        `,
			translationID,
			product.ID.String(),
			t.Language,
			t.Name,
			t.Description,
		)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// FindByID retrieves a product by its ID.
func (r *ProductRepositoryImpl) FindByID(ctx context.Context, id string) (*domain.Product, error) {
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
func (r *ProductRepositoryImpl) FindAll(ctx context.Context) ([]*domain.Product, error) {
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
func (r *ProductRepositoryImpl) FindByCategoryID(ctx context.Context, categoryID string) ([]*domain.Product, error) {
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
func (r *ProductRepositoryImpl) FindAllCategories(ctx context.Context) ([]*domain.Category, error) {
	query := `
        SELECT 
            c.id,
            c.order,
            t.locale,
            t.name
        FROM product_categories c
        LEFT JOIN product_category_translations t ON c.id = t.product_category_id;
    `
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Use a map to group rows by category ID.
	categoriesMap := make(map[string]*domain.Category)

	for rows.Next() {
		var (
			idStr    string
			order    int
			langNull sql.NullString
			nameNull sql.NullString
		)

		if err := rows.Scan(&idStr, &order, &langNull, &nameNull); err != nil {
			return nil, err
		}

		// Check if the category has been added already.
		cat, exists := categoriesMap[idStr]
		if !exists {
			categoryID, err := uuid.Parse(idStr)
			if err != nil {
				return nil, err
			}
			cat = &domain.Category{
				ID:           categoryID,
				Order:        order,
				Translations: []domain.Translation{},
			}
			categoriesMap[idStr] = cat
		}

		// If there's a valid translation row, add it.
		if langNull.Valid && nameNull.Valid {
			translation := domain.Translation{
				Language: langNull.String,
				Name:     nameNull.String,
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
func (r *ProductRepositoryImpl) queryProducts(ctx context.Context, query string, args ...any) ([]*domain.Product, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Group rows by product ID.
	productsMap := make(map[string]*domain.Product)

	for rows.Next() {
		var (
			idStr, categoryStr         string
			price                      float64
			code, slug                 sql.NullString
			isActive, isHalal, isVegan bool
			createdAt, updatedAt       time.Time
			language, transName        sql.NullString
			transDescription           sql.NullString
		)

		if err := rows.Scan(
			&idStr, &price, &code, &slug, &isActive, &isHalal, &isVegan, &categoryStr, &createdAt, &updatedAt,
			&language, &transName, &transDescription,
		); err != nil {
			return nil, err
		}

		prod, exists := productsMap[idStr]
		if !exists {
			productID, err := uuid.Parse(idStr)
			if err != nil {
				return nil, err
			}
			categoryID, err := uuid.Parse(categoryStr)
			if err != nil {
				return nil, err
			}
			prod = &domain.Product{
				ID:           productID,
				Price:        price,
				IsActive:     isActive,
				IsHalal:      isHalal,
				IsVegan:      isVegan,
				CategoryID:   categoryID,
				CreatedAt:    createdAt,
				UpdatedAt:    updatedAt,
				Translations: []domain.Translation{},
			}
			if code.Valid {
				prod.Code = &code.String
			}
			if slug.Valid {
				prod.Slug = &slug.String
			}
			productsMap[idStr] = prod
		}

		// Append translation if available.
		if language.Valid {
			trans := domain.Translation{
				Language: language.String,
				Name:     transName.String,
			}
			if transDescription.Valid {
				trans.Description = &transDescription.String
			}
			prod.Translations = append(prod.Translations, trans)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Convert map to slice.
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
		// Compare the alphabetical part of the code.
		codeA := ""
		if products[i].Code != nil {
			codeA = *products[i].Code
		}
		codeB := ""
		if products[j].Code != nil {
			codeB = *products[j].Code
		}

		alphaA := alphaRegexp.FindString(codeA)
		alphaB := alphaRegexp.FindString(codeB)
		if alphaA != alphaB {
			return alphaA < alphaB
		}

		// Compare the numeric part.
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
