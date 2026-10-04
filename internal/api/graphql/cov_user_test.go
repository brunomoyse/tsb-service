package graphql_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/testhelpers"
)

// The account resolvers: me, updateMe, deleteMe, customerStats and the fields of User.

func TestUserAccountResolvers(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	db := env.DB.DB
	_, token := testhelpers.SeedCustomer(t, db, "account")
	ghostToken, err := testhelpers.GenerateTestAccessToken(uuid.NewString(), false)
	require.NoError(t, err)
	adminTok := adminToken(t, env.TestContext)

	t.Run("me returns the caller's profile, with admin taken from the token", func(t *testing.T) {
		resp := gqlAs(t, env.TestContext, adminTok, "en", `{ me { id email isAdmin firstName } }`, nil)
		require.Empty(t, resp.Errors)
		var data struct {
			Me struct {
				ID, Email, FirstName string
				IsAdmin              bool
			}
		}
		require.NoError(t, json.Unmarshal(resp.Data, &data))
		assert.Equal(t, env.Fixtures.AdminUser.ID.String(), data.Me.ID)
		assert.True(t, data.Me.IsAdmin, "isAdmin comes from the token, not the row")

		resp = gqlAs(t, env.TestContext, token, "en", `{ me { isAdmin } }`, nil)
		require.Empty(t, resp.Errors)
		require.JSONEq(t, `{"me":{"isAdmin":false}}`, string(resp.Data))
	})

	t.Run("me of a token whose user does not exist is an error", func(t *testing.T) {
		resp := gqlAs(t, env.TestContext, ghostToken, "en", `{ me { id } }`, nil)
		require.Len(t, resp.Errors, 1)
		assert.Equal(t, "Internal server error", resp.Errors[0].Message, "the driver text must not reach the client")
	})

	const updateMe = `mutation ($input: UpdateUserInput!) { updateMe(input: $input) { firstName lastName phoneNumber email notifyMarketing notifyOrderUpdates address { id } } }`

	t.Run("updateMe stores the profile, normalises the phone number and the e-mail", func(t *testing.T) {
		resp := gqlAs(t, env.TestContext, token, "fr", updateMe, map[string]any{"input": map[string]any{
			"firstName": "Zoe", "lastName": "Martin", "phoneNumber": "0470 12 34 56", "email": "  Zoe.Martin@Example.COM ",
			"notifyMarketing": true, "notifyOrderUpdates": true,
		}})
		require.Empty(t, resp.Errors, "%+v", resp.Errors)
		require.JSONEq(t, `{"updateMe":{"firstName":"Zoe","lastName":"Martin","phoneNumber":"+32470123456","email":"zoe.martin@example.com","notifyMarketing":true,"notifyOrderUpdates":true,"address":null}}`, string(resp.Data))
	})

	t.Run("updateMe with a phone number that is not one is a USER_ERROR on that field", func(t *testing.T) {
		resp := gqlAs(t, env.TestContext, token, "fr", updateMe, map[string]any{"input": map[string]any{"phoneNumber": "not a phone"}})
		require.Len(t, resp.Errors, 1)
		assert.Equal(t, "USER_ERROR", resp.Errors[0].Extensions["code"])
		assert.Equal(t, "phoneNumber", resp.Errors[0].Extensions["field"])
		assert.Equal(t, "invalid phone number", resp.Errors[0].Message)
	})

	t.Run("updateMe for a user that does not exist fails without leaking the cause", func(t *testing.T) {
		resp := gqlAs(t, env.TestContext, ghostToken, "fr", updateMe, map[string]any{"input": map[string]any{"firstName": "X"}})
		require.Len(t, resp.Errors, 1)
		assert.Equal(t, "Internal server error", resp.Errors[0].Message)
	})

	t.Run("the default address is resolved from the address cache, and cleared with an empty place id", func(t *testing.T) {
		testhelpers.SeedAddress(t, db, "place-default", 1500, "4000")
		resp := gqlAs(t, env.TestContext, token, "fr", updateMe, map[string]any{"input": map[string]any{"addressPlaceId": "place-default"}})
		require.Empty(t, resp.Errors, "%+v", resp.Errors)
		var data struct {
			UpdateMe struct{ Address struct{ ID string } }
		}
		require.NoError(t, json.Unmarshal(resp.Data, &data))
		assert.Equal(t, "place-default", data.UpdateMe.Address.ID)

		resp = gqlAs(t, env.TestContext, token, "fr", updateMe, map[string]any{"input": map[string]any{"addressPlaceId": ""}})
		require.Empty(t, resp.Errors)
		var cleared struct {
			UpdateMe struct{ Address *struct{ ID string } }
		}
		require.NoError(t, json.Unmarshal(resp.Data, &cleared))
		assert.Nil(t, cleared.UpdateMe.Address)
	})
}

func TestDeleteMe(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	db := env.DB.DB
	const deleteMe = `mutation { deleteMe }`

	t.Run("erases the Zitadel identity and anonymises the account", func(t *testing.T) {
		id, token := testhelpers.SeedCustomer(t, db, "erase")
		_, err := db.ExecContext(t.Context(), `INSERT INTO device_push_tokens (user_id, device_token, platform, role) VALUES ($1, 'tok', 'ios', 'user')`, id)
		require.NoError(t, err)

		resp := gqlAs(t, env.TestContext, token, "fr", deleteMe, nil)
		require.Empty(t, resp.Errors, "%+v", resp.Errors)
		require.JSONEq(t, `{"deleteMe":true}`, string(resp.Data))

		assert.Equal(t, []string{id.String()}, env.Zitadel.deleted, "the identity provider is told to forget the user")
		var email string
		require.NoError(t, db.GetContext(t.Context(), &email, `SELECT email FROM users WHERE id = $1`, id))
		assert.Equal(t, "deleted+"+id.String()+"@deleted.invalid", email)
		assert.Zero(t, countRows(t, env.TestContext, `SELECT count(*) FROM device_push_tokens WHERE user_id = $1`, id))
	})

	t.Run("a failing identity provider leaves the account untouched", func(t *testing.T) {
		id, token := testhelpers.SeedCustomer(t, db, "erase-fail")
		env.Zitadel.failDel = assertErr("zitadel down")
		t.Cleanup(func() { env.Zitadel.failDel = nil })

		resp := gqlAs(t, env.TestContext, token, "fr", deleteMe, nil)
		require.Len(t, resp.Errors, 1)
		assert.Equal(t, "Internal server error", resp.Errors[0].Message)
		var email string
		require.NoError(t, db.GetContext(t.Context(), &email, `SELECT email FROM users WHERE id = $1`, id))
		assert.NotContains(t, email, "deleted.invalid")
	})

	t.Run("anonymous callers are refused", func(t *testing.T) {
		resp := gqlAs(t, env.TestContext, "", "fr", deleteMe, nil)
		require.Len(t, resp.Errors, 1)
		assert.Equal(t, "UNAUTHENTICATED", resp.Errors[0].Extensions["code"])
	})
}

type assertErr string

func (e assertErr) Error() string { return string(e) }

func TestCustomerStats(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	db := env.DB.DB
	admin := adminToken(t, env.TestContext)

	pickupFan, _ := testhelpers.SeedCustomer(t, db, "pickup-fan")
	deliveryFan, _ := testhelpers.SeedCustomer(t, db, "delivery-fan")
	oneOff, _ := testhelpers.SeedCustomer(t, db, "one-off")
	insert := func(user uuid.UUID, orderType, status, total string, createdAt time.Time) {
		t.Helper()
		_, err := db.ExecContext(t.Context(), `INSERT INTO orders (user_id, order_type, order_status, total_price, created_at) VALUES ($1, $2, $3, $4, $5)`,
			user, orderType, status, total, createdAt)
		require.NoError(t, err)
	}
	old := time.Now().AddDate(0, -3, 0)
	recent := time.Now().Add(-time.Hour)
	insert(pickupFan, "PICKUP", "DELIVERED", "20.00", old)
	insert(pickupFan, "PICKUP", "DELIVERED", "30.00", recent)
	insert(pickupFan, "DELIVERY", "DELIVERED", "10.00", recent)
	insert(pickupFan, "PICKUP", "CANCELLED", "99.00", recent) // never counted
	insert(deliveryFan, "DELIVERY", "DELIVERED", "40.00", recent)
	insert(deliveryFan, "DELIVERY", "DELIVERED", "50.00", recent)
	insert(oneOff, "PICKUP", "FAILED", "70.00", recent) // never counted

	const q = `query ($input: CustomerStatsInput) { customerStats(input: $input) {
		summary { totalCustomers totalRevenue averageOrderValue totalOrders }
		customers { userId lastName totalOrders totalAmount averageOrderAmount preferredOrderType deliveryCount pickupCount }
	} }`
	type stats struct {
		CustomerStats struct {
			Summary struct {
				TotalCustomers    int
				TotalRevenue      string
				AverageOrderValue string
				TotalOrders       int
			}
			Customers []struct {
				UserID, LastName, TotalAmount, AverageOrderAmount, PreferredOrderType string
				TotalOrders, DeliveryCount, PickupCount                               int
			}
		}
	}
	run := func(t *testing.T, input map[string]any) stats {
		t.Helper()
		vars := map[string]any{}
		if input != nil {
			vars["input"] = input
		}
		resp := gqlAs(t, env.TestContext, admin, "fr", q, vars)
		require.Empty(t, resp.Errors, "%+v", resp.Errors)
		var out stats
		require.NoError(t, json.Unmarshal(resp.Data, &out))
		return out
	}

	t.Run("without a filter every customer with an active order is counted, biggest spender first", func(t *testing.T) {
		got := run(t, nil).CustomerStats
		require.Len(t, got.Customers, 2)
		assert.Equal(t, deliveryFan.String(), got.Customers[0].UserID)
		assert.Equal(t, "90.00", got.Customers[0].TotalAmount)
		assert.Equal(t, "45.00", got.Customers[0].AverageOrderAmount)
		assert.Equal(t, "DELIVERY", got.Customers[0].PreferredOrderType)
		assert.Equal(t, 2, got.Customers[0].DeliveryCount)

		assert.Equal(t, pickupFan.String(), got.Customers[1].UserID)
		assert.Equal(t, "60.00", got.Customers[1].TotalAmount)
		assert.Equal(t, "PICKUP", got.Customers[1].PreferredOrderType)
		assert.Equal(t, 2, got.Customers[1].PickupCount)
		assert.Equal(t, 1, got.Customers[1].DeliveryCount)

		assert.Equal(t, 2, got.Summary.TotalCustomers)
		assert.Equal(t, 5, got.Summary.TotalOrders)
		assert.Equal(t, "150.00", got.Summary.TotalRevenue)
		assert.Equal(t, "30.00", got.Summary.AverageOrderValue)
	})

	t.Run("an order type filter keeps that type only", func(t *testing.T) {
		got := run(t, map[string]any{"orderType": "PICKUP"}).CustomerStats
		require.Len(t, got.Customers, 1)
		assert.Equal(t, pickupFan.String(), got.Customers[0].UserID)
		assert.Equal(t, "50.00", got.Customers[0].TotalAmount)
		assert.Equal(t, 2, got.Summary.TotalOrders)
	})

	t.Run("date bounds and a minimum order count narrow the list", func(t *testing.T) {
		got := run(t, map[string]any{"startDate": time.Now().AddDate(0, -1, 0).Format(time.RFC3339)}).CustomerStats
		assert.Equal(t, 4, got.Summary.TotalOrders, "the three-month-old order is out")

		got = run(t, map[string]any{"endDate": time.Now().AddDate(0, -1, 0).Format(time.RFC3339)}).CustomerStats
		require.Len(t, got.Customers, 1)
		assert.Equal(t, "20.00", got.Customers[0].TotalAmount)

		got = run(t, map[string]any{"minOrders": 3}).CustomerStats
		require.Len(t, got.Customers, 1)
		assert.Equal(t, pickupFan.String(), got.Customers[0].UserID)
	})

	t.Run("no customer at all gives an empty list and a zero average, not a division by zero", func(t *testing.T) {
		got := run(t, map[string]any{"minOrders": 50}).CustomerStats
		assert.Empty(t, got.Customers)
		assert.Equal(t, 0, got.Summary.TotalCustomers)
		assert.Equal(t, "0.00", got.Summary.AverageOrderValue)
		assert.Equal(t, "0.00", got.Summary.TotalRevenue)
	})
}

func TestUserOrdersField(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	db := env.DB.DB
	id, token := testhelpers.SeedCustomer(t, db, "history")
	other, _ := testhelpers.SeedCustomer(t, db, "other")
	for _, u := range []uuid.UUID{id, id, other} {
		_, err := db.ExecContext(t.Context(), `INSERT INTO orders (user_id, order_type, total_price) VALUES ($1, 'PICKUP', 12.50)`, u)
		require.NoError(t, err)
	}

	resp := gqlAs(t, env.TestContext, token, "fr", `{ me { orders { id totalPrice } } }`, nil)
	require.Empty(t, resp.Errors, "%+v", resp.Errors)
	var data struct {
		Me struct {
			Orders []struct{ ID, TotalPrice string }
		}
	}
	require.NoError(t, json.Unmarshal(resp.Data, &data))
	require.Len(t, data.Me.Orders, 2, "only the caller's own orders")
	assert.Equal(t, "12.5", data.Me.Orders[0].TotalPrice)

	t.Run("without the data loader the field reports it instead of panicking", func(t *testing.T) {
		_, err := env.Resolver.User().Orders(t.Context(), nil)
		require.EqualError(t, err, "no user order loader found")
	})
}
