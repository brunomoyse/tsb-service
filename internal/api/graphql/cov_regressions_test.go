package graphql_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/testhelpers"
)

// Regression tests for defects found while covering the resolvers.

// A user whose saved default place is no longer in the address cache (the cache is a cache: rows
// are purged or refreshed) must see a null address, not an "internal system error" for the whole
// profile query: the address service answers a miss with (nil, nil) and the resolver used to
// dereference it.
func TestUserWithADefaultAddressMissingFromTheCache(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	_, token := testhelpers.SeedCustomer(t, env.DB.DB, "evicted")

	resp := gqlAs(t, env.TestContext, token, "fr",
		`mutation { updateMe(input: {addressPlaceId: "place-evicted"}) { id address { id } } }`, nil)
	require.Empty(t, resp.Errors, "%+v", resp.Errors)
	var data struct {
		UpdateMe struct{ Address *struct{ ID string } }
	}
	require.NoError(t, json.Unmarshal(resp.Data, &data))
	assert.Nil(t, data.UpdateMe.Address)

	resp = gqlAs(t, env.TestContext, token, "fr", `{ me { address { id } } }`, nil)
	require.Empty(t, resp.Errors, "%+v", resp.Errors)
	require.JSONEq(t, `{"me":{"address":null}}`, string(resp.Data))
}

// Order.address.id is the Google place id of a delivery address (the schema says so, and the
// customer app prefills the checkout with it). The order mapper dropped it, so a stored order
// always answered an empty id.
func TestOrderAddressIDIsThePlaceID(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	_, token := testhelpers.SeedCustomer(t, env.DB.DB, "placeid")
	testhelpers.SeedAddress(t, env.DB.DB, "addr-place-id", 2000, "4000")
	cat := testhelpers.SeedCategory(t, env.DB.DB, 70, map[string]string{"en": "Mains"})
	big := testhelpers.SeedProduct(t, env.DB.DB, testhelpers.ProductSpec{CategoryID: cat, Code: "BIG", Price: "30.00", Names: map[string]string{"en": "Big"}})

	order := mustCreateOrder(t, env.TestContext, token, "en", map[string]any{
		"orderType": "DELIVERY", "isOnlinePayment": false, "addressPlaceId": "addr-place-id", "items": lines(big, 1),
	})
	resp := gqlAs(t, env.TestContext, token, "en", `query ($id: ID!) { myOrder(id: $id) { address { id postcode } } }`, map[string]any{"id": order.ID})
	require.Empty(t, resp.Errors, "%+v", resp.Errors)
	require.JSONEq(t, `{"myOrder":{"address":{"id":"addr-place-id","postcode":"4000"}}}`, string(resp.Data))
}
