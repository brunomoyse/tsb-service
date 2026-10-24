package application_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/modules/hubrise/domain"
)

// planOver plans the publish of a shop with one product over a HubRise catalog given as JSON, without the
// mock: the catalog may be one HubRise would never store.
func planOver(t *testing.T, base string, local *domain.LocalSnapshot, links ...domain.CatalogLink) *application.PublishPlan {
	t.Helper()
	if local == nil {
		// The planner refuses an empty menu: the shop owns one product.
		local = &domain.LocalSnapshot{
			Categories: []domain.LocalCategory{{ID: puid(1), Slug: "own", Order: 1, Names: ptexts("Own", "")}},
			Products: []domain.LocalProduct{{ID: puid(2), CategoryID: puid(1), Code: "OWN1", Price: pdec("1.00"), VatCategory: "food",
				Visible: true, Available: true, Texts: ptexts("Own product", "")}},
		}
	}
	p, err := application.PlanPublish(application.PublishInput{
		Local: local, Links: links, Settings: application.PublishSettings{Language: "fr"}, Base: json.RawMessage(base),
	})
	require.NoError(t, err)
	return p
}

func blockerKeys(p *application.PublishPlan) map[domain.WarningCode][]string {
	out := map[domain.WarningCode][]string{}
	for _, b := range p.Blockers {
		out[b.Code] = append(out[b.Code], b.Key)
	}
	return out
}

// The planner refuses to upload what HubRise would answer 422 for, and says which item is wrong. The
// faults come from the foreign part of the catalog (the shop's own items are valid by construction).
func TestTheFaultsOfAForeignCatalogBlockThePublish(t *testing.T) {
	const cat = `{"ref":"c","name":"C"}`
	const sku = `{"ref":"S1","price":"1.00 EUR"}`
	cases := []struct {
		name string
		base string
		code domain.WarningCode
		key  string
	}{
		{"duplicate category ref", `{"categories":[` + cat + `,` + cat + `]}`, domain.BlockDuplicateRef, "c"},
		{"unknown parent category", `{"categories":[{"ref":"c","name":"C","parent_ref":"ghost"}]}`, domain.BlockDanglingRef, "c"},
		{"unknown category", `{"categories":[` + cat + `],"products":[{"ref":"P","category_ref":"ghost","name":"P","skus":[` + sku + `]}]}`, domain.BlockDanglingRef, "P"},
		{"partial tax rate", `{"categories":[` + cat + `],"products":[{"ref":"P","category_ref":"c","name":"P","tax_rate":{"delivery":"6"},"skus":[` + sku + `]}]}`, domain.BlockTaxRatePartial, "P"},
		{"tax rate that is no percentage", `{"categories":[` + cat + `],"products":[{"ref":"P","category_ref":"c","name":"P","tax_rate":{"delivery":"six","collection":"6","eat_in":"6"},"skus":[` + sku + `]}]}`, domain.BlockTaxRatePartial, "P"},
		{"SKU without ref", `{"categories":[` + cat + `],"products":[{"ref":"P","category_ref":"c","name":"P","skus":[{"price":"1.00 EUR"}]}]}`, domain.BlockInvalidRef, "P"},
		{"two SKUs with one name", `{"categories":[` + cat + `],"products":[{"ref":"P","category_ref":"c","name":"P","skus":[{"ref":"A","name":"x","price":"1.00 EUR"},{"ref":"B","name":"x","price":"1.00 EUR"}]}]}`, domain.BlockSkuNameDuplicate, "B"},
		{"price missing", `{"categories":[` + cat + `],"products":[{"ref":"P","category_ref":"c","name":"P","skus":[{"ref":"A"}]}]}`, domain.BlockInvalidMoney, "A"},
		{"price that is no amount", `{"categories":[` + cat + `],"products":[{"ref":"P","category_ref":"c","name":"P","skus":[{"ref":"A","price":"cheap"}]}]}`, domain.BlockInvalidMoney, "A"},
		{"barcode of the wrong length", `{"categories":[` + cat + `],"products":[{"ref":"P","category_ref":"c","name":"P","skus":[{"ref":"A","price":"1.00 EUR","barcodes":["123"]}]}]}`, domain.BlockInvalidBarcode, "A"},
		{"barcode with letters", `{"categories":[` + cat + `],"products":[{"ref":"P","category_ref":"c","name":"P","skus":[{"ref":"A","price":"1.00 EUR","barcodes":["1234567a"]}]}]}`, domain.BlockInvalidBarcode, "A"},
		{"unknown option list", `{"categories":[` + cat + `],"products":[{"ref":"P","category_ref":"c","name":"P","skus":[{"ref":"A","price":"1.00 EUR","option_list_refs":["ghost"]}]}]}`, domain.BlockDanglingRef, "A"},
		{"option list without option", `{"option_lists":[{"ref":"L","name":"L","options":[]}]}`, domain.BlockEmptyOptionList, "L"},
		{"minimum above maximum", `{"option_lists":[{"ref":"L","name":"L","min_selections":3,"max_selections":2,"multiple_selection":true,"options":[{"ref":"o1","name":"a","price":"0.00 EUR"},{"ref":"o2","name":"b","price":"0.00 EUR"}]}]}`, domain.BlockOptionLimits, "L"},
		{"maximum above the options", `{"option_lists":[{"ref":"L","name":"L","min_selections":0,"max_selections":3,"options":[{"ref":"o1","name":"a","price":"0.00 EUR"}]}]}`, domain.BlockOptionLimits, "L"},
		{"repeated picks with a maximum of one", `{"option_lists":[{"ref":"L","name":"L","min_selections":0,"max_selections":1,"multiple_selection":true,"options":[{"ref":"o1","name":"a","price":"0.00 EUR"}]}]}`, domain.BlockOptionLimits, "L"},
		{"minimum above the options", `{"option_lists":[{"ref":"L","name":"L","min_selections":2,"options":[{"ref":"o1","name":"a","price":"0.00 EUR"}]}]}`, domain.BlockOptionLimits, "L"},
		{"more defaults than the maximum", `{"option_lists":[{"ref":"L","name":"L","min_selections":0,"max_selections":1,"options":[{"ref":"o1","name":"a","price":"0.00 EUR","default":true},{"ref":"o2","name":"b","price":"0.00 EUR","default":true}]}]}`, domain.BlockOptionLimits, "L"},
		{"duplicate option ref", `{"option_lists":[{"ref":"L","name":"L","options":[{"ref":"o","name":"a","price":"0.00 EUR"},{"ref":"o","name":"b","price":"0.00 EUR"}]}]}`, domain.BlockDuplicateRef, "o"},
		{"option without price", `{"option_lists":[{"ref":"L","name":"L","options":[{"ref":"o","name":"a"}]}]}`, domain.BlockInvalidMoney, "L|o"},
		{"deal on an unknown SKU", `{"deals":[{"ref":"d","name":"D","lines":[{"label":"l","pricing_effect":"fixed_price","pricing_value":"1.00 EUR","skus":[{"ref":"ghost"}]}]}]}`, domain.BlockDanglingRef, "d"},
		{"charge that is no amount", `{"charges":[{"ref":"ch","name":"Pack","type":"other","price":"free"}]}`, domain.BlockInvalidMoney, "ch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := blockerKeys(planOver(t, tc.base, nil))
			assert.Contains(t, got[tc.code], tc.key, "blockers: %+v", got)
		})
	}

	t.Run("a valid foreign catalog has no blocker", func(t *testing.T) {
		p := planOver(t, `{"categories":[`+cat+`],"products":[{"ref":"P","category_ref":"c","name":"P","skus":[{"ref":"A","price":"1.00 EUR"}]}],
			"charges":[{"ref":"ch","name":"Pack","type":"other"}]}`, nil)
		assert.Empty(t, p.Blockers)
	})
}

// A catalog as GET serves it names parents, categories and option lists by id. The planner reads it
// back with refs, and reports what has no ref to be linked by.
func TestTheCatalogAsHubRiseServesItIsNormalized(t *testing.T) {
	p := planOver(t, `{
	  "options_lists": [
	    {"id":"l1","ref":"sauces","name":"Sauces","type":"x","options":[{"id":"o1","option_list_id":"l1","ref":"soja","name":"Soja","price":"0.00 EUR"}]},
	    {"id":"l2","name":"No ref","options":[{"id":"o2","ref":"x","name":"X","price":"0.00 EUR"}]}],
	  "categories": [
	    {"id":"c1","ref":"top","name":"Top"},
	    {"id":"c2","ref":"sub","name":"Sub","parent_id":"c1","images_ids":["i1"]},
	    {"id":"c3","name":"No ref"}],
	  "products": [
	    {"id":"p1","category_id":"c2","name":"P","images_ids":["i2"],"skus":[
	      {"id":"s1","product_id":"p1","ref":"S1","price":"1.00 EUR","option_list_ids":["l1"]},
	      {"id":"s2","ref":"S2","price":"1.00 EUR","option_list_ids":["l1","l2","ghost"]}]}],
	  "deals": [{"id":"d1","ref":"d","category_id":"c1","name":"Deal","images_ids":["i3"],"lines":[]}],
	  "discounts": [{"id":"x1","ref":"disc","name":"Promo","images_ids":["i4"],"pricing_effect":"price_off","pricing_value":"1.00 EUR"}]
	}`, nil)

	keys := blockerKeys(p)
	assert.Contains(t, keys[domain.BlockBaseMissingRef], "c3", "a category without ref")
	assert.Contains(t, keys[domain.BlockBaseMissingRef], "l2", "an option list without ref")
	assert.Contains(t, keys[domain.BlockBaseMissingRef], "S2", "a SKU naming an option list that cannot be named")

	var data struct {
		Categories []map[string]any `json:"categories"`
		Products   []map[string]any `json:"products"`
		Lists      []map[string]any `json:"option_lists"`
		Deals      []map[string]any `json:"deals"`
		Discounts  []map[string]any `json:"discounts"`
	}
	require.NoError(t, json.Unmarshal(p.Data, &data))
	assert.Equal(t, "top", data.Categories[1]["parent_ref"])
	assert.Equal(t, []any{"i1"}, data.Categories[1]["image_ids"])
	assert.Nil(t, data.Categories[1]["images_ids"])
	assert.Equal(t, "sub", data.Products[0]["category_ref"])
	assert.Equal(t, []any{"i2"}, data.Products[0]["image_ids"])
	skus := data.Products[0]["skus"].([]any)
	assert.Equal(t, []any{"sauces"}, skus[0].(map[string]any)["option_list_refs"])
	assert.Nil(t, data.Lists[0]["type"], "the deprecated read only key is dropped")
	assert.Equal(t, "top", data.Deals[0]["category_ref"])
	assert.Equal(t, []any{"i3"}, data.Deals[0]["image_ids"])
	assert.Equal(t, []any{"i4"}, data.Discounts[0]["image_ids"])
}

func TestAnUnreadableCatalogIsAnError(t *testing.T) {
	_, err := application.PlanPublish(application.PublishInput{Local: &domain.LocalSnapshot{}, Base: json.RawMessage(`[1]`)})
	assert.Error(t, err)
	_, err = application.PlanPublish(application.PublishInput{Local: &domain.LocalSnapshot{}, Base: json.RawMessage(`{}`)})
	assert.Error(t, err, "an empty menu is never planned")
}
