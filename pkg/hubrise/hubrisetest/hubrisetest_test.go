package hubrisetest_test

import (
	"context"
	"encoding/json"
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
