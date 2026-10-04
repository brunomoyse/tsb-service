package application

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/product/domain"
)

// stubRepo answers every repository call from the fields below and records the call.
type stubRepo struct {
	calls []string
	err   error

	product    *domain.Product
	products   []*domain.Product
	details    []*domain.ProductOrderDetails
	category   *domain.Category
	categories []*domain.Category
	group      *domain.ProductChoiceGroup
	groups     []*domain.ProductChoiceGroup
	choice     *domain.ProductChoice
	choices    []*domain.ProductChoice
	created    *domain.Product
	ids        []string
	slug       string
}

func (r *stubRepo) rec(name string) { r.calls = append(r.calls, name) }

func (r *stubRepo) Create(_ context.Context, p *domain.Product) error {
	r.rec("Create")
	r.created = p
	return r.err
}
func (r *stubRepo) Update(_ context.Context, p *domain.Product) error {
	r.rec("Update")
	r.created = p
	return r.err
}
func (r *stubRepo) FindByID(context.Context, uuid.UUID) (*domain.Product, error) {
	r.rec("FindByID")
	return r.product, r.err
}
func (r *stubRepo) FindAll(context.Context) ([]*domain.Product, error) {
	r.rec("FindAll")
	return r.products, r.err
}
func (r *stubRepo) FindByCategoryID(context.Context, string) ([]*domain.Product, error) {
	r.rec("FindByCategoryID")
	return r.products, r.err
}
func (r *stubRepo) FindAllCategories(context.Context) ([]*domain.Category, error) {
	r.rec("FindAllCategories")
	return r.categories, r.err
}
func (r *stubRepo) FindCategoryByID(context.Context, uuid.UUID) (*domain.Category, error) {
	r.rec("FindCategoryByID")
	return r.category, r.err
}
func (r *stubRepo) FindCategoryBySlug(_ context.Context, slug string) (*domain.Category, error) {
	r.rec("FindCategoryBySlug")
	r.slug = slug
	return r.category, r.err
}
func (r *stubRepo) FindByIDs(_ context.Context, ids []string) ([]*domain.ProductOrderDetails, error) {
	r.rec("FindByIDs")
	r.ids = ids
	return r.details, r.err
}
func (r *stubRepo) FindNamesByIDs(_ context.Context, ids []string) ([]*domain.ProductOrderDetails, error) {
	r.rec("FindNamesByIDs")
	r.ids = ids
	return r.details, r.err
}
func (r *stubRepo) FindForPricing(_ context.Context, ids []string) ([]*domain.ProductOrderDetails, error) {
	r.rec("FindForPricing")
	r.ids = ids
	return r.details, r.err
}
func (r *stubRepo) FindCategoriesByProductIDs(_ context.Context, ids []string) (map[string][]*domain.Category, error) {
	r.rec("FindCategoriesByProductIDs")
	r.ids = ids
	return map[string][]*domain.Category{"p": r.categories}, r.err
}
func (r *stubRepo) FindByCategoryIDs(_ context.Context, ids []string) (map[string][]*domain.Product, error) {
	r.rec("FindByCategoryIDs")
	r.ids = ids
	return map[string][]*domain.Product{"c": r.products}, r.err
}
func (r *stubRepo) BatchGetProductByIDs(_ context.Context, ids []string) (map[string][]*domain.Product, error) {
	r.rec("BatchGetProductByIDs")
	r.ids = ids
	return map[string][]*domain.Product{"p": r.products}, r.err
}
func (r *stubRepo) BatchGetCategoryTranslations(_ context.Context, ids []string) (map[string][]*domain.Translation, error) {
	r.rec("BatchGetCategoryTranslations")
	r.ids = ids
	return map[string][]*domain.Translation{"c": {{Language: "fr", Name: "cat"}}}, r.err
}
func (r *stubRepo) BatchGetProductTranslations(_ context.Context, ids []string) (map[string][]*domain.Translation, error) {
	r.rec("BatchGetProductTranslations")
	r.ids = ids
	return map[string][]*domain.Translation{"p": {{Language: "fr", Name: "prod"}}}, r.err
}
func (r *stubRepo) FindChoiceGroupsByProductID(context.Context, uuid.UUID) ([]*domain.ProductChoiceGroup, error) {
	r.rec("FindChoiceGroupsByProductID")
	return r.groups, r.err
}
func (r *stubRepo) FindChoiceGroupByID(context.Context, uuid.UUID) (*domain.ProductChoiceGroup, error) {
	r.rec("FindChoiceGroupByID")
	return r.group, r.err
}
func (r *stubRepo) BatchGetChoiceGroupsByProductIDs(_ context.Context, ids []string) (map[string][]*domain.ProductChoiceGroup, error) {
	r.rec("BatchGetChoiceGroupsByProductIDs")
	r.ids = ids
	return map[string][]*domain.ProductChoiceGroup{"p": r.groups}, r.err
}
func (r *stubRepo) BatchGetChoiceGroupsByIDs(_ context.Context, ids []string) (map[string][]*domain.ProductChoiceGroup, error) {
	r.rec("BatchGetChoiceGroupsByIDs")
	r.ids = ids
	return map[string][]*domain.ProductChoiceGroup{"g": r.groups}, r.err
}
func (r *stubRepo) CreateChoiceGroup(context.Context, *domain.ProductChoiceGroup) error {
	r.rec("CreateChoiceGroup")
	return r.err
}
func (r *stubRepo) UpdateChoiceGroup(context.Context, *domain.ProductChoiceGroup) error {
	r.rec("UpdateChoiceGroup")
	return r.err
}
func (r *stubRepo) DeleteChoiceGroup(context.Context, uuid.UUID) error {
	r.rec("DeleteChoiceGroup")
	return r.err
}
func (r *stubRepo) FindChoicesByProductID(context.Context, uuid.UUID) ([]*domain.ProductChoice, error) {
	r.rec("FindChoicesByProductID")
	return r.choices, r.err
}
func (r *stubRepo) FindChoiceByID(context.Context, uuid.UUID) (*domain.ProductChoice, error) {
	r.rec("FindChoiceByID")
	return r.choice, r.err
}
func (r *stubRepo) BatchGetChoicesByProductIDs(_ context.Context, ids []string) (map[string][]*domain.ProductChoice, error) {
	r.rec("BatchGetChoicesByProductIDs")
	r.ids = ids
	return map[string][]*domain.ProductChoice{"p": r.choices}, r.err
}
func (r *stubRepo) BatchGetChoicesByIDs(_ context.Context, ids []string) (map[string][]*domain.ProductChoice, error) {
	r.rec("BatchGetChoicesByIDs")
	r.ids = ids
	return map[string][]*domain.ProductChoice{"c": r.choices}, r.err
}
func (r *stubRepo) CreateChoice(context.Context, *domain.ProductChoice) error {
	r.rec("CreateChoice")
	return r.err
}
func (r *stubRepo) UpdateChoice(context.Context, *domain.ProductChoice) error {
	r.rec("UpdateChoice")
	return r.err
}
func (r *stubRepo) DeleteChoice(context.Context, uuid.UUID) error {
	r.rec("DeleteChoice")
	return r.err
}

func fr3() []domain.Translation {
	return []domain.Translation{{Language: "fr", Name: "A"}, {Language: "en", Name: "B"}, {Language: "nl", Name: "C"}}
}

func TestCreateProduct(t *testing.T) {
	ctx := t.Context()
	cat := uuid.New()
	code := "S1"
	pieces := 8

	t.Run("builds the aggregate from every argument and persists it", func(t *testing.T) {
		repo := &stubRepo{}
		got, err := NewProductService(repo).CreateProduct(ctx, cat, decimal.RequireFromString("9.90"), &code, &pieces,
			true, false, true, true, true, true, false, domain.VatCategoryBeverage, fr3())
		require.NoError(t, err)
		assert.Same(t, got, repo.created)
		assert.Equal(t, []string{"Create"}, repo.calls)
		assert.NotEqual(t, uuid.Nil, got.ID)
		assert.Equal(t, cat, got.CategoryID)
		assert.True(t, got.Price.Equal(decimal.RequireFromString("9.90")))
		assert.Equal(t, "S1", *got.Code)
		assert.Equal(t, 8, *got.PieceCount)
		assert.True(t, got.IsVisible)
		assert.False(t, got.IsAvailable)
		assert.True(t, got.IsHalal)
		assert.True(t, got.IsVegetarian)
		assert.True(t, got.IsSpicy)
		assert.True(t, got.IsLunchOnly)
		assert.False(t, got.IsDiscountable)
		assert.Equal(t, domain.VatCategoryBeverage, got.VatCategory)
		assert.Len(t, got.Translations, 3)
	})

	t.Run("invalid input never reaches the repository", func(t *testing.T) {
		repo := &stubRepo{}
		svc := NewProductService(repo)
		_, err := svc.CreateProduct(ctx, cat, decimal.Zero, nil, nil, true, true, false, false, false, false, true, domain.VatCategoryFood, fr3()[:2])
		assert.ErrorContains(t, err, "at least 3 translations")
		_, err = svc.CreateProduct(ctx, cat, decimal.Zero, nil, nil, false, true, false, false, false, false, true, domain.VatCategoryFood, nil)
		assert.ErrorContains(t, err, "at least one translation")
		_, err = svc.CreateProduct(ctx, cat, decimal.Zero, nil, nil, true, true, false, false, false, false, true, "bogus", fr3())
		assert.ErrorContains(t, err, "invalid vat category")
		assert.Empty(t, repo.calls)
	})

	t.Run("a repository failure returns no product", func(t *testing.T) {
		boom := errors.New("db down")
		got, err := NewProductService(&stubRepo{err: boom}).CreateProduct(ctx, cat, decimal.Zero, nil, nil, true, true, false, false, false, false, true, domain.VatCategoryFood, fr3())
		assert.ErrorIs(t, err, boom)
		assert.Nil(t, got)
	})
}

func TestGetCategoryBySlugTurnsAMissIntoNil(t *testing.T) {
	ctx := t.Context()
	cat := &domain.Category{Slug: "sushi"}

	got, err := NewProductService(&stubRepo{category: cat}).GetCategoryBySlug(ctx, "sushi")
	require.NoError(t, err)
	assert.Same(t, cat, got)

	repo := &stubRepo{err: sql.ErrNoRows}
	got, err = NewProductService(repo).GetCategoryBySlug(ctx, "nope")
	require.NoError(t, err, "an unknown slug is not an error")
	assert.Nil(t, got)
	assert.Equal(t, "nope", repo.slug)

	boom := errors.New("db down")
	_, err = NewProductService(&stubRepo{err: boom}).GetCategoryBySlug(ctx, "x")
	assert.ErrorIs(t, err, boom)
}

// TestProductServiceDelegation checks that every pass-through method calls the matching repository
// method with the caller's identifiers and returns what it answered, and surfaces its failure.
func TestProductServiceDelegation(t *testing.T) {
	ctx := t.Context()
	id := uuid.New()
	ids := []string{"a", "b"}
	boom := errors.New("db down")

	product := &domain.Product{ID: uuid.New()}
	cat := &domain.Category{ID: uuid.New()}
	group := &domain.ProductChoiceGroup{ID: uuid.New()}
	choice := &domain.ProductChoice{ID: uuid.New()}
	details := []*domain.ProductOrderDetails{{Name: "x"}}
	newRepo := func(err error) *stubRepo {
		return &stubRepo{
			err: err, product: product, products: []*domain.Product{product}, details: details, category: cat,
			categories: []*domain.Category{cat}, group: group, groups: []*domain.ProductChoiceGroup{group},
			choice: choice, choices: []*domain.ProductChoice{choice},
		}
	}

	type call struct {
		name     string
		repoName string
		run      func(s ProductService) (any, error)
		want     any
		wantIDs  bool
	}
	calls := []call{
		{"GetProduct", "FindByID", func(s ProductService) (any, error) { return s.GetProduct(ctx, id) }, product, false},
		{"GetProducts", "FindAll", func(s ProductService) (any, error) { return s.GetProducts(ctx) }, []*domain.Product{product}, false},
		{"GetProductsByIDs", "FindByIDs", func(s ProductService) (any, error) { return s.GetProductsByIDs(ctx, ids) }, details, true},
		{"GetProductNamesForInvoice", "FindNamesByIDs", func(s ProductService) (any, error) { return s.GetProductNamesForInvoice(ctx, ids) }, details, true},
		{"GetProductsForPricing", "FindForPricing", func(s ProductService) (any, error) { return s.GetProductsForPricing(ctx, ids) }, details, true},
		{"GetCategories", "FindAllCategories", func(s ProductService) (any, error) { return s.GetCategories(ctx) }, []*domain.Category{cat}, false},
		{"GetCategory", "FindCategoryByID", func(s ProductService) (any, error) { return s.GetCategory(ctx, id) }, cat, false},
		{"BatchGetCategoriesByProductIDs", "FindCategoriesByProductIDs", func(s ProductService) (any, error) { return s.BatchGetCategoriesByProductIDs(ctx, ids) }, map[string][]*domain.Category{"p": {cat}}, true},
		{"BatchGetProductsByCategory", "FindByCategoryIDs", func(s ProductService) (any, error) { return s.BatchGetProductsByCategory(ctx, ids) }, map[string][]*domain.Product{"c": {product}}, true},
		{"BatchGetProductByIDs", "BatchGetProductByIDs", func(s ProductService) (any, error) { return s.BatchGetProductByIDs(ctx, ids) }, map[string][]*domain.Product{"p": {product}}, true},
		{"BatchGetCategoryTranslations", "BatchGetCategoryTranslations", func(s ProductService) (any, error) { return s.BatchGetCategoryTranslations(ctx, ids) }, map[string][]*domain.Translation{"c": {{Language: "fr", Name: "cat"}}}, true},
		{"BatchGetProductTranslations", "BatchGetProductTranslations", func(s ProductService) (any, error) { return s.BatchGetProductTranslations(ctx, ids) }, map[string][]*domain.Translation{"p": {{Language: "fr", Name: "prod"}}}, true},
		{"GetChoiceGroupsByProductID", "FindChoiceGroupsByProductID", func(s ProductService) (any, error) { return s.GetChoiceGroupsByProductID(ctx, id) }, []*domain.ProductChoiceGroup{group}, false},
		{"GetChoiceGroupByID", "FindChoiceGroupByID", func(s ProductService) (any, error) { return s.GetChoiceGroupByID(ctx, id) }, group, false},
		{"BatchGetChoiceGroupsByProductIDs", "BatchGetChoiceGroupsByProductIDs", func(s ProductService) (any, error) { return s.BatchGetChoiceGroupsByProductIDs(ctx, ids) }, map[string][]*domain.ProductChoiceGroup{"p": {group}}, true},
		{"BatchGetChoiceGroupsByIDs", "BatchGetChoiceGroupsByIDs", func(s ProductService) (any, error) { return s.BatchGetChoiceGroupsByIDs(ctx, ids) }, map[string][]*domain.ProductChoiceGroup{"g": {group}}, true},
		{"GetChoicesByProductID", "FindChoicesByProductID", func(s ProductService) (any, error) { return s.GetChoicesByProductID(ctx, id) }, []*domain.ProductChoice{choice}, false},
		{"GetChoiceByID", "FindChoiceByID", func(s ProductService) (any, error) { return s.GetChoiceByID(ctx, id) }, choice, false},
		{"BatchGetChoicesByProductIDs", "BatchGetChoicesByProductIDs", func(s ProductService) (any, error) { return s.BatchGetChoicesByProductIDs(ctx, ids) }, map[string][]*domain.ProductChoice{"p": {choice}}, true},
		{"BatchGetChoicesByIDs", "BatchGetChoicesByIDs", func(s ProductService) (any, error) { return s.BatchGetChoicesByIDs(ctx, ids) }, map[string][]*domain.ProductChoice{"c": {choice}}, true},
		{"UpdateProduct", "Update", func(s ProductService) (any, error) { return nil, s.UpdateProduct(ctx, product) }, nil, false},
		{"CreateChoiceGroup", "CreateChoiceGroup", func(s ProductService) (any, error) { return nil, s.CreateChoiceGroup(ctx, group) }, nil, false},
		{"UpdateChoiceGroup", "UpdateChoiceGroup", func(s ProductService) (any, error) { return nil, s.UpdateChoiceGroup(ctx, group) }, nil, false},
		{"DeleteChoiceGroup", "DeleteChoiceGroup", func(s ProductService) (any, error) { return nil, s.DeleteChoiceGroup(ctx, id) }, nil, false},
		{"CreateChoice", "CreateChoice", func(s ProductService) (any, error) { return nil, s.CreateChoice(ctx, choice) }, nil, false},
		{"UpdateChoice", "UpdateChoice", func(s ProductService) (any, error) { return nil, s.UpdateChoice(ctx, choice) }, nil, false},
		{"DeleteChoice", "DeleteChoice", func(s ProductService) (any, error) { return nil, s.DeleteChoice(ctx, id) }, nil, false},
	}
	for _, c := range calls {
		t.Run(c.name, func(t *testing.T) {
			repo := newRepo(nil)
			got, err := c.run(NewProductService(repo))
			require.NoError(t, err)
			assert.Equal(t, []string{c.repoName}, repo.calls)
			if c.want != nil {
				assert.Equal(t, c.want, got)
			}
			if c.wantIDs {
				assert.Equal(t, ids, repo.ids)
			}

			failing := newRepo(boom)
			_, err = c.run(NewProductService(failing))
			assert.ErrorIs(t, err, boom, "a repository failure reaches the caller")
		})
	}
}

func TestProductDataLoaders(t *testing.T) {
	repo := &stubRepo{
		products:   []*domain.Product{{ID: uuid.New()}},
		categories: []*domain.Category{{ID: uuid.New()}},
		groups:     []*domain.ProductChoiceGroup{{ID: uuid.New()}},
		choices:    []*domain.ProductChoice{{ID: uuid.New()}},
	}
	ctx := AttachDataLoaders(t.Context(), NewProductService(repo))

	cats, err := GetProductCategoryLoader(ctx).Loader.Load(ctx, "p")
	require.NoError(t, err)
	assert.Equal(t, repo.categories, cats)

	prods, err := GetCategoryProductLoader(ctx).Loader.Load(ctx, "c")
	require.NoError(t, err)
	assert.Equal(t, repo.products, prods)

	items, err := GetOrderItemProductLoader(ctx).Loader.Load(ctx, "p")
	require.NoError(t, err)
	assert.Equal(t, repo.products, items)

	catTr, err := GetCategoryTranslationLoader(ctx).Loader.Load(ctx, "c")
	require.NoError(t, err)
	require.Len(t, catTr, 1)
	assert.Equal(t, "cat", catTr[0].Name)

	prodTr, err := GetProductTranslationLoader(ctx).Loader.Load(ctx, "p")
	require.NoError(t, err)
	assert.Equal(t, "prod", prodTr[0].Name)

	chs, err := GetProductChoiceLoader(ctx).Loader.Load(ctx, "p")
	require.NoError(t, err)
	assert.Equal(t, repo.choices, chs)

	grps, err := GetProductChoiceGroupLoader(ctx).Loader.Load(ctx, "p")
	require.NoError(t, err)
	assert.Equal(t, repo.groups, grps)

	byID, err := GetChoiceByIDLoader(ctx).Loader.Load(ctx, "c")
	require.NoError(t, err)
	assert.Equal(t, repo.choices, byID)

	grpByID, err := GetChoiceGroupByIDLoader(ctx).Loader.Load(ctx, "g")
	require.NoError(t, err)
	assert.Equal(t, repo.groups, grpByID)

	missing, err := GetProductChoiceLoader(ctx).Loader.Load(ctx, "unknown")
	require.NoError(t, err)
	assert.Empty(t, missing, "an id with no rows loads as an empty list")

	t.Run("without attached loaders every getter returns nil", func(t *testing.T) {
		bare := t.Context()
		assert.Nil(t, GetProductCategoryLoader(bare))
		assert.Nil(t, GetCategoryProductLoader(bare))
		assert.Nil(t, GetOrderItemProductLoader(bare))
		assert.Nil(t, GetCategoryTranslationLoader(bare))
		assert.Nil(t, GetProductTranslationLoader(bare))
		assert.Nil(t, GetProductChoiceLoader(bare))
		assert.Nil(t, GetProductChoiceGroupLoader(bare))
		assert.Nil(t, GetChoiceByIDLoader(bare))
		assert.Nil(t, GetChoiceGroupByIDLoader(bare))
	})

	t.Run("a repository failure becomes a loader error", func(t *testing.T) {
		failing := AttachDataLoaders(t.Context(), NewProductService(&stubRepo{err: errors.New("db down")}))
		_, err := GetProductCategoryLoader(failing).Loader.Load(failing, "p")
		require.ErrorContains(t, err, "failed to fetch categories")
		_, err = GetChoiceByIDLoader(failing).Loader.Load(failing, "c")
		require.ErrorContains(t, err, "failed to fetch product choices")
	})
}
