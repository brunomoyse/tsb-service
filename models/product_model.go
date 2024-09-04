package models

import (
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
