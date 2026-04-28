package application

import (
	"context"
	"github.com/shopspring/decimal"
	"tsb-service/internal/modules/product/domain"

	"github.com/google/uuid"
)

// ProductService defines the application service interface for product operations.
type ProductService interface {
	CreateProduct(ctx context.Context, categoryID uuid.UUID, price decimal.Decimal, code *string, pieceCount *int, isVisible bool, isAvailable bool, isHalal bool, isVegetarian bool, isSpicy bool, isDiscountable bool, vatCategory domain.VatCategory, translations []domain.Translation) (*domain.Product, error)
	GetProduct(ctx context.Context, id uuid.UUID) (*domain.Product, error)
	GetProducts(ctx context.Context) ([]*domain.Product, error)
	GetProductsByIDs(ctx context.Context, productIDs []string) ([]*domain.ProductOrderDetails, error)
	GetProductNamesForInvoice(ctx context.Context, productIDs []string) ([]*domain.ProductOrderDetails, error)
	GetCategories(ctx context.Context) ([]*domain.Category, error)
	GetCategory(ctx context.Context, id uuid.UUID) (*domain.Category, error)
	UpdateProduct(ctx context.Context, product *domain.Product) error

	BatchGetCategoriesByProductIDs(ctx context.Context, productIDs []string) (map[string][]*domain.Category, error)
	BatchGetProductsByCategory(ctx context.Context, categoryIDs []string) (map[string][]*domain.Product, error)
	BatchGetProductByIDs(ctx context.Context, productIDs []string) (map[string][]*domain.Product, error)
	BatchGetCategoryTranslations(ctx context.Context, categoryIDs []string) (map[string][]*domain.Translation, error)
	BatchGetProductTranslations(ctx context.Context, productIDs []string) (map[string][]*domain.Translation, error)

	// Product choices
	GetChoiceGroupsByProductID(ctx context.Context, productID uuid.UUID) ([]*domain.ProductChoiceGroup, error)
	GetChoiceGroupByID(ctx context.Context, groupID uuid.UUID) (*domain.ProductChoiceGroup, error)
	BatchGetChoiceGroupsByProductIDs(ctx context.Context, productIDs []string) (map[string][]*domain.ProductChoiceGroup, error)
	CreateChoiceGroup(ctx context.Context, group *domain.ProductChoiceGroup) error
	UpdateChoiceGroup(ctx context.Context, group *domain.ProductChoiceGroup) error
	DeleteChoiceGroup(ctx context.Context, groupID uuid.UUID) error

	GetChoicesByProductID(ctx context.Context, productID uuid.UUID) ([]*domain.ProductChoice, error)
	GetChoiceByID(ctx context.Context, choiceID uuid.UUID) (*domain.ProductChoice, error)
	BatchGetChoicesByProductIDs(ctx context.Context, productIDs []string) (map[string][]*domain.ProductChoice, error)
	CreateChoice(ctx context.Context, choice *domain.ProductChoice) error
	UpdateChoice(ctx context.Context, choice *domain.ProductChoice) error
	DeleteChoice(ctx context.Context, choiceID uuid.UUID) error
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
	isVegetarian bool,
	isSpicy bool,
	isDiscountable bool,
	vatCategory domain.VatCategory,
	translations []domain.Translation,
) (*domain.Product, error) {
	product, err := domain.NewProduct(price, categoryID, isVisible, isAvailable, vatCategory, translations)
	if err != nil {
		return nil, err
	}

	product.Code = code
	product.PieceCount = pieceCount
	product.IsVisible = isVisible
	product.IsAvailable = isAvailable
	product.IsHalal = isHalal
	product.IsVegetarian = isVegetarian
	product.IsSpicy = isSpicy
	product.IsDiscountable = isDiscountable

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
func (s *productService) GetProduct(ctx context.Context, id uuid.UUID) (*domain.Product, error) {
	return s.repo.FindByID(ctx, id)
}

// GetProducts retrieves a list of products.
func (s *productService) GetProducts(ctx context.Context) ([]*domain.Product, error) {
	return s.repo.FindAll(ctx)
}

func (s *productService) GetProductsByIDs(ctx context.Context, productIDs []string) ([]*domain.ProductOrderDetails, error) {
	return s.repo.FindByIDs(ctx, productIDs)
}

func (s *productService) GetProductNamesForInvoice(ctx context.Context, productIDs []string) ([]*domain.ProductOrderDetails, error) {
	return s.repo.FindNamesByIDs(ctx, productIDs)
}

// GetCategories retrieves a list of categories.
func (s *productService) GetCategories(ctx context.Context) ([]*domain.Category, error) {
	return s.repo.FindAllCategories(ctx)
}

func (s *productService) GetCategory(ctx context.Context, id uuid.UUID) (*domain.Category, error) {
	return s.repo.FindCategoryByID(ctx, id)
}

func (s *productService) BatchGetCategoriesByProductIDs(ctx context.Context, productIDs []string) (map[string][]*domain.Category, error) {
	return s.repo.FindCategoriesByProductIDs(ctx, productIDs)
}

func (s *productService) BatchGetProductsByCategory(ctx context.Context, categoryIDs []string) (map[string][]*domain.Product, error) {
	return s.repo.FindByCategoryIDs(ctx, categoryIDs)
}

func (s *productService) BatchGetProductByIDs(ctx context.Context, productIDs []string) (map[string][]*domain.Product, error) {
	return s.repo.BatchGetProductByIDs(ctx, productIDs)
}

func (s *productService) BatchGetCategoryTranslations(ctx context.Context, categoryIDs []string) (map[string][]*domain.Translation, error) {
	return s.repo.BatchGetCategoryTranslations(ctx, categoryIDs)
}

func (s *productService) BatchGetProductTranslations(ctx context.Context, productIDs []string) (map[string][]*domain.Translation, error) {
	return s.repo.BatchGetProductTranslations(ctx, productIDs)
}

func (s *productService) GetChoicesByProductID(ctx context.Context, productID uuid.UUID) ([]*domain.ProductChoice, error) {
	return s.repo.FindChoicesByProductID(ctx, productID)
}

func (s *productService) GetChoiceGroupsByProductID(ctx context.Context, productID uuid.UUID) ([]*domain.ProductChoiceGroup, error) {
	return s.repo.FindChoiceGroupsByProductID(ctx, productID)
}

func (s *productService) GetChoiceGroupByID(ctx context.Context, groupID uuid.UUID) (*domain.ProductChoiceGroup, error) {
	return s.repo.FindChoiceGroupByID(ctx, groupID)
}

func (s *productService) BatchGetChoiceGroupsByProductIDs(ctx context.Context, productIDs []string) (map[string][]*domain.ProductChoiceGroup, error) {
	return s.repo.BatchGetChoiceGroupsByProductIDs(ctx, productIDs)
}

func (s *productService) CreateChoiceGroup(ctx context.Context, group *domain.ProductChoiceGroup) error {
	return s.repo.CreateChoiceGroup(ctx, group)
}

func (s *productService) UpdateChoiceGroup(ctx context.Context, group *domain.ProductChoiceGroup) error {
	return s.repo.UpdateChoiceGroup(ctx, group)
}

func (s *productService) DeleteChoiceGroup(ctx context.Context, groupID uuid.UUID) error {
	return s.repo.DeleteChoiceGroup(ctx, groupID)
}

func (s *productService) GetChoiceByID(ctx context.Context, choiceID uuid.UUID) (*domain.ProductChoice, error) {
	return s.repo.FindChoiceByID(ctx, choiceID)
}

func (s *productService) BatchGetChoicesByProductIDs(ctx context.Context, productIDs []string) (map[string][]*domain.ProductChoice, error) {
	return s.repo.BatchGetChoicesByProductIDs(ctx, productIDs)
}

func (s *productService) CreateChoice(ctx context.Context, choice *domain.ProductChoice) error {
	return s.repo.CreateChoice(ctx, choice)
}

func (s *productService) UpdateChoice(ctx context.Context, choice *domain.ProductChoice) error {
	return s.repo.UpdateChoice(ctx, choice)
}

func (s *productService) DeleteChoice(ctx context.Context, choiceID uuid.UUID) error {
	return s.repo.DeleteChoice(ctx, choiceID)
}
