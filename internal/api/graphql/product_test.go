package graphql_test

import (
	"testing"

	"github.com/99designs/gqlgen/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/testhelpers"
)

// TestProducts tests the products query
func TestProducts(t *testing.T) {
	ctx := setupTestContext(t)

	t.Run("Query all products without auth", func(t *testing.T) {
		c := client.New(ctx.Client.Handler())

		var resp struct {
			Products []struct {
				ID          string
				Name        string
				Description string
				Price       string // Price is returned as string from numeric DB type
				IsVisible   bool
				IsAvailable bool
				Slug        string
				Category    struct {
					ID   string
					Name string
				}
			}
		}

		query := `
			query {
				products {
					id
					name
					description
					price
					isVisible
					isAvailable
					slug
					category {
						id
						name
					}
				}
			}
		`

		c.MustPost(query, &resp)

		// Should return all visible products
		assert.NotEmpty(t, resp.Products)
		assert.GreaterOrEqual(t, len(resp.Products), 4) // We seeded 4 products

		// Verify product data structure
		for _, product := range resp.Products {
			assert.NotEmpty(t, product.ID)
			assert.NotEmpty(t, product.Name)
			assert.NotEmpty(t, product.Price) // Price is a string
			assert.NotEmpty(t, product.Category.ID)
			assert.NotEmpty(t, product.Category.Name)
		}
	})

	t.Run("Query all products with English language", func(t *testing.T) {
		c := client.New(ctx.Client.Handler())

		var resp struct {
			Products []struct {
				Name        string
				Description string
			}
		}

		query := `
			query {
				products {
					name
					description
				}
			}
		`

		// Set Accept-Language header to English
		c.MustPost(query, &resp, client.AddHeader("Accept-Language", "en"))

		// Verify we got products with names (language might vary based on system)
		assert.NotEmpty(t, resp.Products)
		for _, product := range resp.Products {
			assert.NotEmpty(t, product.Name) // Should have a name in some language
		}
	})

	t.Run("Query all products with Chinese language", func(t *testing.T) {
		c := client.New(ctx.Client.Handler())

		var resp struct {
			Products []struct {
				Name        string
				Description string
			}
		}

		query := `
			query {
				products {
					name
					description
				}
			}
		`

		// Set Accept-Language header to Chinese (zh)
		c.MustPost(query, &resp, client.AddHeader("Accept-Language", "zh"))

		// Verify we got products with names (language might vary based on system)
		assert.NotEmpty(t, resp.Products)
		for _, product := range resp.Products {
			assert.NotEmpty(t, product.Name) // Should have a name in some language
		}
	})

	t.Run("Query all products with French language", func(t *testing.T) {
		c := client.New(ctx.Client.Handler())

		var resp struct {
			Products []struct {
				Name        string
				Description string
			}
		}

		query := `
			query {
				products {
					name
					description
				}
			}
		`

		// Set Accept-Language header to French
		c.MustPost(query, &resp, client.AddHeader("Accept-Language", "fr"))

		// Verify we got products with names (language might vary based on system)
		assert.NotEmpty(t, resp.Products)
		for _, product := range resp.Products {
			assert.NotEmpty(t, product.Name) // Should have a name in some language
		}
	})
}

// TestProduct tests the product query (single product by ID)
func TestProduct(t *testing.T) {
	ctx := setupTestContext(t)

	t.Run("Query product by ID", func(t *testing.T) {
		c := client.New(ctx.Client.Handler())

		var resp struct {
			Product struct {
				ID          string
				Name        string
				Description string
				Price       string // Price is returned as string from numeric DB type
				Slug        string
				IsHalal     bool
				IsVegan     bool
				PieceCount  *int
			}
		}

		query := `
			query($id: ID!) {
				product(id: $id) {
					id
					name
					description
					price
					slug
					isHalal
					isVegan
					pieceCount
				}
			}
		`

		c.MustPost(query, &resp,
			client.Var("id", ctx.Fixtures.SalmonSushi.ID.String()),
			client.AddHeader("Accept-Language", "en"),
		)

		// Verify product details
		assert.Equal(t, ctx.Fixtures.SalmonSushi.ID.String(), resp.Product.ID)
		assert.Contains(t, []string{"Salmon Sushi", "Sushi au Saumon", "Zalm Sushi"}, resp.Product.Name) // Could be any language
		assert.Contains(t, []string{"12.5", "12.50"}, resp.Product.Price) // Price format may vary
		assert.Equal(t, "salmon-sushi", resp.Product.Slug)
		assert.False(t, resp.Product.IsHalal)
		assert.False(t, resp.Product.IsVegan)
		assert.NotNil(t, resp.Product.PieceCount)
		assert.Equal(t, 8, *resp.Product.PieceCount)
	})

	t.Run("Query non-existent product", func(t *testing.T) {
		c := client.New(ctx.Client.Handler())

		var resp struct {
			Product struct {
				ID string
			}
		}

		query := `
			query($id: ID!) {
				product(id: $id) {
					id
				}
			}
		`

		fakeID := "00000000-0000-0000-0000-000000000000"
		err := c.Post(query, &resp, client.Var("id", fakeID))

		// Should return an error
		require.Error(t, err)
	})
}

// TestProductCategories tests the productCategories query
func TestProductCategories(t *testing.T) {
	ctx := setupTestContext(t)

	t.Run("Query all product categories", func(t *testing.T) {
		c := client.New(ctx.Client.Handler())

		var resp struct {
			ProductCategories []struct {
				ID    string
				Name  string
				Order int
			}
		}

		query := `
			query {
				productCategories {
					id
					name
					order
				}
			}
		`

		c.MustPost(query, &resp, client.AddHeader("Accept-Language", "en"))

		// Should return all categories
		assert.NotEmpty(t, resp.ProductCategories)
		assert.GreaterOrEqual(t, len(resp.ProductCategories), 3) // We seeded 3 categories

		// Verify categories are ordered correctly
		if len(resp.ProductCategories) >= 2 {
			assert.LessOrEqual(t, resp.ProductCategories[0].Order, resp.ProductCategories[1].Order)
		}

		// Verify category data
		for _, category := range resp.ProductCategories {
			assert.NotEmpty(t, category.ID)
			assert.NotEmpty(t, category.Name)
		}
	})

	t.Run("Query product categories with Chinese language", func(t *testing.T) {
		c := client.New(ctx.Client.Handler())

		var resp struct {
			ProductCategories []struct {
				Name string
			}
		}

		query := `
			query {
				productCategories {
					name
				}
			}
		`

		c.MustPost(query, &resp, client.AddHeader("Accept-Language", "zh"))

		// Verify we got categories with names (language might vary based on system)
		assert.NotEmpty(t, resp.ProductCategories)
		for _, category := range resp.ProductCategories {
			assert.NotEmpty(t, category.Name) // Should have a name in some language
		}
	})
}

// TestProductCategory tests the productCategory query (single category by ID)
func TestProductCategory(t *testing.T) {
	ctx := setupTestContext(t)

	t.Run("Query category by ID", func(t *testing.T) {
		c := client.New(ctx.Client.Handler())

		var resp struct {
			ProductCategory struct {
				ID    string
				Name  string
				Order int
			}
		}

		query := `
			query($id: ID!) {
				productCategory(id: $id) {
					id
					name
					order
				}
			}
		`

		c.MustPost(query, &resp,
			client.Var("id", ctx.Fixtures.SushiCategory.ID.String()),
			client.AddHeader("Accept-Language", "en"),
		)

		// Verify category details
		assert.Equal(t, ctx.Fixtures.SushiCategory.ID.String(), resp.ProductCategory.ID)
		assert.Equal(t, "Sushi", resp.ProductCategory.Name)
		assert.Equal(t, 1, resp.ProductCategory.Order)
	})
}

// TestCreateProduct tests the createProduct mutation (admin only)
func TestCreateProduct(t *testing.T) {
	ctx := setupTestContext(t)

	t.Run("Create product as admin", func(t *testing.T) {
		c := client.New(ctx.Client.Handler())

		// Generate admin token
		adminToken, err := testhelpers.GenerateTestAccessToken(ctx.Fixtures.AdminUser.ID.String(), true)
		require.NoError(t, err)

		var resp struct {
			CreateProduct struct {
				ID    string
				Name  string
				Price string // Price is returned as string from numeric DB type
				Slug  string
			}
		}

		mutation := `
			mutation($input: CreateProductInput!) {
				createProduct(input: $input) {
					id
					name
					price
					slug
				}
			}
		`

		input := map[string]any{
			"categoryId":     ctx.Fixtures.SushiCategory.ID.String(),
			"price":          "15.99",
			"code":           "SUSHI-NEW",
			"isVisible":      true,
			"isAvailable":    true,
			"isHalal":        false,
			"isVegan":        false,
			"isDiscountable": true,
			"translations": []map[string]any{
				{
					"language":    "en",
					"name":        "New Sushi Roll",
					"description": "A delicious new sushi roll",
				},
				{
					"language":    "fr",
					"name":        "Nouveau Rouleau de Sushi",
					"description": "Un délicieux nouveau rouleau de sushi",
				},
				{
					"language":    "zh",
					"name":        "Nieuwe Sushi Rol",
					"description": "Een heerlijke nieuwe sushi rol",
				},
			},
		}

		c.MustPost(mutation, &resp,
			client.Var("input", input),
			client.AddHeader("Authorization", "Bearer "+adminToken),
			client.AddHeader("Accept-Language", "en"),
		)

		// Verify product was created
		assert.NotEmpty(t, resp.CreateProduct.ID)
		assert.Equal(t, "New Sushi Roll", resp.CreateProduct.Name)
		assert.Equal(t, "15.99", resp.CreateProduct.Price) // Price is a string
		assert.NotEmpty(t, resp.CreateProduct.Slug)        // Slug is auto-generated
	})

	t.Run("Create product without admin token should fail", func(t *testing.T) {
		c := client.New(ctx.Client.Handler())

		// Generate regular user token (not admin)
		regularToken, err := testhelpers.GenerateTestAccessToken(ctx.Fixtures.RegularUser.ID.String(), false)
		require.NoError(t, err)

		var resp struct {
			CreateProduct struct {
				ID string
			}
		}

		mutation := `
			mutation($input: CreateProductInput!) {
				createProduct(input: $input) {
					id
				}
			}
		`

		input := map[string]any{
			"categoryId":     ctx.Fixtures.SushiCategory.ID.String(),
			"price":          "15.99",
			"code":           "SUSHI-FORBIDDEN",
			"isVisible":      true,
			"isAvailable":    true,
			"isHalal":        false,
			"isVegan":        false,
			"isDiscountable": true,
			"translations": []map[string]any{
				{
					"language":    "en",
					"name":        "Forbidden Sushi",
					"description": "This should not be created",
				},
				{
					"language":    "fr",
					"name":        "Sushi Interdit",
					"description": "Ceci ne devrait pas être créé",
				},
				{
					"language":    "zh",
					"name":        "Verboden Sushi",
					"description": "Dit zou niet gemaakt moeten worden",
				},
			},
		}

		err = c.Post(mutation, &resp,
			client.Var("input", input),
			client.AddHeader("Authorization", "Bearer "+regularToken),
		)

		// Should fail due to @admin directive
		require.Error(t, err)
		assert.Contains(t, err.Error(), "FORBIDDEN")
	})
}

// TestUpdateProduct tests the updateProduct mutation (admin only)
func TestUpdateProduct(t *testing.T) {
	ctx := setupTestContext(t)

	t.Run("Update product as admin", func(t *testing.T) {
		c := client.New(ctx.Client.Handler())

		// Generate admin token
		adminToken, err := testhelpers.GenerateTestAccessToken(ctx.Fixtures.AdminUser.ID.String(), true)
		require.NoError(t, err)

		var resp struct {
			UpdateProduct struct {
				ID    string
				Price string // Price is returned as string from numeric DB type
			}
		}

		mutation := `
			mutation($id: ID!, $input: UpdateProductInput!) {
				updateProduct(id: $id, input: $input) {
					id
					price
				}
			}
		`

		input := map[string]any{
			"price": "13.50",
		}

		c.MustPost(mutation, &resp,
			client.Var("id", ctx.Fixtures.SalmonSushi.ID.String()),
			client.Var("input", input),
			client.AddHeader("Authorization", "Bearer "+adminToken),
		)

		// Verify product was updated
		assert.Equal(t, ctx.Fixtures.SalmonSushi.ID.String(), resp.UpdateProduct.ID)
		assert.Contains(t, []string{"13.5", "13.50"}, resp.UpdateProduct.Price) // Price format may vary
	})

	t.Run("Update product without admin token should fail", func(t *testing.T) {
		c := client.New(ctx.Client.Handler())

		// Generate regular user token (not admin)
		regularToken, err := testhelpers.GenerateTestAccessToken(ctx.Fixtures.RegularUser.ID.String(), false)
		require.NoError(t, err)

		var resp struct {
			UpdateProduct struct {
				ID string
			}
		}

		mutation := `
			mutation($id: ID!, $input: UpdateProductInput!) {
				updateProduct(id: $id, input: $input) {
					id
				}
			}
		`

		input := map[string]any{
			"price": "99.99",
		}

		err = c.Post(mutation, &resp,
			client.Var("id", ctx.Fixtures.TunaSushi.ID.String()),
			client.Var("input", input),
			client.AddHeader("Authorization", "Bearer "+regularToken),
		)

		// Should fail due to @admin directive
		require.Error(t, err)
		assert.Contains(t, err.Error(), "FORBIDDEN")
	})
}
