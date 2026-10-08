package graphql_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/resolver"
	"tsb-service/internal/api/graphql/testhelpers"
	orderApplication "tsb-service/internal/modules/order/application"
	orderDomain "tsb-service/internal/modules/order/domain"
)

// The menu's product rows: the customer's own products ("your favourites") and the most ordered ones.

type productCount struct {
	ProductID  string `json:"productId"`
	OrderCount int    `json:"orderCount"`
}

func (e *covEnv) queryProductRow(t *testing.T, token, query, path string) []productCount {
	t.Helper()
	resp := gqlAs(t, e.TestContext, token, "fr", query, nil)
	require.Empty(t, resp.Errors, "%+v", resp.Errors)
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(resp.Data, &raw))
	var out []productCount
	require.NoError(t, json.Unmarshal(raw[path], &out))
	return out
}

func TestMenuProductRows(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	db := env.DB.DB
	fx := env.Fixtures
	salmon, tuna, tea, mochi := fx.SalmonSushi.ID, fx.TunaSushi.ID, fx.GreenTea.ID, fx.MochiIce.ID
	maki := testhelpers.SeedProduct(t, db, testhelpers.ProductSpec{CategoryID: fx.SushiCategory.ID, Price: "5.00", Names: map[string]string{"fr": "Maki"}})
	soldOut := testhelpers.SeedProduct(t, db, testhelpers.ProductSpec{CategoryID: fx.DessertsCategory.ID, Price: "4.00", SoldOut: true, Names: map[string]string{"fr": "Dorayaki"}})
	hidden := testhelpers.SeedProduct(t, db, testhelpers.ProductSpec{CategoryID: fx.DessertsCategory.ID, Price: "4.00", NotVisible: true, Names: map[string]string{"fr": "Ancien"}})

	alice, aliceTok := testhelpers.SeedCustomer(t, db, "Alice")
	bob, bobTok := testhelpers.SeedCustomer(t, db, "Bob")
	_, carolTok := testhelpers.SeedCustomer(t, db, "Carol")

	type line struct {
		product uuid.UUID
		qty     int
	}
	order := func(user uuid.UUID, status string, age time.Duration, isTest bool, lines ...line) {
		t.Helper()
		var id uuid.UUID
		require.NoError(t, db.QueryRowxContext(t.Context(), `
			INSERT INTO orders (user_id, order_type, order_status, total_price, created_at, is_test)
			VALUES ($1, 'PICKUP', $2, 10, $3, $4) RETURNING id`,
			user, status, time.Now().Add(-age), isTest).Scan(&id))
		for _, l := range lines {
			_, err := db.ExecContext(t.Context(), `
				INSERT INTO order_product (order_id, product_id, unit_price, quantity, total_price, vat_rate_applied)
				VALUES ($1, $2, 5, $3, 5 * $3, 6)`, id, l.product, l.qty)
			require.NoError(t, err)
		}
	}

	// Alice: salmon in three orders (twice on two lines of one order), tuna in two, maki in one
	// with more units than tuna; the rest never counts.
	order(alice, "DELIVERED", 10*time.Hour, false, line{salmon, 1}, line{salmon, 2}, line{tuna, 2})
	order(alice, "PICKED_UP", 9*time.Hour, false, line{salmon, 1}, line{tuna, 1}, line{soldOut, 1}, line{hidden, 1})
	order(alice, "DELIVERED", 8*time.Hour, false, line{salmon, 1}, line{maki, 5})
	order(alice, "CANCELLED", 7*time.Hour, false, line{tea, 9})
	order(alice, "PREPARING", 6*time.Hour, false, line{mochi, 1})
	order(alice, "DELIVERED", 5*time.Hour, true, line{mochi, 1})
	// Bob: tea twice, mochi once, and an order from before the 90-day window.
	order(bob, "DELIVERED", 4*time.Hour, false, line{tea, 1}, line{mochi, 1})
	order(bob, "PICKED_UP", 3*time.Hour, false, line{tea, 1})
	order(bob, "DELIVERED", 100*24*time.Hour, false, line{maki, 1}, line{maki, 1})

	t.Run("myOrderedProducts lists the customer's products on sale, most often ordered first", func(t *testing.T) {
		got := env.queryProductRow(t, aliceTok, `{ myOrderedProducts { productId orderCount } }`, "myOrderedProducts")
		assert.Equal(t, []productCount{
			{salmon.String(), 3},
			{tuna.String(), 2},
			{maki.String(), 1},
		}, got, "a product on two lines counts once per order; cancelled, unfinished, test, sold-out and hidden ones are left out")
	})

	t.Run("myOrderedProducts is the caller's own, and empty for a new customer", func(t *testing.T) {
		got := env.queryProductRow(t, bobTok, `{ myOrderedProducts { productId orderCount } }`, "myOrderedProducts")
		assert.Equal(t, []productCount{{tea.String(), 2}, {maki.String(), 1}, {mochi.String(), 1}}, got,
			"a tie on orders goes to the product with more units")
		assert.Empty(t, env.queryProductRow(t, carolTok, `{ myOrderedProducts { productId } }`, "myOrderedProducts"))
	})

	t.Run("first is honoured, capped, and below 1 falls back to the default", func(t *testing.T) {
		got := env.queryProductRow(t, aliceTok, `{ myOrderedProducts(first: 1) { productId } }`, "myOrderedProducts")
		assert.Equal(t, []productCount{{ProductID: salmon.String()}}, got)
		assert.Len(t, env.queryProductRow(t, aliceTok, `{ myOrderedProducts(first: 0) { productId } }`, "myOrderedProducts"), 3)
		assert.Len(t, env.queryProductRow(t, aliceTok, `{ myOrderedProducts(first: 500) { productId } }`, "myOrderedProducts"), 3)
	})

	t.Run("myOrderedProducts needs a signed-in customer", func(t *testing.T) {
		resp := gqlAs(t, env.TestContext, "", "fr", `{ myOrderedProducts { productId } }`, nil)
		require.Len(t, resp.Errors, 1)
		assert.Equal(t, "UNAUTHENTICATED", resp.Errors[0].Extensions["code"])
	})

	t.Run("popularProducts is public: the last 90 days, at most two per category", func(t *testing.T) {
		got := env.queryProductRow(t, "", `{ popularProducts { productId orderCount } }`, "popularProducts")
		assert.Equal(t, []productCount{
			{salmon.String(), 3},
			{tuna.String(), 2},
			{tea.String(), 2},
			{mochi.String(), 1},
		}, got, "maki is the third sushi (and its old order is out of the window); tuna has more units than tea")
	})
}

// failingRows makes the product rows' store fail.
type failingRows struct{ orderApplication.OrderService }

func (failingRows) GetOrderedProducts(context.Context, uuid.UUID, int) ([]*orderDomain.ProductOrderCount, error) {
	return nil, errBoom
}

func (failingRows) GetPopularProducts(context.Context, int) ([]*orderDomain.ProductOrderCount, error) {
	return nil, errBoom
}

func TestMenuProductRowsThatFail(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	user, _ := testhelpers.SeedCustomer(t, env.DB.DB, "rows")
	r := env.with(func(r *resolver.Resolver) { r.OrderService = failingRows{r.OrderService} })

	_, err := r.Query().MyOrderedProducts(env.ctxFor(user.String(), false, "fr"), nil)
	require.ErrorContains(t, err, "failed to get ordered products")
	_, err = r.Query().PopularProducts(env.ctxFor("", false, "fr"), nil)
	require.ErrorContains(t, err, "failed to get popular products")

	_, err = env.Resolver.Query().MyOrderedProducts(env.ctxFor("not-a-uuid", false, "fr"), nil)
	require.ErrorContains(t, err, "invalid user ID")
}
