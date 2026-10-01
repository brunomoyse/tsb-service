package graphql_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/testhelpers"
)

// OrderItemSelection.group / .choice are schema-non-null fields that used to have no resolver: the
// mapper only filled the ids, so selecting either one nulled the whole order. They now resolve
// through request-scoped id-keyed DataLoaders.
func TestOrderItemSelectionResolvers(t *testing.T) {
	tc := setupTestContext(t)
	ctx := t.Context()
	db := tc.DB.DB

	token, err := testhelpers.GenerateTestAccessToken(tc.Fixtures.RegularUser.ID.String(), false)
	require.NoError(t, err)

	productID := tc.Fixtures.SalmonSushi.ID
	groupID, sweetID, saltyID := uuid.New(), uuid.New(), uuid.New()

	_, err = db.ExecContext(ctx, `INSERT INTO product_choice_groups (id, product_id) VALUES ($1, $2)`, groupID, productID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO product_choice_group_translations (product_choice_group_id, locale, name) VALUES ($1, 'en', 'Sauce'), ($1, 'fr', 'Sauce FR')`, groupID)
	require.NoError(t, err)
	for _, c := range []struct {
		id   uuid.UUID
		name string
	}{{sweetID, "Sweet"}, {saltyID, "Salty"}} {
		_, err = db.ExecContext(ctx, `INSERT INTO product_choices (id, product_id, choice_group_id, price_modifier) VALUES ($1, $2, $3, 0.50)`, c.id, productID, groupID)
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, `INSERT INTO product_choice_translations (product_choice_id, locale, name) VALUES ($1, 'en', $2)`, c.id, c.name)
		require.NoError(t, err)
	}

	orderID := insertTestOrder(t, tc, tc.Fixtures.RegularUser.ID)
	for _, choiceID := range []uuid.UUID{sweetID, saltyID} {
		lineID := uuid.New()
		_, err = db.ExecContext(ctx, `
			INSERT INTO order_product (id, order_id, product_id, unit_price, quantity, total_price, vat_rate_applied)
			VALUES ($1, $2, $3, 13.00, 1, 13.00, 6.00)`, lineID, orderID, productID)
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, `
			INSERT INTO order_product_choices (order_product_id, product_choice_group_id, product_choice_id, quantity)
			VALUES ($1, $2, $3, 1)`, lineID, groupID, choiceID)
		require.NoError(t, err)
	}

	resp := postGraphQLWithExtensions(t, tc.Client.URL(), graphqlRequest{
		Query: `query ($id: ID!) {
			myOrder(id: $id) {
				id
				items { selections { groupId choiceId quantity group { id name } choice { id name } } }
			}
		}`,
		Variables: map[string]any{"id": orderID.String()},
	}, token)
	require.Empty(t, resp.Errors, "errors: %+v", resp.Errors)

	var data struct {
		MyOrder struct {
			Items []struct {
				Selections []struct {
					GroupID  string `json:"groupId"`
					ChoiceID string `json:"choiceId"`
					Group    struct{ ID, Name string }
					Choice   struct{ ID, Name string }
				} `json:"selections"`
			} `json:"items"`
		} `json:"myOrder"`
	}
	require.NoError(t, json.Unmarshal(resp.Data, &data))
	require.Len(t, data.MyOrder.Items, 2)

	names := map[string]string{sweetID.String(): "Sweet", saltyID.String(): "Salty"}
	for _, item := range data.MyOrder.Items {
		require.Len(t, item.Selections, 1)
		sel := item.Selections[0]
		assert.Equal(t, groupID.String(), sel.Group.ID)
		assert.Equal(t, "Sauce", sel.Group.Name)
		assert.Equal(t, sel.ChoiceID, sel.Choice.ID)
		assert.Equal(t, names[sel.ChoiceID], sel.Choice.Name)
	}
}
