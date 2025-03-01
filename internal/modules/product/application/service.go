package application

import (
	"context"
	"time"

	"tsb-service/internal/modules/product/domain"

	"github.com/google/uuid"
)

// ProductService defines the application service interface for product operations.
type ProductService interface {
	CreateProduct(ctx context.Context, categoryID uuid.UUID, price float64, code *string, isActive bool, isHalal bool, isVegan bool, translations []domain.Translation) (*domain.Product, error)
	GetProduct(ctx context.Context, id string) (*domain.Product, error)
	GetProducts(ctx context.Context) ([]*domain.Product, error)
	GetProductsByCategory(ctx context.Context, categoryID string) ([]*domain.Product, error)
	GetCategories(ctx context.Context) ([]*domain.Category, error)
}

type productService struct {
	repo domain.ProductRepository
}

func NewProductService(repo domain.ProductRepository) ProductService {
	return &productService{
		repo: repo,
	}
}

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
	product, err := domain.NewProduct(price, categoryID, isActive, translations)
	if err != nil {
		return nil, err
	}

	product.Code = code
	product.IsActive = isActive
	product.IsHalal = isHalal
	product.IsVegan = isVegan
	product.UpdatedAt = time.Now() // update the timestamp
	
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

