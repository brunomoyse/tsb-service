package graphql_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/testhelpers"
)

// graphqlResponseWithExtensions parses GraphQL errors including the
// `extensions` map, so tests can assert the error code contract that the
// resolver/resolver.go error presenter relies on to demote expected errors
// (i.e. avoid Sentry spam on FORBIDDEN/NOT_FOUND/USER_ERROR).
type graphqlResponseWithExtensions struct {
	Data   json.RawMessage `json:"data"`
	Errors []struct {
		Message    string         `json:"message"`
		Path       []string       `json:"path"`
		Extensions map[string]any `json:"extensions"`
	} `json:"errors"`
}

func postGraphQLWithExtensions(t *testing.T, url string, reqBody graphqlRequest, token string) graphqlResponseWithExtensions {
	t.Helper()

	body, err := json.Marshal(reqBody)
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var out graphqlResponseWithExtensions
	require.NoError(t, json.Unmarshal(respBody, &out), "response body: %s", string(respBody))
	return out
}

// insertTestOrder inserts a minimal PENDING pickup order owned by userID and
// returns the generated order ID. The orders table has defaults for status,
// type-aware columns, and timestamps; we only need user_id + order_type +
// total_price.
func insertTestOrder(t *testing.T, tc *TestContext, userID uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	const q = `
		INSERT INTO orders (user_id, order_type, total_price)
		VALUES ($1, 'PICKUP', 12.50)
		RETURNING id
	`
	require.NoError(t, tc.DB.DB.QueryRowxContext(t.Context(), q, userID).Scan(&id))
	return id
}

// TestMyOrderOwnership covers the FORBIDDEN-noise fix in
// `resolver/order.go` MyOrder + MyOrderUpdated: a customer querying an order
// that isn't theirs must get a *gqlerror.Error tagged with
// `extensions.code = "FORBIDDEN"` (and same for "NOT_FOUND" on an unknown
// order). Without the extension code the error presenter routes the failure
// to Sentry at ERROR level — which is exactly the production paging we hit.
//
// The subscription resolver (MyOrderUpdated) uses the identical ownership
// check; testing the query path here gives us regression coverage for the
// same code shape without a WebSocket harness.
func TestMyOrderOwnership(t *testing.T) {
	tc := setupTestContext(t)
	url := tc.Client.URL()

	ownerToken, err := testhelpers.GenerateTestAccessToken(tc.Fixtures.RegularUser.ID.String(), false)
	require.NoError(t, err)

	intruderToken, err := testhelpers.GenerateTestAccessToken(tc.Fixtures.AdminUser.ID.String(), false)
	require.NoError(t, err)

	orderID := insertTestOrder(t, tc, tc.Fixtures.RegularUser.ID)

	const myOrderQuery = `
		query ($id: ID!) {
			myOrder(id: $id) {
				id
			}
		}
	`

	t.Run("owner sees their own order", func(t *testing.T) {
		resp := postGraphQLWithExtensions(t, url, graphqlRequest{
			Query:     myOrderQuery,
			Variables: map[string]any{"id": orderID.String()},
		}, ownerToken)

		require.Empty(t, resp.Errors, "expected no errors, got: %+v", resp.Errors)

		var data struct {
			MyOrder struct {
				ID string
			}
		}
		require.NoError(t, json.Unmarshal(resp.Data, &data))
		assert.Equal(t, orderID.String(), data.MyOrder.ID)
	})

	t.Run("different authenticated user is forbidden with code", func(t *testing.T) {
		resp := postGraphQLWithExtensions(t, url, graphqlRequest{
			Query:     myOrderQuery,
			Variables: map[string]any{"id": orderID.String()},
		}, intruderToken)

		require.NotEmpty(t, resp.Errors, "expected a FORBIDDEN error")

		gqlErr := resp.Errors[0]
		assert.Contains(t, gqlErr.Message, "FORBIDDEN")
		assert.Equal(t, []string{"myOrder"}, gqlErr.Path)

		// The contract that keeps Sentry quiet: extensions.code must be set.
		require.NotNil(t, gqlErr.Extensions, "missing extensions on FORBIDDEN error — error presenter will route this to Sentry")
		assert.Equal(t, "FORBIDDEN", gqlErr.Extensions["code"],
			"FORBIDDEN errors must carry extensions.code so the error presenter demotes them away from Sentry")
	})

	t.Run("unauthenticated request hits the @auth directive", func(t *testing.T) {
		resp := postGraphQLWithExtensions(t, url, graphqlRequest{
			Query:     myOrderQuery,
			Variables: map[string]any{"id": orderID.String()},
		}, "")

		require.NotEmpty(t, resp.Errors)
		assert.Contains(t, resp.Errors[0].Message, "UNAUTHENTICATED")
		require.NotNil(t, resp.Errors[0].Extensions)
		assert.Equal(t, "UNAUTHENTICATED", resp.Errors[0].Extensions["code"])
	})
}
