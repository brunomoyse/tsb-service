package infrastructure_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/testhelpers"
)

// env is a fresh migrated database with the standard fixtures (users, categories, products).
type env struct {
	db *sqlx.DB
	td *testhelpers.TestDatabase
	fx *testhelpers.TestFixtures
}

func newEnv(t *testing.T) *env {
	t.Helper()
	td := testhelpers.SetupTestDatabase(t)
	return &env{db: td.DB, td: td, fx: testhelpers.SeedTestData(t, td.DB)}
}

func (e *env) newOrder(t *testing.T) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, e.db.GetContext(t.Context(), &id, `
		INSERT INTO orders (user_id, order_status, order_type, is_online_payment, total_price, language)
		VALUES ($1, 'PENDING', 'PICKUP', false, 10, 'fr') RETURNING id`, e.fx.RegularUser.ID))
	return id
}

// newChoice inserts a choice group with one choice on the first fixture product.
func (e *env) newChoice(t *testing.T) (groupID, choiceID uuid.UUID) {
	t.Helper()
	groupID, choiceID = uuid.New(), uuid.New()
	productID := e.fx.SalmonSushi.ID
	_, err := e.db.ExecContext(t.Context(), `INSERT INTO product_choice_groups (id, product_id) VALUES ($1, $2)`, groupID, productID)
	require.NoError(t, err)
	_, err = e.db.ExecContext(t.Context(),
		`INSERT INTO product_choices (id, product_id, choice_group_id, price_modifier) VALUES ($1, $2, $3, 0.50)`, choiceID, productID, groupID)
	require.NoError(t, err)
	return groupID, choiceID
}
