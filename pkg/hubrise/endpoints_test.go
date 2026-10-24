package hubrise_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/pkg/hubrise"
	"tsb-service/pkg/hubrise/hubrisetest"
	"tsb-service/pkg/timezone"
)

var ctx = context.Background()

// webshopOrder is an order as the webshop pushes it: 2 x (10.00 + 0.50 option) + 2.50 delivery
// - 1.00 discount = 22.50.
func webshopOrder(privateRef string) *hubrise.Order {
	return &hubrise.Order{
		Status: hubrise.StatusNew, PrivateRef: privateRef, Ref: "ABCD1234", Channel: "webshop",
		ServiceType: hubrise.ServiceDelivery, Asap: hubrise.Bool(true),
		Items: []hubrise.OrderItem{{
			ProductName: "Maki saumon", SkuName: "12 pieces", SkuRef: "MAKI-SAUMON-12",
			Price: "10.00 EUR", Quantity: "2", TaxRate: "6",
			Options: []hubrise.OrderOption{{OptionListName: "Sauce", Name: "Soja sucree", Ref: "SAUCE-SUCREE", Price: "0.50 EUR", Quantity: 1}},
		}},
		Charges:   []hubrise.OrderCharge{{Name: "Delivery", Ref: "DELIVERY", Price: "2.50 EUR"}},
		Discounts: []hubrise.OrderDiscount{{Name: "Pickup", Ref: "TAKEAWAY", PriceOff: "1.00 EUR"}},
		Payments:  []hubrise.OrderPayment{{Name: "Mollie", Ref: "MOLLIE", Amount: "22.50 EUR", Info: json.RawMessage(`{"mollie_payment_id":"tr_123"}`)}},
		Customer:  &hubrise.Customer{FirstName: "Ada", LastName: "Lovelace", Email: "ada@example.com", Phone: "+32470000000"},
	}
}

func TestCreateAndRetrieveAnOrder(t *testing.T) {
	srv := hubrisetest.New(t)
	c := srv.NewClient(t)

	// The request claims a total: HubRise calculates its own.
	in := webshopOrder("uuid-1")
	in.Total = "999.00 EUR"
	got, err := c.CreateOrder(ctx, in)
	require.NoError(t, err)
	assert.NotEmpty(t, got.ID)
	assert.Equal(t, hubrisetest.LocationID, got.LocationID)
	assert.Equal(t, "tsb-webshop", got.CreatedBy)
	assert.Equal(t, hubrise.Money("22.50 EUR"), got.Total, "total = items + charges - discounts, as HubRise calculates it")
	assert.Equal(t, hubrise.Money("0.00 EUR"), got.PaymentDiscrepancy)
	assert.Equal(t, hubrise.Money("21.00 EUR"), got.Items[0].Subtotal)
	assert.NotEmpty(t, got.Items[0].ID)
	assert.NotEmpty(t, got.Charges[0].ID)
	assert.NotEmpty(t, got.Discounts[0].ID)
	assert.NotEmpty(t, got.Payments[0].ID)
	assert.Equal(t, "uuid-1", got.PrivateRef)
	_, offset := got.CreatedAt.Zone()
	_, brussels := got.CreatedAt.In(timezone.Location).Zone()
	assert.Equal(t, brussels, offset, "timestamps are in the restaurant time zone")
	assert.JSONEq(t, `{"mollie_payment_id":"tr_123"}`, string(got.Payments[0].Info))

	again, err := c.GetOrder(ctx, got.ID)
	require.NoError(t, err)
	assert.Equal(t, got.Total, again.Total)
	assert.Equal(t, got.Status, again.Status)

	// What went over the wire: percent tax rate, option quantity, no response-only fields.
	reqs := srv.RequestsTo("POST", "/v1/location/orders")
	require.Len(t, reqs, 1)
	var wire map[string]any
	require.NoError(t, json.Unmarshal(reqs[0].Body, &wire))
	item := wire["items"].([]any)[0].(map[string]any)
	assert.Equal(t, "6", item["tax_rate"])
	assert.Equal(t, "2", item["quantity"])
	assert.EqualValues(t, 1, item["options"].([]any)[0].(map[string]any)["quantity"])
	assert.NotContains(t, wire, "id")
	assert.Equal(t, "tsb-webshop", reqs[0].Client)
}

func TestTheMockRefusesWhatHubRiseRefuses(t *testing.T) {
	srv := hubrisetest.New(t)
	c := srv.NewClient(t)

	for name, mutate := range map[string]func(o *hubrise.Order){
		"no status":           func(o *hubrise.Order) { o.Status = "" },
		"unknown status":      func(o *hubrise.Order) { o.Status = "teleported" },
		"bad amount":          func(o *hubrise.Order) { o.Items[0].Price = "abc" },
		"wrong currency":      func(o *hubrise.Order) { o.Charges[0].Price = "2.50 USD" },
		"ratio as a tax rate": func(o *hubrise.Order) { o.Items[0].TaxRate = "106" },
		"zero quantity":       func(o *hubrise.Order) { o.Items[0].Quantity = "0" },
		"customer twice":      func(o *hubrise.Order) { o.CustomerID = "c1" },
		"unknown service":     func(o *hubrise.Order) { o.ServiceType = "teleport" },
		"item without a name": func(o *hubrise.Order) { o.Items[0].ProductName = "" },
		"option without name": func(o *hubrise.Order) { o.Items[0].Options[0].Name = "" },
		"discount no amount":  func(o *hubrise.Order) { o.Discounts[0].PriceOff = "" },
		"payment no amount":   func(o *hubrise.Order) { o.Payments[0].Amount = "" },
	} {
		t.Run(name, func(t *testing.T) {
			o := webshopOrder("uuid-x")
			mutate(o)
			_, err := c.CreateOrder(ctx, o)
			var he *hubrise.Error
			require.ErrorAs(t, err, &he)
			assert.Equal(t, hubrise.KindInvalid, he.Kind)
			assert.Equal(t, 422, he.Status)
			assert.NotEmpty(t, he.Fields, "a 422 names the fields")
		})
	}
	assert.Empty(t, srv.Orders(), "a refused order is never stored")
}

func TestPrivateRefIsOnlyVisibleToTheClientThatSetIt(t *testing.T) {
	srv := hubrisetest.New(t)
	c := srv.NewClient(t)

	mine, err := c.CreateOrder(ctx, webshopOrder("mine"))
	require.NoError(t, err)

	foreign := webshopOrder("theirs")
	theirs, err := srv.InjectOrder(*foreign)
	require.NoError(t, err)

	// The other client's private_ref is blank to us, and cannot be looked up.
	seen, err := c.GetOrder(ctx, theirs.ID)
	require.NoError(t, err)
	assert.Empty(t, seen.PrivateRef)
	none, err := c.FindOrderByPrivateRef(ctx, "theirs")
	require.NoError(t, err)
	assert.Nil(t, none)

	// Ours is visible to us and found.
	found, err := c.FindOrderByPrivateRef(ctx, "mine")
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, mine.ID, found.ID)

	// Nothing matches an unknown ref.
	none, err = c.FindOrderByPrivateRef(ctx, "nope")
	require.NoError(t, err)
	assert.Nil(t, none)

	// And the other client does not see ours.
	stored, ok := srv.OrderByPrivateRef("mine")
	require.True(t, ok)
	assert.Equal(t, mine.ID, stored.ID)
	_, ok = srv.OrderByPrivateRef("theirs")
	assert.False(t, ok, "only orders created by the system under test")
}

func TestFindOrderByPrivateRefReturnsTheEarliestOfADoubleCreate(t *testing.T) {
	clock := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	srv := hubrisetest.New(t, hubrisetest.WithClock(func() time.Time { clock = clock.Add(time.Minute); return clock }))
	c := srv.NewClient(t)
	first, err := c.CreateOrder(ctx, webshopOrder("dup"))
	require.NoError(t, err)
	_, err = c.CreateOrder(ctx, webshopOrder("dup"))
	require.NoError(t, err)

	found, err := c.FindOrderByPrivateRef(ctx, "dup")
	require.NoError(t, err)
	assert.Equal(t, first.ID, found.ID)
}

func TestUpdateOrder(t *testing.T) {
	srv := hubrisetest.New(t)
	c := srv.NewClient(t)
	o, err := c.CreateOrder(ctx, webshopOrder("u1"))
	require.NoError(t, err)

	confirmed := time.Date(2026, 10, 24, 19, 30, 0, 0, time.UTC)
	got, err := c.UpdateOrder(ctx, o.ID, &hubrise.OrderPatch{
		Status: hubrise.StatusAccepted, ConfirmedTime: &confirmed, SellerNotes: "no wasabi", CollectionCode: "A17",
	})
	require.NoError(t, err)
	assert.Equal(t, hubrise.StatusAccepted, got.Status)
	assert.Equal(t, "no wasabi", got.SellerNotes)
	assert.Equal(t, "A17", got.CollectionCode)
	require.NotNil(t, got.ConfirmedTime)
	assert.True(t, got.ConfirmedTime.Equal(confirmed))
	assert.Equal(t, hubrise.Money("22.50 EUR"), got.Total)

	// Collections: delete, add, set a private ref. HubRise never modifies an element in place.
	got, err = c.UpdateOrder(ctx, o.ID, &hubrise.OrderPatch{
		Discounts: []hubrise.OrderDiscount{{ID: o.Discounts[0].ID, Deleted: true}},
		Charges:   []hubrise.OrderCharge{{Name: "Rounding", Ref: "ROUNDING", Price: "0.10 EUR"}},
		Items:     []hubrise.OrderItem{{ID: o.Items[0].ID, PrivateRef: "line-1"}},
	})
	require.NoError(t, err)
	assert.Equal(t, hubrise.Money("23.60 EUR"), got.Total, "21.00 + 2.50 + 0.10, discount deleted")
	assert.Equal(t, hubrise.Money("-1.10 EUR"), got.PaymentDiscrepancy)
	assert.Equal(t, "line-1", got.Items[0].PrivateRef)
	require.Len(t, got.Charges, 2)
	assert.NotEmpty(t, got.Charges[1].ID)

	// Payments can be added too.
	got, err = c.UpdateOrder(ctx, o.ID, &hubrise.OrderPatch{
		Payments: []hubrise.OrderPayment{{ID: o.Payments[0].ID, Deleted: true}, {Name: "Cash", Ref: "CASH", Amount: "23.60 EUR"}},
	})
	require.NoError(t, err)
	assert.Equal(t, hubrise.Money("0.00 EUR"), got.PaymentDiscrepancy)

	for name, patch := range map[string]*hubrise.OrderPatch{
		"unknown element":  {Charges: []hubrise.OrderCharge{{ID: "ghost", Deleted: true}}},
		"modify an item":   {Items: []hubrise.OrderItem{{ID: o.Items[0].ID, Quantity: "9"}}},
		"unknown status":   {Status: "teleported"},
		"invalid addition": {Charges: []hubrise.OrderCharge{{Name: "Bad", Price: "x"}}},
	} {
		_, err = c.UpdateOrder(ctx, o.ID, patch)
		assert.Equal(t, hubrise.KindInvalid, hubrise.KindOf(err), name)
	}

	_, err = c.UpdateOrder(ctx, "ghost", &hubrise.OrderPatch{Status: hubrise.StatusAccepted})
	assert.Equal(t, hubrise.KindNotFound, hubrise.KindOf(err))
	_, err = c.GetOrder(ctx, "ghost")
	assert.Equal(t, hubrise.KindNotFound, hubrise.KindOf(err))
}

func TestStrictTransitionsLockFinalStatuses(t *testing.T) {
	srv := hubrisetest.New(t, hubrisetest.WithStrictTransitions())
	c := srv.NewClient(t)
	o, err := c.CreateOrder(ctx, webshopOrder("s1"))
	require.NoError(t, err)

	_, err = c.UpdateOrder(ctx, o.ID, &hubrise.OrderPatch{Status: hubrise.StatusAccepted})
	require.NoError(t, err)
	_, err = c.UpdateOrder(ctx, o.ID, &hubrise.OrderPatch{Status: hubrise.StatusNew})
	assert.Equal(t, hubrise.KindInvalid, hubrise.KindOf(err), "nothing goes back to new")
	_, err = c.UpdateOrder(ctx, o.ID, &hubrise.OrderPatch{Status: hubrise.StatusCancelled})
	require.NoError(t, err)
	_, err = c.UpdateOrder(ctx, o.ID, &hubrise.OrderPatch{Status: hubrise.StatusAccepted})
	assert.Equal(t, hubrise.KindInvalid, hubrise.KindOf(err), "cancelled is final")
	_, err = c.UpdateOrder(ctx, o.ID, &hubrise.OrderPatch{Status: hubrise.StatusCancelled})
	require.NoError(t, err, "staying in a status is fine")
}

func TestByDefaultAnyStatusMayFollowAnother(t *testing.T) {
	srv := hubrisetest.New(t)
	c := srv.NewClient(t)
	o, err := c.CreateOrder(ctx, webshopOrder("p1"))
	require.NoError(t, err)
	for _, st := range []hubrise.OrderStatus{hubrise.StatusCompleted, hubrise.StatusNew, hubrise.StatusAwaitingShipment, hubrise.StatusAccepted} {
		_, err = c.UpdateOrder(ctx, o.ID, &hubrise.OrderPatch{Status: st})
		require.NoError(t, err, st)
	}
}

func TestListOrders(t *testing.T) {
	clock := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	srv := hubrisetest.New(t, hubrisetest.WithClock(func() time.Time { clock = clock.Add(time.Hour); return clock }))
	c := srv.NewClient(t)
	var ids []string
	for i := range 5 {
		o, err := c.CreateOrder(ctx, webshopOrder(fmt.Sprintf("l%d", i)))
		require.NoError(t, err)
		ids = append(ids, o.ID)
	}
	_, err := srv.InjectOrder(hubrise.Order{Status: hubrise.StatusReceived, Items: []hubrise.OrderItem{{ProductName: "x", Price: "1.00 EUR", Quantity: "1"}}})
	require.NoError(t, err)

	// Newest first, paginated through X-Cursor-Next.
	var seen []string
	cursor := ""
	pages := 0
	for {
		page, err := c.ListOrders(ctx, hubrise.OrderFilter{CreatedBy: "tsb-webshop", Count: 2}, cursor)
		require.NoError(t, err)
		pages++
		for _, o := range page.Items {
			seen = append(seen, o.ID)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	assert.Equal(t, 3, pages)
	assert.Equal(t, []string{ids[4], ids[3], ids[2], ids[1], ids[0]}, seen)

	status, err := c.ListOrders(ctx, hubrise.OrderFilter{Status: hubrise.StatusReceived}, "")
	require.NoError(t, err)
	assert.Len(t, status.Items, 1)

	third, _ := srv.Order(ids[2])
	window, err := c.ListOrders(ctx, hubrise.OrderFilter{After: third.CreatedAt, Before: third.CreatedAt.Add(time.Minute), CreatedBy: "tsb-webshop"}, "")
	require.NoError(t, err)
	require.Len(t, window.Items, 1, "after is inclusive, before exclusive")
	assert.Equal(t, ids[2], window.Items[0].ID)

	byCustomer, err := c.ListOrders(ctx, hubrise.OrderFilter{CustomerID: "nobody"}, "")
	require.NoError(t, err)
	assert.Empty(t, byCustomer.Items)

	_, err = c.ListOrders(ctx, hubrise.OrderFilter{Count: 1000}, "")
	assert.Equal(t, hubrise.KindInvalid, hubrise.KindOf(err), "count is capped at 100")
	_, err = c.ListOrders(ctx, hubrise.OrderFilter{}, "garbage")
	assert.Equal(t, hubrise.KindInvalid, hubrise.KindOf(err))
	past, err := c.ListOrders(ctx, hubrise.OrderFilter{}, "999")
	require.NoError(t, err)
	assert.Empty(t, past.Items)
}

func TestGetLocationAndCatalog(t *testing.T) {
	srv := hubrisetest.New(t)
	c := srv.NewClient(t)

	loc, err := c.GetLocation(ctx)
	require.NoError(t, err)
	assert.Equal(t, hubrisetest.LocationID, loc.ID)
	assert.Equal(t, hubrisetest.CatalogID, loc.CatalogID)
	assert.Equal(t, hubrisetest.CustomerListID, loc.CustomerListID)
	assert.Equal(t, "Europe/Brussels", loc.Timezone.Name)

	cat, err := c.GetCatalog(ctx, loc.CatalogID)
	require.NoError(t, err)
	require.NotNil(t, cat.Data)
	assert.Equal(t, hubrisetest.CatalogName, cat.Name)
	require.Len(t, cat.Data.Products, 5)
	maki := cat.Data.Products[0]
	assert.Equal(t, "MAKI-SAUMON", maki.Ref)
	assert.NotEmpty(t, maki.CategoryID, "a response names the category by id, not by ref")
	assert.Empty(t, maki.CategoryRef)
	require.Len(t, maki.Skus, 2)
	assert.Equal(t, "MAKI-SAUMON-6", maki.Skus[0].Ref)
	assert.Equal(t, hubrise.Money("5.50 EUR"), maki.Skus[0].Price)
	assert.Len(t, maki.Skus[0].OptionListIDs, 1)
	assert.Empty(t, maki.Skus[0].OptionListRefs)
	assert.Equal(t, maki.ID, maki.Skus[0].ProductID)
	require.NotNil(t, maki.TaxRate)
	assert.Equal(t, "6.0", maki.TaxRate.Collection)
	assert.Equal(t, cat.Data.OptionLists[0].ID, maki.Skus[0].OptionListIDs[0])
	assert.Equal(t, cat.Data.OptionLists[0].ID, cat.Data.OptionLists[0].Options[0].OptionListID)

	_, err = c.GetCatalog(ctx, "other-catalog")
	assert.Equal(t, hubrise.KindNotFound, hubrise.KindOf(err))
}

func TestReplaceCatalogIsValidatedWholeAndAnnounced(t *testing.T) {
	srv := hubrisetest.New(t)
	c := srv.NewClient(t)
	_, err := c.SetCallback(ctx, hubrise.Callback{Events: map[string][]string{"catalog": {"create", "update", "delete"}}})
	require.NoError(t, err)

	data := hubrisetest.SeedCatalog()
	data.Products[0].Skus[0].Price = "6.00 EUR"
	require.NoError(t, srv.ReplaceCatalog(data))
	cat, err := c.GetCatalog(ctx, hubrisetest.CatalogID)
	require.NoError(t, err)
	assert.Equal(t, hubrise.Money("6.00 EUR"), cat.Data.Products[0].Skus[0].Price)

	events := srv.Events(hubrisetest.Token)
	require.Len(t, events, 1)
	assert.True(t, events[0].Is("catalog", "update"))
	assert.Equal(t, hubrisetest.CatalogID, events[0].CatalogID)
	assert.Empty(t, events[0].NewState, "catalog events carry no state: re-fetch")

	srv.DeleteCatalog()
	assert.True(t, srv.Events(hubrisetest.Token)[1].Is("catalog", "delete"))

	bad := hubrisetest.SeedCatalog()
	bad.Products[0].CategoryRef = "NOPE"
	bad.Products[1].Skus = nil
	bad.Products[2].Skus[0].Ref = bad.Products[0].Skus[0].Ref
	bad.Products[3].Skus[0].OptionListRefs = []string{"GHOST"}
	bad.Products[3].TaxRate = &hubrise.TaxRate{Delivery: "6"}
	bad.Categories = append(bad.Categories, hubrise.Category{Ref: "MAKI", Name: "dup"}, hubrise.Category{Ref: "ORPHAN", ParentRef: "GHOST", Name: "o"})
	bad.OptionLists[0].Options = nil
	bad.OptionLists[1].MinSelections = 5
	max := 1
	bad.OptionLists[1].MaxSelections = &max
	bad.Charges[0].Price = "free"
	err = srv.ReplaceCatalog(bad)
	require.Error(t, err)
	for _, want := range []string{"unknown category ref", "at least one sku", "duplicate ref", "unknown option list ref", "delivery, collection and eat_in", "needs at least one option", "must not exceed", "valid monetary amount"} {
		assert.Contains(t, err.Error(), want)
	}
	cat, _ = c.GetCatalog(ctx, hubrisetest.CatalogID)
	assert.Equal(t, hubrise.Money("6.00 EUR"), cat.Data.Products[0].Skus[0].Price, "an invalid upload is refused whole")
}

func TestImages(t *testing.T) {
	srv := hubrisetest.New(t)
	c := srv.NewClient(t)
	png := []byte("\x89PNG-bytes")
	meta := srv.AddImage("img1", "image/png", png)
	assert.Equal(t, len(png), meta.Size)
	assert.Len(t, meta.MD5, 32)

	got, err := c.GetImage(ctx, hubrisetest.CatalogID, "img1")
	require.NoError(t, err)
	assert.Equal(t, meta, *got)

	data, err := c.GetImageData(ctx, hubrisetest.CatalogID, "img1")
	require.NoError(t, err)
	assert.Equal(t, png, data.Data)
	assert.Equal(t, "image/png", data.ContentType)

	_, err = c.GetImage(ctx, hubrisetest.CatalogID, "ghost")
	assert.Equal(t, hubrise.KindNotFound, hubrise.KindOf(err))
	_, err = c.GetImageData(ctx, "other", "img1")
	assert.Equal(t, hubrise.KindNotFound, hubrise.KindOf(err))
}

func TestInventory(t *testing.T) {
	srv := hubrisetest.New(t)
	c := srv.NewClient(t)

	empty, err := c.GetInventory(ctx)
	require.NoError(t, err)
	assert.NotNil(t, empty)
	assert.Empty(t, empty)

	expires := time.Date(2026, 10, 24, 23, 0, 0, 0, time.UTC)
	patched, err := c.PatchInventory(ctx, []hubrise.InventoryEntry{
		hubrise.SoldOutSku("MAKI-SAUMON-6", &expires),
		hubrise.SoldOutSku("COCA-33", nil),
		{OptionRef: "EXTRA-AVOCAT", Stock: ptr("3")},
	})
	require.NoError(t, err)
	assert.Len(t, patched, 3)

	got, err := c.GetInventory(ctx)
	require.NoError(t, err)
	require.Len(t, got, 3)
	byRef := map[string]hubrise.InventoryEntry{}
	for _, e := range got {
		byRef[e.TargetRef()] = e
	}
	assert.True(t, byRef["MAKI-SAUMON-6"].IsSoldOut())
	assert.True(t, byRef["MAKI-SAUMON-6"].ExpiresAt.Equal(expires))
	assert.True(t, byRef["COCA-33"].IsSoldOut())
	assert.False(t, byRef["EXTRA-AVOCAT"].IsSoldOut())

	// A nil stock removes the entry: the SKU is available again.
	_, err = c.PatchInventory(ctx, []hubrise.InventoryEntry{hubrise.AvailableSku("COCA-33")})
	require.NoError(t, err)
	got, _ = c.GetInventory(ctx)
	assert.Len(t, got, 2)

	// PUT replaces everything; entries without stock are dropped.
	put, err := c.ReplaceInventory(ctx, []hubrise.InventoryEntry{hubrise.SoldOutSku("ASAHI-33", nil), hubrise.AvailableSku("MISO-1")})
	require.NoError(t, err)
	require.Len(t, put, 1)
	assert.Equal(t, "ASAHI-33", put[0].SkuRef)
	cleared, err := c.ReplaceInventory(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, cleared)

	for name, entries := range map[string][]hubrise.InventoryEntry{
		"unknown sku":      {hubrise.SoldOutSku("GHOST", nil)},
		"unknown option":   {{OptionRef: "GHOST", Stock: ptr("0")}},
		"both refs":        {{SkuRef: "MISO-1", OptionRef: "EXTRA-AVOCAT", Stock: ptr("0")}},
		"no ref":           {{Stock: ptr("0")}},
		"negative stock":   {{SkuRef: "MISO-1", Stock: ptr("-1")}},
		"4 decimals":       {{SkuRef: "MISO-1", Stock: ptr("1.2345")}},
		"expiry with some": {{SkuRef: "MISO-1", Stock: ptr("2"), ExpiresAt: &expires}},
		"expiry no stock":  {{SkuRef: "MISO-1", ExpiresAt: &expires}},
	} {
		_, err = c.PatchInventory(ctx, entries)
		assert.Equal(t, hubrise.KindInvalid, hubrise.KindOf(err), name)
		_, err = c.ReplaceInventory(ctx, entries)
		assert.Equal(t, hubrise.KindInvalid, hubrise.KindOf(err), name)
	}
}

func ptr[T any](v T) *T { return &v }

func TestInventoryScopedRoutesAndControl(t *testing.T) {
	srv := hubrisetest.New(t)
	c := srv.NewClient(t)
	_, err := c.SetCallback(ctx, hubrise.Callback{Events: map[string][]string{"inventory": {"update", "patch"}}})
	require.NoError(t, err)

	require.NoError(t, srv.SetInventory([]hubrise.InventoryEntry{hubrise.SoldOutSku("MISO-1", nil)}))
	require.NoError(t, srv.PatchInventory([]hubrise.InventoryEntry{hubrise.SoldOutSku("COCA-33", nil)}))
	assert.Len(t, srv.Inventory(), 2)
	events := srv.Events(hubrisetest.Token)
	require.Len(t, events, 2)
	assert.True(t, events[0].Is("inventory", "update"))
	assert.True(t, events[1].Is("inventory", "patch"))
	assert.Contains(t, string(events[1].StateChange), "COCA-33")
	assert.Empty(t, events[0].StateChange)

	assert.Error(t, srv.SetInventory([]hubrise.InventoryEntry{hubrise.SoldOutSku("GHOST", nil)}))
	assert.Error(t, srv.PatchInventory([]hubrise.InventoryEntry{hubrise.SoldOutSku("GHOST", nil)}))

	// The documented scoped routes serve the same inventory.
	for _, path := range []string{
		"/v1/catalogs/" + hubrisetest.CatalogID + "/location/inventory",
		"/v1/catalogs/" + hubrisetest.CatalogID + "/locations/" + hubrisetest.LocationID + "/inventory",
	} {
		resp := rawRequest(t, srv, http.MethodGet, path, hubrisetest.Token, "")
		assert.Equal(t, 200, resp.StatusCode, path)
	}
	for _, path := range []string{
		"/v1/catalogs/ghost/location/inventory",
		"/v1/catalogs/" + hubrisetest.CatalogID + "/locations/ghost/inventory",
	} {
		resp := rawRequest(t, srv, http.MethodGet, path, hubrisetest.Token, "")
		assert.Equal(t, 404, resp.StatusCode, path)
	}

	// An entry with an expiry passed is released, announced as the documented inventory.patch.
	clock := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	srv2 := hubrisetest.New(t, hubrisetest.WithClock(func() time.Time { return clock }))
	c2 := srv2.NewClient(t)
	_, err = c2.SetCallback(ctx, hubrise.Callback{Events: map[string][]string{"inventory": {"patch"}}})
	require.NoError(t, err)
	past, future := clock.Add(-time.Minute), clock.Add(time.Hour)
	require.NoError(t, srv2.SetInventory([]hubrise.InventoryEntry{hubrise.SoldOutSku("MISO-1", &past), hubrise.SoldOutSku("COCA-33", &future), hubrise.SoldOutSku("ASAHI-33", nil)}))
	expired := srv2.ExpireInventory()
	require.Len(t, expired, 1)
	assert.Equal(t, "MISO-1", expired[0].SkuRef)
	assert.Len(t, srv2.Inventory(), 2)
	require.Len(t, srv2.Events(hubrisetest.Token), 1)
	assert.Empty(t, srv2.ExpireInventory(), "nothing left to expire, no event")
	assert.Len(t, srv2.Events(hubrisetest.Token), 1)
}

func rawRequest(t *testing.T, srv *hubrisetest.Server, method, path, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, nil)
	require.NoError(t, err)
	if body != "" {
		req, err = http.NewRequest(method, srv.URL+path, stringsReader(body))
		require.NoError(t, err)
	}
	if token != "" {
		req.Header.Set("X-Access-Token", token)
	}
	resp, err := srv.HTTPClient().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestCallbackRegistration(t *testing.T) {
	srv := hubrisetest.New(t)
	c := srv.NewClient(t)

	none, err := c.GetCallback(ctx)
	require.NoError(t, err)
	assert.Empty(t, none.URL)
	assert.Empty(t, none.Events)

	events := map[string][]string{"order": {"update"}, "catalog": {"create", "update", "delete"}, "inventory": {"update", "patch"}}
	set, err := c.SetCallback(ctx, hubrise.Callback{URL: "https://example.test/hubrise", Events: events})
	require.NoError(t, err)
	assert.Equal(t, "https://example.test/hubrise", set.URL)
	assert.Equal(t, events, set.Events)

	got, err := c.GetCallback(ctx)
	require.NoError(t, err)
	assert.Equal(t, *set, *got)
	assert.Equal(t, set, srv.Callback(hubrisetest.Token))
	assert.Nil(t, srv.Callback(hubrisetest.EPOSToken))
	assert.Nil(t, srv.Callback("nobody"))

	// A new registration replaces the old one; an empty URL is a passive callback (url: null).
	passive, err := c.SetCallback(ctx, hubrise.Callback{Events: map[string][]string{"order": {"create", "update"}}})
	require.NoError(t, err)
	assert.Empty(t, passive.URL)
	reqs := srv.RequestsTo("POST", "/v1/callback")
	assert.Contains(t, string(reqs[len(reqs)-1].Body), `"url":null`)

	for name, cb := range map[string]hubrise.Callback{
		"customer events":       {Events: map[string][]string{"customer": {"create"}}},
		"order delete":          {Events: map[string][]string{"order": {"delete"}}},
		"inventory create":      {Events: map[string][]string{"inventory": {"create"}}},
		"unknown resource":      {Events: map[string][]string{"banana": {"update"}}},
		"url that is not a url": {URL: "ftp://x", Events: events},
	} {
		_, err = c.SetCallback(ctx, cb)
		assert.Equal(t, hubrise.KindInvalid, hubrise.KindOf(err), name)
	}
	still, _ := c.GetCallback(ctx)
	assert.Equal(t, *passive, *still, "a refused registration changes nothing")

	require.NoError(t, c.DeleteCallback(ctx))
	gone, err := c.GetCallback(ctx)
	require.NoError(t, err)
	assert.Empty(t, gone.Events)
}

func TestEventsAreOnlyForOtherClientsAndRegisteredKinds(t *testing.T) {
	srv := hubrisetest.New(t)
	c := srv.NewClient(t)

	// No callback registered yet: no event is kept.
	o, err := c.CreateOrder(ctx, webshopOrder("e1"))
	require.NoError(t, err)
	require.NoError(t, srv.SetOrderStatus(o.ID, hubrise.StatusReceived))
	assert.Empty(t, srv.Events(hubrisetest.Token))

	_, err = c.SetCallback(ctx, hubrise.Callback{Events: map[string][]string{"order": {"update"}}})
	require.NoError(t, err)

	// The EPOS moves the order: we are told, with the previous and new state, and our private_ref.
	require.NoError(t, srv.SetOrderStatus(o.ID, hubrise.StatusAccepted))
	events := srv.Events(hubrisetest.Token)
	require.Len(t, events, 1)
	ev := events[0]
	assert.True(t, ev.Is("order", "update"))
	assert.Equal(t, o.ID, ev.OrderID)
	assert.Equal(t, hubrisetest.LocationID, ev.LocationID)
	assert.NotEmpty(t, ev.ID)
	assert.False(t, ev.CreatedAt.IsZero())
	newOrder, err := ev.NewOrder()
	require.NoError(t, err)
	assert.Equal(t, hubrise.StatusAccepted, newOrder.Status)
	assert.Equal(t, "e1", newOrder.PrivateRef, "the creating client sees its private_ref in the event")
	var prev hubrise.Order
	require.NoError(t, json.Unmarshal(ev.PreviousState, &prev))
	assert.Equal(t, hubrise.StatusReceived, prev.Status)

	// Our own PATCH produces no event for us...
	_, err = c.UpdateOrder(ctx, o.ID, &hubrise.OrderPatch{Status: hubrise.StatusInPreparation})
	require.NoError(t, err)
	assert.Len(t, srv.Events(hubrisetest.Token), 1)

	// ...but the EPOS, if it registered, hears about it, without our private_ref.
	epos, err := hubrise.New(srv.APIBaseURL(), hubrisetest.EPOSToken, hubrise.WithHTTPClient(srv.HTTPClient()), hubrise.WithLimiter(hubrise.NewLimiter(hubrise.Limits{})))
	require.NoError(t, err)
	_, err = epos.SetCallback(ctx, hubrise.Callback{Events: map[string][]string{"order": {"create", "update"}}})
	require.NoError(t, err)
	_, err = c.UpdateOrder(ctx, o.ID, &hubrise.OrderPatch{Status: hubrise.StatusAwaitingCollection})
	require.NoError(t, err)
	eposEvents := srv.Events(hubrisetest.EPOSToken)
	require.Len(t, eposEvents, 1)
	seenByEpos, _ := eposEvents[0].NewOrder()
	assert.Empty(t, seenByEpos.PrivateRef)

	// An order created by another channel: order.create only reaches those who registered for it.
	_, err = srv.InjectOrder(hubrise.Order{Status: hubrise.StatusNew, Items: []hubrise.OrderItem{{ProductName: "x", Price: "1.00 EUR", Quantity: "1"}}})
	require.NoError(t, err)
	assert.Len(t, srv.Events(hubrisetest.Token), 1, "we only registered for order.update")
	_, err = c.CreateOrder(ctx, webshopOrder("e2"))
	require.NoError(t, err)
	assert.Len(t, srv.Events(hubrisetest.EPOSToken), 2, "the EPOS registered for order.create and hears about our order")
	assert.True(t, srv.Events(hubrisetest.EPOSToken)[1].Is("order", "create"))
	assert.Nil(t, srv.Events("nobody"))

	// Failing to decode a missing state.
	_, err = (&hubrise.Event{ID: "x"}).NewOrder()
	assert.Error(t, err)
	_, err = (&hubrise.Event{ID: "x", NewState: json.RawMessage(`[1]`)}).NewOrder()
	assert.Error(t, err)
}

func TestPollingCallbackEventsAndAcknowledging(t *testing.T) {
	srv := hubrisetest.New(t)
	c := srv.NewClient(t)
	_, err := c.SetCallback(ctx, hubrise.Callback{Events: map[string][]string{"order": {"update"}}})
	require.NoError(t, err)
	o, err := c.CreateOrder(ctx, webshopOrder("poll"))
	require.NoError(t, err)
	for _, st := range []hubrise.OrderStatus{hubrise.StatusReceived, hubrise.StatusAccepted, hubrise.StatusInPreparation} {
		require.NoError(t, srv.SetOrderStatus(o.ID, st))
	}

	page, err := c.ListCallbackEvents(ctx, 2, "")
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	assert.NotEmpty(t, page.NextCursor)
	for _, e := range page.Items {
		assert.Empty(t, e.NewState, "the list carries no state fields")
		assert.Empty(t, e.PreviousState)
		assert.Equal(t, o.ID, e.OrderID)
	}
	next, err := c.ListCallbackEvents(ctx, 2, page.NextCursor)
	require.NoError(t, err)
	require.Len(t, next.Items, 1)
	assert.Empty(t, next.NextCursor)

	for _, e := range append(page.Items, next.Items...) {
		require.NoError(t, c.AckCallbackEvent(ctx, e.ID))
	}
	assert.Empty(t, srv.Events(hubrisetest.Token))
	assert.Equal(t, hubrise.KindNotFound, hubrise.KindOf(c.AckCallbackEvent(ctx, page.Items[0].ID)))
	done, err := c.ListCallbackEvents(ctx, 0, "")
	require.NoError(t, err)
	assert.Empty(t, done.Items)
}

func TestDispatchSignsEventsLikeHubRise(t *testing.T) {
	srv := hubrisetest.New(t)
	c := srv.NewClient(t)

	var received atomic.Int32
	status := atomic.Int32{}
	status.Store(200)
	var lastBody []byte
	var lastSig string
	var badSigs atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		lastBody, lastSig = body, r.Header.Get(hubrise.SignatureHeader)
		if !hubrise.VerifySignature(hubrisetest.ClientSecret, body, lastSig) {
			badSigs.Add(1)
		}
		received.Add(1)
		w.WriteHeader(int(status.Load()))
	}))
	t.Cleanup(receiver.Close)

	_, err := c.SetCallback(ctx, hubrise.Callback{URL: receiver.URL, Events: map[string][]string{"order": {"update"}}})
	require.NoError(t, err)
	o, err := c.CreateOrder(ctx, webshopOrder("d1"))
	require.NoError(t, err)
	require.NoError(t, srv.SetOrderStatus(o.ID, hubrise.StatusAccepted))
	require.NoError(t, srv.SetOrderStatus(o.ID, hubrise.StatusInPreparation))

	d := hubrisetest.Dispatcher{Secret: hubrisetest.ClientSecret}

	// A receiver that fails keeps the events queued, to be sent again.
	status.Store(500)
	out := srv.Dispatch(ctx, d)
	require.Len(t, out, 2)
	assert.False(t, out[0].Acknowledged())
	assert.Equal(t, 500, out[0].Status)
	assert.Len(t, srv.Events(hubrisetest.Token), 2)

	// A forged signature is rejected by the receiver; a 4xx still counts as an answer.
	status.Store(401)
	out = srv.Dispatch(ctx, hubrisetest.Dispatcher{Secret: hubrisetest.ClientSecret, BadSignature: true, Keep: true})
	assert.True(t, out[0].Acknowledged())
	assert.EqualValues(t, 2, badSigs.Load())
	assert.Len(t, srv.Events(hubrisetest.Token), 2, "Keep leaves them queued")

	// A good delivery empties the queue, in order, with the body HubRise would send.
	status.Store(200)
	badSigs.Store(0)
	out = srv.Dispatch(ctx, d)
	require.Len(t, out, 2)
	assert.True(t, out[0].Acknowledged() && out[1].Acknowledged())
	assert.Zero(t, badSigs.Load())
	assert.Empty(t, srv.Events(hubrisetest.Token))
	ev, err := hubrise.ParseEvent(lastBody)
	require.NoError(t, err)
	assert.True(t, ev.Is("order", "update"))
	nw, _ := ev.NewOrder()
	assert.Equal(t, hubrise.StatusInPreparation, nw.Status)
	assert.True(t, hubrise.VerifySignature(hubrisetest.ClientSecret, lastBody, lastSig))
	assert.Nil(t, srv.Dispatch(ctx, d), "nothing queued")

	// A receiver that is down is a transport error, and the event stays.
	require.NoError(t, srv.SetOrderStatus(o.ID, hubrise.StatusCompleted))
	receiver.Close()
	out = srv.Dispatch(ctx, hubrisetest.Dispatcher{Secret: hubrisetest.ClientSecret, Timeout: time.Second})
	require.Len(t, out, 1)
	assert.Error(t, out[0].Err)
	assert.False(t, out[0].Acknowledged())
	assert.Len(t, srv.Events(hubrisetest.Token), 1)

	// Passive callback (no URL): nothing is sent.
	_, err = c.SetCallback(ctx, hubrise.Callback{Events: map[string][]string{"order": {"update"}}})
	require.NoError(t, err)
	assert.Nil(t, srv.Dispatch(ctx, d))
	assert.Nil(t, srv.Dispatch(ctx, hubrisetest.Dispatcher{Token: "nobody"}))
}

func TestSignatures(t *testing.T) {
	body := []byte(`{"id":"ev1"}`)
	sig := hubrise.Sign("secret", body)
	assert.Len(t, sig, 64)
	assert.True(t, hubrise.VerifySignature("secret", body, sig))
	assert.False(t, hubrise.VerifySignature("other", body, sig))
	assert.False(t, hubrise.VerifySignature("secret", []byte(`{"id":"ev2"}`), sig))
	assert.False(t, hubrise.VerifySignature("secret", body, ""))
	assert.False(t, hubrise.VerifySignature("", body, sig))
	assert.False(t, hubrise.VerifySignature("secret", body, "zz-not-hex"))
	assert.False(t, hubrise.VerifySignature("secret", body, sig[:10]))
}

func TestParseEvent(t *testing.T) {
	ev, err := hubrise.ParseEvent([]byte(`{"id":"e1","resource_type":"inventory","event_type":"patch","created_at":"2026-10-24T12:00:00+02:00","catalog_id":"c","location_id":"l","state_change":[{"sku_ref":"A"}]}`))
	require.NoError(t, err)
	assert.True(t, ev.Is("inventory", "patch"))
	assert.False(t, ev.Is("inventory", "update"))
	assert.NotEmpty(t, ev.StateChange)
	for _, bad := range []string{`not json`, `{}`, `{"id":"e"}`, `{"id":"e","resource_type":"order"}`} {
		_, err = hubrise.ParseEvent([]byte(bad))
		assert.Error(t, err, bad)
	}
}

func stringsReader(s string) *strings.Reader { return strings.NewReader(s) }

func TestRawInventoryAndCallbackBodies(t *testing.T) {
	srv := hubrisetest.New(t)
	call := func(method, path, body string) int {
		req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("X-Access-Token", hubrisetest.Token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := srv.HTTPClient().Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	assert.Equal(t, 422, call(http.MethodPut, "/v1/location/inventory", `{`))
	assert.Equal(t, 422, call(http.MethodPatch, "/v1/location/inventory", `{`))
	assert.Equal(t, 422, call(http.MethodPost, "/v1/callback", `{`))
	assert.Equal(t, 404, call(http.MethodPut, "/v1/catalogs/ghost/location/inventory", `[]`))
	assert.Equal(t, 404, call(http.MethodPatch, "/v1/catalogs/ghost/location/inventory", `[]`))
	assert.Equal(t, 200, call(http.MethodPatch, "/v1/location/inventory", `[]`))

	cat := srv.Catalog()
	assert.Equal(t, hubrisetest.CatalogID, cat.ID)
	require.NotNil(t, cat.Data)
}
