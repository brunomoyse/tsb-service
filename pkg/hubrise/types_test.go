package hubrise_test

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
)

func TestOrderStatusHelpers(t *testing.T) {
	for _, s := range []hubrise.OrderStatus{
		hubrise.StatusNew, hubrise.StatusReceived, hubrise.StatusAccepted, hubrise.StatusInPreparation,
		hubrise.StatusAwaitingCollection, hubrise.StatusInDelivery, hubrise.StatusCompleted, hubrise.StatusRejected,
		hubrise.StatusCancelled, hubrise.StatusDeliveryFailed, hubrise.StatusAwaitingShipment,
	} {
		assert.True(t, s.Valid(), s)
	}
	assert.False(t, hubrise.OrderStatus("").Valid())
	assert.False(t, hubrise.OrderStatus("teleported").Valid())

	assert.Equal(t, hubrise.StatusAwaitingCollection, hubrise.StatusAwaitingShipment.Canonical(), "deprecated alias")
	assert.Equal(t, hubrise.StatusAccepted, hubrise.StatusAccepted.Canonical())

	for s, final := range map[hubrise.OrderStatus]bool{
		hubrise.StatusNew: false, hubrise.StatusAccepted: false, hubrise.StatusInDelivery: false,
		hubrise.StatusCompleted: true, hubrise.StatusRejected: true, hubrise.StatusCancelled: true, hubrise.StatusDeliveryFailed: true,
	} {
		assert.Equal(t, final, s.Final(), s)
	}
}

func TestPointerHelpers(t *testing.T) {
	now := time.Now()
	assert.Equal(t, now, *hubrise.Time(now))
	assert.True(t, *hubrise.Bool(true))
	assert.False(t, *hubrise.Bool(false))
}

func TestOrderOptionQuantityDefaultsToOne(t *testing.T) {
	// Marshalling a zero quantity sends the documented default.
	b, err := json.Marshal(hubrise.OrderOption{OptionListName: "Sauce", Name: "Soja"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"option_list_name":"Sauce","name":"Soja","quantity":1}`, string(b))
	b, err = json.Marshal(hubrise.OrderOption{OptionListName: "Sauce", Name: "Soja", Quantity: 3, Price: "0.50 EUR", Ref: "S"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"option_list_name":"Sauce","name":"Soja","quantity":3,"price":"0.50 EUR","ref":"S"}`, string(b))

	for in, want := range map[string]int{
		`{"option_list_name":"a","name":"b"}`:                 1,
		`{"option_list_name":"a","name":"b","quantity":null}`: 1,
		`{"option_list_name":"a","name":"b","quantity":4}`:    4,
	} {
		var o hubrise.OrderOption
		require.NoError(t, json.Unmarshal([]byte(in), &o), in)
		assert.Equal(t, want, o.Quantity, in)
	}
	var o hubrise.OrderOption
	assert.Error(t, json.Unmarshal([]byte(`{"price":5}`), &o))
}

func TestCatalogDataReadsTheDocumentationsOptionsListsSpelling(t *testing.T) {
	var d hubrise.CatalogData
	require.NoError(t, json.Unmarshal([]byte(`{"options_lists":[{"ref":"A","name":"a","options":[]}]}`), &d))
	require.Len(t, d.OptionLists, 1)
	assert.Equal(t, "A", d.OptionLists[0].Ref)

	var both hubrise.CatalogData
	require.NoError(t, json.Unmarshal([]byte(`{"option_lists":[{"ref":"X","name":"x","options":[]}],"options_lists":[{"ref":"Y","name":"y","options":[]}]}`), &both))
	assert.Equal(t, "X", both.OptionLists[0].Ref, "the correct spelling wins")

	assert.Error(t, json.Unmarshal([]byte(`{"products":"nope"}`), &d))
}

func TestRestrictions(t *testing.T) {
	var nilR *hubrise.Restrictions
	assert.True(t, nilR.IsEnabled())
	assert.True(t, (&hubrise.Restrictions{EndTime: "13:30"}).IsEnabled())
	assert.True(t, (&hubrise.Restrictions{Enabled: hubrise.Bool(true)}).IsEnabled())
	assert.False(t, (&hubrise.Restrictions{Enabled: hubrise.Bool(false)}).IsEnabled())
}

func TestInventoryEntryHelpers(t *testing.T) {
	soon := time.Date(2026, 10, 24, 23, 0, 0, 0, time.UTC)
	sold := hubrise.SoldOutSku("A", &soon)
	assert.True(t, sold.IsSoldOut())
	assert.Equal(t, "A", sold.TargetRef())
	b, _ := json.Marshal(sold)
	assert.JSONEq(t, `{"sku_ref":"A","stock":"0","expires_at":"2026-10-24T23:00:00Z"}`, string(b))

	avail := hubrise.AvailableSku("A")
	assert.False(t, avail.IsSoldOut())
	b, _ = json.Marshal(avail)
	assert.JSONEq(t, `{"sku_ref":"A","stock":null}`, string(b), "a removal is an explicit null stock")

	opt := hubrise.InventoryEntry{OptionRef: "EGG", Stock: ptr("2")}
	assert.Equal(t, "EGG", opt.TargetRef())
	assert.False(t, opt.IsSoldOut())
	assert.True(t, hubrise.InventoryEntry{SkuRef: "B", Stock: ptr("0.000")}.IsSoldOut())
	assert.False(t, hubrise.InventoryEntry{SkuRef: "B", Stock: ptr("garbage")}.IsSoldOut())
}

func TestCallbackJSON(t *testing.T) {
	b, err := json.Marshal(hubrise.Callback{})
	require.NoError(t, err)
	assert.JSONEq(t, `{"url":null,"events":{}}`, string(b))
	b, err = json.Marshal(hubrise.Callback{URL: "https://x/cb", Events: map[string][]string{"order": {"update"}}})
	require.NoError(t, err)
	assert.JSONEq(t, `{"url":"https://x/cb","events":{"order":["update"]}}`, string(b))

	var cb hubrise.Callback
	require.NoError(t, json.Unmarshal([]byte(`{"url":null,"events":{}}`), &cb))
	assert.Empty(t, cb.URL)
	require.NoError(t, json.Unmarshal([]byte(`{"url":"https://x/cb","events":{"catalog":["update"]}}`), &cb))
	assert.Equal(t, "https://x/cb", cb.URL)
	assert.Error(t, json.Unmarshal([]byte(`{"url":5}`), &cb))

	assert.True(t, hubrise.ValidCallbackEvent("order", "create"))
	assert.True(t, hubrise.ValidCallbackEvent("order", "update"))
	assert.True(t, hubrise.ValidCallbackEvent("catalog", "delete"))
	assert.True(t, hubrise.ValidCallbackEvent("inventory", "patch"))
	assert.False(t, hubrise.ValidCallbackEvent("order", "delete"))
	assert.False(t, hubrise.ValidCallbackEvent("inventory", "create"))
	assert.False(t, hubrise.ValidCallbackEvent("customer", "update"))
}

// Every endpoint reports a failure as a typed error (and not a zero value), including the ones the
// scenario tests only exercise on the happy path.
func TestEveryEndpointReportsFailures(t *testing.T) {
	c, _ := stub(t, respond(http.StatusInternalServerError, `{"error_type":"internal_error"}`))
	calls := map[string]func() error{
		"GetLocation":           func() error { _, err := c.GetLocation(ctx); return err },
		"CreateOrder":           func() error { _, err := c.CreateOrder(ctx, &hubrise.Order{}); return err },
		"GetOrder":              func() error { _, err := c.GetOrder(ctx, "o"); return err },
		"UpdateOrder":           func() error { _, err := c.UpdateOrder(ctx, "o", &hubrise.OrderPatch{}); return err },
		"ListOrders":            func() error { _, err := c.ListOrders(ctx, hubrise.OrderFilter{}, ""); return err },
		"FindOrderByPrivateRef": func() error { _, err := c.FindOrderByPrivateRef(ctx, "r"); return err },
		"CreateCustomer":        func() error { _, err := c.CreateCustomer(ctx, "l", &hubrise.Customer{}); return err },
		"UpdateCustomer":        func() error { _, err := c.UpdateCustomer(ctx, "l", "c", &hubrise.Customer{}); return err },
		"AnonymiseCustomer":     func() error { _, err := c.AnonymiseCustomer(ctx, "l", "c"); return err },
		"FindCustomer":          func() error { _, err := c.FindCustomerByPrivateRef(ctx, "l", "r"); return err },
		"GetCatalog":            func() error { _, err := c.GetCatalog(ctx, "c"); return err },
		"GetImage":              func() error { _, err := c.GetImage(ctx, "c", "i"); return err },
		"GetImageData":          func() error { _, err := c.GetImageData(ctx, "c", "i"); return err },
		"GetInventory":          func() error { _, err := c.GetInventory(ctx); return err },
		"ReplaceInventory":      func() error { _, err := c.ReplaceInventory(ctx, nil); return err },
		"PatchInventory":        func() error { _, err := c.PatchInventory(ctx, nil); return err },
		"GetCallback":           func() error { _, err := c.GetCallback(ctx); return err },
		"SetCallback":           func() error { _, err := c.SetCallback(ctx, hubrise.Callback{}); return err },
		"DeleteCallback":        func() error { return c.DeleteCallback(ctx) },
		"ListCallbackEvents":    func() error { _, err := c.ListCallbackEvents(ctx, 0, ""); return err },
		"AckCallbackEvent":      func() error { return c.AckCallbackEvent(ctx, "e") },
	}
	for name, call := range calls {
		err := call()
		require.Error(t, err, name)
		assert.Equal(t, hubrise.KindServer, hubrise.KindOf(err), name)
	}
}

// private_ref is a server side filter; a server that ignores it must not make the client adopt
// somebody else's order.
func TestFindOrderByPrivateRefChecksTheMatch(t *testing.T) {
	c, _ := stub(t, func(w http.ResponseWriter, r *http.Request) {
		respond(200, `[{"id":"o1","private_ref":"other"},{"id":"o2","private_ref":"wanted","created_at":"2026-10-24T12:00:00+02:00"},{"id":"o3","private_ref":"wanted","created_at":"2026-10-24T11:00:00+02:00"}]`)(w, r)
	})
	found, err := c.FindOrderByPrivateRef(ctx, "wanted")
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, "o3", found.ID, "the earliest of the matches")
}

// The same for customers: a server that ignores the filter must not make the client adopt somebody
// else's customer.
func TestFindCustomerByPrivateRefChecksTheMatch(t *testing.T) {
	c, _ := stub(t, func(w http.ResponseWriter, r *http.Request) {
		respond(200, `[{"id":"c1","private_ref":"other"},{"id":"c2","private_ref":"wanted","created_at":"2026-10-24T12:00:00+02:00"},{"id":"c3","private_ref":"wanted","created_at":"2026-10-24T11:00:00+02:00"}]`)(w, r)
	})
	found, err := c.FindCustomerByPrivateRef(ctx, "l", "wanted")
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, "c3", found.ID, "the earliest of the matches")
	none, err := c.FindCustomerByPrivateRef(ctx, "l", "ghost")
	require.NoError(t, err)
	assert.Nil(t, none)
}

func TestOAuthBodyReadFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("short"))
	}))
	t.Cleanup(srv.Close)
	o, err := hubrise.NewOAuth(srv.URL, "cid", "csecret")
	require.NoError(t, err)
	_, err = o.ExchangeCode(context.Background(), "code")
	assert.Equal(t, hubrise.KindNetwork, hubrise.KindOf(err))
}
