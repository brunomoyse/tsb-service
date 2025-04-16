package application

import (
	"context"
	"github.com/shopspring/decimal"
	"tsb-service/internal/modules/product/domain"

	"github.com/google/uuid"
)

// ProductService defines the application service interface for product operations.
type ProductService interface {
	CreateProduct(ctx context.Context, categoryID uuid.UUID, price decimal.Decimal, code *string, pieceCount *int, isVisible bool, isAvailable bool, isHalal bool, isVegan bool, translations []domain.Translation) (*domain.Product, error)
	GetProduct(ctx context.Context, id string) (*domain.Product, error)
	GetProducts(ctx context.Context) ([]*domain.Product, error)
	GetProductsByIDs(ctx context.Context, productIDs []string) ([]*domain.ProductOrderDetails, error)
	GetProductsByCategory(ctx context.Context, categoryID string) ([]*domain.Product, error)
	GetCategories(ctx context.Context) ([]*domain.Category, error)
	UpdateProduct(ctx context.Context, product *domain.Product) error

	BatchGetCategoriesByProductIDs(ctx context.Context, productIDs []string) (map[string][]*domain.Category, error)
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
	price decimal.Decimal,
	code *string,
	pieceCount *int,
	isVisible bool,
	isAvailable bool,
	isHalal bool,
	isVegan bool,
	translations []domain.Translation,
) (*domain.Product, error) {
	product, err := domain.NewProduct(price, categoryID, isVisible, isAvailable, translations)
	if err != nil {
		return nil, err
	}

	product.Code = code
	product.PieceCount = pieceCount
	product.IsVisible = isVisible
	product.IsAvailable = isAvailable
	product.IsHalal = isHalal
	product.IsVegan = isVegan

	if err := s.repo.Create(ctx, product); err != nil {
		return nil, err
	}

	return product, nil
}

// UpdateProduct updates an existing product.
func (s *productService) UpdateProduct(ctx context.Context, product *domain.Product) error {
	return s.repo.Update(ctx, product)
}

// GetProduct retrieves a product by its unique identifier.
func (s *productService) GetProduct(ctx context.Context, id string) (*domain.Product, error) {
	return s.repo.FindByID(ctx, id)
}

// GetProducts retrieves a list of products.
func (s *productService) GetProducts(ctx context.Context) ([]*domain.Product, error) {
	return s.repo.FindAll(ctx)
}

func (s *productService) GetProductsByIDs(ctx context.Context, productIDs []string) ([]*domain.ProductOrderDetails, error) {
	return s.repo.FindByIDs(ctx, productIDs)
}

// GetCategories retrieves a list of categories.
func (s *productService) GetCategories(ctx context.Context) ([]*domain.Category, error) {
	return s.repo.FindAllCategories(ctx)
}

// GetProductsByCategory retrieves a list of products by category.
func (s *productService) GetProductsByCategory(ctx context.Context, categoryID string) ([]*domain.Product, error) {
	return s.repo.FindByCategoryID(ctx, categoryID)
}

func (s *productService) BatchGetCategoriesByProductIDs(ctx context.Context, productIDs []string) (map[string][]*domain.Category, error) {

	return s.repo.FindCategoriesByProductIDs(ctx, productIDs)
}
