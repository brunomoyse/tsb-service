package graphql_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/apperr"
	"tsb-service/internal/api/graphql/model"
	"tsb-service/internal/api/graphql/resolver"
	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/internal/modules/hubrise/infrastructure"
	hubtest "tsb-service/internal/modules/hubrise/testhelpers"
)

// managedEnv is the menu admin of a shop whose catalog is managed by HubRise: the fixtures' salmon
// sushi, one choice group and one choice of it are linked.
type managedEnv struct {
	*covEnv
	conns  domain.ConnectionRepository
	group  *model.ProductChoiceGroup
	choice *model.ProductChoice
}

func newManagedEnv(t *testing.T) *managedEnv {
	t.Helper()
	env := setupCovEnv(t, covOptions{})
	hubtest.SeedConnection(t, env.DB.DB, hubtest.WithFeatures(true, true, true, true, true))
	conns := infrastructure.NewConnectionRepository(env.DB.DB)
	links := infrastructure.NewCatalogLinkRepository(env.DB.DB)
	env.Resolver.Hubrise = resolver.CatalogGuardHooks{Guard: &application.Guard{Connections: conns, Links: links}}

	ctx := env.ctxFor(env.Fixtures.AdminUser.ID.String(), true, "en")
	r := env.Resolver
	product := env.Fixtures.SalmonSushi.ID
	// Seeding bypasses the guard: it is not linked yet.
	group, err := r.Mutation().CreateProductChoiceGroup(ctx, model.CreateProductChoiceGroupInput{
		ProductID: product, MinSelections: 1, MaxSelections: 2,
		Translations: []*model.ChoiceTranslationInput{{Locale: "fr", Name: "Sauce"}, {Locale: "nl", Name: "Saus"}}})
	require.NoError(t, err)
	choice, err := r.Mutation().CreateProductChoice(ctx, model.CreateProductChoiceInput{
		ChoiceGroupID: &group.ID, PriceModifier: "0.5", Translations: []*model.ChoiceTranslationInput{{Locale: "fr", Name: "Soja"}}})
	require.NoError(t, err)

	svc := &application.CatalogService{Connections: conns, Links: links}
	require.NoError(t, svc.LinkProduct(ctx, product, new("SKU")))
	for _, l := range []struct {
		t  domain.EntityType
		id uuid.UUID
		k  string
	}{{domain.EntityChoiceGroup, group.ID, "SKU|S"}, {domain.EntityChoice, choice.ID, "SKU|S|X"}} {
		link := &domain.CatalogLink{EntityType: l.t, HubriseKey: l.k, HubriseRef: l.k, Origin: domain.OriginImport}
		link.SetLocalID(l.id)
		require.NoError(t, links.Upsert(ctx, link))
	}
	return &managedEnv{covEnv: env, conns: conns, group: group, choice: choice}
}

func managed(t *testing.T, err error, fields ...string) {
	t.Helper()
	appErr, ok := apperr.From(err)
	require.True(t, ok, "not a typed error: %v", err)
	assert.Equal(t, apperr.CodeHubriseManaged, appErr.Code)
	assert.Equal(t, fields, appErr.Extensions()["fields"])
}

func TestHubriseOwnedProductFieldsAreLockedWhileTheCatalogIsManaged(t *testing.T) {
	env := newManagedEnv(t)
	r := env.Resolver
	ctx := env.ctxFor(env.Fixtures.AdminUser.ID.String(), true, "en")
	product := env.Fixtures.SalmonSushi.ID

	_, err := r.Mutation().UpdateProduct(ctx, product, model.UpdateProductInput{Price: new("99")})
	managed(t, err, "price")
	_, err = r.Mutation().UpdateProduct(ctx, product, model.UpdateProductInput{CategoryID: &env.Fixtures.DessertsCategory.ID, VatCategory: new("beverage")})
	managed(t, err, "category", "vatCategory")

	// What the shop owns stays editable: flags, availability, texts, a price sent unchanged.
	got, err := r.Mutation().UpdateProduct(ctx, product, model.UpdateProductInput{IsHalal: new(true), IsAvailable: new(false),
		Translations: []*model.TranslationInput{tr("en", "Salmon, my way")}})
	require.NoError(t, err)
	assert.True(t, got.IsHalal)
	assert.Equal(t, "Salmon, my way", got.Name)
	_, err = r.Mutation().UpdateProduct(ctx, product, model.UpdateProductInput{Price: new(got.Price)})
	assert.NoError(t, err)

	// Products that are not linked are not touched by the rule.
	_, err = r.Mutation().UpdateProduct(ctx, env.Fixtures.TunaSushi.ID, model.UpdateProductInput{Price: new("99")})
	assert.NoError(t, err)
}

func TestHubriseOwnedChoiceGroupsAndChoicesAreLocked(t *testing.T) {
	env := newManagedEnv(t)
	r := env.Resolver
	ctx := env.ctxFor(env.Fixtures.AdminUser.ID.String(), true, "en")
	product := env.Fixtures.SalmonSushi.ID

	_, err := r.Mutation().UpdateProductChoiceGroup(ctx, env.group.ID, model.UpdateProductChoiceGroupInput{MaxSelections: new(3), SortOrder: new(4)})
	managed(t, err, "selections", "sortOrder")
	_, err = r.Mutation().UpdateProductChoiceGroup(ctx, env.group.ID, model.UpdateProductChoiceGroupInput{
		Translations: []*model.ChoiceTranslationInput{{Locale: "fr", Name: "Sauces"}}})
	managed(t, err, "name")
	_, err = r.Mutation().UpdateProductChoiceGroup(ctx, env.group.ID, model.UpdateProductChoiceGroupInput{
		Translations: []*model.ChoiceTranslationInput{{Locale: "nl", Name: "Sauzen"}}})
	assert.NoError(t, err, "the other languages are the shop's")
	_, err = r.Mutation().CreateProductChoiceGroup(ctx, model.CreateProductChoiceGroupInput{ProductID: product, MinSelections: 0, MaxSelections: 1})
	managed(t, err, "choiceGroups")
	_, err = r.Mutation().DeleteProductChoiceGroup(ctx, env.group.ID)
	managed(t, err, "choiceGroups")

	_, err = r.Mutation().UpdateProductChoice(ctx, env.choice.ID, model.UpdateProductChoiceInput{PriceModifier: new("2"), SortOrder: new(3)})
	managed(t, err, "priceModifier", "sortOrder")
	_, err = r.Mutation().CreateProductChoice(ctx, model.CreateProductChoiceInput{ChoiceGroupID: &env.group.ID, PriceModifier: "1"})
	managed(t, err, "choices")
	_, err = r.Mutation().DeleteProductChoice(ctx, env.choice.ID)
	managed(t, err, "choices")
	// A choice added by product to a product that has groups lands in a linked product too.
	_, err = r.Mutation().UpdateProductChoice(ctx, env.choice.ID, model.UpdateProductChoiceInput{
		Translations: []*model.ChoiceTranslationInput{{Locale: "zh", Name: "酱油"}}})
	assert.NoError(t, err)
}

func TestTheSameEditsSucceedOnceTheCatalogIsNoLongerManaged(t *testing.T) {
	env := newManagedEnv(t)
	r := env.Resolver
	ctx := env.ctxFor(env.Fixtures.AdminUser.ID.String(), true, "en")
	product := env.Fixtures.SalmonSushi.ID

	_, err := r.Mutation().UpdateProduct(ctx, product, model.UpdateProductInput{Price: new("99")})
	require.Error(t, err)

	off := false
	for name, patch := range map[string]domain.SettingsPatch{
		"catalog sync off": {SyncCatalog: &off},
		"disabled":         {Enabled: &off},
	} {
		_, uerr := env.conns.UpdateSettings(ctx, domain.SettingsPatch{SyncCatalog: new(true), Enabled: new(true)})
		require.NoError(t, uerr)
		_, uerr = env.conns.UpdateSettings(ctx, patch)
		require.NoError(t, uerr)
		_, err = r.Mutation().UpdateProduct(ctx, product, model.UpdateProductInput{Price: new("11")})
		assert.NoError(t, err, name)
		_, err = r.Mutation().UpdateProductChoice(ctx, env.choice.ID, model.UpdateProductChoiceInput{PriceModifier: new("1.5")})
		assert.NoError(t, err, name)
		_, err = r.Mutation().UpdateProductChoiceGroup(ctx, env.group.ID, model.UpdateProductChoiceGroupInput{MaxSelections: new(3)})
		assert.NoError(t, err, name)
	}
}

func TestWithoutHubRiseTheMenuAdminIsUnchanged(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	require.Nil(t, env.Resolver.Hubrise, "standalone: no hooks")
	ctx := env.ctxFor(env.Fixtures.AdminUser.ID.String(), true, "en")
	_, err := env.Resolver.Mutation().UpdateProduct(ctx, env.Fixtures.SalmonSushi.ID, model.UpdateProductInput{Price: new("99")})
	assert.NoError(t, err)

	// Hooks without a connection row behave the same.
	env.Resolver.Hubrise = resolver.CatalogGuardHooks{Guard: &application.Guard{
		Connections: infrastructure.NewConnectionRepository(env.DB.DB), Links: infrastructure.NewCatalogLinkRepository(env.DB.DB)}}
	_, err = env.Resolver.Mutation().UpdateProduct(ctx, env.Fixtures.SalmonSushi.ID, model.UpdateProductInput{Price: new("98")})
	assert.NoError(t, err)
}

func TestHubriseErrorsBecomeTypedGraphQLErrors(t *testing.T) {
	assert.NoError(t, resolver.HubriseError(nil))
	plain := errors.New("plain")
	assert.Same(t, plain, resolver.HubriseError(plain))

	cases := []struct {
		err  error
		code apperr.Code
		ext  map[string]any
	}{
		{&domain.ManagedError{Fields: []string{"price"}}, apperr.CodeHubriseManaged, map[string]any{"fields": []string{"price"}}},
		{&domain.RetireConfirmError{Retiring: 6, Linked: 8}, apperr.CodeHubriseRetireConfirmRequired, map[string]any{"retiring": 6, "linked": 8}},
		{domain.ErrRelinkRequired, apperr.CodeHubriseRelinkRequired, nil},
		{domain.ErrPreviewRequired, apperr.CodeHubrisePreviewRequired, nil},
	}
	for _, c := range cases {
		got, ok := apperr.From(resolver.HubriseError(c.err))
		require.True(t, ok, c.err)
		assert.Equal(t, c.code, got.Code)
		for k, v := range c.ext {
			assert.Equal(t, v, got.Extensions()[k])
		}
	}
}

func TestProductsChangedTellsTheDashboards(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	ctx := env.ctxFor(env.Fixtures.AdminUser.ID.String(), true, "en")
	updates, err := env.Resolver.Subscription().ProductUpdated(ctx)
	require.NoError(t, err)

	// An unknown id is skipped, the known one is published.
	env.Resolver.ProductsChanged(ctx, []uuid.UUID{uuid.New(), env.Fixtures.SalmonSushi.ID})

	assert.Equal(t, env.Fixtures.SalmonSushi.ID, recvProduct(t, updates).ID)
}
