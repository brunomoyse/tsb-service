package application_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/modules/hubrise/domain"
	hubtest "tsb-service/internal/modules/hubrise/testhelpers"
	userApplication "tsb-service/internal/modules/user/application"
	userInfra "tsb-service/internal/modules/user/infrastructure"
	"tsb-service/pkg/db"
	pkghubrise "tsb-service/pkg/hubrise"
	"tsb-service/pkg/hubrise/hubrisetest"
)

const customersPath = "/v1/customer_lists/"

// fakeIdentity is the identity provider of the user service.
type fakeIdentity struct{ deleted []string }

func (f *fakeIdentity) FetchUserInfo(context.Context, string) (string, string, string, error) {
	return "", "", "", nil
}

func (f *fakeIdentity) DeleteUser(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

// custEnv is an env whose connection has a customer list (the mock's), and the real user service
// wired to the customer sync as its profile observer, like the composition root does.
type custEnv struct {
	*env
	users userApplication.UserService
	user  uuid.UUID
}

var _ userApplication.ProfileObserver = (*application.CustomerSync)(nil)

func newCustEnv(t *testing.T, opts ...envOption) *custEnv {
	t.Helper()
	opts = append([]envOption{withSeed(hubtest.WithCustomerList(hubrisetest.CustomerListID, hubrisetest.CustomerList))}, opts...)
	e := newEnv(t, opts...)
	pool := &db.DBPool{Customer: e.db, Admin: e.db}
	users := userApplication.NewUserService(userInfra.NewUserRepository(pool), &fakeIdentity{})
	users.SetProfileObserver(e.push.Customers)
	return &custEnv{env: e, users: users, user: e.fx.RegularUser.ID}
}

// placed queues the push of a new order of the user and returns its id.
func (c *custEnv) placed(spec orderSpec) uuid.UUID {
	c.t.Helper()
	id := c.order(spec)
	c.ready(id)
	return id
}

func (c *custEnv) customerRequests(method string) []hubrisetest.Request {
	return c.srv.RequestsTo(method, customersPath)
}

func (c *custEnv) customerRows(kind domain.OutboxKind) []domain.OutboxItem {
	c.t.Helper()
	var items []domain.OutboxItem
	require.NoError(c.t, c.db.SelectContext(c.t.Context(), &items, `
		SELECT id, kind, order_id, dedupe_key, payload, state, attempts, leased_until, next_attempt_at, last_error,
		       created_at, sent_at, parked_at
		FROM hubrise_outbox WHERE kind = $1 ORDER BY id`, kind))
	return items
}

func (c *custEnv) customerLink() *domain.CustomerLink {
	c.t.Helper()
	l, err := c.mod.CustomerLinks.Get(c.t.Context(), c.user)
	require.NoError(c.t, err)
	return l
}

// requestIndex is the position of the first request with the method and path prefix, -1 when none.
func requestIndex(reqs []hubrisetest.Request, method, prefix string) int {
	for i, r := range reqs {
		if r.Method == method && len(r.Path) >= len(prefix) && r.Path[:len(prefix)] == prefix {
			return i
		}
	}
	return -1
}

func wireOf(t *testing.T, r hubrisetest.Request) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(r.Body, &m))
	return m
}

func TestARegisteredUsersOrderCreatesTheCustomerOnceAndTheNextOrderReusesIt(t *testing.T) {
	e := newCustEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")

	first := e.placed(orderSpec{})
	e.drainAll()

	customers := e.srv.Customers()
	require.Len(t, customers, 1)
	cu := customers[0]
	assert.Equal(t, e.user.String(), cu.PrivateRef, "the private ref is the user id")
	assert.Equal(t, "John", cu.FirstName)
	assert.Equal(t, "Doe", cu.LastName)
	assert.Equal(t, "john@example.com", cu.Email)
	assert.Empty(t, cu.Address1, "a pickup order carries no address")

	remote := e.remote(first)
	assert.Equal(t, cu.ID, remote.CustomerID)
	assert.Nil(t, remote.Customer, "customer_id and the guest object are never combined")
	var sent map[string]any
	reqs := e.srv.RequestsTo(http.MethodPost, "/v1/location/orders")
	require.Len(t, reqs, 1)
	require.NoError(t, json.Unmarshal(reqs[0].Body, &sent))
	assert.Equal(t, cu.ID, sent["customer_id"])
	assert.NotContains(t, sent, "customer")
	assert.Less(t, requestIndex(e.srv.Requests(), http.MethodPost, customersPath), requestIndex(e.srv.Requests(), http.MethodPost, "/v1/location/orders"),
		"the customer is written before the order")
	assert.Empty(t, e.customerRequests(http.MethodGet), "a brand new customer needs no lookup")

	link := e.customerLink()
	require.NotNil(t, link.HubriseCustomerID)
	assert.Equal(t, cu.ID, *link.HubriseCustomerID)
	assert.NotEmpty(t, link.ContentHash)
	assert.NotNil(t, link.SyncedAt)

	// The next order reuses it: no creation, no update, no lookup.
	e.srv.ClearRequests()
	second := e.placed(orderSpec{})
	e.drainAll()
	assert.Empty(t, e.customerRequests(""), "nothing is sent to the customer list")
	assert.Equal(t, cu.ID, e.remote(second).CustomerID)
	assert.Len(t, e.srv.Customers(), 1)
}

func TestADeliveryAddressIsPatchedOntoTheCustomerJustBeforeTheOrder(t *testing.T) {
	e := newCustEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")

	e.placed(orderSpec{})
	e.drainAll()
	e.srv.ClearRequests()

	// The first delivery order brings the address: a PATCH with the address only (the profile is
	// unchanged), sent before the order.
	delivery := e.placed(orderSpec{orderType: "DELIVERY", deliveryFee: "2.50", total: "15.00"})
	e.drainAll()
	patches := e.customerRequests(http.MethodPatch)
	require.Len(t, patches, 1)
	wire := wireOf(t, patches[0])
	assert.Equal(t, "Rue du Test 12 bte 3", wire["address_1"])
	assert.Equal(t, "4000", wire["postal_code"])
	assert.Equal(t, "Liège", wire["city"])
	assert.Equal(t, "BE", wire["country"])
	assert.Equal(t, "50.6326", wire["latitude"])
	assert.Equal(t, "Second floor", wire["delivery_notes"])
	assert.NotContains(t, wire, "first_name", "the profile did not change")
	assert.Less(t, requestIndex(e.srv.Requests(), http.MethodPatch, customersPath), requestIndex(e.srv.Requests(), http.MethodPost, "/v1/location/orders"))
	cu := e.srv.Customers()[0]
	assert.Equal(t, "Rue du Test 12 bte 3", cu.Address1)
	assert.Equal(t, cu.ID, e.remote(delivery).CustomerID)

	// A pickup order leaves the address as it is, and a second delivery to the same address changes nothing.
	e.srv.ClearRequests()
	e.placed(orderSpec{})
	e.placed(orderSpec{orderType: "DELIVERY", deliveryFee: "2.50", total: "15.00"})
	e.drainAll()
	assert.Empty(t, e.customerRequests(""))

	// Another address is a new PATCH.
	e.exec(`UPDATE orders SET street_name = 'Rue Neuve' WHERE id = (SELECT id FROM orders ORDER BY created_at DESC LIMIT 1)`)
	e.placed(orderSpec{orderType: "DELIVERY", deliveryFee: "2.50", total: "15.00"})
	e.exec(`UPDATE hubrise_outbox SET payload = jsonb_set(payload, '{customer,address_1}', '"Rue Neuve 12 bte 3"') WHERE id = (SELECT max(id) FROM hubrise_outbox)`)
	e.drainAll()
	patches = e.customerRequests(http.MethodPatch)
	require.Len(t, patches, 1)
	assert.Equal(t, "Rue Neuve 12 bte 3", wireOf(t, patches[0])["address_1"])
}

func TestAFirstDeliveryOrderCreatesTheCustomerWithItsAddress(t *testing.T) {
	e := newCustEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	e.placed(orderSpec{orderType: "DELIVERY", deliveryFee: "2.50", total: "15.00"})
	e.drainAll()
	require.Len(t, e.customerRequests(http.MethodPost), 1)
	assert.Empty(t, e.customerRequests(http.MethodPatch), "the address went with the creation")
	cu := e.srv.Customers()[0]
	assert.Equal(t, "4000", cu.PostalCode)
	assert.Equal(t, "Second floor", cu.DeliveryNotes)
}

func TestAProfileChangeIsPatchedOntoTheCustomer(t *testing.T) {
	e := newCustEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")

	// Before the first order the customer does not exist: nothing is queued.
	grace := "Grace"
	_, err := e.users.UpdateMe(t.Context(), e.user.String(), &grace, nil, nil, nil, nil, nil, nil)
	require.NoError(t, err)
	assert.Empty(t, e.customerRows(domain.OutboxCustomer))

	e.placed(orderSpec{})
	e.drainAll()
	cu := e.srv.Customers()[0]
	assert.Equal(t, "Grace", cu.FirstName, "the customer is created with the current profile")

	// A burst of edits is one row and one PATCH with the latest profile.
	e.srv.ClearRequests()
	hopper, phone := "Hopper", "0470 12 34 56"
	_, err = e.users.UpdateMe(t.Context(), e.user.String(), nil, &hopper, nil, nil, nil, nil, nil)
	require.NoError(t, err)
	_, err = e.users.UpdateMe(t.Context(), e.user.String(), nil, nil, nil, &phone, nil, nil, nil)
	require.NoError(t, err)
	rows := e.customerRows(domain.OutboxCustomer)
	require.Len(t, rows, 1, "the second edit joined the pending row")
	assert.Equal(t, domain.CustomerDedupeKey(e.user), *rows[0].DedupeKey)
	assert.Nil(t, rows[0].OrderID)

	e.drainAll()
	patches := e.customerRequests(http.MethodPatch)
	require.Len(t, patches, 1)
	wire := wireOf(t, patches[0])
	assert.Equal(t, "Hopper", wire["last_name"])
	assert.Equal(t, "+32470123456", wire["phone"])
	assert.NotContains(t, wire, "address_1")
	cu, _ = e.srv.Customer(cu.ID)
	assert.Equal(t, "Hopper", cu.LastName)
	assert.Equal(t, "+32470123456", cu.Phone)
	assert.Equal(t, domain.OutboxSent, e.customerRows(domain.OutboxCustomer)[0].State)

	// An edit made while the row is on its way queues a new one.
	e.srv.ClearRequests()
	email := "grace@example.com"
	_, err = e.users.UpdateMe(t.Context(), e.user.String(), nil, nil, &email, nil, nil, nil, nil)
	require.NoError(t, err)
	e.drainAll()
	require.Len(t, e.customerRequests(http.MethodPatch), 1)
	assert.Len(t, e.customerRows(domain.OutboxCustomer), 2)

	// The next order finds the customer up to date.
	e.srv.ClearRequests()
	e.placed(orderSpec{})
	e.drainAll()
	assert.Empty(t, e.customerRequests(""))
}

func TestAProfileChangedAfterAnOrderWasQueuedIsWhatTheOrderCarries(t *testing.T) {
	e := newCustEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	id := e.placed(orderSpec{}) // queued with the profile "John Doe"
	last := "Hopper"
	_, err := e.users.UpdateMe(t.Context(), e.user.String(), nil, &last, nil, nil, nil, nil, nil)
	require.NoError(t, err)
	e.drainAll()
	cu := e.srv.Customers()[0]
	assert.Equal(t, "Hopper", cu.LastName, "the customer is written from the profile as it is when the order leaves")
	assert.Equal(t, cu.ID, e.remote(id).CustomerID)
}

func TestDeletingTheAccountAnonymisesTheHubRiseCustomer(t *testing.T) {
	e := newCustEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	e.placed(orderSpec{})
	e.drainAll()
	cu := e.srv.Customers()[0]

	// A profile update still queued is dropped by the deletion.
	first := "Grace"
	_, err := e.users.UpdateMe(t.Context(), e.user.String(), &first, nil, nil, nil, nil, nil, nil)
	require.NoError(t, err)
	require.Len(t, e.customerRows(domain.OutboxCustomer), 1)

	require.NoError(t, e.users.DeleteMe(t.Context(), e.user.String()))
	profileRows := e.customerRows(domain.OutboxCustomer)
	require.Len(t, profileRows, 1)
	assert.Equal(t, domain.OutboxSkipped, profileRows[0].State)
	rows := e.customerRows(domain.OutboxCustomerAnonymise)
	require.Len(t, rows, 1)
	assert.Nil(t, rows[0].OrderID, "the row belongs to no order")
	var payload domain.CustomerAnonymisePayload
	require.NoError(t, json.Unmarshal(rows[0].Payload, &payload))
	assert.Equal(t, cu.ID, payload.HubriseCustomerID, "the payload carries the HubRise id")
	assert.Equal(t, hubrisetest.CustomerListID, payload.CustomerListID)
	assert.Equal(t, e.user, payload.UserID)

	var email string
	require.NoError(t, e.db.GetContext(t.Context(), &email, `SELECT email FROM users WHERE id = $1`, e.user))
	assert.Contains(t, email, "deleted.invalid", "the local account is erased all the same")

	// The row is sent even if the link row is gone with the user.
	e.exec(`DELETE FROM hubrise_customers WHERE user_id = $1`, e.user)
	e.srv.ClearRequests()
	e.drainAll()
	posts := e.customerRequests(http.MethodPost)
	require.Len(t, posts, 1)
	assert.Contains(t, posts[0].Path, cu.ID+"/anonymise")
	cu, _ = e.srv.Customer(cu.ID)
	assert.True(t, cu.Anonymised)
	assert.Empty(t, cu.Email)
	assert.Empty(t, cu.FirstName)
	assert.Equal(t, domain.OutboxSent, e.customerRows(domain.OutboxCustomerAnonymise)[0].State)
}

func TestTheLinkRecordsThatTheCustomerWasAnonymised(t *testing.T) {
	e := newCustEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	e.placed(orderSpec{})
	e.drainAll()
	require.NoError(t, e.users.DeleteMe(t.Context(), e.user.String()))
	e.drainAll()
	link := e.customerLink()
	assert.NotNil(t, link.AnonymisedAt)

	// An order of the deleted user that is still on its way goes out with the guest object and never
	// brings the customer back.
	e.srv.ClearRequests()
	late := e.placed(orderSpec{})
	e.drainAll()
	assert.Empty(t, e.customerRequests(""))
	remote := e.remote(late)
	assert.Empty(t, remote.CustomerID)
	require.NotNil(t, remote.Customer)
	assert.Equal(t, "Anonyme", remote.Customer.FirstName, "the order shows the user as the shop now knows them")
}

func TestAnAnonymisedLinkNeverWritesTheCustomerAgain(t *testing.T) {
	e := newCustEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	e.placed(orderSpec{})
	e.drainAll()
	// The account is not deleted locally, only the link says the customer was anonymised.
	require.NoError(t, e.mod.CustomerLinks.MarkAnonymised(t.Context(), e.user, e.clock.Now()))
	e.srv.ClearRequests()
	id := e.placed(orderSpec{})
	e.drainAll()
	assert.Empty(t, e.customerRequests(""))
	assert.Empty(t, e.remote(id).CustomerID)
	assert.NotNil(t, e.remote(id).Customer)
}

func TestWithoutCustomerSyncTheOrderCarriesAGuestCustomer(t *testing.T) {
	for name, opts := range map[string][]envOption{
		"the sync is off":      {withSeed(hubtest.WithFeatures(true, true, true, false, false))},
		"there is no list":     {withSeed(hubtest.WithCustomerList("", ""))},
		"the list is not ours": {withSeed(hubtest.WithCustomerList("some-other-list", "Elsewhere"))},
	} {
		t.Run(name, func(t *testing.T) {
			e := newCustEnv(t, opts...)
			e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
			id := e.placed(orderSpec{})
			e.drainAll()
			remote := e.remote(id)
			assert.Empty(t, remote.CustomerID)
			require.NotNil(t, remote.Customer)
			assert.Equal(t, "John", remote.Customer.FirstName)
			assert.Equal(t, domain.PushSent, e.link(id).PushState, "the order is never held back")
			assert.Empty(t, e.srv.Customers())
			if name != "the list is not ours" {
				assert.Empty(t, e.customerRequests(""), "no call is made to the customer list")
			}
		})
	}
}

func TestAWorkerWithoutTheCustomerSyncSendsGuestCustomers(t *testing.T) {
	e := newCustEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	id := e.placed(orderSpec{})
	w := application.NewOutboxWorker(application.WorkerDeps{
		Connections: e.mod.Connections, Outbox: e.mod.Outbox, Orders: e.mod.Orders, Links: e.mod.CatalogLinks,
		Source: e.mod.Source, Clients: e.opts.Clients, Clock: e.clock,
	})
	e.drainWith(w)
	assert.Empty(t, e.remote(id).CustomerID)
	assert.NotNil(t, e.remote(id).Customer)
	assert.Empty(t, e.customerRequests(""))

	// It does not lease the customer rows either: they wait for a worker that handles them.
	_, _, err := e.mod.CustomerLinks.Begin(t.Context(), e.user)
	require.NoError(t, err)
	require.NoError(t, e.mod.CustomerLinks.SetCustomer(t.Context(), e.user, "cu-1", "", e.clock.Now()))
	require.NoError(t, e.push.Customers.AccountDeleted(t.Context(), e.user))
	require.Len(t, e.customerRows(domain.OutboxCustomerAnonymise), 1)
	assert.Zero(t, e.countDrained(w))
	assert.Equal(t, domain.OutboxPending, e.customerRows(domain.OutboxCustomerAnonymise)[0].State)
}

func (e *env) countDrained(w *application.OutboxWorker) int {
	e.t.Helper()
	n, err := w.DrainOnce(e.t.Context())
	require.NoError(e.t, err)
	return n
}

func TestAPayloadWithoutAUserIsSentAsAGuest(t *testing.T) {
	e := newCustEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	id := e.placed(orderSpec{})
	e.exec(`UPDATE hubrise_outbox SET payload = jsonb_set(payload, '{user_id}', '"00000000-0000-0000-0000-000000000000"') WHERE order_id = $1`, id)
	e.drainAll()
	assert.Empty(t, e.remote(id).CustomerID)
	assert.NotNil(t, e.remote(id).Customer)
	assert.Empty(t, e.customerRequests(""))
}

func TestACustomerLostAfterTheCreationIsFoundAgainAndNeverDuplicated(t *testing.T) {
	t.Run("the response of the creation is lost", func(t *testing.T) {
		e := newCustEnv(t)
		e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
		id := e.placed(orderSpec{})

		e.srv.AddFault(hubrisetest.Fault{Method: http.MethodPost, Path: customersPath, Times: 1, DropAfter: true})
		e.drain()
		require.Len(t, e.srv.Customers(), 1, "the customer reached HubRise")
		assert.Empty(t, e.posts(), "but the order waits")
		link := e.customerLink()
		assert.Nil(t, link.HubriseCustomerID, "and the link says a creation was attempted")
		row := e.outboxRows(id, domain.OutboxOrderCreate)[0]
		assert.Equal(t, domain.OutboxPending, row.State)

		e.clock.Advance(31 * time.Second)
		e.drainAll()
		assert.Len(t, e.srv.Customers(), 1, "no second customer")
		assert.Len(t, e.customerRequests(http.MethodGet), 1, "the customer was looked up by private_ref")
		assert.Len(t, e.customerRequests(http.MethodPost), 1)
		assert.Equal(t, e.srv.Customers()[0].ID, e.remote(id).CustomerID)
		assert.Equal(t, e.srv.Customers()[0].ID, *e.customerLink().HubriseCustomerID)
	})

	t.Run("the process dies before the link is saved", func(t *testing.T) {
		e := newCustEnv(t)
		e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
		id := e.placed(orderSpec{})

		f := newFaults("CustomerLinks.SetCustomer")
		cs := e.customerSync(f)
		w := e.customerWorker(cs)
		e.drainWith(w)
		require.Len(t, e.srv.Customers(), 1)
		assert.Empty(t, e.posts())
		assert.Nil(t, e.customerLink().HubriseCustomerID)

		f.set("CustomerLinks.SetCustomer", nil)
		e.clock.Advance(31 * time.Second)
		e.drainWith(w)
		assert.Len(t, e.srv.Customers(), 1, "still one customer")
		assert.Equal(t, e.srv.Customers()[0].ID, e.remote(id).CustomerID)
	})

	t.Run("a second order after a failed creation finds the customer too", func(t *testing.T) {
		e := newCustEnv(t)
		e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
		first := e.placed(orderSpec{})
		e.srv.AddFault(hubrisetest.Fault{Method: http.MethodPost, Path: customersPath, Times: 1, DropAfter: true})
		e.drain()
		// A different order of the same user reaches the customer first (its row has its own attempts).
		second := e.placed(orderSpec{})
		e.drainAll()
		assert.Len(t, e.srv.Customers(), 1)
		assert.Equal(t, e.srv.Customers()[0].ID, e.remote(second).CustomerID)
		e.clock.Advance(31 * time.Second)
		e.drainAll()
		assert.Equal(t, e.srv.Customers()[0].ID, e.remote(first).CustomerID)
		assert.Len(t, e.srv.Customers(), 1)
	})
}

func TestACustomerDeletedAtHubRiseIsCreatedAgain(t *testing.T) {
	e := newCustEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	e.placed(orderSpec{})
	e.drainAll()
	old := e.srv.Customers()[0]
	require.True(t, e.srv.DeleteCustomer(old.ID))

	// The profile changes, the PATCH meets a 404: the customer is looked up, not found, created again.
	e.srv.ClearRequests()
	last := "Hopper"
	_, err := e.users.UpdateMe(t.Context(), e.user.String(), nil, &last, nil, nil, nil, nil, nil)
	require.NoError(t, err)
	e.drainAll()
	customers := e.srv.Customers()
	require.Len(t, customers, 1)
	assert.NotEqual(t, old.ID, customers[0].ID)
	assert.Equal(t, "Hopper", customers[0].LastName)
	assert.Equal(t, customers[0].ID, *e.customerLink().HubriseCustomerID)
	assert.Equal(t, domain.OutboxSent, e.customerRows(domain.OutboxCustomer)[0].State)

	// An order that finds its customer gone does the same.
	require.True(t, e.srv.DeleteCustomer(customers[0].ID))
	phone := "0470 12 34 56"
	_, err = e.users.UpdateMe(t.Context(), e.user.String(), nil, nil, nil, &phone, nil, nil, nil)
	require.NoError(t, err)
	id := e.placed(orderSpec{})
	e.exec(`DELETE FROM hubrise_outbox WHERE kind = 'customer' AND state = 'pending'`)
	e.drainAll()
	require.Len(t, e.srv.Customers(), 1)
	assert.Equal(t, e.srv.Customers()[0].ID, e.remote(id).CustomerID)
}

func TestACustomerListThatRefusesTheCustomerDoesNotHoldBackTheOrder(t *testing.T) {
	e := newCustEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	e.exec(`UPDATE users SET email = 'not-an-email' WHERE id = $1`, e.user)
	id := e.placed(orderSpec{})
	e.drainAll()
	assert.Empty(t, e.srv.Customers())
	remote := e.remote(id)
	assert.Empty(t, remote.CustomerID)
	assert.NotNil(t, remote.Customer, "the order goes out with the guest customer")
	assert.Equal(t, domain.PushSent, e.link(id).PushState)
	assert.Nil(t, e.link(id).NeedsAttention)
}

func TestACustomerListThatIsDownDelaysTheOrderThenGivesUp(t *testing.T) {
	e := newCustEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	id := e.placed(orderSpec{})
	e.srv.AddFault(hubrisetest.Fault{Path: customersPath, Status: 503})

	e.drain()
	row := e.outboxRows(id, domain.OutboxOrderCreate)[0]
	assert.Equal(t, domain.OutboxPending, row.State)
	assert.Equal(t, 1, row.Attempts)
	assert.Empty(t, e.posts())
	e.clock.Advance(31 * time.Second)
	e.drain()
	assert.Equal(t, 2, e.outboxRows(id, domain.OutboxOrderCreate)[0].Attempts)
	assert.Empty(t, e.posts())

	e.clock.Advance(61 * time.Second)
	e.drain()
	assert.Len(t, e.posts(), 1, "the third attempt sends the order with a guest customer")
	remote := e.remote(id)
	assert.Empty(t, remote.CustomerID)
	assert.NotNil(t, remote.Customer)
	assert.Equal(t, domain.PushSent, e.link(id).PushState)
}

func TestARevokedTokenOnTheCustomerListPausesThePipelineLikeAnyOtherCall(t *testing.T) {
	e := newCustEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	id := e.placed(orderSpec{})
	e.srv.AddFault(hubrisetest.Fault{Path: customersPath, Status: 403, Times: 1})
	e.drain()

	row := e.outboxRows(id, domain.OutboxOrderCreate)[0]
	assert.Equal(t, domain.OutboxPending, row.State)
	assert.Equal(t, 0, row.Attempts, "the claim is given back")
	conn, err := e.mod.Connections.Get(t.Context())
	require.NoError(t, err)
	assert.NotNil(t, conn.AuthErrorAt, "the connection is flagged")
	assert.Empty(t, e.posts())
}

func TestAnOrderWaitsWhileAnotherProcessWritesTheSameCustomer(t *testing.T) {
	e := newCustEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	id := e.placed(orderSpec{})
	ok, err := e.mod.Leases.Acquire(t.Context(), "customer:"+e.user.String(), "another-replica", e.clock.Now(), time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	e.drain()
	row := e.outboxRows(id, domain.OutboxOrderCreate)[0]
	assert.Equal(t, domain.OutboxPending, row.State)
	assert.Equal(t, 0, row.Attempts, "waiting for the lease is not an attempt")
	assert.True(t, row.NextAttemptAt.After(e.clock.Now()), "it is retried in a few seconds")
	assert.Empty(t, e.srv.Customers())

	require.NoError(t, e.mod.Leases.Release(t.Context(), "customer:"+e.user.String(), "another-replica"))
	e.clock.Advance(6 * time.Second)
	e.drainAll()
	assert.Len(t, e.srv.Customers(), 1)
	assert.Equal(t, e.srv.Customers()[0].ID, e.remote(id).CustomerID)
	assert.Zero(t, e.count(`SELECT count(*) FROM hubrise_job_leases WHERE name LIKE 'customer:%'`), "the lease is released")
}

func TestTwoInstancesNeverCreateTheSameCustomerTwice(t *testing.T) {
	e := newCustEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	const orders = 8
	for range orders {
		e.ready(e.order(orderSpec{}))
	}
	second := e.rebuild(e.opts)

	done := make(chan struct{}, 2)
	run := func(w *application.OutboxWorker) {
		defer func() { done <- struct{}{} }()
		for range 40 {
			if _, err := w.DrainOnce(t.Context()); err != nil {
				t.Error(err)
				return
			}
		}
	}
	for round := 0; round < 4; round++ {
		go run(e.push.Worker)
		go run(second.Worker)
		<-done
		<-done
		e.clock.Advance(6 * time.Second) // lets the rows that met the lease go again
	}
	e.drainAll()

	assert.Len(t, e.posts(), orders, "one POST per order")
	assert.Len(t, e.srv.Customers(), 1, "one customer, however many instances raced for it")
	for _, o := range e.srv.Orders() {
		assert.Equal(t, e.srv.Customers()[0].ID, o.CustomerID)
	}
}

func TestSyncingCustomersDoesNotChangeWhatTheOrderCarries(t *testing.T) {
	e := newCustEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	id := e.placed(orderSpec{couponCode: "WELCOME", coupon: "1.00", total: "11.50"})
	e.drainAll()
	remote := e.remote(id)
	assert.Equal(t, pkghubrise.StatusNew, remote.Status)
	assert.Equal(t, []string{"WELCOME"}, remote.CouponCodes)
	require.Len(t, remote.Items, 1)
	assert.Equal(t, "SUSHI-SALMON", remote.Items[0].SkuRef)
	assert.Equal(t, "11.50 EUR", string(remote.Payments[0].Amount))
}

func TestDeletingTheAccountLeavesNoPersonalDataInTheOutbox(t *testing.T) {
	e := newCustEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	var email string
	require.NoError(t, e.db.GetContext(t.Context(), &email, `SELECT email FROM users WHERE id = $1`, e.user))
	leaks := func() int {
		return e.count(`SELECT count(*) FROM hubrise_outbox WHERE payload::text LIKE '%' || $1 || '%'`, email)
	}

	queued := e.placed(orderSpec{})
	assert.Equal(t, 1, leaks(), "the queued order carries the customer")
	e.drainAll()
	assert.Zero(t, leaks(), "an order that reached HubRise keeps nothing")

	// An order the push gave up on keeps its data for the retry, until the account is erased.
	parked := e.placed(orderSpec{})
	e.exec(`UPDATE hubrise_outbox SET state = 'parked' WHERE order_id = $1 AND kind = 'order_create'`, parked)
	require.Equal(t, 1, leaks())

	require.NoError(t, e.users.DeleteMe(t.Context(), e.user.String()))
	assert.Zero(t, leaks(), "nothing of the customer survives the deletion")
	assert.NotEqual(t, queued, parked)
}

// The erasure of a deleted account is a legal duty: it must not wait for the customer sync to be
// switched back on.
func TestAnErasureIsSentWhileTheCustomerSyncIsOff(t *testing.T) {
	e := newCustEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	e.placed(orderSpec{})
	e.drainAll()
	cu := e.srv.Customers()[0]

	e.exec(`UPDATE hubrise_connection SET sync_customers = FALSE`)
	require.NoError(t, e.users.DeleteMe(t.Context(), e.user.String()))
	e.srv.ClearRequests()
	e.drainAll()

	posts := e.customerRequests(http.MethodPost)
	require.Len(t, posts, 1)
	assert.Contains(t, posts[0].Path, cu.ID+"/anonymise")
	cu, _ = e.srv.Customer(cu.ID)
	assert.True(t, cu.Anonymised)
	assert.NotNil(t, e.customerLink().AnonymisedAt)
}
