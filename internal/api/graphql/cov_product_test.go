package graphql_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	gqlgraphql "github.com/99designs/gqlgen/graphql"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/apperr"
	"tsb-service/internal/api/graphql/model"
	"tsb-service/internal/api/graphql/testhelpers"
)

// The menu administration: products, choice groups and choices, with their validation, and the
// fields of Product / ProductCategory / ProductChoiceGroup that resolve through loaders.

// fileService stands in for the image service the product mutations forward uploads to.
type fileService struct {
	mu       sync.Mutex
	requests []fileUpload
	status   int
	reply    string
}

type fileUpload struct {
	Path, Slug, Filename, Content string
}

func newFileService(t *testing.T) *fileService {
	t.Helper()
	fs := &fileService{status: http.StatusOK, reply: `{"status":"ok"}`}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseMultipartForm(1<<20))
		up := fileUpload{Path: r.URL.Path, Slug: r.FormValue("product_slug")}
		if f, h, err := r.FormFile("image"); err == nil {
			b, _ := io.ReadAll(f)
			up.Filename, up.Content = h.Filename, string(b)
		}
		fs.mu.Lock()
		fs.requests = append(fs.requests, up)
		status, reply := fs.status, fs.reply
		fs.mu.Unlock()
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("FILE_SERVICE_URL", srv.URL)
	return fs
}

func (f *fileService) uploads() []fileUpload {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fileUpload(nil), f.requests...)
}

func upload(name, content string) *gqlgraphql.Upload {
	return &gqlgraphql.Upload{File: strings.NewReader(content), Filename: name, Size: int64(len(content))}
}

func tr(lang, name string) *model.TranslationInput {
	return &model.TranslationInput{Language: lang, Name: name}
}

func TestProductAdministration(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	files := newFileService(t)
	r := env.Resolver
	ctx := env.ctxFor(env.Fixtures.AdminUser.ID.String(), true, "en")
	category := env.Fixtures.SushiCategory.ID

	var seq int
	baseInput := func() model.CreateProductInput {
		seq++
		name := fmt.Sprintf("Dragon roll %d", seq)
		return model.CreateProductInput{
			CategoryID: category, IsAvailable: true, IsVisible: true, IsDiscountable: true, Price: "12,50", VatCategory: "food",
			Translations: []*model.TranslationInput{tr("en", name), tr("fr", "Rouleau "+name), tr("zh", "龙卷"+name)},
		}
	}

	t.Run("createProduct stores the product, accepting a decimal comma", func(t *testing.T) {
		in := baseInput()
		code, pieces := "DRG", 8
		in.Code, in.PieceCount, in.IsHalal, in.IsSpicy, in.IsVegetarian, in.IsLunchOnly = &code, &pieces, true, true, true, true
		got, err := r.Mutation().CreateProduct(ctx, in)
		require.NoError(t, err)
		assert.Equal(t, "Dragon roll 1", got.Name)
		assert.Equal(t, "12.5", got.Price)
		assert.Equal(t, "food", got.VatCategory)
		assert.True(t, got.IsHalal && got.IsSpicy && got.IsVegetarian && got.IsLunchOnly && got.IsDiscountable)
		require.NotNil(t, got.PieceCount)
		assert.Equal(t, 8, *got.PieceCount)
		assert.Empty(t, files.uploads(), "no image, no upload")

		fr, err := r.Query().Product(env.ctxFor("", false, "fr"), got.ID)
		require.NoError(t, err)
		assert.Equal(t, "Rouleau Dragon roll 1", fr.Name, "the product reads back in the caller's language")
	})

	t.Run("createProduct forwards the image under the product id, with or without background removal", func(t *testing.T) {
		in := baseInput()
		in.Image = upload("dragon.png", "PNGDATA")
		got, err := r.Mutation().CreateProduct(ctx, in)
		require.NoError(t, err)
		require.Len(t, files.uploads(), 1)
		assert.Equal(t, fileUpload{Path: "/upload", Slug: got.ID.String(), Filename: "dragon.png", Content: "PNGDATA"}, files.uploads()[0])

		in = baseInput()
		in.Image, in.RemoveBackground = upload("cut.png", "CUT"), new(true)
		got, err = r.Mutation().CreateProduct(ctx, in)
		require.NoError(t, err)
		require.Len(t, files.uploads(), 2)
		assert.Equal(t, "/images/upload/processed", files.uploads()[1].Path)
		assert.Equal(t, got.ID.String(), files.uploads()[1].Slug)
	})

	t.Run("a failing image upload is logged, the product is kept", func(t *testing.T) {
		files.mu.Lock()
		files.status = http.StatusInternalServerError
		files.mu.Unlock()
		t.Cleanup(func() { files.mu.Lock(); files.status = http.StatusOK; files.mu.Unlock() })
		in := baseInput()
		in.Image = upload("x.png", "X")
		got, err := r.Mutation().CreateProduct(ctx, in)
		require.NoError(t, err)
		assert.NotEqual(t, uuid.Nil, got.ID)
	})

	t.Run("createProduct refuses what cannot be stored", func(t *testing.T) {
		in := baseInput()
		in.Translations = in.Translations[:2]
		_, err := r.Mutation().CreateProduct(ctx, in)
		require.ErrorContains(t, err, "visible product must have at least 3 translations")

		in = baseInput()
		in.Price = "twelve"
		_, err = r.Mutation().CreateProduct(ctx, in)
		require.ErrorContains(t, err, "invalid price format")

		in = baseInput()
		in.VatCategory = "luxury"
		_, err = r.Mutation().CreateProduct(ctx, in)
		require.ErrorContains(t, err, `invalid vatCategory: "luxury"`)

		in = baseInput()
		in.CategoryID = uuid.New()
		_, err = r.Mutation().CreateProduct(ctx, in)
		require.ErrorContains(t, err, "failed to create product")
	})

	t.Run("a second product with the same name in the category is a readable user error", func(t *testing.T) {
		first := baseInput()
		_, err := r.Mutation().CreateProduct(ctx, first)
		require.NoError(t, err)

		same := baseInput()
		same.Translations = first.Translations // same French name, same category: same slug
		_, err = r.Mutation().CreateProduct(ctx, same)

		appErr, ok := apperr.From(err)
		require.True(t, ok, "%v", err)
		assert.Equal(t, apperr.CodeUserError, appErr.Code)
		assert.Equal(t, "a product with this name already exists in this category, choose another name", err.Error())
		assert.NotContains(t, err.Error(), "23505", "no driver text")

		// Through HTTP the message reaches the dashboard as it is, not as "Internal server error".
		resp := gqlAs(t, env.TestContext, adminToken(t, env.TestContext), "en",
			`mutation ($i: CreateProductInput!) { createProduct(input: $i) { id } }`,
			map[string]any{"i": map[string]any{
				"categoryId": category.String(), "isAvailable": true, "isVisible": true, "isDiscountable": true, "isHalal": false, "isLunchOnly": false,
				"isSpicy": false, "isVegetarian": false, "price": "5", "vatCategory": "food",
				"translations": []map[string]string{
					{"language": "en", "name": "x"}, {"language": "fr", "name": first.Translations[1].Name}, {"language": "zh", "name": "y"},
				},
			}})
		require.Len(t, resp.Errors, 1)
		assert.Equal(t, "USER_ERROR", resp.Errors[0].Extensions["code"])
		assert.Contains(t, resp.Errors[0].Message, "already exists in this category")
	})

	created, err := r.Mutation().CreateProduct(ctx, baseInput())
	require.NoError(t, err)

	t.Run("updateProduct patches only the fields that are given and tells the dashboards", func(t *testing.T) {
		updates, err := r.Subscription().ProductUpdated(ctx)
		require.NoError(t, err)

		code, pieces := "NEW-CODE", 12
		got, err := r.Mutation().UpdateProduct(ctx, created.ID, model.UpdateProductInput{
			CategoryID: &env.Fixtures.DessertsCategory.ID, Code: &code, PieceCount: &pieces,
			Price: new("9,90"), IsVisible: new(false), IsAvailable: new(false), IsHalal: new(true), IsVegetarian: new(true),
			IsSpicy: new(true), IsDiscountable: new(false), IsLunchOnly: new(true), VatCategory: new("beverage"),
			Translations: []*model.TranslationInput{tr("en", "Renamed"), tr("fr", "Renommé"), tr("zh", "改名")},
		})
		require.NoError(t, err)
		assert.Equal(t, "Renamed", got.Name)
		assert.Equal(t, "9.9", got.Price)
		assert.Equal(t, "beverage", got.VatCategory)
		assert.False(t, got.IsVisible || got.IsAvailable || got.IsDiscountable)
		assert.True(t, got.IsHalal && got.IsVegetarian && got.IsSpicy && got.IsLunchOnly)
		assert.Equal(t, "NEW-CODE", *got.Code)
		assert.Equal(t, 12, *got.PieceCount)
		assert.Equal(t, created.ID, recvProduct(t, updates).ID)

		again, err := r.Mutation().UpdateProduct(ctx, created.ID, model.UpdateProductInput{})
		require.NoError(t, err)
		assert.Equal(t, "Renamed", again.Name, "an empty patch changes nothing")
		assert.Equal(t, "9.9", again.Price)
	})

	t.Run("updateProduct replaces the image under the immutable product id", func(t *testing.T) {
		before := len(files.uploads())
		_, err := r.Mutation().UpdateProduct(ctx, created.ID, model.UpdateProductInput{Image: upload("v2.png", "V2"), RemoveBackground: new(true)})
		require.NoError(t, err)
		ups := files.uploads()
		require.Len(t, ups, before+1)
		assert.Equal(t, created.ID.String(), ups[before].Slug)
		assert.Equal(t, "/images/upload/processed", ups[before].Path)

		files.mu.Lock()
		files.reply = `{"status":"error"}`
		files.mu.Unlock()
		t.Cleanup(func() { files.mu.Lock(); files.reply = `{"status":"ok"}`; files.mu.Unlock() })
		_, err = r.Mutation().UpdateProduct(ctx, created.ID, model.UpdateProductInput{Image: upload("v3.png", "V3"), RemoveBackground: new(true)})
		require.NoError(t, err, "a refused image is logged, the update stands")
	})

	t.Run("updateProduct refuses what cannot be stored", func(t *testing.T) {
		_, err := r.Mutation().UpdateProduct(ctx, uuid.New(), model.UpdateProductInput{})
		require.ErrorContains(t, err, "failed to fetch product")
		_, err = r.Mutation().UpdateProduct(ctx, created.ID, model.UpdateProductInput{Price: new("free")})
		require.ErrorContains(t, err, "invalid price format")
		_, err = r.Mutation().UpdateProduct(ctx, created.ID, model.UpdateProductInput{VatCategory: new("luxury")})
		require.ErrorContains(t, err, `invalid vatCategory: "luxury"`)
		unknown := uuid.New()
		_, err = r.Mutation().UpdateProduct(ctx, created.ID, model.UpdateProductInput{CategoryID: &unknown})
		require.ErrorContains(t, err, "failed to update product")
	})

	t.Run("renaming a product to the name of another one in its category is a readable user error", func(t *testing.T) {
		other, err := r.Mutation().CreateProduct(ctx, baseInput())
		require.NoError(t, err)
		taken := baseInput()
		_, err = r.Mutation().CreateProduct(ctx, taken)
		require.NoError(t, err)

		_, err = r.Mutation().UpdateProduct(ctx, other.ID, model.UpdateProductInput{Translations: taken.Translations})

		appErr, ok := apperr.From(err)
		require.True(t, ok, "%v", err)
		assert.Equal(t, apperr.CodeUserError, appErr.Code)
	})

	t.Run("only an admin may change the menu", func(t *testing.T) {
		_, token := testhelpers.SeedCustomer(t, env.DB.DB, "nosy")
		resp := gqlAs(t, env.TestContext, token, "en", `mutation ($id: ID!) { updateProduct(id: $id, input: {isVisible: true}) { id } }`, map[string]any{"id": created.ID.String()})
		require.Len(t, resp.Errors, 1)
		assert.Equal(t, "FORBIDDEN", resp.Errors[0].Extensions["code"])
		assert.False(t, loadVisible(t, env, created.ID), "the refused call changed nothing: the product stays as the previous subtest left it")
	})
}

func loadVisible(t *testing.T, env *covEnv, id uuid.UUID) bool {
	t.Helper()
	var v bool
	require.NoError(t, env.DB.DB.GetContext(t.Context(), &v, `SELECT is_visible FROM products WHERE id = $1`, id))
	return v
}

func recvProduct(t *testing.T, ch <-chan *model.Product) *model.Product {
	t.Helper()
	select {
	case p := <-ch:
		require.NotNil(t, p)
		return p
	case <-time.After(10 * time.Second):
		require.FailNow(t, "no product was published")
		return nil
	}
}

func userErr(t *testing.T, err error, contains string) {
	t.Helper()
	appErr, ok := apperr.From(err)
	require.True(t, ok, "not a typed error: %v", err)
	assert.Equal(t, apperr.CodeUserError, appErr.Code)
	assert.Contains(t, err.Error(), contains)
}

func TestProductChoiceAdministration(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	r := env.Resolver
	ctx := env.ctxFor(env.Fixtures.AdminUser.ID.String(), true, "en")
	product := env.Fixtures.SalmonSushi.ID
	names := []*model.ChoiceTranslationInput{{Locale: "en", Name: "Size"}, {Locale: "fr", Name: "Taille"}}

	t.Run("a choice group needs a sane selection range", func(t *testing.T) {
		for _, c := range []struct {
			min, max int
			msg      string
		}{
			{-1, 1, "min selections must be >= 0"},
			{0, 0, "max selections must be >= 1"},
			{3, 2, "min selections cannot be greater than max selections"},
		} {
			_, err := r.Mutation().CreateProductChoiceGroup(ctx, model.CreateProductChoiceGroupInput{ProductID: product, MinSelections: c.min, MaxSelections: c.max, Translations: names})
			userErr(t, err, c.msg)
		}
		_, err := r.Mutation().CreateProductChoiceGroup(ctx, model.CreateProductChoiceGroupInput{ProductID: uuid.New(), MinSelections: 1, MaxSelections: 1, Translations: names})
		require.ErrorContains(t, err, "failed to create product choice group")
	})

	group, err := r.Mutation().CreateProductChoiceGroup(ctx, model.CreateProductChoiceGroupInput{
		ProductID: product, MinSelections: 1, MaxSelections: 2, SortOrder: 3, Translations: names})
	require.NoError(t, err)
	assert.Equal(t, "Size", group.Name)
	assert.Equal(t, 3, group.SortOrder)
	require.Len(t, group.Translations, 2)

	t.Run("updating a choice group keeps what is not given and validates the final range", func(t *testing.T) {
		got, err := r.Mutation().UpdateProductChoiceGroup(ctx, group.ID, model.UpdateProductChoiceGroupInput{
			MaxSelections: new(4), SortOrder: new(9), Translations: []*model.ChoiceTranslationInput{{Locale: "en", Name: "Portion"}}})
		require.NoError(t, err)
		assert.Equal(t, 1, got.MinSelections)
		assert.Equal(t, 4, got.MaxSelections)
		assert.Equal(t, 9, got.SortOrder)
		assert.Equal(t, "Portion", got.Name)

		_, err = r.Mutation().UpdateProductChoiceGroup(ctx, group.ID, model.UpdateProductChoiceGroupInput{MinSelections: new(-1)})
		userErr(t, err, "min selections must be >= 0")
		_, err = r.Mutation().UpdateProductChoiceGroup(ctx, group.ID, model.UpdateProductChoiceGroupInput{MaxSelections: new(0), MinSelections: new(0)})
		userErr(t, err, "max selections must be >= 1")
		_, err = r.Mutation().UpdateProductChoiceGroup(ctx, group.ID, model.UpdateProductChoiceGroupInput{MinSelections: new(5)})
		userErr(t, err, "min selections cannot be greater than max selections")
		_, err = r.Mutation().UpdateProductChoiceGroup(ctx, uuid.New(), model.UpdateProductChoiceGroupInput{})
		require.ErrorContains(t, err, "failed to fetch product choice group")
	})

	t.Run("a choice can be added to a group, with its price modifier", func(t *testing.T) {
		got, err := r.Mutation().CreateProductChoice(ctx, model.CreateProductChoiceInput{
			ChoiceGroupID: &group.ID, PriceModifier: "1,50", SortOrder: 1, Translations: []*model.ChoiceTranslationInput{{Locale: "en", Name: "Large"}}})
		require.NoError(t, err)
		assert.Equal(t, "1.5", got.PriceModifier)
		assert.Equal(t, group.ID, got.ChoiceGroupID)
		assert.Equal(t, product, got.ProductID)
		assert.Equal(t, "Large", got.Name)

		_, err = r.Mutation().CreateProductChoice(ctx, model.CreateProductChoiceInput{ChoiceGroupID: new(uuid.New()), PriceModifier: "1"})
		require.ErrorContains(t, err, "failed to fetch choice group")
	})

	t.Run("a choice added to a product without groups creates the default group, later ones reuse it", func(t *testing.T) {
		bare := env.Fixtures.TunaSushi.ID
		first, err := r.Mutation().CreateProductChoice(ctx, model.CreateProductChoiceInput{
			ProductID: &bare, PriceModifier: "0", Translations: []*model.ChoiceTranslationInput{{Locale: "en", Name: "Plain"}}})
		require.NoError(t, err)
		groups, err := env.Resolver.ProductService.GetChoiceGroupsByProductID(t.Context(), bare)
		require.NoError(t, err)
		require.Len(t, groups, 1)
		assert.Equal(t, groups[0].ID, first.ChoiceGroupID)
		assert.Equal(t, 1, groups[0].MinSelections)
		assert.Equal(t, 1, groups[0].MaxSelections)
		assert.Equal(t, "Choice", groups[0].GetTranslationFor("en"))
		assert.Equal(t, "Choix", groups[0].GetTranslationFor("fr"))

		second, err := r.Mutation().CreateProductChoice(ctx, model.CreateProductChoiceInput{ProductID: &bare, PriceModifier: "0.5"})
		require.NoError(t, err)
		assert.Equal(t, first.ChoiceGroupID, second.ChoiceGroupID)

		unknown := uuid.New()
		_, err = r.Mutation().CreateProductChoice(ctx, model.CreateProductChoiceInput{ProductID: &unknown, PriceModifier: "0"})
		require.ErrorContains(t, err, "failed to create default choice group")
	})

	t.Run("a choice needs a group or a product, and a modifier of zero or more", func(t *testing.T) {
		_, err := r.Mutation().CreateProductChoice(ctx, model.CreateProductChoiceInput{PriceModifier: "0"})
		userErr(t, err, "either choiceGroupId or productId is required")
		_, err = r.Mutation().CreateProductChoice(ctx, model.CreateProductChoiceInput{ChoiceGroupID: &group.ID, PriceModifier: "-1"})
		userErr(t, err, "price modifier must be zero or positive")
		_, err = r.Mutation().CreateProductChoice(ctx, model.CreateProductChoiceInput{ChoiceGroupID: &group.ID, PriceModifier: "lots"})
		require.ErrorContains(t, err, "invalid price modifier format")
	})

	choice, err := r.Mutation().CreateProductChoice(ctx, model.CreateProductChoiceInput{
		ChoiceGroupID: &group.ID, PriceModifier: "2", Translations: []*model.ChoiceTranslationInput{{Locale: "en", Name: "Extra"}}})
	require.NoError(t, err)

	t.Run("updating a choice changes the given fields", func(t *testing.T) {
		got, err := r.Mutation().UpdateProductChoice(ctx, choice.ID, model.UpdateProductChoiceInput{
			PriceModifier: new("3,25"), SortOrder: new(7), Translations: []*model.ChoiceTranslationInput{{Locale: "en", Name: "Super extra"}, {Locale: "fr", Name: "Super supplément"}}})
		require.NoError(t, err)
		assert.Equal(t, "3.25", got.PriceModifier)
		assert.Equal(t, 7, got.SortOrder)
		assert.Equal(t, "Super extra", got.Name)
		require.Len(t, got.Translations, 2)

		_, err = r.Mutation().UpdateProductChoice(ctx, choice.ID, model.UpdateProductChoiceInput{PriceModifier: new("-1")})
		userErr(t, err, "price modifier must be zero or positive")
		_, err = r.Mutation().UpdateProductChoice(ctx, choice.ID, model.UpdateProductChoiceInput{PriceModifier: new("x")})
		require.ErrorContains(t, err, "invalid price modifier format")
		_, err = r.Mutation().UpdateProductChoice(ctx, uuid.New(), model.UpdateProductChoiceInput{})
		require.ErrorContains(t, err, "failed to fetch product choice")
	})

	t.Run("a group lists only its own choices", func(t *testing.T) {
		other, err := r.Mutation().CreateProductChoiceGroup(ctx, model.CreateProductChoiceGroupInput{ProductID: product, MinSelections: 0, MaxSelections: 1, Translations: names})
		require.NoError(t, err)
		_, err = r.Mutation().CreateProductChoice(ctx, model.CreateProductChoiceInput{ChoiceGroupID: &other.ID, PriceModifier: "0"})
		require.NoError(t, err)

		resp := gqlAs(t, env.TestContext, "", "en", `query ($id: ID!) { product(id: $id) { choices { id } choiceGroups { id choices { id } } } }`, map[string]any{"id": product.String()})
		require.Empty(t, resp.Errors, "%+v", resp.Errors)
		var data struct {
			Product struct {
				Choices      []struct{ ID string }
				ChoiceGroups []struct {
					ID      string
					Choices []struct{ ID string }
				}
			}
		}
		require.NoError(t, json.Unmarshal(resp.Data, &data))
		assert.Len(t, data.Product.Choices, 3, "every choice of the product")
		sizes := map[string]int{}
		for _, g := range data.Product.ChoiceGroups {
			sizes[g.ID] = len(g.Choices)
		}
		assert.Equal(t, 2, sizes[group.ID.String()])
		assert.Equal(t, 1, sizes[other.ID.String()])
	})

	t.Run("deleting a choice and a group removes them", func(t *testing.T) {
		ok, err := r.Mutation().DeleteProductChoice(ctx, choice.ID)
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Zero(t, countRows(t, env.TestContext, `SELECT count(*) FROM product_choices WHERE id = $1`, choice.ID))
		ok, err = r.Mutation().DeleteProductChoiceGroup(ctx, group.ID)
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Zero(t, countRows(t, env.TestContext, `SELECT count(*) FROM product_choice_groups WHERE id = $1`, group.ID))
	})

	t.Run("a failing store is reported by every choice mutation", func(t *testing.T) {
		broken := env.brokenResolver(t)
		id := uuid.New()
		_, err := broken.Mutation().UpdateProductChoiceGroup(ctx, id, model.UpdateProductChoiceGroupInput{})
		require.ErrorContains(t, err, "failed to fetch product choice group")
		_, err = broken.Mutation().DeleteProductChoiceGroup(ctx, id)
		require.ErrorContains(t, err, "failed to delete product choice group")
		_, err = broken.Mutation().DeleteProductChoice(ctx, id)
		require.ErrorContains(t, err, "failed to delete product choice")
		_, err = broken.Mutation().CreateProductChoice(ctx, model.CreateProductChoiceInput{ProductID: &id, PriceModifier: "0"})
		require.ErrorContains(t, err, "failed to fetch product choice groups")
		_, err = broken.Mutation().UpdateProductChoice(ctx, id, model.UpdateProductChoiceInput{})
		require.ErrorContains(t, err, "failed to fetch product choice")
	})
}
