package testhelpers

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/argon2"
)

// TestFixtures holds common test data
type TestFixtures struct {
	// Users
	RegularUser *TestUser
	AdminUser   *TestUser

	// Product Categories
	SushiCategory    *TestProductCategory
	DrinksCategory   *TestProductCategory
	DessertsCategory *TestProductCategory

	// Products
	SalmonSushi *TestProduct
	TunaSushi   *TestProduct
	GreenTea    *TestProduct
	MochiIce    *TestProduct

	// Orders
	Order1 *TestOrder
}

// TestUser represents a test user
type TestUser struct {
	ID        uuid.UUID
	FirstName string
	LastName  string
	Email     string
	Password  string // Plaintext for testing
	IsAdmin   bool
}

// TestProductCategory represents a test product category
type TestProductCategory struct {
	ID             uuid.UUID
	Order          int
	NameEN         string
	NameNL         string
	NameFR         string
	DescriptionEN  string
	DescriptionNL  string
	DescriptionFR  string
}

// TestProduct represents a test product
type TestProduct struct {
	ID            uuid.UUID
	CategoryID    uuid.UUID
	Price         float64
	IsVisible     bool
	IsAvailable   bool
	Code          string
	Slug          string
	IsHalal       bool
	IsVegan       bool
	IsSpicy       bool
	PieceCount    *int
	IsDiscountable bool
	NameEN        string
	NameNL        string
	NameFR        string
	DescriptionEN string
	DescriptionNL string
	DescriptionFR string
}

// TestOrder represents a test order
type TestOrder struct {
	ID     uuid.UUID
	UserID uuid.UUID
	Status string
}

// SeedTestData creates and inserts test data into the database
func SeedTestData(t *testing.T, db *sqlx.DB) *TestFixtures {
	ctx := t.Context()
	fixtures := &TestFixtures{}

	// Create test users
	fixtures.RegularUser = createTestUser(t, ctx, db, "John", "Doe", "john@example.com", "password123", false)
	fixtures.AdminUser = createTestUser(t, ctx, db, "Admin", "User", "admin@example.com", "admin123", true)

	// Create product categories with translations
	fixtures.SushiCategory = createTestProductCategory(t, ctx, db, 1, "Sushi", "Sushi", "Sushi", "Fresh sushi rolls", "Verse sushi rollen", "Rouleaux de sushi frais")
	fixtures.DrinksCategory = createTestProductCategory(t, ctx, db, 2, "Drinks", "Dranken", "Boissons", "Refreshing beverages", "Verfrissende dranken", "Boissons rafraîchissantes")
	fixtures.DessertsCategory = createTestProductCategory(t, ctx, db, 3, "Desserts", "Desserts", "Desserts", "Sweet treats", "Zoete lekkernijen", "Douceurs sucrées")

	// Create products with translations
	pieces8 := 8
	fixtures.SalmonSushi = createTestProduct(t, ctx, db, TestProduct{
		CategoryID:     fixtures.SushiCategory.ID,
		Price:          12.50,
		IsVisible:      true,
		IsAvailable:    true,
		Code:           "SUSHI-SALMON",
		Slug:           "salmon-sushi",
		IsHalal:        false,
		IsVegan:        false,
		PieceCount:     &pieces8,
		IsDiscountable: true,
		NameEN:         "Salmon Sushi",
		NameNL:         "Zalm Sushi",
		NameFR:         "Sushi au Saumon",
		DescriptionEN:  "Fresh salmon nigiri sushi",
		DescriptionNL:  "Verse zalm nigiri sushi",
		DescriptionFR:  "Nigiri sushi au saumon frais",
	})

	pieces6 := 6
	fixtures.TunaSushi = createTestProduct(t, ctx, db, TestProduct{
		CategoryID:     fixtures.SushiCategory.ID,
		Price:          14.00,
		IsVisible:      true,
		IsAvailable:    true,
		Code:           "SUSHI-TUNA",
		Slug:           "tuna-sushi",
		IsHalal:        false,
		IsVegan:        false,
		PieceCount:     &pieces6,
		IsDiscountable: true,
		NameEN:         "Tuna Sushi",
		NameNL:         "Tonijn Sushi",
		NameFR:         "Sushi au Thon",
		DescriptionEN:  "Premium tuna nigiri sushi",
		DescriptionNL:  "Premium tonijn nigiri sushi",
		DescriptionFR:  "Nigiri sushi au thon premium",
	})

	fixtures.GreenTea = createTestProduct(t, ctx, db, TestProduct{
		CategoryID:     fixtures.DrinksCategory.ID,
		Price:          3.50,
		IsVisible:      true,
		IsAvailable:    true,
		Code:           "DRINK-TEA",
		Slug:           "green-tea",
		IsHalal:        true,
		IsVegan:        true,
		PieceCount:     nil,
		IsDiscountable: false,
		NameEN:         "Green Tea",
		NameNL:         "Groene Thee",
		NameFR:         "Thé Vert",
		DescriptionEN:  "Traditional Japanese green tea",
		DescriptionNL:  "Traditionele Japanse groene thee",
		DescriptionFR:  "Thé vert japonais traditionnel",
	})

	fixtures.MochiIce = createTestProduct(t, ctx, db, TestProduct{
		CategoryID:     fixtures.DessertsCategory.ID,
		Price:          5.00,
		IsVisible:      true,
		IsAvailable:    true,
		Code:           "DESSERT-MOCHI",
		Slug:           "mochi-ice-cream",
		IsHalal:        false,
		IsVegan:        false,
		PieceCount:     nil,
		IsDiscountable: false,
		NameEN:         "Mochi Ice Cream",
		NameNL:         "Mochi Ijs",
		NameFR:         "Crème Glacée Mochi",
		DescriptionEN:  "Japanese rice cake with ice cream filling",
		DescriptionNL:  "Japanse rijstcake met ijsvulling",
		DescriptionFR:  "Gâteau de riz japonais fourré à la crème glacée",
	})

	return fixtures
}

// createTestUser inserts a test user into the database
func createTestUser(t *testing.T, ctx context.Context, db *sqlx.DB, firstName, lastName, email, password string, isAdmin bool) *TestUser {
	userID := uuid.New()
	salt := "test-salt"
	passwordHash := hashPasswordForTest(password, salt)

	// Try to insert with is_admin column first, fall back to without it if column doesn't exist
	query := `
		INSERT INTO users (id, created_at, updated_at, first_name, last_name, email, email_verified_at, password_hash, salt)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`

	now := time.Now()
	_, err := db.ExecContext(ctx, query,
		userID, now, now, firstName, lastName, email, now, passwordHash, salt)
	require.NoError(t, err, "Failed to create test user")

	// If is_admin column exists, update it
	if isAdmin {
		updateQuery := `UPDATE users SET is_admin = $1 WHERE id = $2`
		_, _ = db.ExecContext(ctx, updateQuery, isAdmin, userID)
	}

	return &TestUser{
		ID:        userID,
		FirstName: firstName,
		LastName:  lastName,
		Email:     email,
		Password:  password,
		IsAdmin:   isAdmin,
	}
}

// createTestProductCategory inserts a product category with translations
func createTestProductCategory(t *testing.T, ctx context.Context, db *sqlx.DB, order int, nameEN, nameNL, nameFR, descEN, descNL, descFR string) *TestProductCategory {
	categoryID := uuid.New()

	// Insert category
	categoryQuery := `
		INSERT INTO product_categories (id, created_at, updated_at, "order")
		VALUES ($1, $2, $3, $4)
	`
	now := time.Now()
	_, err := db.ExecContext(ctx, categoryQuery, categoryID, now, now, order)
	require.NoError(t, err, "Failed to create product category")

	// Insert translations (note: column is product_category_id, not category_id)
	translationQuery := `
		INSERT INTO product_category_translations (id, product_category_id, language, name, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`

	translations := []struct {
		lang string
		name string
	}{
		{"en", nameEN},
		{"fr", nameFR},
		{"zh", nameNL}, // Using zh slot for Dutch since migration only supports en/fr/zh
	}

	for _, trans := range translations {
		translationID := uuid.New()
		_, err := db.ExecContext(ctx, translationQuery,
			translationID, categoryID, trans.lang, trans.name, now, now)
		require.NoError(t, err, "Failed to create category translation")
	}

	return &TestProductCategory{
		ID:             categoryID,
		Order:          order,
		NameEN:         nameEN,
		NameNL:         nameNL,
		NameFR:         nameFR,
		DescriptionEN:  descEN,
		DescriptionNL:  descNL,
		DescriptionFR:  descFR,
	}
}

// createTestProduct inserts a product with translations
func createTestProduct(t *testing.T, ctx context.Context, db *sqlx.DB, product TestProduct) *TestProduct {
	if product.ID == uuid.Nil {
		product.ID = uuid.New()
	}

	// Insert product
	productQuery := `
		INSERT INTO products (id, created_at, updated_at, category_id, price, is_visible, is_available, code, slug, is_halal, is_vegan, is_spicy, piece_count, is_discountable)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
	`
	now := time.Now()
	_, err := db.ExecContext(ctx, productQuery,
		product.ID, now, now, product.CategoryID, product.Price, product.IsVisible, product.IsAvailable,
		product.Code, product.Slug, product.IsHalal, product.IsVegan, product.IsSpicy, product.PieceCount, product.IsDiscountable)
	require.NoError(t, err, "Failed to create product")

	// Insert translations (migration only supports en/fr/zh)
	translationQuery := `
		INSERT INTO product_translations (id, product_id, language, name, description, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`

	translations := []struct {
		lang string
		name string
		desc string
	}{
		{"en", product.NameEN, product.DescriptionEN},
		{"fr", product.NameFR, product.DescriptionFR},
		{"zh", product.NameNL, product.DescriptionNL}, // Using zh slot for Dutch
	}

	for _, trans := range translations {
		translationID := uuid.New()
		_, err := db.ExecContext(ctx, translationQuery,
			translationID, product.ID, trans.lang, trans.name, trans.desc, now, now)
		require.NoError(t, err, "Failed to create product translation")
	}

	return &product
}

// hashPasswordForTest is a simplified password hashing for test data
func hashPasswordForTest(password, salt string) string {
	hash := argon2.IDKey([]byte(password), []byte(salt), 1, 64*1024, 4, 32)
	// Encode as hex to avoid UTF-8 encoding issues in PostgreSQL
	return hex.EncodeToString(hash)
}
