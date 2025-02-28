package application

import (
	"context"
	"time"

	"tsb-service/internal/modules/product/domain"

	"github.com/google/uuid"
)

// ProductService defines the application service interface for product operations.
type ProductService interface {
	// CreateProduct creates a new product.
	CreateProduct(ctx context.Context, categoryID uuid.UUID, price float64, code *string, isActive bool, isHalal bool, isVegan bool, translations []domain.Translation) (*domain.Product, error)
	// GetProduct retrieves a product by its ID.
	GetProduct(ctx context.Context, id string) (*domain.Product, error)
	// GetProducts retrieves a list of products.
	GetProducts(ctx context.Context) ([]*domain.Product, error)
	// GetProductsByCategory retrieves a list of products by category.
	GetProductsByCategory(ctx context.Context, categoryID string) ([]*domain.Product, error)
	// Get categories
	GetCategories(ctx context.Context) ([]*domain.Category, error)
}

type productService struct {
	repo domain.ProductRepository
}

// NewProductService creates a new instance of ProductService.
func NewProductService(repo domain.ProductRepository) ProductService {
	return &productService{
		repo: repo,
	}
}

// CreateProduct creates a new product using the domain constructor and persists it.
func (s *productService) CreateProduct(
	ctx context.Context,
	categoryID uuid.UUID,
	price float64,
	code *string,
	isActive bool,
	isHalal bool,
	isVegan bool,
	translations []domain.Translation,
) (*domain.Product, error) {
	// Create the product using the domain constructor.
	product, err := domain.NewProduct(price, categoryID, isActive, translations)
	if err != nil {
		return nil, err
	}

	// Set additional fields.
	product.Code = code
	product.IsActive = isActive
	product.IsHalal = isHalal
	product.IsVegan = isVegan
	product.UpdatedAt = time.Now() // update the timestamp
	
	// Persist the product.
	if err := s.repo.Save(ctx, product); err != nil {
		return nil, err
	}

	return product, nil
}

// GetProduct retrieves a product by its unique identifier.
func (s *productService) GetProduct(ctx context.Context, id string) (*domain.Product, error) {
	return s.repo.FindByID(ctx, id)
}

// GetProducts retrieves a list of products.
func (s *productService) GetProducts(ctx context.Context) ([]*domain.Product, error) {
	return s.repo.FindAll(ctx)
}

// GetCategories retrieves a list of categories.
func (s *productService) GetCategories(ctx context.Context) ([]*domain.Category, error) {
	return s.repo.FindAllCategories(ctx)
}

// GetProductsByCategory retrieves a list of products by category.
func (s *productService) GetProductsByCategory(ctx context.Context, categoryID string) ([]*domain.Product, error) {
	return s.repo.FindByCategoryID(ctx, categoryID)
}

