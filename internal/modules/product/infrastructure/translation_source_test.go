package infrastructure

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/testhelpers"
	"tsb-service/internal/modules/product/domain"
)

// sources reads the source column of one translation table, keyed by language.
func (e *env) sources(t *testing.T, table, ownerColumn, languageColumn string, owner uuid.UUID) map[string]string {
	t.Helper()
	rows, err := e.tdb.DB.QueryxContext(t.Context(),
		`SELECT `+languageColumn+`, source FROM `+table+` WHERE `+ownerColumn+` = $1`, owner)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var lang, source string
		require.NoError(t, rows.Scan(&lang, &source))
		out[lang] = source
	}
	require.NoError(t, rows.Err())
	return out
}

// The dashboard sends every language on every save. Only a translation whose text really changed
// becomes a manual override; the ones the HubRise import wrote keep their source, so the import
// can still refresh them.
func TestDashboardSavesOnlyOverrideTheTranslationsTheyChange(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	cat := testhelpers.SeedCategory(t, e.tdb.DB, 1, map[string]string{"fr": "Sushi"})

	t.Run("product translations", func(t *testing.T) {
		p := e.newProduct(t, cat, nil)
		assert.Equal(t, map[string]string{"fr": "manual", "en": "manual", "nl": "manual"},
			e.sources(t, "product_translations", "product_id", "language", p.ID), "a dashboard created row is manual")

		// The import wrote fr and en.
		_, err := e.tdb.DB.ExecContext(ctx, `UPDATE product_translations SET source = 'hubrise' WHERE product_id = $1 AND language IN ('fr', 'en')`, p.ID)
		require.NoError(t, err)

		// A price edit: the dashboard resends all three languages unchanged.
		got, err := e.repo.FindByID(ctx, p.ID)
		require.NoError(t, err)
		got.Price = dec("13.00")
		require.NoError(t, e.repo.Update(ctx, got))
		assert.Equal(t, map[string]string{"fr": "hubrise", "en": "hubrise", "nl": "manual"},
			e.sources(t, "product_translations", "product_id", "language", p.ID), "unchanged text keeps its source")

		// Renaming the English name overrides it; the description alone counts as a change too.
		got, err = e.repo.FindByID(ctx, p.ID)
		require.NoError(t, err)
		for i := range got.Translations {
			switch got.Translations[i].Language {
			case "en":
				got.Translations[i].Name = "Salmon deluxe"
			case "fr":
				d := "autre description"
				got.Translations[i].Description = &d
			}
		}
		require.NoError(t, e.repo.Update(ctx, got))
		assert.Equal(t, map[string]string{"fr": "manual", "en": "manual", "nl": "manual"},
			e.sources(t, "product_translations", "product_id", "language", p.ID))
	})

	t.Run("an empty description resent for a NULL one is not a change", func(t *testing.T) {
		p := e.newProduct(t, cat, nil)
		// The import stores an empty description as NULL.
		_, err := e.tdb.DB.ExecContext(ctx, `UPDATE product_translations SET source = 'hubrise', description = NULL WHERE product_id = $1`, p.ID)
		require.NoError(t, err)

		got, err := e.repo.FindByID(ctx, p.ID)
		require.NoError(t, err)
		empty := ""
		for i := range got.Translations {
			got.Translations[i].Description = &empty
		}
		require.NoError(t, e.repo.Update(ctx, got))
		assert.Equal(t, map[string]string{"fr": "hubrise", "en": "hubrise", "nl": "hubrise"},
			e.sources(t, "product_translations", "product_id", "language", p.ID))
	})

	t.Run("a language added in the dashboard is manual", func(t *testing.T) {
		p := e.newProduct(t, cat, func(p *domain.Product) {
			p.Translations = []domain.Translation{tr("fr", "Thon "+uuid.NewString()[:6])}
		})
		got, err := e.repo.FindByID(ctx, p.ID)
		require.NoError(t, err)
		got.Translations = append(got.Translations, tr("zh", "金枪鱼"))
		require.NoError(t, e.repo.Update(ctx, got))
		assert.Equal(t, "manual", e.sources(t, "product_translations", "product_id", "language", p.ID)["zh"])
	})

	t.Run("choice group and choice translations", func(t *testing.T) {
		productID := testhelpers.SeedProduct(t, e.tdb.DB, testhelpers.ProductSpec{CategoryID: cat, Price: "10"})
		group := &domain.ProductChoiceGroup{ID: uuid.New(), ProductID: productID, MinSelections: 0, MaxSelections: 2,
			Translations: []domain.ChoiceTranslation{{Locale: "fr", Name: "Sauce"}, {Locale: "en", Name: "Sauce"}}}
		require.NoError(t, e.repo.CreateChoiceGroup(ctx, group))
		choice := &domain.ProductChoice{ID: uuid.New(), ProductID: productID, ChoiceGroupID: group.ID, PriceModifier: dec("0.5"),
			Translations: []domain.ChoiceTranslation{{Locale: "fr", Name: "Soja"}, {Locale: "en", Name: "Soy"}}}
		require.NoError(t, e.repo.CreateChoice(ctx, choice))
		assert.Equal(t, map[string]string{"fr": "manual", "en": "manual"},
			e.sources(t, "product_choice_group_translations", "product_choice_group_id", "locale", group.ID))

		_, err := e.tdb.DB.ExecContext(ctx, `UPDATE product_choice_group_translations SET source = 'hubrise' WHERE product_choice_group_id = $1`, group.ID)
		require.NoError(t, err)
		_, err = e.tdb.DB.ExecContext(ctx, `UPDATE product_choice_translations SET source = 'hubrise' WHERE product_choice_id = $1`, choice.ID)
		require.NoError(t, err)

		// Resave unchanged (a limit edit and a surcharge edit): the sources stay.
		group.MaxSelections = 3
		require.NoError(t, e.repo.UpdateChoiceGroup(ctx, group))
		choice.PriceModifier = dec("0.8")
		require.NoError(t, e.repo.UpdateChoice(ctx, choice))
		assert.Equal(t, map[string]string{"fr": "hubrise", "en": "hubrise"},
			e.sources(t, "product_choice_group_translations", "product_choice_group_id", "locale", group.ID))
		assert.Equal(t, map[string]string{"fr": "hubrise", "en": "hubrise"},
			e.sources(t, "product_choice_translations", "product_choice_id", "locale", choice.ID))

		// Renaming one language overrides that language only.
		group.Translations[1].Name = "Sauces"
		require.NoError(t, e.repo.UpdateChoiceGroup(ctx, group))
		choice.Translations[0].Name = "Soja salé"
		require.NoError(t, e.repo.UpdateChoice(ctx, choice))
		assert.Equal(t, map[string]string{"fr": "hubrise", "en": "manual"},
			e.sources(t, "product_choice_group_translations", "product_choice_group_id", "locale", group.ID))
		assert.Equal(t, map[string]string{"fr": "manual", "en": "hubrise"},
			e.sources(t, "product_choice_translations", "product_choice_id", "locale", choice.ID))
	})
}

// is_available is not a dashboard field: reads report it, writes leave it alone.
func TestChoiceAvailabilityIsReadButNeverWrittenByTheDashboard(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	cat := testhelpers.SeedCategory(t, e.tdb.DB, 1, map[string]string{"fr": "Sushi"})
	productID := testhelpers.SeedProduct(t, e.tdb.DB, testhelpers.ProductSpec{CategoryID: cat, Price: "10"})
	group := &domain.ProductChoiceGroup{ID: uuid.New(), ProductID: productID, MaxSelections: 1}
	require.NoError(t, e.repo.CreateChoiceGroup(ctx, group))
	choice := &domain.ProductChoice{ID: uuid.New(), ProductID: productID, ChoiceGroupID: group.ID, PriceModifier: dec("0")}
	require.NoError(t, e.repo.CreateChoice(ctx, choice))

	got, err := e.repo.FindChoiceByID(ctx, choice.ID)
	require.NoError(t, err)
	assert.True(t, got.IsAvailable, "a new choice is available")

	_, err = e.tdb.DB.ExecContext(ctx, `UPDATE product_choices SET is_available = false WHERE id = $1`, choice.ID)
	require.NoError(t, err)

	got, err = e.repo.FindChoiceByID(ctx, choice.ID)
	require.NoError(t, err)
	assert.False(t, got.IsAvailable)
	byProduct, err := e.repo.FindChoicesByProductID(ctx, productID)
	require.NoError(t, err)
	require.Len(t, byProduct, 1)
	assert.False(t, byProduct[0].IsAvailable)
	batch, err := e.repo.BatchGetChoicesByProductIDs(ctx, []string{productID.String()})
	require.NoError(t, err)
	assert.False(t, batch[productID.String()][0].IsAvailable)
	byIDs, err := e.repo.BatchGetChoicesByIDs(ctx, []string{choice.ID.String()})
	require.NoError(t, err)
	assert.False(t, byIDs[choice.ID.String()][0].IsAvailable)

	// A dashboard edit of the surcharge (the domain value is available) must not flip it back.
	choice.PriceModifier = dec("1")
	require.NoError(t, e.repo.UpdateChoice(ctx, choice))
	got, err = e.repo.FindChoiceByID(ctx, choice.ID)
	require.NoError(t, err)
	assert.False(t, got.IsAvailable)
	assert.True(t, got.PriceModifier.Equal(dec("1")))
}
