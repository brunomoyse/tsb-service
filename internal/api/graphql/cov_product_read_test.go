package graphql_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/model"
	"tsb-service/internal/api/graphql/resolver"
	"tsb-service/internal/api/graphql/testhelpers"
)

// The public menu queries and the loader-backed fields of the menu types.

func TestMenuQueries(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	admin := adminToken(t, env.TestContext)
	sushi := env.Fixtures.SushiCategory

	t.Run("categories list their products, in the caller's language", func(t *testing.T) {
		resp := gqlAs(t, env.TestContext, "", "fr", `{ productCategories { id slug name order products { name price category { id } } translations { language name } } }`, nil)
		require.Empty(t, resp.Errors, "%+v", resp.Errors)
		var data struct {
			ProductCategories []struct {
				ID, Slug, Name string
				Order          int
				Products       []struct {
					Name     string
					Price    string
					Category struct{ ID string }
				}
				Translations []struct{ Language, Name string }
			}
		}
		require.NoError(t, json.Unmarshal(resp.Data, &data))
		require.Len(t, data.ProductCategories, 3)
		var got *struct {
			ID, Slug, Name string
			Order          int
			Products       []struct {
				Name     string
				Price    string
				Category struct{ ID string }
			}
			Translations []struct{ Language, Name string }
		}
		for i := range data.ProductCategories {
			if data.ProductCategories[i].ID == sushi.ID.String() {
				got = &data.ProductCategories[i]
			}
		}
		require.NotNil(t, got)
		assert.Equal(t, "Sushi", got.Name)
		assert.Len(t, got.Products, 2)
		assert.Len(t, got.Translations, 3)
		for _, p := range got.Products {
			assert.Equal(t, sushi.ID.String(), p.Category.ID)
		}
	})

	t.Run("a category is found by id and by slug, an unknown slug is null", func(t *testing.T) {
		resp := gqlAs(t, env.TestContext, "", "en", `query ($id: ID!) { productCategory(id: $id) { name products { name } } }`, map[string]any{"id": sushi.ID.String()})
		require.Empty(t, resp.Errors, "%+v", resp.Errors)
		assert.Contains(t, string(resp.Data), "Salmon Sushi")

		resp = gqlAs(t, env.TestContext, "", "en", `query ($s: String!) { productCategoryBySlug(slug: $s) { id } }`, map[string]any{"s": sushi.Slug})
		require.Empty(t, resp.Errors)
		assert.Contains(t, string(resp.Data), sushi.ID.String())

		resp = gqlAs(t, env.TestContext, "", "en", `{ productCategoryBySlug(slug: "nope") { id } }`, nil)
		require.Empty(t, resp.Errors)
		require.JSONEq(t, `{"productCategoryBySlug":null}`, string(resp.Data))

		resp = gqlAs(t, env.TestContext, "", "en", `query ($id: ID!) { productCategory(id: $id) { id } }`, map[string]any{"id": uuid.NewString()})
		require.Len(t, resp.Errors, 1)
	})

	t.Run("the product list and a product", func(t *testing.T) {
		resp := gqlAs(t, env.TestContext, "", "en", `{ products { id name } }`, nil)
		require.Empty(t, resp.Errors)
		assert.Contains(t, string(resp.Data), "Green Tea")

		resp = gqlAs(t, env.TestContext, "", "en", `query ($id: ID!) { product(id: $id) { name translations { language name } category { name } } }`, map[string]any{"id": env.Fixtures.GreenTea.ID.String()})
		require.Empty(t, resp.Errors, "%+v", resp.Errors)
		assert.Contains(t, string(resp.Data), `"name":"Green Tea"`)

		resp = gqlAs(t, env.TestContext, admin, "en", `query ($id: ID!) { product(id: $id) { id } }`, map[string]any{"id": uuid.NewString()})
		require.Len(t, resp.Errors, 1)
	})

	t.Run("lookups that find nothing answer null", func(t *testing.T) {
		nothing := env.with(func(r *resolver.Resolver) {
			r.ProductService = faultyProducts{ProductService: env.Resolver.ProductService, nothing: true}
		})
		ctx := env.ctxFor("", false, "en")
		p, err := nothing.Query().Product(ctx, uuid.New())
		require.NoError(t, err)
		assert.Nil(t, p)
		c, err := nothing.Query().ProductCategory(ctx, uuid.New())
		require.NoError(t, err)
		assert.Nil(t, c)
		choice, err := nothing.OrderItem().Choice(ctx, &model.OrderItem{ChoiceID: new(uuid.New())})
		require.NoError(t, err)
		assert.Nil(t, choice)
	})

	t.Run("a failing store is reported by every menu query", func(t *testing.T) {
		broken := env.brokenResolver(t)
		ctx := env.ctxFor("", false, "en")
		_, err := broken.Query().Product(ctx, uuid.New())
		require.ErrorContains(t, err, "failed to get product")
		_, err = broken.Query().Products(ctx)
		require.ErrorContains(t, err, "failed to get products")
		_, err = broken.Query().ProductCategory(ctx, uuid.New())
		require.ErrorContains(t, err, "failed to get category")
		_, err = broken.Query().ProductCategories(ctx)
		require.ErrorContains(t, err, "failed to get categories")
		_, err = broken.Query().ProductCategoryBySlug(ctx, "x")
		require.ErrorContains(t, err, "failed to get category")
	})
}

func TestMenuFieldResolversWithoutOrWithFailingLoaders(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	r := env.Resolver
	bare := t.Context()
	p := &model.Product{ID: uuid.New()}
	c := &model.ProductCategory{ID: uuid.New()}
	g := &model.ProductChoiceGroup{ID: uuid.New(), ProductID: uuid.New()}

	t.Run("without the request's loaders each field reports which one is missing", func(t *testing.T) {
		_, err := r.Product().Category(bare, p)
		require.EqualError(t, err, "no product category loader found")
		_, err = r.Product().Choices(bare, p)
		require.EqualError(t, err, "no product choice loader found")
		_, err = r.Product().ChoiceGroups(bare, p)
		require.EqualError(t, err, "no product choice group loader found")
		_, err = r.Product().Translations(bare, p)
		require.EqualError(t, err, "no product translations loader found")
		_, err = r.ProductCategory().Products(bare, c)
		require.EqualError(t, err, "no category product loader found")
		_, err = r.ProductCategory().Translations(bare, c)
		require.EqualError(t, err, "no category translations loader found")
		_, err = r.ProductChoiceGroup().Choices(bare, g)
		require.EqualError(t, err, "no product choice loader found")
	})

	t.Run("loaders that fail say which field failed", func(t *testing.T) {
		broken := env.brokenResolver(t)
		ctx := loadersFor(broken, "", false, "en")
		_, err := broken.Product().Category(ctx, p)
		require.ErrorContains(t, err, "failed to load product category")
		_, err = broken.Product().Choices(ctx, p)
		require.ErrorContains(t, err, "failed to load product choices")
		_, err = broken.Product().ChoiceGroups(ctx, p)
		require.ErrorContains(t, err, "failed to load product choice groups")
		_, err = broken.Product().Translations(ctx, p)
		require.ErrorContains(t, err, "failed to load product translations")
		_, err = broken.ProductCategory().Products(ctx, c)
		require.ErrorContains(t, err, "failed to load products")
		_, err = broken.ProductCategory().Translations(ctx, c)
		require.ErrorContains(t, err, "failed to load category translations")
		_, err = broken.ProductChoiceGroup().Choices(ctx, g)
		require.ErrorContains(t, err, "failed to load product choices")
	})

	t.Run("a product or category that has none of a thing resolves to null", func(t *testing.T) {
		ctx := env.ctxFor("", false, "en")
		cat, err := r.Product().Category(ctx, p)
		require.NoError(t, err)
		assert.Nil(t, cat)
		tr, err := r.Product().Translations(ctx, p)
		require.NoError(t, err)
		assert.Nil(t, tr)
		ctr, err := r.ProductCategory().Translations(ctx, c)
		require.NoError(t, err)
		assert.Nil(t, ctr)
		products, err := r.ProductCategory().Products(ctx, c)
		require.NoError(t, err)
		assert.Empty(t, products)
	})

	t.Run("a customer sees a product's translations as the loader delivers them", func(t *testing.T) {
		_, token := testhelpers.SeedCustomer(t, env.DB.DB, "reader")
		resp := gqlAs(t, env.TestContext, token, "en", `query ($id: ID!) { product(id: $id) { translations { language name description } } }`, map[string]any{"id": env.Fixtures.GreenTea.ID.String()})
		require.Empty(t, resp.Errors, "%+v", resp.Errors)
		assert.Contains(t, string(resp.Data), "Traditional Japanese green tea")
	})
}
