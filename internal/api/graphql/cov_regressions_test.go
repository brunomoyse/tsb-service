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
