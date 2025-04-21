package infrastructure

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/gosimple/slug"
	"github.com/lib/pq"
	"github.com/shopspring/decimal"
	"log"
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
		  AND language = 'fr'
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
		INSERT INTO products (id, price, code, piece_count, slug, is_visible, is_available, is_halal, is_vegan, category_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`
	_, err = tx.ExecContext(ctx, query,
		product.ID.String(),
		product.Price,
		product.Code,
		product.PieceCount,
		product.Slug,
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
		  AND language = 'fr'
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

	// Upsert translations for the provided languages.
	// This query inserts a new translation, or if a conflict on (product_id, language) occurs,
	// updates the name and description.
	upsertQuery := `
		INSERT INTO product_translations (product_id, language, name, description)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (product_id, language)
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
			return fmt.Errorf("failed to upsert translation for language %s: %w", t.Language, err)
		}
	}

	// Commit the transaction.
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}
	return nil
}

// FindByID retrieves a product by its ID.
func (r *ProductRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Product, error) {
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
            t.language,
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
            t.language,
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
		AND pt.language = $2
		AND pct.language = $2
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
            t.language,
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
            t.language,
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
		ID       string  `db:"id"`
		Order    int     `db:"order"`
		Language *string `db:"language"`
		Name     *string `db:"name"`
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

		// Append a translation if both language and name are non-nil.
		if row.Language != nil && row.Name != nil {
			translation := domain.Translation{
				Language: *row.Language,
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

// FindCategoryByID retrieves a category by its ID.
func (r *ProductRepository) FindCategoryByID(ctx context.Context, id uuid.UUID) (*domain.Category, error) {
	const query = `
        SELECT 
            c.id,
            c.order,
            t.language,
            t.name
        FROM product_categories c
        LEFT JOIN product_category_translations t 
          ON c.id = t.product_category_id
        WHERE c.id = $1;
    `
	rows, err := r.db.QueryxContext(ctx, query, id)
	if err != nil {
		log.Printf("FindCategoryByID: query failed: %v", err)
		return nil, err
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			log.Printf("FindCategoryByID: error closing rows: %v", cerr)
		}
	}()

	// temp struct for each row
	type categoryRow struct {
		ID       uuid.UUID `db:"id"`
		Order    int       `db:"order"`
		Language *string   `db:"language"`
		Name     *string   `db:"name"`
	}

	var cat *domain.Category

	for rows.Next() {
		var cr categoryRow
		if err := rows.StructScan(&cr); err != nil {
			log.Printf("FindCategoryByID: row scan error: %v", err)
			return nil, err
		}

		// on first row, initialize the domain.Category
		if cat == nil {
			cat = &domain.Category{
				ID:           cr.ID,
				Order:        cr.Order,
				Translations: make([]domain.Translation, 0, 1),
			}
		}

		// only append if we actually have a translation row
		if cr.Language != nil && cr.Name != nil {
			cat.Translations = append(cat.Translations, domain.Translation{
				Language:    *cr.Language,
				Name:        *cr.Name,
				Description: nil, // no description selected here
			})
		}
	}

	if err := rows.Err(); err != nil {
		log.Printf("FindCategoryByID: row iteration error: %v", err)
		return nil, err
	}

	if cat == nil {
		// no rows => not found
		return nil, sql.ErrNoRows
	}

	return cat, nil
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
		Language         *string         `db:"language"`
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
		if row.Language != nil && row.TransName != nil {
			trans := domain.Translation{
				Language: *row.Language,
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

func (r *ProductRepository) FindCategoriesByProductIDs(
	ctx context.Context,
	productIDs []string,
) (map[string][]*domain.Category, error) {
	// 1) Query without language‑filter so we get every translation row
	query := `
    SELECT 
        p.id               AS product_id,
        pc.id              AS category_id,
        pc.order           AS category_order,
        pct.language       AS language,
        pct.name           AS category_name
    FROM products p
    JOIN product_categories pc ON p.category_id = pc.id
    JOIN product_category_translations pct ON pc.id = pct.product_category_id
    WHERE p.id = ANY($1)
    `
	rows, err := r.db.QueryxContext(ctx, query, pq.Array(productIDs))
	if err != nil {
		log.Printf("FindCategoriesByProductIDs: Query failed: %v", err)
		return nil, err
	}
	defer func() {
		if err := rows.Close(); err != nil {
			log.Printf("FindCategoriesByProductIDs: Error closing rows: %v", err)
		}
	}()

	// 2) Temp structure: productID → (categoryID → *domain.Category)
	temp := make(map[string]map[uuid.UUID]*domain.Category)

	for rows.Next() {
		var cr struct {
			ProductID     uuid.UUID `db:"product_id"`
			CategoryID    uuid.UUID `db:"category_id"`
			CategoryOrder int       `db:"category_order"`
			Language      string    `db:"language"`
			CategoryName  string    `db:"category_name"`
		}
		if err := rows.StructScan(&cr); err != nil {
			log.Printf("FindCategoriesByProductIDs: Error scanning row: %v", err)
			return nil, err
		}

		pid := cr.ProductID.String()
		if _, ok := temp[pid]; !ok {
			temp[pid] = make(map[uuid.UUID]*domain.Category)
		}

		catMap := temp[pid]
		if cat, exists := catMap[cr.CategoryID]; exists {
			// we've already seen this category → just append another translation
			cat.Translations = append(cat.Translations, domain.Translation{
				Language: cr.Language,
				Name:     cr.CategoryName,
			})
		} else {
			// first time we see this category for this product
			catMap[cr.CategoryID] = &domain.Category{
				ID:    cr.CategoryID,
				Order: cr.CategoryOrder,
				Translations: []domain.Translation{{
					Language: cr.Language,
					Name:     cr.CategoryName,
				}},
			}
		}
	}
	if err := rows.Err(); err != nil {
		log.Printf("FindCategoriesByProductIDs: Row iteration error: %v", err)
		return nil, err
	}

	// 3) Flatten to the desired result type: map[string][]*domain.Category
	result := make(map[string][]*domain.Category, len(temp))
	for pid, catsByID := range temp {
		slice := make([]*domain.Category, 0, len(catsByID))
		for _, cat := range catsByID {
			slice = append(slice, cat)
		}
		result[pid] = slice
	}

	return result, nil
}

// FindByCategoryIDs retrieves products for each of the given category IDs,
// batching the SQL-to-domain mapping via queryProducts, then grouping by category.
func (r *ProductRepository) FindByCategoryIDs(ctx context.Context, categoryIDs []string) (map[string][]*domain.Product, error) {
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
            t.language,
            t.name,
            t.description
        FROM products p
        LEFT JOIN product_translations t ON p.id = t.product_id
        WHERE p.category_id = ANY($1)
        ORDER BY p.code;
    `

	// Delegate to the shared helper to do the heavy lifting.
	products, err := r.queryProducts(ctx, query, pq.Array(categoryIDs))
	if err != nil {
		log.Printf("FindByCategoryIDs: queryProducts failed: %v", err)
		return nil, err
	}

	// Group the returned products by their CategoryID.
	result := make(map[string][]*domain.Product, len(products))
	for _, p := range products {
		key := p.CategoryID.String()
		result[key] = append(result[key], p)
	}

	return result, nil
}

func (r *ProductRepository) BatchGetProductByIDs(ctx context.Context, productIDs []string) (map[string][]*domain.Product, error) {
	if len(productIDs) == 0 {
		return map[string][]*domain.Product{}, nil
	}

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
            t.language,
            t.name,
            t.description
        FROM products p
        LEFT JOIN product_translations t ON p.id = t.product_id
        WHERE p.id = ANY($1)
        ORDER BY p.code;
    `

	// Appel du helper pour exécuter la requête et mapper les produits
	products, err := r.queryProducts(ctx, query, pq.Array(productIDs))
	if err != nil {
		return nil, err
	}

	// On groupe les produits par leur ID
	result := make(map[string][]*domain.Product, len(productIDs))
	for _, p := range products {
		key := p.ID.String()
		result[key] = append(result[key], p)
	}

	return result, nil
}

func (r *ProductRepository) BatchGetProductTranslations(
	ctx context.Context,
	productIDs []string,
) (map[string][]*domain.Translation, error) {
	// 1) early return for empty input
	if len(productIDs) == 0 {
		return make(map[string][]*domain.Translation), nil
	}

	// 2) pull back product_id so we can key our map
	const query = `
    SELECT
      pt.product_id,
      pt.language,
      pt.name,
      pt.description
    FROM product_translations AS pt
    WHERE pt.product_id = ANY($1);
    `

	// 3) execute the query
	rows, err := r.db.QueryxContext(ctx, query, pq.Array(productIDs))
	if err != nil {
		return nil, fmt.Errorf("failed to query product translations: %w", err)
	}
	defer rows.Close()

	// 4) prepare the result map
	translationsByProduct := make(map[string][]*domain.Translation)

	// 5) scan & group
	for rows.Next() {
		var tr struct {
			ProductID string `db:"product_id"`
			domain.Translation
		}
		if err := rows.StructScan(&tr); err != nil {
			return nil, fmt.Errorf("failed to scan translation row: %w", err)
		}

		t := &domain.Translation{
			Language:    tr.Language,
			Name:        tr.Name,
			Description: tr.Description,
		}
		translationsByProduct[tr.ProductID] = append(translationsByProduct[tr.ProductID], t)
	}

	// 6) catch any iteration error
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating translation rows: %w", err)
	}

	return translationsByProduct, nil
}

func (r *ProductRepository) BatchGetCategoryTranslations(
	ctx context.Context,
	categoryIDs []string,
) (map[string][]*domain.Translation, error) {
	// 1) early exit on no input
	if len(categoryIDs) == 0 {
		return make(map[string][]*domain.Translation), nil
	}

	// 2) pull back the FK so we can key the map
	const query = `
    SELECT
      pct.product_category_id,
      pct.language,
      pct.name
    FROM product_category_translations AS pct
    WHERE pct.product_category_id = ANY($1);
    `

	// 3) run the query with pq.Array
	rows, err := r.db.QueryxContext(ctx, query, pq.Array(categoryIDs))
	if err != nil {
		return nil, fmt.Errorf("failed to query category translations: %w", err)
	}
	defer rows.Close()

	// 4) prepare the result map
	translationsByCat := make(map[string][]*domain.Translation)

	// 5) scan each row and group
	for rows.Next() {
		var tr struct {
			CategoryID string `db:"product_category_id"`
			Language   string `db:"language"`
			Name       string `db:"name"`
		}
		if err := rows.StructScan(&tr); err != nil {
			return nil, fmt.Errorf("failed to scan translation row: %w", err)
		}

		t := &domain.Translation{
			Language: tr.Language,
			Name:     tr.Name,
		}
		translationsByCat[tr.CategoryID] = append(translationsByCat[tr.CategoryID], t)
	}

	// 6) check iteration errors
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating translation rows: %w", err)
	}

	return translationsByCat, nil
}
