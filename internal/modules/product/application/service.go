package application

import (
	"context"
	"database/sql"
	"errors"
	"sync"

	"github.com/shopspring/decimal"
	"go.uber.org/zap"
	"tsb-service/internal/modules/product/domain"
	"tsb-service/pkg/logging"

	"github.com/google/uuid"
)

// MenuKind names what a menu edit touched.
type MenuKind string

const (
	MenuProduct     MenuKind = "product"
	MenuChoiceGroup MenuKind = "choice_group"
	MenuChoice      MenuKind = "choice"
)

// MenuObserver is told, once, after every successful write of the menu that goes through the
// ProductService (a product, a choice group or a choice created, updated or deleted). It exists so
// an integration (the HubRise catalog publisher) can follow the menu without this module knowing
// about it. It is called on the caller's goroutine after the write, so an implementation must be
// quick (a row update, never a network call). An error, and a panic, is logged and never fails the
// write: the menu already changed.
type MenuObserver interface {
	MenuChanged(ctx context.Context, kind MenuKind, id uuid.UUID) error
}

// ProductService defines the application service interface for product operations.
type ProductService interface {
	// SetMenuObserver installs the observer of menu edits; nil (the default) removes it. It is meant
	// to be called once at startup, before the service takes requests.
	SetMenuObserver(observer MenuObserver)

	CreateProduct(ctx context.Context, categoryID uuid.UUID, price decimal.Decimal, code *string, pieceCount *int, isVisible bool, isAvailable bool, isHalal bool, isVegetarian bool, isSpicy bool, isLunchOnly bool, isDiscountable bool, vatCategory domain.VatCategory, translations []domain.Translation) (*domain.Product, error)
	GetProduct(ctx context.Context, id uuid.UUID) (*domain.Product, error)
	GetProducts(ctx context.Context) ([]*domain.Product, error)
	GetProductsByIDs(ctx context.Context, productIDs []string) ([]*domain.ProductOrderDetails, error)
	GetProductNamesForInvoice(ctx context.Context, productIDs []string) ([]*domain.ProductOrderDetails, error)
	// GetProductsForPricing returns the products of an order with their current price and
	// availability; unlike GetProductsByIDs it does not fail when one is sold out.
	GetProductsForPricing(ctx context.Context, productIDs []string) ([]*domain.ProductOrderDetails, error)
	GetCategories(ctx context.Context) ([]*domain.Category, error)
	GetCategory(ctx context.Context, id uuid.UUID) (*domain.Category, error)
	GetCategoryBySlug(ctx context.Context, slug string) (*domain.Category, error)
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
	BatchGetChoiceGroupsByIDs(ctx context.Context, groupIDs []string) (map[string][]*domain.ProductChoiceGroup, error)
	CreateChoiceGroup(ctx context.Context, group *domain.ProductChoiceGroup) error
	UpdateChoiceGroup(ctx context.Context, group *domain.ProductChoiceGroup) error
	DeleteChoiceGroup(ctx context.Context, groupID uuid.UUID) error

	GetChoicesByProductID(ctx context.Context, productID uuid.UUID) ([]*domain.ProductChoice, error)
	GetChoiceByID(ctx context.Context, choiceID uuid.UUID) (*domain.ProductChoice, error)
	BatchGetChoicesByProductIDs(ctx context.Context, productIDs []string) (map[string][]*domain.ProductChoice, error)
	BatchGetChoicesByIDs(ctx context.Context, choiceIDs []string) (map[string][]*domain.ProductChoice, error)
	CreateChoice(ctx context.Context, choice *domain.ProductChoice) error
	UpdateChoice(ctx context.Context, choice *domain.ProductChoice) error
	DeleteChoice(ctx context.Context, choiceID uuid.UUID) error
}

type productService struct {
	repo       domain.ProductRepository
	observerMu sync.RWMutex
	observer   MenuObserver
}

func (s *productService) SetMenuObserver(observer MenuObserver) {
	s.observerMu.Lock()
	defer s.observerMu.Unlock()
	s.observer = observer
}

// menuChanged tells the observer about a successful write. Nothing the observer does can fail it.
func (s *productService) menuChanged(ctx context.Context, kind MenuKind, id uuid.UUID) {
	s.observerMu.RLock()
	observer := s.observer
	s.observerMu.RUnlock()
	if observer == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			logging.FromContext(ctx).Error("menu observer panicked", zap.String("kind", string(kind)), zap.String("id", id.String()), zap.Any("panic", r))
		}
	}()
	if err := observer.MenuChanged(ctx, kind, id); err != nil {
		logging.FromContext(ctx).Error("menu observer failed", zap.String("kind", string(kind)), zap.String("id", id.String()), zap.Error(err))
	}
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
	isLunchOnly bool,
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
	product.IsLunchOnly = isLunchOnly
	product.IsDiscountable = isDiscountable

	if err := s.repo.Create(ctx, product); err != nil {
		return nil, err
	}
	s.menuChanged(ctx, MenuProduct, product.ID)

	return product, nil
}

// UpdateProduct updates an existing product.
func (s *productService) UpdateProduct(ctx context.Context, product *domain.Product) error {
	if err := s.repo.Update(ctx, product); err != nil {
		return err
	}
	s.menuChanged(ctx, MenuProduct, product.ID)
	return nil
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

func (s *productService) GetProductsForPricing(ctx context.Context, productIDs []string) ([]*domain.ProductOrderDetails, error) {
	return s.repo.FindForPricing(ctx, productIDs)
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

// GetCategoryBySlug retrieves one category by its slug; (nil, nil) when there is none.
func (s *productService) GetCategoryBySlug(ctx context.Context, slug string) (*domain.Category, error) {
	c, err := s.repo.FindCategoryBySlug(ctx, slug)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return c, err
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

func (s *productService) BatchGetChoiceGroupsByIDs(ctx context.Context, groupIDs []string) (map[string][]*domain.ProductChoiceGroup, error) {
	return s.repo.BatchGetChoiceGroupsByIDs(ctx, groupIDs)
}

func (s *productService) BatchGetChoicesByIDs(ctx context.Context, choiceIDs []string) (map[string][]*domain.ProductChoice, error) {
	return s.repo.BatchGetChoicesByIDs(ctx, choiceIDs)
}

func (s *productService) CreateChoiceGroup(ctx context.Context, group *domain.ProductChoiceGroup) error {
	if err := s.repo.CreateChoiceGroup(ctx, group); err != nil {
		return err
	}
	s.menuChanged(ctx, MenuChoiceGroup, group.ID)
	return nil
}

func (s *productService) UpdateChoiceGroup(ctx context.Context, group *domain.ProductChoiceGroup) error {
	if err := s.repo.UpdateChoiceGroup(ctx, group); err != nil {
		return err
	}
	s.menuChanged(ctx, MenuChoiceGroup, group.ID)
	return nil
}

func (s *productService) DeleteChoiceGroup(ctx context.Context, groupID uuid.UUID) error {
	if err := s.repo.DeleteChoiceGroup(ctx, groupID); err != nil {
		return err
	}
	s.menuChanged(ctx, MenuChoiceGroup, groupID)
	return nil
}

func (s *productService) GetChoiceByID(ctx context.Context, choiceID uuid.UUID) (*domain.ProductChoice, error) {
	return s.repo.FindChoiceByID(ctx, choiceID)
}

func (s *productService) BatchGetChoicesByProductIDs(ctx context.Context, productIDs []string) (map[string][]*domain.ProductChoice, error) {
	return s.repo.BatchGetChoicesByProductIDs(ctx, productIDs)
}

func (s *productService) CreateChoice(ctx context.Context, choice *domain.ProductChoice) error {
	if err := s.repo.CreateChoice(ctx, choice); err != nil {
		return err
	}
	s.menuChanged(ctx, MenuChoice, choice.ID)
	return nil
}

func (s *productService) UpdateChoice(ctx context.Context, choice *domain.ProductChoice) error {
	if err := s.repo.UpdateChoice(ctx, choice); err != nil {
		return err
	}
	s.menuChanged(ctx, MenuChoice, choice.ID)
	return nil
}

func (s *productService) DeleteChoice(ctx context.Context, choiceID uuid.UUID) error {
	if err := s.repo.DeleteChoice(ctx, choiceID); err != nil {
		return err
	}
	s.menuChanged(ctx, MenuChoice, choiceID)
	return nil
}
