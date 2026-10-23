package hubrise_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/pkg/hubrise"
	"tsb-service/pkg/hubrise/hubrisetest"
)

const list = hubrisetest.CustomerListID

func TestCustomerCreateUpdateFindAndAnonymise(t *testing.T) {
	srv := hubrisetest.New(t)
	c := srv.NewClient(t)

	created, err := c.CreateCustomer(ctx, list, &hubrise.Customer{
		PrivateRef: "user-1", FirstName: "Ada", LastName: "Lovelace", Email: "ada@example.com", Phone: "+32470000000",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, created.ID)
	assert.Equal(t, list, created.CustomerListID)
	assert.Equal(t, "user-1", created.PrivateRef)
	assert.False(t, created.CreatedAt.IsZero())

	// The PATCH sends only what is set; the rest of the customer stays.
	updated, err := c.UpdateCustomer(ctx, list, created.ID, &hubrise.Customer{
		LastName: "Byron", Address1: "Rue du Test 12", PostalCode: "4000", City: "Liege", Country: "BE",
	})
	require.NoError(t, err)
	assert.Equal(t, "Ada", updated.FirstName)
	assert.Equal(t, "Byron", updated.LastName)
	assert.Equal(t, "ada@example.com", updated.Email)
	assert.Equal(t, "Rue du Test 12", updated.Address1)
	assert.Equal(t, created.ID, updated.ID)
	patch := srv.RequestsTo(http.MethodPatch, "/v1/customer_lists/")
	require.Len(t, patch, 1)
	var wire map[string]any
	require.NoError(t, json.Unmarshal(patch[0].Body, &wire))
	assert.NotContains(t, wire, "first_name", "an empty field is not sent")
	assert.NotContains(t, wire, "id")

	found, err := c.FindCustomerByPrivateRef(ctx, list, "user-1")
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, created.ID, found.ID)
	missing, err := c.FindCustomerByPrivateRef(ctx, list, "nobody")
	require.NoError(t, err)
	assert.Nil(t, missing)

	anon, err := c.AnonymiseCustomer(ctx, list, created.ID)
	require.NoError(t, err)
	assert.True(t, anon.Anonymised)
	assert.Empty(t, anon.Email)
	assert.Empty(t, anon.FirstName)
	again, err := c.AnonymiseCustomer(ctx, list, created.ID)
	require.NoError(t, err, "anonymising twice is harmless")
	assert.True(t, again.Anonymised)

	_, err = c.UpdateCustomer(ctx, list, created.ID, &hubrise.Customer{FirstName: "Back"})
	assert.ErrorIs(t, err, hubrise.KindInvalid, "an anonymised customer cannot be modified")
	gone, err := c.FindCustomerByPrivateRef(ctx, list, "user-1")
	require.NoError(t, err)
	assert.Nil(t, gone, "the private ref is erased with the rest")
}

func TestCustomerLookupIsPerClientAndTakesTheEarliestOfDuplicates(t *testing.T) {
	srv := hubrisetest.New(t)
	c := srv.NewClient(t)
	first, err := c.CreateCustomer(ctx, list, &hubrise.Customer{PrivateRef: "dup", Email: "a@example.com"})
	require.NoError(t, err)
	_, err = c.CreateCustomer(ctx, list, &hubrise.Customer{PrivateRef: "dup", Email: "a@example.com"})
	require.NoError(t, err)
	_, err = c.CreateCustomer(ctx, list, &hubrise.Customer{PrivateRef: "other"})
	require.NoError(t, err)

	got, err := c.FindCustomerByPrivateRef(ctx, list, "dup")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, first.ID, got.ID, "a double create is resolved to the first customer")
	assert.Len(t, srv.Customers(), 3)

	// Another client does not see the private ref, so it cannot find the customer by it.
	other, err := hubrise.New(srv.APIBaseURL(), hubrisetest.EPOSToken, hubrise.WithHTTPClient(srv.HTTPClient()),
		hubrise.WithLimiter(hubrise.NewLimiter(hubrise.Limits{})))
	require.NoError(t, err)
	none, err := other.FindCustomerByPrivateRef(ctx, list, "dup")
	require.NoError(t, err)
	assert.Nil(t, none)
}

func TestCustomerRequestsFailLikeTheAPI(t *testing.T) {
	srv := hubrisetest.New(t)
	c := srv.NewClient(t)

	_, err := c.CreateCustomer(ctx, "other-list", &hubrise.Customer{Email: "a@example.com"})
	assert.ErrorIs(t, err, hubrise.KindNotFound, "an unknown customer list")
	_, err = c.UpdateCustomer(ctx, "other-list", "cu1", &hubrise.Customer{})
	assert.ErrorIs(t, err, hubrise.KindNotFound)
	_, err = c.AnonymiseCustomer(ctx, "other-list", "cu1")
	assert.ErrorIs(t, err, hubrise.KindNotFound)
	_, err = c.FindCustomerByPrivateRef(ctx, "other-list", "x")
	assert.ErrorIs(t, err, hubrise.KindNotFound)

	_, err = c.UpdateCustomer(ctx, list, "ghost", &hubrise.Customer{FirstName: "x"})
	assert.ErrorIs(t, err, hubrise.KindNotFound, "an unknown customer")
	_, err = c.AnonymiseCustomer(ctx, list, "ghost")
	assert.ErrorIs(t, err, hubrise.KindNotFound)

	_, err = c.CreateCustomer(ctx, list, &hubrise.Customer{Email: "not-an-email"})
	assert.ErrorIs(t, err, hubrise.KindInvalid)
	_, err = c.CreateCustomer(ctx, list, &hubrise.Customer{Country: "Belgium"})
	assert.ErrorIs(t, err, hubrise.KindInvalid)
	created, err := c.CreateCustomer(ctx, list, &hubrise.Customer{Email: "ok@example.com"})
	require.NoError(t, err)
	_, err = c.UpdateCustomer(ctx, list, created.ID, &hubrise.Customer{Email: "broken"})
	assert.ErrorIs(t, err, hubrise.KindInvalid)
	stored, ok := srv.Customer(created.ID)
	require.True(t, ok)
	assert.Equal(t, "ok@example.com", stored.Email, "a refused PATCH changes nothing")
}

func TestAnOrderNamesACustomerOfTheList(t *testing.T) {
	srv := hubrisetest.New(t)
	c := srv.NewClient(t)
	cu, err := c.CreateCustomer(ctx, list, &hubrise.Customer{PrivateRef: "user-1", FirstName: "Ada"})
	require.NoError(t, err)

	order := webshopOrder("order-1")
	order.Customer = nil
	order.CustomerID = cu.ID
	got, err := c.CreateOrder(ctx, order)
	require.NoError(t, err)
	assert.Equal(t, cu.ID, got.CustomerID)

	order = webshopOrder("order-2")
	order.Customer = nil
	order.CustomerID = "ghost"
	_, err = c.CreateOrder(ctx, order)
	assert.ErrorIs(t, err, hubrise.KindInvalid, "the customer must exist")

	_, err = c.AnonymiseCustomer(ctx, list, cu.ID)
	require.NoError(t, err)
	order = webshopOrder("order-3")
	order.Customer = nil
	order.CustomerID = cu.ID
	_, err = c.CreateOrder(ctx, order)
	assert.ErrorIs(t, err, hubrise.KindInvalid, "an anonymised customer cannot be put on a new order")

	// Combined with a guest object it is refused as well.
	order = webshopOrder("order-4")
	order.CustomerID = "x"
	_, err = c.CreateOrder(ctx, order)
	assert.ErrorIs(t, err, hubrise.KindInvalid)
}

func TestDeletingACustomerAtHubRiseIsSeenAs404(t *testing.T) {
	srv := hubrisetest.New(t)
	c := srv.NewClient(t)
	cu, err := c.CreateCustomer(ctx, list, &hubrise.Customer{PrivateRef: "user-1"})
	require.NoError(t, err)
	_, ok := srv.Customer(cu.ID)
	assert.True(t, ok)
	assert.True(t, srv.DeleteCustomer(cu.ID))
	assert.False(t, srv.DeleteCustomer(cu.ID))
	_, ok = srv.Customer(cu.ID)
	assert.False(t, ok)
	_, err = c.UpdateCustomer(ctx, list, cu.ID, &hubrise.Customer{FirstName: "x"})
	assert.ErrorIs(t, err, hubrise.KindNotFound)
	assert.Empty(t, srv.Customers())
}
