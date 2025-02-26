package repository

import (
	"database/sql"
	"fmt"
	"strings"

	model "tsb-service/internal/product"

	"github.com/google/uuid"
)

type ProductRepository interface {
	GetDashboardProducts(lang string) ([]model.DashboardProductListItem, error)
	GetDashboardProductByID(productID uuid.UUID) (model.DashboardProductDetails, error)
	GetDashboardCategories() ([]model.DashboardCategoryDetails, error)
	GetProductsGroupedByCategory(lang string) ([]model.CategoryWithProducts, error)
	GetCategories(lang string) ([]model.Category, error)
	GetProductsByCategory(lang string, categoryID uuid.UUID) ([]model.ProductInfo, error)
	UpdateProduct(productID uuid.UUID, form model.UpdateProductForm) (model.ProductFormResponse, error)
	CreateProduct(form model.CreateProductForm) (model.ProductFormResponse, error)
	CategoryExists(categoryID uuid.UUID) (bool, error)
}

type productRepository struct {
	db *sql.DB
}

func NewProductRepository(db *sql.DB) *productRepository {
	return &productRepository{db: db}
}

func (r *productRepository) GetDashboardProducts(lang string) ([]model.DashboardProductListItem, error) {
	query := `
	SELECT 
	    p.id,
	    pt.name,
	    p.code,
	    p.is_active,
		p.is_halal,
		p.is_vegan,
		pct.name AS category
	FROM 
	    products p
	INNER JOIN
	    product_translations pt
		ON p.id = pt.product_id
	INNER JOIN
	    product_categories pc
		ON p.category_id = pc.id
	INNER JOIN
	    product_category_translations pct
		ON pc.id = pct.product_category_id
	WHERE
		pt.locale = $1
	    AND pct.locale = $1
	ORDER BY 
		pc."order" ASC, -- Sort categories by "order"
		substring(p.code, '^[A-Za-z]+') ASC, -- Sort by the alphabetical part of the code (e.g., 'A')
		NULLIF(substring(p.code, '[0-9]+')::int, 0) ASC, -- Sort by the numeric part as an integer
		pt.name ASC; -- Sort by name if the codes are identical
	`

	rows, err := r.db.Query(query, lang)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var products []model.DashboardProductListItem
	for rows.Next() {
		var product model.DashboardProductListItem
		err := rows.Scan(
			&product.ID,
			&product.Name,
			&product.Code,
			&product.IsActive,
			&product.IsHalal,
			&product.IsVegan,
			&product.CategoryName,
		)
		if err != nil {
			return nil, err
		}
		products = append(products, product)
	}

	return products, nil
}

func (r *productRepository) GetDashboardProductByID(productID uuid.UUID) (model.DashboardProductDetails, error) {
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
	    pt.name,
	    pt.description,
		pt.locale
	FROM 
	    products p
	INNER JOIN
		product_translations pt
		ON p.id = pt.product_id
	WHERE
		p.id = $1
	`

	// Declare the product and initialize the translations slice
	var product model.DashboardProductDetails
	product.Translations = []*model.ProductTranslation{}

	// Execute the query
	rows, err := r.db.Query(query, productID)
	if err != nil {
		return product, err
	}
	defer rows.Close()

	// Flag to check if we have processed the first row
	firstRow := true

	// Iterate over the rows
	for rows.Next() {
		var translation model.ProductTranslation

		// Declare temporary variables for product-specific fields
		var id uuid.UUID
		var price float64
		var code, slug *string
		var isActive bool
		var isHalal bool
		var isVegan bool
		var categoryId uuid.UUID

		if firstRow {
			// Scan product-specific fields and translation fields in the first row
			err := rows.Scan(
				&product.ID,
				&product.Price,
				&product.Code,
				&product.Slug,
				&product.IsActive,
				&product.IsHalal,
				&product.IsVegan,
				&product.CategoryId,
				&translation.Name,
				&translation.Description,
				&translation.Locale,
			)
			if err != nil {
				return product, err
			}
			firstRow = false
		} else {
			// Scan only the translation fields and use dummy variables for product fields
			err := rows.Scan(
				&id, // Ignore product-specific fields
				&price,
				&code,
				&slug,
				&isActive,
				&isHalal,
				&isVegan,
				&categoryId,
				&translation.Name,
				&translation.Description,
				&translation.Locale,
			)
			if err != nil {
				return product, err
			}
		}

		// Append the translation to the product's translations slice
		product.Translations = append(product.Translations, &translation)
	}

	// Check for any errors during row iteration
	if err = rows.Err(); err != nil {
		return product, err
	}

	return product, nil
}

func (r *productRepository) GetDashboardCategories() ([]model.DashboardCategoryDetails, error) {
	query := `
	SELECT 
	    pc.id,
	    pct.name,
	    pct.locale
	FROM 
	    product_categories pc
	INNER JOIN
	    product_category_translations pct
		ON pc.id = pct.product_category_id
	ORDER BY 
		pc."order" ASC, -- Sort categories by "order"
		pct.name ASC; -- Sort by name
	`

	rows, err := r.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Create a map to hold categories temporarily, using category ID as the key
	categoryMap := make(map[uuid.UUID]*model.DashboardCategoryDetails)

	for rows.Next() {
		var id uuid.UUID
		var translation model.CategoryTranslation

		// Scan the current row
		err := rows.Scan(
			&id,
			&translation.Name,
			&translation.Locale,
		)
		if err != nil {
			return nil, err
		}

		// Check if the category already exists in the map
		if category, exists := categoryMap[id]; exists {
			// Append the new translation to the existing category
			category.Translations = append(category.Translations, &translation)
		} else {
			// Create a new category and append the first translation
			newCategory := model.DashboardCategoryDetails{
				ID:           id,
				Translations: []*model.CategoryTranslation{&translation}, // Initialize with the first translation
			}
			// Add the new category to the map
			categoryMap[id] = &newCategory
		}
	}

	// Convert the map back to a slice
	var categories []model.DashboardCategoryDetails
	for _, category := range categoryMap {
		categories = append(categories, *category)
	}

	return categories, nil
}

func (r *productRepository) GetProductsGroupedByCategory(lang string) ([]model.CategoryWithProducts, error) {
	query := `
	SELECT 
	    pc.id AS product_category_id,
	    pct.name AS product_category_name,
	    pc."order",
	    p.id AS product_id,
	    pt.name AS product_name,
	    pt.description,
	    p.price,
	    p.code,
	    p.slug,
	    p.is_active,
		p.is_halal,
		p.is_vegan
	FROM 
	    product_categories pc
	INNER JOIN 
	    product_category_translations pct 
	    ON pc.id = pct.product_category_id
	INNER JOIN 
	    products p 
	    ON pc.id = p.category_id
	INNER JOIN
	    product_translations pt 
	    ON p.id = pt.product_id
	WHERE 
	    pt.locale = $1
	    AND pct.locale = $1
	    AND p.is_active = true
	ORDER BY 
		pc."order" ASC, -- Sort categories by "order"
		substring(p.code, '^[A-Za-z]+') ASC, -- Sort by the alphabetical part of the code (e.g., 'A')
		NULLIF(substring(p.code, '[0-9]+')::int, 0) ASC, -- Sort by the numeric part as an integer
		pt.name ASC; -- Sort by name if the codes are identical
	`

	rows, err := r.db.Query(query, lang)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var categories []model.CategoryWithProducts
	var currentCategory *model.CategoryWithProducts

	for rows.Next() {
		var category model.CategoryWithProducts
		var product model.ProductInfo

		err := rows.Scan(
			&category.ID,
			&category.Name,
			&category.Order,
			&product.ID,
			&product.Name,
			&product.Description,
			&product.Price,
			&product.Code,
			&product.Slug,
			&product.IsActive,
			&product.IsHalal,
			&product.IsVegan,
		)
		if err != nil {
			return nil, err
		}

		// If it's a new category, append the current category (if any) and start a new one
		if currentCategory == nil || currentCategory.ID != category.ID {
			if currentCategory != nil {
				categories = append(categories, *currentCategory)
			}
			currentCategory = &model.CategoryWithProducts{
				ID:       category.ID,
				Name:     category.Name,
				Order:    category.Order,
				Products: []model.ProductInfo{},
			}
		}

		// Add the product to the current category
		currentCategory.Products = append(currentCategory.Products, product)
	}

	// Append the last category (if not nil)
	if currentCategory != nil {
		categories = append(categories, *currentCategory)
	}

	return categories, nil
}

func (r *productRepository) GetCategories(lang string) ([]model.Category, error) {
	query := `
	SELECT 
	    pc.id,
	    pct.name,
	    pc."order"
	FROM 
	    product_categories pc
	INNER JOIN
	    product_category_translations pct
		ON pc.id = pct.product_category_id
	WHERE 
		pct.locale = $1
	ORDER BY 
		pc."order" ASC, -- Sort categories by "order"
		pct.name ASC; -- Sort by name
	`

	rows, err := r.db.Query(query, lang)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	categories := []model.Category{}
	for rows.Next() {
		var category model.Category
		err := rows.Scan(
			&category.ID,
			&category.Name,
			&category.Order,
		)
		if err != nil {
			return nil, err
		}
		categories = append(categories, category)
	}

	return categories, nil
}

func (r *productRepository) GetProductsByCategory(lang string, categoryID uuid.UUID) ([]model.ProductInfo, error) {
	query := `
	SELECT 
	    p.id,
	    pt.name,
	    pt.description,
	    p.price,
	    p.code,
	    p.slug,
	    p.is_active,
		p.is_halal,
		p.is_vegan
	FROM 
	    products p
	INNER JOIN
		product_translations pt
		ON p.id = pt.product_id
	WHERE
		p.category_id = $1
		AND pt.locale = $2
		AND p.is_active = true
	ORDER BY
		substring(p.code, '^[A-Za-z]+') ASC, -- Sort by the alphabetical part of the code (e.g., 'A')
		NULLIF(substring(p.code, '[0-9]+')::int, 0) ASC, -- Sort by the numeric part as an integer
		pt.name ASC; -- Sort by name if the codes are identical
	`

	rows, err := r.db.Query(query, categoryID, lang)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	products := []model.ProductInfo{}

	for rows.Next() {
		var product model.ProductInfo
		err := rows.Scan(
			&product.ID,
			&product.Name,
			&product.Description,
			&product.Price,
			&product.Code,
			&product.Slug,
			&product.IsActive,
			&product.IsHalal,
			&product.IsVegan,
		)
		if err != nil {
			return nil, err
		}
		products = append(products, product)
	}

	return products, nil
}

func (r *productRepository) UpdateProduct(productID uuid.UUID, form model.UpdateProductForm) (model.ProductFormResponse, error) {
	// Start the transaction.
	tx, err := r.db.Begin()
	if err != nil {
		return model.ProductFormResponse{}, err
	}
	// Rollback if any error occurs.
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	// Build the update query dynamically.
	updateFields := []string{}
	// args[0] is reserved for productId in the WHERE clause.
	args := []interface{}{productID}

	if form.Price != nil {
		updateFields = append(updateFields, fmt.Sprintf("price = $%d", len(args)+1))
		args = append(args, *form.Price)
	}
	if form.Code != nil {
		updateFields = append(updateFields, fmt.Sprintf("code = $%d", len(args)+1))
		args = append(args, *form.Code)
	}
	// Update booleans only if a value was provided.
	if form.IsActive != nil {
		updateFields = append(updateFields, fmt.Sprintf("is_active = $%d", len(args)+1))
		args = append(args, *form.IsActive)
	}
	if form.IsHalal != nil {
		updateFields = append(updateFields, fmt.Sprintf("is_halal = $%d", len(args)+1))
		args = append(args, *form.IsHalal)
	}
	if form.IsVegan != nil {
		updateFields = append(updateFields, fmt.Sprintf("is_vegan = $%d", len(args)+1))
		args = append(args, *form.IsVegan)
	}
	if form.CategoryId != nil {
		updateFields = append(updateFields, fmt.Sprintf("category_id = $%d", len(args)+1))
		args = append(args, *form.CategoryId)
	}

	if len(updateFields) > 0 {
		query := fmt.Sprintf("UPDATE products SET %s WHERE id = $1", strings.Join(updateFields, ", "))
		if _, err = tx.Exec(query, args...); err != nil {
			return model.ProductFormResponse{}, err
		}
	}

	// Process and update translations.
	translations := make([]model.ProductTranslation, len(form.Translations))
	for i, t := range form.Translations {
		translations[i] = model.ProductTranslation{
			Locale:      t.Locale,
			Name:        t.Name,
			Description: t.Description,
		}
	}
	if err = createUpdateProductTranslations(productID, translations, tx); err != nil {
		return model.ProductFormResponse{}, err
	}

	// Commit the transaction.
	if err = tx.Commit(); err != nil {
		return model.ProductFormResponse{}, err
	}

	// Now, query and return the updated product.
	updatedProduct := model.ProductFormResponse{}
	query := `
		SELECT
			p.id,
			p.price,
			p.code,
			p.slug,
			p.is_active,
			p.is_halal,
			p.is_vegan,
			p.category_id
		FROM products p
		WHERE p.id = $1
	`
	if err = r.db.QueryRow(query, productID).Scan(
		&updatedProduct.ID,
		&updatedProduct.Price,
		&updatedProduct.Code,
		&updatedProduct.Slug,
		&updatedProduct.IsActive,
		&updatedProduct.IsHalal,
		&updatedProduct.IsVegan,
		&updatedProduct.CategoryId,
	); err != nil {
		return model.ProductFormResponse{}, err
	}

	// Query the translations.
	rows, err := r.db.Query(`
		SELECT locale, name, description
		FROM product_translations
		WHERE product_id = $1
	`, productID)
	if err != nil {
		return model.ProductFormResponse{}, err
	}
	defer rows.Close()

	for rows.Next() {
		var t model.ProductTranslation
		if err = rows.Scan(&t.Locale, &t.Name, &t.Description); err != nil {
			return model.ProductFormResponse{}, err
		}
		updatedProduct.Translations = append(updatedProduct.Translations, t)
	}

	return updatedProduct, nil
}

func (r *productRepository) CreateProduct(form model.CreateProductForm) (model.ProductFormResponse, error) {
	// Check if the category exists
	var exists bool
	err := r.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM product_categories WHERE id = $1)`, *form.CategoryId).Scan(&exists)
	if err != nil {
		return model.ProductFormResponse{}, err
	}
	if !exists {
		return model.ProductFormResponse{}, fmt.Errorf("category with ID %s does not exist", *form.CategoryId)
	}

	// Start a transaction
	tx, err := r.db.Begin()
	if err != nil {
		return model.ProductFormResponse{}, err
	}
	defer tx.Rollback()

	// Insert the product
	var productId uuid.UUID
	err = tx.QueryRow(`
		INSERT INTO products (category_id, price, code, is_active, is_halal, is_vegan)
		VALUES ($1, $2, $3)
		RETURNING id
	`, form.CategoryId, form.Price, form.Code).Scan(&productId)

	if err != nil {
		return model.ProductFormResponse{}, err
	}

	// Insert the translations
	err = createUpdateProductTranslations(productId, form.Translations, tx)
	if err != nil {
		return model.ProductFormResponse{}, err
	}

	// Commit the transaction
	err = tx.Commit()

	if err != nil {
		return model.ProductFormResponse{}, err
	}

	// Query & return the created product
	createdProduct := model.ProductFormResponse{}
	err = r.db.QueryRow(`
		SELECT
			p.id,
			p.price,	
			p.code,
			p.slug,
			p.is_active,
			p.is_halal,
			p.is_vegan,
			p.category_id
		FROM
			products p
		WHERE
			p.id = $1
	`, productId).Scan(
		&createdProduct.ID,
		&createdProduct.Price,
		&createdProduct.Code,
		&createdProduct.Slug,
		&createdProduct.IsActive,
		&createdProduct.IsHalal,
		&createdProduct.IsVegan,
		&createdProduct.CategoryId,
	)
	if err != nil {
		return model.ProductFormResponse{}, err
	}

	// Query the translations
	rows, err := r.db.Query(`
		SELECT
			locale,
			name,
			description
		FROM
			product_translations
		WHERE	
			product_id = $1
	`, productId)
	if err != nil {
		return model.ProductFormResponse{}, err
	}
	defer rows.Close()

	// Iterate over the translations and append them to the response
	for rows.Next() {
		var t model.ProductTranslation
		err = rows.Scan(&t.Locale, &t.Name, &t.Description)
		if err != nil {
			return model.ProductFormResponse{}, err
		}
		createdProduct.Translations = append(createdProduct.Translations, t)
	}

	return createdProduct, nil
}

func (r *productRepository) CategoryExists(categoryID uuid.UUID) (bool, error) {
	var exists bool
	err := r.db.QueryRow(
		`SELECT EXISTS(SELECT 1 FROM product_categories WHERE id = $1)`,
		categoryID,
	).Scan(&exists)
	return exists, err
}

func createUpdateProductTranslations(productID uuid.UUID, translations []model.ProductTranslation, tx *sql.Tx) error {
	// Build the base query with placeholders
	query := `
		INSERT INTO product_translations (product_id, locale, name, description)
		VALUES %s
		ON CONFLICT (product_id, locale) DO UPDATE
		SET name = EXCLUDED.name, description = EXCLUDED.description;
	`

	// Slice to hold values for placeholders
	var values []interface{}

	// Placeholder builder
	placeholder := []string{}
	placeholderIdx := 1

	// Loop through the translations and add them to the query
	for _, t := range translations {
		placeholder = append(placeholder, fmt.Sprintf("($%d, $%d, $%d, $%d)", placeholderIdx, placeholderIdx+1, placeholderIdx+2, placeholderIdx+3))
		values = append(values, productID, t.Locale, t.Name, t.Description)
		placeholderIdx += 4
	}

	// Final query with placeholders
	query = fmt.Sprintf(query, strings.Join(placeholder, ", "))

	// Execute the query
	_, err := tx.Exec(query, values...)
	if err != nil {
		return fmt.Errorf("failed to update translations: %v", err)
	}

	return nil
}
