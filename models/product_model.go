package models

import (
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"tsb-service/config"

	"github.com/google/uuid"
)

type ProductInfo struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Description *string   `json:"description"`
	Price       float64   `json:"price"`
	Code        *string   `json:"code"`
	Slug        *string   `json:"slug"`
}

type Category struct {
	ID       uuid.UUID     `json:"id"`
	Name     string        `json:"name"`
	Order    int           `json:"order"`
	Products []ProductInfo `json:"products"`
}

type ProductForm struct {
	Price        *float64              `json:"price"`
	Code         *string               `json:"code"`
	Translations []*ProductTranslation `json:"translations"`
}

type ProductTranslation struct {
	Locale      string  `json:"locale" binding:"required"` // Locale is required
	Name        string  `json:"name" binding:"required"`   // Name is required
	Description *string `json:"description"`               // Description is optional
}

type ProductFormResponse struct {
	ID           uuid.UUID            `json:"id"`
	Price        float64              `json:"price"`
	Code         *string              `json:"code"`
	Slug         *string              `json:"slug"`
	Translations []ProductTranslation `json:"translations"`
}

func GetProductsGroupedByCategory(currentUserLang string) ([]Category, error) {
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
	    p.slug
	FROM 
	    product_categories pc
	INNER JOIN 
	    product_category_translations pct 
	    ON pc.id = pct.product_category_id
	INNER JOIN 
	    product_product_category ppc 
	    ON pc.id = ppc.product_category_id
	INNER JOIN 
	    products p 
	    ON ppc.product_id = p.id
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

	rows, err := config.DB.Query(query, currentUserLang)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var categories []Category
	var currentCategory *Category

	for rows.Next() {
		var category Category
		var product ProductInfo

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
		)
		if err != nil {
			return nil, err
		}

		// If it's a new category, append the current category (if any) and start a new one
		if currentCategory == nil || currentCategory.ID != category.ID {
			if currentCategory != nil {
				categories = append(categories, *currentCategory)
			}
			currentCategory = &Category{
				ID:       category.ID,
				Name:     category.Name,
				Order:    category.Order,
				Products: []ProductInfo{},
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

func UpdateProduct(productId uuid.UUID, form ProductForm) (ProductFormResponse, error) {
	// Check if the product exists
	var exists bool
	err := config.DB.QueryRow(`SELECT EXISTS(SELECT 1 FROM products WHERE id = $1)`, productId).Scan(&exists)
	if err != nil {
		return ProductFormResponse{}, err
	}
	if !exists {
		return ProductFormResponse{}, fmt.Errorf("product with ID %s does not exist", productId)
	}

	// Start a transaction
	tx, err := config.DB.Begin()
	if err != nil {
		return ProductFormResponse{}, err
	}
	defer tx.Rollback()

	// Update the product if fields are provided
	if form.Price != nil || form.Code != nil {
		query := `UPDATE products SET `
		args := []interface{}{productId}
		argCount := 1

		if form.Price != nil {
			argCount++
			query += `price = $` + strconv.Itoa(argCount) + `, `
			args = append(args, *form.Price)
		}

		if form.Code != nil {
			argCount++
			query += `code = $` + strconv.Itoa(argCount) + `, `
			args = append(args, *form.Code)
		}

		// Remove the trailing comma and space
		query = strings.TrimSuffix(query, ", ")

		// Add the WHERE clause
		query += ` WHERE id = $1`

		// Execute the update query
		_, err = tx.Exec(query, args...)
		if err != nil {
			return ProductFormResponse{}, err
		}
	}

	// Batch update translations
	if len(form.Translations) > 0 {
		translations := make([]ProductTranslation, len(form.Translations))
		for i, t := range form.Translations {
			translations[i] = *t
		}
		err = UpdateProductTranslations(productId, translations, tx)
		if err != nil {
			return ProductFormResponse{}, err
		}
	}

	// Commit the transaction
	err = tx.Commit()
	if err != nil {
		return ProductFormResponse{}, err
	}

	// Query & return the updated product
	updatedProduct := ProductFormResponse{}
	err = config.DB.QueryRow(`
		SELECT
			p.id,
			p.price,
			p.code,
			p.slug
		FROM
			products p
		WHERE	
			p.id = $1
	`, productId).Scan(
		&updatedProduct.ID,
		&updatedProduct.Price,
		&updatedProduct.Code,
		&updatedProduct.Slug,
	)
	if err != nil {
		return ProductFormResponse{}, err
	}

	// Query the translations
	rows, err := config.DB.Query(`	
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
		return ProductFormResponse{}, err
	}
	defer rows.Close()

	// Iterate over the translations and append them to the response
	for rows.Next() {
		var t ProductTranslation
		err = rows.Scan(&t.Locale, &t.Name, &t.Description)
		if err != nil {
			return ProductFormResponse{}, err
		}
		updatedProduct.Translations = append(updatedProduct.Translations, t)
	}

	return updatedProduct, nil
}

func UpdateProductTranslations(productId uuid.UUID, translations []ProductTranslation, tx *sql.Tx) error {
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
		values = append(values, productId, t.Locale, t.Name, t.Description)
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

func Slugify(s string) string {
	return strings.ToLower(
		regexp.MustCompile(`\s+`).ReplaceAllString(
			regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(s, "-"),
			" ",
		),
	)
}
