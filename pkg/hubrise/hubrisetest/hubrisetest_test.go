package hubrisetest_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/pkg/hubrise"
	"tsb-service/pkg/hubrise/hubrisetest"
)

func TestDroppedFieldsFindsWhatTheTypesDoNotModel(t *testing.T) {
	raw := []byte(`{"id":"o1","status":"new","bogus":"x","empty":"","nothing":null,"flag":false,"items":[{"product_name":"a","price":"1.00 EUR","quantity":"1","mystery":[1]}],"nested":{"deep":{"x":1}}}`)
	var o hubrise.Order
	require.NoError(t, json.Unmarshal(raw, &o))
	dropped, err := hubrisetest.DroppedFields(raw, o)
	require.NoError(t, err)
	assert.Equal(t, []string{".bogus", ".items[0].mystery", ".nested"}, dropped, "empty values are not fields; the rest is reported by path, sorted")

	clean := []byte(`{"id":"o1","status":"new","deleted":false}`)
	require.NoError(t, json.Unmarshal(clean, &o))
	dropped, err = hubrisetest.DroppedFields(clean, o)
	require.NoError(t, err)
	assert.Empty(t, dropped)

	// A list element missing from the decoded value is reported too.
	dropped, err = hubrisetest.DroppedFields([]byte(`{"items":[{"product_name":"a"},{"product_name":"b"}]}`), hubrise.Order{Items: []hubrise.OrderItem{{ProductName: "a"}}})
	require.NoError(t, err)
	assert.Equal(t, []string{".items[1]"}, dropped)

	_, err = hubrisetest.DroppedFields([]byte(`{`), nil)
	assert.Error(t, err)
	_, err = hubrisetest.DroppedFields([]byte(`{}`), make(chan int))
	assert.Error(t, err)
}

func TestFixtures(t *testing.T) {
	names := hubrisetest.FixtureNames()
	assert.Contains(t, names, "docs/order_retrieve_doc.json")
	assert.Contains(t, names, "scenarios/catalog_mirror/001_PUT_catalogs_3p5j6.json")
	assert.Greater(t, len(names), 40)
	for _, n := range names {
		assert.True(t, json.Valid(hubrisetest.Fixture(n)), n)
	}
	assert.Panics(t, func() { hubrisetest.Fixture("docs/missing.json") })
	assert.Panics(t, func() { hubrisetest.Scenario("missing") })

	ex := hubrisetest.Scenario("prepaid_cancel_refund")
	require.NotEmpty(t, ex)
	assert.Equal(t, "PUT", ex[0].Method)
	assert.Contains(t, ex[1].Path, "/orders")
	assert.Equal(t, 200, ex[0].Status)

	seed := hubrisetest.SeedCatalog()
	assert.Len(t, seed.Products, 5)
	assert.NotEmpty(t, seed.OptionLists)
	assert.NotEmpty(t, seed.Charges)
}

func TestNewHandlerServesWithoutAListener(t *testing.T) {
	h := hubrisetest.NewHandler()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	c, err := hubrise.New(ts.URL+"/v1", hubrisetest.Token)
	require.NoError(t, err)
	loc, err := c.GetLocation(context.Background())
	require.NoError(t, err)
	assert.Equal(t, hubrisetest.LocationID, loc.ID)
}

func TestControlMethodsRefuseWhatTheAPIRefuses(t *testing.T) {
	srv := hubrisetest.New(t)
	_, err := srv.InjectOrder(hubrise.Order{})
	assert.Error(t, err)
	_, err = srv.PatchOrderAs(hubrisetest.EPOSToken, "ghost", hubrise.OrderPatch{Status: hubrise.StatusAccepted})
	assert.Error(t, err)
	_, err = srv.PatchOrderAs("nobody", "ghost", hubrise.OrderPatch{})
	assert.Error(t, err)
	assert.Error(t, srv.SetOrderStatus("ghost", hubrise.StatusAccepted))
	_, ok := srv.Order("ghost")
	assert.False(t, ok)

	o, err := srv.InjectOrder(hubrise.Order{Status: hubrise.StatusNew, PrivateRef: "epos-ref", Items: []hubrise.OrderItem{{ProductName: "x", Price: "1.00 EUR", Quantity: "1"}}})
	require.NoError(t, err)
	stored, ok := srv.Order(o.ID)
	require.True(t, ok)
	assert.Equal(t, "epos-ref", stored.PrivateRef, "the stored order keeps every private ref")
	assert.Equal(t, "pili-pos", stored.CreatedBy)
	require.NotNil(t, stored.Asap)
	assert.True(t, *stored.Asap, "asap defaults to true without an expected time")

	// Requests can be filtered and cleared.
	c := srv.NewClient(t)
	_, _ = c.GetLocation(context.Background())
	_, _ = c.GetOrder(context.Background(), o.ID)
	assert.Len(t, srv.RequestsTo("", "/v1/location"), 2)
	assert.Len(t, srv.RequestsTo("GET", "/v1/location/orders"), 1)
	assert.Empty(t, srv.RequestsTo("POST", "/v1/location"))
	srv.ClearRequests()
	assert.Empty(t, srv.Requests())
}

func TestAnExpectedTimeKeepsAsapUnset(t *testing.T) {
	srv := hubrisetest.New(t)
	c := srv.NewClient(t)
	at := time.Date(2026, 10, 24, 19, 0, 0, 0, time.UTC)
	o, err := c.CreateOrder(context.Background(), &hubrise.Order{
		Status: hubrise.StatusNew, ExpectedTime: &at,
		Items: []hubrise.OrderItem{{ProductName: "x", Price: "1.00 EUR", Quantity: "1"}},
	})
	require.NoError(t, err)
	assert.Nil(t, o.Asap)
	require.NotNil(t, o.ExpectedTime)
	assert.True(t, o.ExpectedTime.Equal(at))
}

// ---- PUT /catalogs/:id and the images ----

func ids(t *testing.T, data map[string]any) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if id, ok := x["id"].(string); ok {
				out[id] = true
			}
			for _, c := range x {
				walk(c)
			}
		case []any:
			for _, c := range x {
				walk(c)
			}
		}
	}
	walk(data)
	return out
}

const uploadMenu = `{"variants":[{"ref":"DELIV","name":"Delivery"}],
"categories":[{"ref":"MAKI","name":"Makis"},{"ref":"SUB","parent_ref":"MAKI","name":"Sub"}],
"option_lists":[{"ref":"SAUCE","name":"Sauce","min_selections":0,"max_selections":1,"options":[{"ref":"SOJA","name":"Soja","price":"0.00 EUR","x_opt":1},{"ref":"SUCREE","name":"Sucree","price":"0.50 EUR"}]}],
"products":[{"ref":"MAKI-S","category_ref":"MAKI","name":"Maki","x_unknown":{"a":[1,2.50]},"tax_rate":{"delivery":"6","collection":"6","eat_in":"12"},
 "skus":[{"ref":"MAKI-S-6","name":"6 pieces","price":"5.50 EUR","option_list_refs":["SAUCE"],"custom_fields":{"code":"M6","x":{"y":1}},
          "price_overrides":[{"variant_refs":["DELIV"],"price":"6.00 EUR"}]},
         {"ref":"MAKI-S-12","name":"12 pieces","price":"10.00 EUR"}]}],
"deals":[{"ref":"D1","name":"Menu","lines":[{"skus":[{"ref":"MAKI-S-6"}],"pricing_effect":"price_off","pricing_value":"1.00 EUR"}]}],
"discounts":[{"ref":"X","name":"Ten","pricing_effect":"percentage_off","pricing_value":"10"}],
"charges":[{"ref":"DELIVERY","name":"Delivery","type":"delivery","price":"2.50 EUR"}]}`

func TestPutCatalogClearsAndRecreatesWithNewIDs(t *testing.T) {
	ctx := context.Background()
	srv := hubrisetest.New(t)
	c := srv.NewClient(t)

	first, err := c.ReplaceCatalog(ctx, hubrisetest.CatalogID, json.RawMessage(uploadMenu))
	require.NoError(t, err)
	assert.Equal(t, hubrisetest.CatalogName, first.Name, "no name sent: the catalog keeps its name")
	assert.Equal(t, hubrisetest.LocationID, first.LocationID)
	var d1 map[string]any
	require.NoError(t, json.Unmarshal(first.Data, &d1))
	ids1 := ids(t, d1)
	assert.NotEmpty(t, ids1)

	// The stored form is what a GET shows: link refs became ids, defaults appear, unknown keys stay.
	products := d1["products"].([]any)
	p := products[0].(map[string]any)
	assert.Nil(t, p["category_ref"])
	assert.NotEmpty(t, p["category_id"])
	assert.Contains(t, p, "x_unknown", "keys the mock does not model are kept")
	assert.Equal(t, "6.0", p["tax_rate"].(map[string]any)["delivery"], "percentages come back with a decimal")
	sku := p["skus"].([]any)[0].(map[string]any)
	assert.NotEmpty(t, sku["option_list_ids"])
	assert.Nil(t, sku["option_list_refs"])
	assert.Equal(t, "M6", sku["custom_fields"].(map[string]any)["code"])
	lists := d1["option_lists"].([]any)
	assert.Equal(t, "multiple", lists[0].(map[string]any)["type"], "min 0 max 1 is a multiple list")
	sub := d1["categories"].([]any)[1].(map[string]any)
	assert.Equal(t, d1["categories"].([]any)[0].(map[string]any)["id"], sub["parent_id"])

	second, err := c.ReplaceCatalog(ctx, hubrisetest.CatalogID, json.RawMessage(uploadMenu))
	require.NoError(t, err)
	var d2 map[string]any
	require.NoError(t, json.Unmarshal(second.Data, &d2))
	for id := range ids(t, d2) {
		assert.False(t, ids1[id], "id %s survived a PUT: refs, not ids, are the stable keys", id)
	}
	assert.Equal(t, 2, srv.CatalogPuts())
	assert.JSONEq(t, string(second.Data), string(srv.CatalogData()))

	// The typed view follows (inventory validation and the typed GET use it).
	typed, err := c.GetCatalog(ctx, hubrisetest.CatalogID)
	require.NoError(t, err)
	require.Len(t, typed.Data.Products, 1)
	assert.Equal(t, "MAKI-S-6", typed.Data.Products[0].Skus[0].Ref)
	assert.Len(t, srv.Catalog().Data.Products, 1)
	zero := "0"
	_, err = c.PatchInventory(ctx, []hubrise.InventoryEntry{{SkuRef: "MAKI-S-6", Stock: &zero}})
	require.NoError(t, err, "the inventory knows the new SKU refs")
}

func TestPutCatalogValidation(t *testing.T) {
	cases := map[string]string{
		"duplicate category ref":     `{"categories":[{"ref":"A","name":"a"},{"ref":"A","name":"b"}]}`,
		"duplicate sku name":         `{"categories":[{"ref":"A","name":"a"}],"products":[{"category_ref":"A","name":"p","skus":[{"name":"x","price":"1.00 EUR"},{"name":"x","price":"1.00 EUR"}]}]}`,
		"two unnamed skus":           `{"categories":[{"ref":"A","name":"a"}],"products":[{"category_ref":"A","name":"p","skus":[{"price":"1.00 EUR"},{"price":"1.00 EUR"}]}]}`,
		"product without sku":        `{"categories":[{"ref":"A","name":"a"}],"products":[{"category_ref":"A","name":"p","skus":[]}]}`,
		"product without category":   `{"products":[{"name":"p","skus":[{"price":"1.00 EUR"}]}]}`,
		"partial tax rate":           `{"categories":[{"ref":"A","name":"a"}],"products":[{"category_ref":"A","name":"p","tax_rate":{"delivery":"6"},"skus":[{"price":"1.00 EUR"}]}]}`,
		"bad barcode":                `{"categories":[{"ref":"A","name":"a"}],"products":[{"category_ref":"A","name":"p","skus":[{"price":"1.00 EUR","barcodes":["123"]}]}]}`,
		"empty option list":          `{"option_lists":[{"ref":"L","name":"l","options":[]}]}`,
		"min above max":              `{"option_lists":[{"ref":"L","name":"l","min_selections":2,"max_selections":1,"options":[{"name":"a","price":"0.00 EUR"},{"name":"b","price":"0.00 EUR"}]}]}`,
		"multiple with max 1":        `{"option_lists":[{"ref":"L","name":"l","max_selections":1,"multiple_selection":true,"options":[{"name":"a","price":"0.00 EUR"}]}]}`,
		"too many defaults":          `{"option_lists":[{"ref":"L","name":"l","max_selections":1,"options":[{"name":"a","price":"0.00 EUR","default":true},{"name":"b","price":"0.00 EUR","default":true}]}]}`,
		"duplicate option ref":       `{"option_lists":[{"ref":"L","name":"l","options":[{"ref":"o","name":"a","price":"0.00 EUR"},{"ref":"o","name":"b","price":"0.00 EUR"}]}]}`,
		"option without price":       `{"option_lists":[{"ref":"L","name":"l","options":[{"name":"a"}]}]}`,
		"override without condition": `{"categories":[{"ref":"A","name":"a"}],"products":[{"category_ref":"A","name":"p","skus":[{"price":"1.00 EUR","price_overrides":[{"price":"2.00 EUR"}]}]}]}`,
		"override unknown variant":   `{"categories":[{"ref":"A","name":"a"}],"products":[{"category_ref":"A","name":"p","skus":[{"price":"1.00 EUR","price_overrides":[{"variant_refs":["ghost"],"price":"2.00 EUR"}]}]}]}`,
		"empty override list":        `{"categories":[{"ref":"A","name":"a"}],"products":[{"category_ref":"A","name":"p","skus":[{"price":"1.00 EUR","price_overrides":[{"variant_refs":[],"price":"2.00 EUR"}]}]}]}`,
		"dangling restriction":       `{"categories":[{"ref":"A","name":"a"}],"products":[{"category_ref":"A","name":"p","skus":[{"price":"1.00 EUR","restrictions":{"variant_refs":["ghost"]}}]}]}`,
		"deal without line":          `{"deals":[{"name":"d","lines":[]}]}`,
		"deal line without sku":      `{"deals":[{"name":"d","lines":[{"skus":[],"pricing_effect":"unchanged"}]}]}`,
		"deal sku without ref":       `{"deals":[{"name":"d","lines":[{"skus":[{}],"pricing_effect":"unchanged"}]}]}`,
		"deal sku with a stale id":   `{"categories":[{"ref":"A","name":"a"}],"products":[{"category_ref":"A","name":"p","skus":[{"ref":"S","price":"1.00 EUR"}]}],"deals":[{"name":"d","lines":[{"skus":[{"id":"stale","ref":"S"}],"pricing_effect":"unchanged"}]}]}`,
		"deal dangling sku":          `{"deals":[{"name":"d","lines":[{"skus":[{"ref":"ghost"}],"pricing_effect":"unchanged"}]}]}`,
		"deal bad effect":            `{"categories":[{"ref":"A","name":"a"}],"products":[{"category_ref":"A","name":"p","skus":[{"ref":"S","price":"1.00 EUR"}]}],"deals":[{"name":"d","lines":[{"skus":[{"ref":"S"}],"pricing_effect":"magic"}]}]}`,
		"discount bad effect":        `{"discounts":[{"name":"d","pricing_effect":"free"}]}`,
		"charge bad type":            `{"charges":[{"name":"d","type":"mystery"}]}`,
		"variant duplicate":          `{"variants":[{"ref":"A","name":"a"},{"ref":"A","name":"b"}]}`,
		"unknown image":              `{"categories":[{"ref":"A","name":"a","image_ids":["ghost"]}]}`,
		"bad money":                  `{"categories":[{"ref":"A","name":"a"}],"products":[{"category_ref":"A","name":"p","skus":[{"price":"1,00"}]}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			srv := hubrisetest.New(t)
			before := srv.CatalogData()
			_, err := srv.NewClient(t).ReplaceCatalog(context.Background(), hubrisetest.CatalogID, json.RawMessage(body))
			var he *hubrise.Error
			require.ErrorAs(t, err, &he)
			assert.Equal(t, 422, he.Status)
			assert.NotEmpty(t, he.Fields)
			assert.JSONEq(t, string(before), string(srv.CatalogData()), "an invalid upload stores nothing")
			assert.Zero(t, srv.CatalogPuts())
		})
	}
}

func TestPutCatalogBodyRules(t *testing.T) {
	ctx := context.Background()
	srv := hubrisetest.New(t)
	c := srv.NewClient(t)

	_, err := c.ReplaceCatalog(ctx, "ghost", json.RawMessage(`{}`))
	assert.Equal(t, hubrise.KindNotFound, hubrise.KindOf(err))
	_, err = c.ReplaceCatalog(ctx, hubrisetest.CatalogID, json.RawMessage(`null`))
	assert.Equal(t, hubrise.KindInvalid, hubrise.KindOf(err))
	_, err = c.ReplaceCatalog(ctx, hubrisetest.CatalogID, json.RawMessage(`[1]`))
	assert.Equal(t, hubrise.KindInvalid, hubrise.KindOf(err))
	_, err = c.ReplaceCatalog(ctx, hubrisetest.CatalogID, json.RawMessage(`{"categories":"x"}`))
	assert.Equal(t, hubrise.KindInvalid, hubrise.KindOf(err))

	// An option list above the option count is capped, 0 becomes null: autocorrected, not refused.
	got, err := c.ReplaceCatalog(ctx, hubrisetest.CatalogID, json.RawMessage(`{"option_lists":[
	  {"ref":"A","name":"a","max_selections":5,"options":[{"name":"x","price":"0.00 EUR"}]},
	  {"ref":"B","name":"b","max_selections":0,"options":[{"name":"x","price":"0.00 EUR"}]},
	  {"ref":"C","name":"c","min_selections":1,"max_selections":1,"options":[{"name":"x","price":"0.00 EUR"}]}]}`))
	require.NoError(t, err)
	var d map[string]any
	require.NoError(t, json.Unmarshal(got.Data, &d))
	lists := d["option_lists"].([]any)
	assert.EqualValues(t, 1, lists[0].(map[string]any)["max_selections"])
	assert.Nil(t, lists[1].(map[string]any)["max_selections"])
	assert.Equal(t, "single", lists[2].(map[string]any)["type"])

	// A name in the body renames the catalog.
	_, err = c.ReplaceCatalog(ctx, hubrisetest.CatalogID, json.RawMessage(`{}`))
	require.NoError(t, err)
}

func TestPutCatalogScopeAndLevel(t *testing.T) {
	ctx := context.Background()
	srv := hubrisetest.New(t)
	c := srv.NewClient(t)

	srv.SetTokenScopes(hubrisetest.Token, "orders.write", "catalog.read")
	_, err := c.ReplaceCatalog(ctx, hubrisetest.CatalogID, json.RawMessage(`{}`))
	assert.Equal(t, hubrise.KindForbidden, hubrise.KindOf(err), "no catalog.write: 403")
	_, err = c.UploadImage(ctx, hubrisetest.CatalogID, "r", "image/png", []byte{1})
	assert.Equal(t, hubrise.KindForbidden, hubrise.KindOf(err))
	_, err = c.GetCatalogRaw(ctx, hubrisetest.CatalogID)
	require.NoError(t, err, "reading still works")

	srv.SetTokenScopes(hubrisetest.Token, "catalog.*")
	_, err = c.ReplaceCatalog(ctx, hubrisetest.CatalogID, json.RawMessage(`{}`))
	require.NoError(t, err, "the wildcard grants it")

	srv.SetTokenScopes(hubrisetest.Token) // everything again
	srv.SetCatalogLevel(hubrisetest.LevelAccount)
	_, err = c.ReplaceCatalog(ctx, hubrisetest.CatalogID, json.RawMessage(`{}`))
	assert.Equal(t, hubrise.KindUnauthorized, hubrise.KindOf(err), "an account catalog needs an account token")
	got, err := c.GetCatalogRaw(ctx, hubrisetest.CatalogID)
	require.NoError(t, err)
	assert.Empty(t, got.LocationID, "an account catalog has no location id")
	assert.Equal(t, hubrisetest.AccountID, got.AccountID)
	hidden, err := c.GetCatalog(ctx, hubrisetest.CatalogID)
	require.NoError(t, err)
	assert.NotNil(t, hidden)

	srv.SetCatalogLevel(hubrisetest.LevelLocation)
	_, err = c.ReplaceCatalog(ctx, hubrisetest.CatalogID, json.RawMessage(`{}`))
	require.NoError(t, err)
	assert.Equal(t, 2, srv.CatalogPuts())
	srv.SetTokenScopes("nobody", "x") // unknown tokens are ignored
}

func TestAuthorizationScopeDecidesTheTokenScope(t *testing.T) {
	ctx := context.Background()
	srv := hubrisetest.New(t)
	o := srv.NewOAuth(t)
	exchange := func(scope string) {
		code := srv.IssueAuthCode()
		if scope != "" {
			noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			resp, err := noFollow.Get(o.AuthorizeURL("https://shop.test/cb", scope, "st"))
			require.NoError(t, err)
			_ = resp.Body.Close()
			loc, err := resp.Location()
			require.NoError(t, err)
			code = loc.Query().Get("code")
		}
		_, err := o.ExchangeCode(ctx, code)
		require.NoError(t, err)
	}
	c := srv.NewClient(t)

	exchange("location[orders.write,catalog.read]")
	_, err := c.ReplaceCatalog(ctx, hubrisetest.CatalogID, json.RawMessage(`{}`))
	assert.Equal(t, hubrise.KindForbidden, hubrise.KindOf(err), "a connection made before catalog.write existed cannot publish")

	exchange(hubrise.DefaultScope)
	_, err = c.ReplaceCatalog(ctx, hubrisetest.CatalogID, json.RawMessage(`{}`))
	require.NoError(t, err, "reconnecting with the new scope fixes it")

	exchange("") // a code without scope grants everything
	_, err = c.ReplaceCatalog(ctx, hubrisetest.CatalogID, json.RawMessage(`{}`))
	require.NoError(t, err)
}

func TestPutCatalogEventsAndCounters(t *testing.T) {
	ctx := context.Background()
	srv := hubrisetest.New(t, hubrisetest.WithClock(func() time.Time { return time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC) }))
	c := srv.NewClient(t)
	epos, err := hubrise.New(srv.APIBaseURL(), hubrisetest.EPOSToken, hubrise.WithHTTPClient(srv.HTTPClient()), hubrise.WithLimiter(hubrise.NewLimiter(hubrise.Limits{})))
	require.NoError(t, err)
	events := map[string][]string{"catalog": {"update"}}
	_, err = epos.SetCallback(ctx, hubrise.Callback{Events: events})
	require.NoError(t, err)
	_, err = c.SetCallback(ctx, hubrise.Callback{Events: events})
	require.NoError(t, err)

	_, err = c.ReplaceCatalog(ctx, hubrisetest.CatalogID, json.RawMessage(uploadMenu))
	require.NoError(t, err)
	assert.Empty(t, srv.Events(hubrisetest.Token), "HubRise never calls back the author")
	require.Len(t, srv.Events(hubrisetest.EPOSToken), 1)
	assert.True(t, srv.Events(hubrisetest.EPOSToken)[0].Is("catalog", "update"))

	// The other side editing tells us.
	require.NoError(t, srv.ReplaceCatalogRaw(json.RawMessage(uploadMenu)))
	require.Len(t, srv.Events(hubrisetest.Token), 1)
	assert.Error(t, srv.ReplaceCatalogRaw(json.RawMessage(`{"categories":[{"ref":"A","name":"a"},{"ref":"A","name":"a"}]}`)))

	// A PUT is not in the 10 a minute bucket, a GET is.
	srv.ClearRequests()
	_, err = c.GetCatalogRaw(ctx, hubrisetest.CatalogID)
	require.NoError(t, err)
	assert.Equal(t, 1, srv.HeavyUsed(hubrisetest.Token))
	for range 3 {
		_, err = c.ReplaceCatalog(ctx, hubrisetest.CatalogID, json.RawMessage(uploadMenu))
		require.NoError(t, err)
	}
	assert.Equal(t, 1, srv.HeavyUsed(hubrisetest.Token))
	assert.Len(t, srv.RequestsTo("PUT", "/v1/catalogs/"), 3)
	var sent struct {
		Data json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(srv.RequestsTo("PUT", "/v1/catalogs/")[0].Body, &sent))
	assert.JSONEq(t, uploadMenu, string(sent.Data), "the request log keeps what was sent")

	srv.SetLimits(hubrisetest.Limits{HeavyPerMinute: 1})
	_, err = c.GetCatalogRaw(ctx, hubrisetest.CatalogID)
	assert.Equal(t, hubrise.KindRateLimited, hubrise.KindOf(err))
	_, err = srv.NewClient(t).ReplaceCatalog(ctx, hubrisetest.CatalogID, json.RawMessage(uploadMenu)) // (the 429 paused c's limiter)
	require.NoError(t, err, "the PUT still goes through when the list bucket is full")
}

func TestPutCatalogFaults(t *testing.T) {
	ctx := context.Background()
	srv := hubrisetest.New(t)
	c := srv.NewClient(t)
	for _, status := range []int{401, 403, 404, 422, 500, 503} {
		srv.AddFault(hubrisetest.Fault{Method: "PUT", Path: "/v1/catalogs/", Status: status, Times: 1})
		_, err := c.ReplaceCatalog(ctx, hubrisetest.CatalogID, json.RawMessage(uploadMenu))
		var he *hubrise.Error
		require.ErrorAs(t, err, &he, status)
		assert.Equal(t, status, he.Status)
	}
	assert.Zero(t, srv.CatalogPuts(), "a faulted PUT is not processed")
	srv.AddFault(hubrisetest.Fault{Method: "PUT", Path: "/v1/catalogs/", DropAfter: true, Times: 1})
	_, err := c.ReplaceCatalog(ctx, hubrisetest.CatalogID, json.RawMessage(uploadMenu))
	require.Error(t, err)
	assert.Equal(t, 1, srv.CatalogPuts(), "the response was lost but the PUT took effect")
	_, err = c.ReplaceCatalog(ctx, hubrisetest.CatalogID, json.RawMessage(uploadMenu))
	require.NoError(t, err)
}

func TestImages(t *testing.T) {
	ctx := context.Background()
	srv := hubrisetest.New(t)
	c := srv.NewClient(t)
	png := []byte("\x89PNG-bytes")

	img, err := c.UploadImage(ctx, hubrisetest.CatalogID, "tsb-product-1", "image/png", png)
	require.NoError(t, err)
	assert.Equal(t, "image/png", img.Type)
	assert.Equal(t, len(png), img.Size)
	assert.NotEmpty(t, img.MD5)
	assert.Equal(t, "tsb-product-1", img.PrivateRef)
	assert.Positive(t, img.SecondsBeforeRemoval, "unattached images are purged after 30 days")

	_, err = c.UploadImage(ctx, hubrisetest.CatalogID, "tsb-product-1", "image/png", png)
	assert.Equal(t, hubrise.KindInvalid, hubrise.KindOf(err), "private_ref is unique per catalog")
	_, err = c.UploadImage(ctx, hubrisetest.CatalogID, "x", "application/pdf", png)
	assert.Equal(t, hubrise.KindInvalid, hubrise.KindOf(err), "415 is an invalid request")
	var he *hubrise.Error
	_, err = c.UploadImage(ctx, hubrisetest.CatalogID, "x", "application/pdf", png)
	require.ErrorAs(t, err, &he)
	assert.Equal(t, 415, he.Status)
	_, err = c.UploadImage(ctx, hubrisetest.CatalogID, "big", "image/jpeg", make([]byte, 1<<20+1))
	require.ErrorAs(t, err, &he)
	assert.Equal(t, 422, he.Status)
	ok, err := c.UploadImage(ctx, hubrisetest.CatalogID, "limit", "image/jpeg", make([]byte, 1<<20))
	require.NoError(t, err, "exactly 1 MB is fine")
	assert.Equal(t, 1<<20, ok.Size)
	_, err = c.UploadImage(ctx, "ghost", "r", "image/png", png)
	assert.Equal(t, hubrise.KindNotFound, hubrise.KindOf(err))

	all, err := c.ListImages(ctx, hubrisetest.CatalogID, "")
	require.NoError(t, err)
	assert.Len(t, all, 2)
	one, err := c.ListImages(ctx, hubrisetest.CatalogID, "tsb-product-1")
	require.NoError(t, err)
	require.Len(t, one, 1)
	assert.Equal(t, img.ID, one[0].ID)
	none, err := c.ListImages(ctx, hubrisetest.CatalogID, "nope")
	require.NoError(t, err)
	assert.Empty(t, none)
	_, err = c.ListImages(ctx, "ghost", "")
	assert.Equal(t, hubrise.KindNotFound, hubrise.KindOf(err))
	got, found := srv.ImageBytes(img.ID)
	require.True(t, found)
	assert.Equal(t, png, got)
	_, found = srv.ImageBytes("ghost")
	assert.False(t, found)
	assert.Len(t, srv.Images(), 2)

	// Referencing the image in a PUT attaches it: it is no longer scheduled for removal.
	_, err = c.ReplaceCatalog(ctx, hubrisetest.CatalogID, json.RawMessage(`{"categories":[{"ref":"A","name":"a","image_ids":["`+img.ID+`"]}]}`))
	require.NoError(t, err)
	attached, err := c.GetImage(ctx, hubrisetest.CatalogID, img.ID)
	require.NoError(t, err)
	assert.Zero(t, attached.SecondsBeforeRemoval)
	other, err := c.GetImage(ctx, hubrisetest.CatalogID, ok.ID)
	require.NoError(t, err)
	assert.Positive(t, other.SecondsBeforeRemoval)
	data, err := c.GetImageData(ctx, hubrisetest.CatalogID, img.ID)
	require.NoError(t, err)
	assert.Equal(t, png, data.Data)

	// The control AddImage still works and is listed.
	srv.AddImage("legacy", "image/png", png)
	srv.AddImage("legacy", "image/png", png)
	assert.Len(t, srv.Images(), 3)
}

func TestTheMockAuthorizeEndpointOnlyRedirectsToTestHosts(t *testing.T) {
	srv := hubrisetest.New(t)
	o := srv.NewOAuth(t)
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	authorize := func(redirectURI string) *http.Response {
		t.Helper()
		resp, err := noFollow.Get(o.AuthorizeURL(redirectURI, hubrise.DefaultScope, "st"))
		require.NoError(t, err)
		_ = resp.Body.Close()
		return resp
	}

	for _, ok := range []string{"https://shop.test/cb", "https://api.example.test/api/v1/hubrise/oauth/callback", "http://localhost:3000/cb", "http://127.0.0.1:8080/cb", "http://[::1]/cb"} {
		resp := authorize(ok)
		assert.Equal(t, http.StatusFound, resp.StatusCode, ok)
		assert.NotEmpty(t, resp.Header.Get("Location"), ok)
	}
	for _, refused := range []string{"https://evil.example.com/cb", "https://shop.test.evil.com/cb", "javascript:alert(1)", "//evil.example.com/cb", "ftp://shop.test/cb", "https://10.0.0.1/cb"} {
		resp := authorize(refused)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, refused)
		assert.Empty(t, resp.Header.Get("Location"), refused)
	}
}
