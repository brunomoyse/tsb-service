package hubrise_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/pkg/hubrise"
	"tsb-service/pkg/hubrise/hubrisetest"
)

// decodeAs decodes raw into a new T and fails when a non empty field of raw is not modelled by T.
func decodeAs[T any](t *testing.T, label string, raw json.RawMessage) T {
	t.Helper()
	var v T
	require.NoError(t, json.Unmarshal(raw, &v), label)
	dropped, err := hubrisetest.DroppedFields(raw, v)
	require.NoError(t, err)
	assert.Empty(t, dropped, "%s: fields not modelled by the Go types", label)
	return v
}

// TestContractDocSamples decodes the JSON samples of the HubRise documentation.
func TestContractDocSamples(t *testing.T) {
	doc := func(name string) json.RawMessage { return hubrisetest.Fixture("docs/" + name) }

	t.Run("order retrieve", func(t *testing.T) {
		o := decodeAs[hubrise.Order](t, "order_retrieve_doc", doc("order_retrieve_doc.json"))
		assert.Equal(t, "17654321", o.Ref)
		assert.Equal(t, hubrise.StatusInDelivery, o.Status)
		assert.Equal(t, hubrise.ServiceDelivery, o.ServiceType)
		require.Len(t, o.Items, 2)
		assert.Equal(t, "3", o.Items[0].SkuRef)
		assert.Equal(t, "31", o.Items[1].Options[0].Ref)
		assert.Equal(t, 1, o.Items[1].Options[0].Quantity)
		require.NotNil(t, o.Items[0].DealLine)
		assert.Equal(t, "0", o.Items[0].DealLine.DealKey)
		assert.Equal(t, "10", o.Deals["0"].Ref)
		assert.Equal(t, "45", o.Discounts[0].Ref)
		assert.Equal(t, "DEL", o.Charges[0].Ref)
		assert.Equal(t, "CSH", o.Payments[0].Ref)
		assert.NotEmpty(t, o.Delivery)
		cents, err := o.Total.Cents()
		require.NoError(t, err)
		assert.EqualValues(t, 1890, cents)
	})

	t.Run("order create body", func(t *testing.T) {
		o := decodeAs[hubrise.Order](t, "order_create_doc", doc("order_create_doc.json"))
		assert.Equal(t, "MAR-SM", o.Items[0].SkuRef)
		assert.Equal(t, "BBQ", o.Items[0].Options[0].Ref)
		assert.Equal(t, "PP", o.Payments[0].Ref)
		assert.NotEmpty(t, o.CustomerID)
	})

	t.Run("order list", func(t *testing.T) {
		orders := decodeAs[[]hubrise.Order](t, "order_list_doc", doc("order_list_doc.json"))
		require.Len(t, orders, 1)
		assert.Equal(t, hubrise.StatusNew, orders[0].Status)
		assert.Equal(t, "3345", orders[0].PrivateRef)
	})

	// The customer samples follow the field list of the customer resource in the documentation
	// (docs/hubrise/API_NOTES.md); the moyun-pos recordings hold no customer list exchange.
	t.Run("customer create body and resource", func(t *testing.T) {
		in := decodeAs[hubrise.Customer](t, "customer_create_doc", doc("customer_create_doc.json"))
		assert.Equal(t, "5f0b8a0e-3d38-4c3b-9d63-2b1e7f7f2f10", in.PrivateRef)
		assert.Equal(t, "BE", in.Country)
		assert.True(t, in.EmailMarketing)

		re := decodeAs[hubrise.Customer](t, "customer_retrieve_doc", doc("customer_retrieve_doc.json"))
		assert.Equal(t, "wd9lz", re.ID)
		assert.Equal(t, "cl1x9", re.CustomerListID)
		assert.False(t, re.Anonymised)
		assert.Equal(t, 3, re.NbOrders)
		cents, err := re.OrderTotal.Cents()
		require.NoError(t, err)
		assert.EqualValues(t, 5240, cents)
		require.NotNil(t, re.LastOrderDate)
	})

	t.Run("catalog upload", func(t *testing.T) {
		var body struct {
			Data hubrise.CatalogData `json:"data"`
		}
		require.NoError(t, json.Unmarshal(doc("catalog_create_doc.json"), &body))
		assert.Equal(t, "1", body.Data.Categories[1].ParentRef)
		assert.Equal(t, "TS_DUAL", body.Data.Products[0].Skus[0].Ref)
		assert.Equal(t, []string{"TES_COL"}, body.Data.Products[0].Skus[0].OptionListRefs)
		assert.Len(t, body.Data.OptionLists[0].Options, 2)
	})

	t.Run("products", func(t *testing.T) {
		up := decodeAs[hubrise.Product](t, "product_upload_doc", doc("product_upload_doc.json"))
		require.NotNil(t, up.TaxRate)
		assert.Equal(t, "20.0", up.TaxRate.Delivery)
		assert.Equal(t, "5.5", up.TaxRate.Collection)
		assert.Equal(t, "5.5", up.TaxRate.EatIn)

		re := decodeAs[hubrise.Product](t, "product_retrieve_doc", doc("product_retrieve_doc.json"))
		assert.Empty(t, re.TaxRate.EatIn, "a null eat_in stays unset")
		assert.Equal(t, []string{"e2sfj"}, re.Skus[0].OptionListIDs)
	})

	t.Run("sku with restrictions and price overrides", func(t *testing.T) {
		sku := decodeAs[hubrise.Sku](t, "sku_upload_doc", doc("sku_upload_doc.json"))
		require.NotNil(t, sku.Restrictions)
		assert.Equal(t, "13:30", sku.Restrictions.EndTime)
		assert.Equal(t, hubrise.Money("12.30 EUR"), sku.PriceOverrides[0].Price)
		assert.Equal(t, []string{"1"}, sku.PriceOverrides[0].VariantRefs)
		assert.Equal(t, []string{"hidden"}, sku.Tags)
	})

	t.Run("option list with an unlimited max", func(t *testing.T) {
		l := decodeAs[hubrise.OptionList](t, "option_list_retrieve_doc", doc("option_list_retrieve_doc.json"))
		assert.Nil(t, l.MaxSelections)
		assert.Equal(t, "BBQ", l.Options[0].Ref)
	})
}

// TestContractRecordedScenarios decodes every request and response recorded from HubRise by the
// moyun-pos integration.
func TestContractRecordedScenarios(t *testing.T) {
	var seen int
	for _, scenario := range []string{"catalog_mirror", "prepaid_cancel_refund", "unpaid_collected"} {
		for _, x := range hubrisetest.Scenario(scenario) {
			seen++
			label := scenario + " " + x.Method + " " + x.Path
			t.Run(label, func(t *testing.T) {
				path := x.Path
				if i := strings.IndexByte(path, '?'); i >= 0 {
					path = path[:i]
				}
				switch {
				case strings.HasSuffix(path, "/callback"):
					if x.Method == "POST" {
						decodeAs[hubrise.Callback](t, "request", x.Request)
					}
					decodeAs[hubrise.Callback](t, "response", x.Response)
				case strings.Contains(path, "/catalogs/"):
					if x.Method == "PUT" {
						var req struct {
							Name string              `json:"name"`
							Data hubrise.CatalogData `json:"data"`
						}
						require.NoError(t, json.Unmarshal(x.Request, &req))
						dropped, err := hubrisetest.DroppedFields(x.Request, req)
						require.NoError(t, err)
						assert.Empty(t, dropped)
					}
					cat := decodeAs[hubrise.Catalog](t, "response", x.Response)
					require.NotNil(t, cat.Data)
					assert.NotEmpty(t, cat.Data.Products)
				case strings.HasSuffix(path, "/orders") && x.Method == "GET":
					decodeAs[[]hubrise.Order](t, "response", x.Response)
				case strings.Contains(path, "/orders"):
					if x.Method == "POST" {
						decodeAs[hubrise.Order](t, "request", x.Request)
					}
					if x.Method == "PATCH" {
						decodeAs[hubrise.OrderPatch](t, "request", x.Request)
					}
					decodeAs[hubrise.Order](t, "response", x.Response)
				default:
					t.Fatalf("unhandled recorded exchange %s %s", x.Method, x.Path)
				}
			})
		}
	}
	assert.Greater(t, seen, 30, "the fixtures were not read")
}

// TestEveryFixtureIsCovered makes a new fixture file fail loudly until a contract test reads it.
func TestEveryFixtureIsCovered(t *testing.T) {
	for _, name := range hubrisetest.FixtureNames() {
		switch {
		case strings.HasPrefix(name, "docs/"), strings.HasPrefix(name, "scenarios/"):
		default:
			t.Errorf("fixture %s is in neither docs/ nor scenarios/", name)
		}
	}
}
