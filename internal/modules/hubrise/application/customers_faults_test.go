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
	pkghubrise "tsb-service/pkg/hubrise"
	"tsb-service/pkg/hubrise/hubrisetest"
)

type faultCustLinks struct {
	domain.CustomerLinkRepository
	f *faults
}

func (l faultCustLinks) Get(ctx context.Context, id uuid.UUID) (*domain.CustomerLink, error) {
	if err := l.f.hit("CustomerLinks.Get"); err != nil {
		return nil, err
	}
	return l.CustomerLinkRepository.Get(ctx, id)
}

func (l faultCustLinks) Begin(ctx context.Context, id uuid.UUID) (*domain.CustomerLink, bool, error) {
	if err := l.f.hit("CustomerLinks.Begin"); err != nil {
		return nil, false, err
	}
	return l.CustomerLinkRepository.Begin(ctx, id)
}

func (l faultCustLinks) SetCustomer(ctx context.Context, id uuid.UUID, cu, hash string, at time.Time) error {
	if err := l.f.hit("CustomerLinks.SetCustomer"); err != nil {
		return err
	}
	return l.CustomerLinkRepository.SetCustomer(ctx, id, cu, hash, at)
}

func (l faultCustLinks) SetHash(ctx context.Context, id uuid.UUID, hash string, at time.Time) error {
	if err := l.f.hit("CustomerLinks.SetHash"); err != nil {
		return err
	}
	return l.CustomerLinkRepository.SetHash(ctx, id, hash, at)
}

func (l faultCustLinks) Forget(ctx context.Context, id uuid.UUID) error {
	if err := l.f.hit("CustomerLinks.Forget"); err != nil {
		return err
	}
	return l.CustomerLinkRepository.Forget(ctx, id)
}

func (l faultCustLinks) MarkAnonymised(ctx context.Context, id uuid.UUID, at time.Time) error {
	if err := l.f.hit("CustomerLinks.MarkAnonymised"); err != nil {
		return err
	}
	return l.CustomerLinkRepository.MarkAnonymised(ctx, id, at)
}

type faultCustSource struct {
	domain.CustomerSource
	f *faults
}

func (s faultCustSource) Profile(ctx context.Context, id uuid.UUID) (*domain.CustomerProfile, error) {
	if err := s.f.hit("Source.Profile"); err != nil {
		return nil, err
	}
	return s.CustomerSource.Profile(ctx, id)
}

func (s faultCustSource) DeletedNotErased(ctx context.Context, limit int) ([]uuid.UUID, error) {
	if err := s.f.hit("Source.DeletedNotErased"); err != nil {
		return nil, err
	}
	return s.CustomerSource.DeletedNotErased(ctx, limit)
}

type faultCustLeases struct {
	domain.LeaseRepository
	f *faults
}

func (l faultCustLeases) Acquire(ctx context.Context, name, holder string, now time.Time, ttl time.Duration) (bool, error) {
	if err := l.f.hit("Leases.Acquire"); err != nil {
		return false, err
	}
	return l.LeaseRepository.Acquire(ctx, name, holder, now, ttl)
}

func (l faultCustLeases) Release(ctx context.Context, name, holder string) error {
	if err := l.f.hit("Leases.Release"); err != nil {
		return err
	}
	return l.LeaseRepository.Release(ctx, name, holder)
}

type faultCustConns struct {
	domain.ConnectionRepository
	f *faults
}

func (c faultCustConns) Get(ctx context.Context) (*domain.Connection, error) {
	if err := c.f.hit("Connections.Get"); err != nil {
		return nil, err
	}
	return c.ConnectionRepository.Get(ctx)
}

// customerSync builds a customer sync on the real stores whose stores can be made to fail.
func (e *env) customerSync(f *faults) *application.CustomerSync {
	return application.NewCustomerSync(application.CustomerDeps{
		Connections: faultCustConns{e.mod.Connections, f}, Links: faultCustLinks{e.mod.CustomerLinks, f},
		Source: faultCustSource{e.mod.Customers, f}, Outbox: faultOutbox{e.mod.Outbox, f},
		Leases: faultCustLeases{e.mod.Leases, f}, Clock: e.clock,
	})
}

// customerWorker builds a worker that sends customer rows with the given sync.
func (e *env) customerWorker(cs *application.CustomerSync) *application.OutboxWorker {
	return application.NewOutboxWorker(application.WorkerDeps{
		Connections: e.mod.Connections, Outbox: e.mod.Outbox, Orders: e.mod.Orders, Links: e.mod.CatalogLinks,
		Source: e.mod.Source, Customers: cs, Clients: e.opts.Clients, Clock: e.clock,
	})
}

// sendFixture is what the direct calls of the customer sync need: a client and the connection.
func (c *custEnv) sendFixture() (*pkghubrise.Client, *domain.Connection) {
	c.t.Helper()
	conn, err := c.mod.Connections.Get(c.t.Context())
	require.NoError(c.t, err)
	return c.srv.NewClient(c.t), conn
}

func (c *custEnv) payload(userID uuid.UUID) domain.CreatePayload {
	return domain.CreatePayload{
		Version: domain.PayloadVersion, UserID: userID,
		Customer: &domain.PayloadCustomer{FirstName: "Order", LastName: "Snapshot"},
	}
}

func TestCustomerHooksStayQuietWhenThereIsNothingToSync(t *testing.T) {
	linked := func(e *custEnv) {
		e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
		e.placed(orderSpec{})
		e.drainAll()
	}
	rows := func(e *custEnv) int {
		return e.count(`SELECT count(*) FROM hubrise_outbox WHERE kind LIKE 'customer%'`)
	}
	update := func(e *custEnv) error { return e.push.Customers.ProfileChanged(e.t.Context(), e.user) }

	t.Run("no connection", func(t *testing.T) {
		e := newCustEnv(t, withoutConnection())
		require.NoError(t, update(e))
		require.NoError(t, e.push.Customers.AccountDeleted(t.Context(), e.user))
		assert.Zero(t, rows(e))
	})
	t.Run("disabled, paused or not syncing customers", func(t *testing.T) {
		for name, q := range map[string]string{
			"disabled": `UPDATE hubrise_connection SET enabled = FALSE`,
			"paused":   `UPDATE hubrise_connection SET auth_error_at = now()`,
			"sync off": `UPDATE hubrise_connection SET sync_customers = FALSE`,
			"no list":  `UPDATE hubrise_connection SET customer_list_id = NULL`,
		} {
			e := newCustEnv(t)
			linked(e)
			e.exec(q)
			require.NoError(t, update(e), name)
			assert.Zero(t, rows(e), name)
		}
	})
	t.Run("a user without a customer", func(t *testing.T) {
		e := newCustEnv(t)
		require.NoError(t, update(e))
		require.NoError(t, e.push.Customers.AccountDeleted(t.Context(), e.user))
		assert.Zero(t, rows(e), "HubRise holds nothing about the user")
	})
	t.Run("a link whose creation never succeeded", func(t *testing.T) {
		e := newCustEnv(t)
		_, _, err := e.mod.CustomerLinks.Begin(t.Context(), e.user)
		require.NoError(t, err)
		require.NoError(t, update(e), "the order creates the customer with the current profile")
		assert.Zero(t, rows(e))
	})
	t.Run("an anonymised customer", func(t *testing.T) {
		e := newCustEnv(t)
		linked(e)
		require.NoError(t, e.mod.CustomerLinks.MarkAnonymised(t.Context(), e.user, e.clock.Now()))
		require.NoError(t, update(e))
		require.NoError(t, e.push.Customers.AccountDeleted(t.Context(), e.user))
		assert.Zero(t, rows(e))
	})
	t.Run("a connection without a list", func(t *testing.T) {
		e := newCustEnv(t)
		linked(e)
		e.exec(`UPDATE hubrise_connection SET customer_list_id = NULL`)
		require.NoError(t, e.push.Customers.AccountDeleted(t.Context(), e.user))
		assert.Zero(t, rows(e))
	})
}

func TestCustomerHooksReportAStoreThatIsDown(t *testing.T) {
	setup := func(t *testing.T) *custEnv {
		e := newCustEnv(t)
		e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
		e.placed(orderSpec{})
		e.drainAll()
		return e
	}
	for _, name := range []string{"Connections.Get", "CustomerLinks.Get", "Outbox.EnqueueIfNoPending"} {
		t.Run("ProfileChanged "+name, func(t *testing.T) {
			e := setup(t)
			cs := e.customerSync(newFaults(name))
			assert.ErrorIs(t, cs.ProfileChanged(t.Context(), e.user), errStore)
		})
	}
	for _, name := range []string{"Connections.Get", "CustomerLinks.Get", "Outbox.SkipOpenByDedupeKey", "Outbox.Enqueue"} {
		t.Run("AccountDeleted "+name, func(t *testing.T) {
			e := setup(t)
			cs := e.customerSync(newFaults(name))
			assert.ErrorIs(t, cs.AccountDeleted(t.Context(), e.user), errStore)
		})
	}
}

func TestCustomerHooksWakeTheWorkerOnlyForANewRow(t *testing.T) {
	e := newCustEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	e.placed(orderSpec{})
	e.drainAll()

	cs := e.customerSync(newFaults())
	cs.ProfileChanged(t.Context(), e.user) //nolint:errcheck // without a wake function installed yet
	woken := 0
	cs.SetWake(func() { woken++ })
	require.NoError(t, cs.ProfileChanged(t.Context(), e.user))
	assert.Zero(t, woken, "the pending row from before absorbs the change")
	e.exec(`UPDATE hubrise_outbox SET state = 'sent' WHERE kind = 'customer'`)
	require.NoError(t, cs.ProfileChanged(t.Context(), e.user))
	assert.Equal(t, 1, woken)
	require.NoError(t, cs.AccountDeleted(t.Context(), e.user))
	assert.Equal(t, 2, woken)
	cs.SetWake(nil)
	require.NoError(t, cs.AccountDeleted(t.Context(), e.user), "a nil wake function is the default one")
}

func TestAccountDeletionStillQueuesWhileTheIntegrationIsPaused(t *testing.T) {
	e := newCustEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	e.placed(orderSpec{})
	e.drainAll()
	e.exec(`UPDATE hubrise_connection SET enabled = FALSE`)
	require.NoError(t, e.users.DeleteMe(t.Context(), e.user.String()))
	require.Len(t, e.customerRows(domain.OutboxCustomerAnonymise), 1)
	e.srv.ClearRequests()
	e.drainAll()
	assert.Empty(t, e.srv.Requests(), "nothing leaves while the integration is off")

	e.exec(`UPDATE hubrise_connection SET enabled = TRUE`)
	e.drainAll()
	assert.True(t, e.srv.Customers()[0].Anonymised, "it is sent once the integration runs again")
}

func TestAnAccountWhoseCreationWasInDoubtIsLookedUpByPrivateRef(t *testing.T) {
	e := newCustEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	e.placed(orderSpec{})
	e.srv.AddFault(hubrisetest.Fault{Method: http.MethodPost, Path: customersPath, Times: 1, DropAfter: true})
	e.drain()
	require.Nil(t, e.customerLink().HubriseCustomerID)
	require.Len(t, e.srv.Customers(), 1)

	require.NoError(t, e.users.DeleteMe(t.Context(), e.user.String()))
	rows := e.customerRows(domain.OutboxCustomerAnonymise)
	require.Len(t, rows, 1)
	var payload domain.CustomerAnonymisePayload
	require.NoError(t, json.Unmarshal(rows[0].Payload, &payload))
	assert.Empty(t, payload.HubriseCustomerID, "the id is not known")

	e.drainAll()
	assert.True(t, e.srv.Customers()[0].Anonymised, "found by private_ref, then anonymised")
}

func TestForOrderFailuresOfTheStores(t *testing.T) {
	for _, name := range []string{"Source.Profile", "Leases.Acquire", "CustomerLinks.Begin"} {
		t.Run(name, func(t *testing.T) {
			e := newCustEnv(t)
			client, conn := e.sendFixture()
			cs := e.customerSync(newFaults(name))
			id, err := cs.ForOrder(t.Context(), client, conn, e.payload(e.user), 1)
			assert.ErrorIs(t, err, errStore)
			assert.Empty(t, id)
		})
	}

	t.Run("a user that does not exist is a guest", func(t *testing.T) {
		e := newCustEnv(t)
		client, conn := e.sendFixture()
		id, err := e.push.Customers.ForOrder(t.Context(), client, conn, e.payload(uuid.New()), 1)
		require.NoError(t, err)
		assert.Empty(t, id)
	})

	t.Run("a lease that cannot be released is only logged", func(t *testing.T) {
		e := newCustEnv(t)
		client, conn := e.sendFixture()
		cs := e.customerSync(newFaults("Leases.Release"))
		id, err := cs.ForOrder(t.Context(), client, conn, e.payload(e.user), 1)
		require.NoError(t, err)
		assert.NotEmpty(t, id)
	})

	t.Run("the id of a new customer cannot be recorded", func(t *testing.T) {
		e := newCustEnv(t)
		client, conn := e.sendFixture()
		cs := e.customerSync(newFaults("CustomerLinks.SetCustomer"))
		_, err := cs.ForOrder(t.Context(), client, conn, e.payload(e.user), 1)
		assert.ErrorIs(t, err, errStore)
		assert.Len(t, e.srv.Customers(), 1)
	})

	t.Run("a customer found again cannot be recorded", func(t *testing.T) {
		e := newCustEnv(t)
		client, conn := e.sendFixture()
		_, _, err := e.mod.CustomerLinks.Begin(t.Context(), e.user)
		require.NoError(t, err)
		_, err = client.CreateCustomer(t.Context(), hubrisetest.CustomerListID, &pkghubrise.Customer{PrivateRef: e.user.String()})
		require.NoError(t, err)
		cs := e.customerSync(newFaults("CustomerLinks.SetCustomer"))
		_, err = cs.ForOrder(t.Context(), client, conn, e.payload(e.user), 1)
		assert.ErrorIs(t, err, errStore)
	})

	t.Run("the hash of an update cannot be recorded", func(t *testing.T) {
		e := newCustEnv(t)
		client, conn := e.sendFixture()
		_, err := e.push.Customers.ForOrder(t.Context(), client, conn, e.payload(e.user), 1)
		require.NoError(t, err)
		e.exec(`UPDATE users SET last_name = 'Hopper' WHERE id = $1`, e.user)
		cs := e.customerSync(newFaults("CustomerLinks.SetHash"))
		_, err = cs.ForOrder(t.Context(), client, conn, e.payload(e.user), 1)
		assert.ErrorIs(t, err, errStore)
	})

	t.Run("a missing customer that cannot be forgotten", func(t *testing.T) {
		e := newCustEnv(t)
		client, conn := e.sendFixture()
		_, err := e.push.Customers.ForOrder(t.Context(), client, conn, e.payload(e.user), 1)
		require.NoError(t, err)
		require.True(t, e.srv.DeleteCustomer(e.srv.Customers()[0].ID))
		e.exec(`UPDATE users SET last_name = 'Hopper' WHERE id = $1`, e.user)
		cs := e.customerSync(newFaults("CustomerLinks.Forget"))
		_, err = cs.ForOrder(t.Context(), client, conn, e.payload(e.user), 1)
		assert.ErrorIs(t, err, errStore)
	})

	t.Run("a lookup that fails keeps the order waiting, then gives up", func(t *testing.T) {
		e := newCustEnv(t)
		client, conn := e.sendFixture()
		_, _, err := e.mod.CustomerLinks.Begin(t.Context(), e.user)
		require.NoError(t, err)
		e.srv.AddFault(hubrisetest.Fault{Method: http.MethodGet, Path: customersPath, Status: 500})
		_, err = e.push.Customers.ForOrder(t.Context(), client, conn, e.payload(e.user), 1)
		assert.ErrorIs(t, err, pkghubrise.KindServer)
		id, err := e.push.Customers.ForOrder(t.Context(), client, conn, e.payload(e.user), 3)
		require.NoError(t, err)
		assert.Empty(t, id, "the third attempt sends a guest")
	})

	t.Run("a customer lost twice in a row is a guest", func(t *testing.T) {
		e := newCustEnv(t)
		client, conn := e.sendFixture()
		_, err := e.push.Customers.ForOrder(t.Context(), client, conn, e.payload(e.user), 1)
		require.NoError(t, err)
		e.exec(`UPDATE users SET last_name = 'Hopper' WHERE id = $1`, e.user)
		// Every PATCH finds the customer missing, even the one that follows the lookup.
		e.srv.AddFault(hubrisetest.Fault{Method: http.MethodPatch, Path: customersPath, Status: 404})
		id, err := e.push.Customers.ForOrder(t.Context(), client, conn, e.payload(e.user), 1)
		require.NoError(t, err)
		assert.Empty(t, id)
		assert.Len(t, e.customerRequests(http.MethodPatch), 2, "the PATCH is not repeated a third time")
	})

	t.Run("a canceled context stops it", func(t *testing.T) {
		e := newCustEnv(t)
		client, conn := e.sendFixture()
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := e.push.Customers.ForOrder(ctx, client, conn, e.payload(e.user), 1)
		assert.Error(t, err)
	})
}

func TestSendProfileSkipsWhatHasNothingToSend(t *testing.T) {
	e := newCustEnv(t)
	client, conn := e.sendFixture()
	cs := e.push.Customers
	ctx := t.Context()

	skip, err := cs.SendProfile(ctx, client, conn, e.user)
	require.NoError(t, err)
	assert.Contains(t, skip, "no HubRise customer", "the customer is created by the first order")

	off := *conn
	off.SyncCustomers = false
	skip, err = cs.SendProfile(ctx, client, &off, e.user)
	require.NoError(t, err)
	assert.Contains(t, skip, "off")
	noList := *conn
	noList.CustomerListID = nil
	skip, err = cs.SendProfile(ctx, client, &noList, e.user)
	require.NoError(t, err)
	assert.Contains(t, skip, "no customer list")

	_, _, err = e.mod.CustomerLinks.Begin(ctx, e.user)
	require.NoError(t, err)
	e.exec(`UPDATE users SET deletion_requested_at = now() WHERE id = $1`, e.user)
	skip, err = cs.SendProfile(ctx, client, conn, e.user)
	require.NoError(t, err)
	assert.Contains(t, skip, "deleted")

	require.NoError(t, e.mod.CustomerLinks.MarkAnonymised(ctx, e.user, e.clock.Now()))
	skip, err = cs.SendProfile(ctx, client, conn, e.user)
	require.NoError(t, err)
	assert.Contains(t, skip, "anonymised")

	other := insertedUser(t, e.env, "other@example.com")
	_, _, err = e.mod.CustomerLinks.Begin(ctx, other)
	require.NoError(t, err)
	e.exec(`DELETE FROM users WHERE id = $1`, other) // the link goes with the user
	skip, err = cs.SendProfile(ctx, client, conn, other)
	require.NoError(t, err)
	assert.NotEmpty(t, skip)
	assert.Empty(t, e.srv.Customers())
}

func insertedUser(t *testing.T, e *env, email string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, e.db.GetContext(t.Context(), &id, `
		INSERT INTO users (first_name, last_name, email, zitadel_user_id) VALUES ('T', 'U', $1, $1) RETURNING id`, email))
	return id
}

func TestSendProfileReportsFailures(t *testing.T) {
	for _, name := range []string{"CustomerLinks.Get", "Source.Profile"} {
		t.Run(name, func(t *testing.T) {
			e := newCustEnv(t)
			client, conn := e.sendFixture()
			_, _, err := e.mod.CustomerLinks.Begin(t.Context(), e.user)
			require.NoError(t, err)
			_, err = e.customerSync(newFaults(name)).SendProfile(t.Context(), client, conn, e.user)
			assert.ErrorIs(t, err, errStore)
		})
	}

	t.Run("a customer written elsewhere", func(t *testing.T) {
		e := newCustEnv(t)
		client, conn := e.sendFixture()
		ok, err := e.mod.Leases.Acquire(t.Context(), "customer:"+e.user.String(), "another-replica", e.clock.Now(), time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		_, err = e.push.Customers.SendProfile(t.Context(), client, conn, e.user)
		assert.ErrorIs(t, err, application.ErrCustomerBusy)
	})
}

func TestSendAnonymise(t *testing.T) {
	e := newCustEnv(t)
	client, conn := e.sendFixture()
	cs := e.push.Customers
	ctx := t.Context()
	list := hubrisetest.CustomerListID
	created, err := client.CreateCustomer(ctx, list, &pkghubrise.Customer{PrivateRef: e.user.String(), Email: "a@example.com"})
	require.NoError(t, err)
	_, _, err = e.mod.CustomerLinks.Begin(ctx, e.user)
	require.NoError(t, err)

	t.Run("no customer list anywhere", func(t *testing.T) {
		bare := *conn
		bare.CustomerListID = nil
		skip, err := cs.SendAnonymise(ctx, client, &bare, domain.CustomerAnonymisePayload{UserID: e.user, HubriseCustomerID: created.ID})
		require.NoError(t, err)
		assert.Contains(t, skip, "no customer list")
	})

	t.Run("a customer without a HubRise id that HubRise does not have", func(t *testing.T) {
		skip, err := cs.SendAnonymise(ctx, client, conn, domain.CustomerAnonymisePayload{UserID: uuid.New()})
		require.NoError(t, err)
		assert.Contains(t, skip, "no customer")
	})

	t.Run("the lookup fails", func(t *testing.T) {
		e.srv.AddFault(hubrisetest.Fault{Method: http.MethodGet, Path: customersPath, Status: 500, Times: 1})
		_, err := cs.SendAnonymise(ctx, client, conn, domain.CustomerAnonymisePayload{UserID: e.user})
		assert.ErrorIs(t, err, pkghubrise.KindServer)
	})

	t.Run("the list of the connection is used when the payload names none", func(t *testing.T) {
		skip, err := cs.SendAnonymise(ctx, client, conn, domain.CustomerAnonymisePayload{UserID: e.user, HubriseCustomerID: created.ID})
		require.NoError(t, err)
		assert.Empty(t, skip)
		got, _ := e.srv.Customer(created.ID)
		assert.True(t, got.Anonymised)
		assert.NotNil(t, e.customerLinkOf(e.user).AnonymisedAt)
	})

	t.Run("a customer HubRise no longer has is nothing left to erase", func(t *testing.T) {
		other := insertedUser(t, e.env, "gone@example.com")
		_, _, err := e.mod.CustomerLinks.Begin(ctx, other)
		require.NoError(t, err)
		skip, err := cs.SendAnonymise(ctx, client, conn, domain.CustomerAnonymisePayload{
			UserID: other, HubriseCustomerID: "ghost", CustomerListID: list,
		})
		require.NoError(t, err)
		assert.Contains(t, skip, "no longer has")
		assert.NotNil(t, e.customerLinkOf(other).AnonymisedAt)
	})

	t.Run("a refusal is returned", func(t *testing.T) {
		e.srv.AddFault(hubrisetest.Fault{Method: http.MethodPost, Path: customersPath, Status: 500, Times: 1})
		_, err := cs.SendAnonymise(ctx, client, conn, domain.CustomerAnonymisePayload{UserID: e.user, HubriseCustomerID: created.ID})
		assert.ErrorIs(t, err, pkghubrise.KindServer)
	})

	t.Run("the link cannot be marked", func(t *testing.T) {
		for _, id := range []string{created.ID, "ghost"} {
			_, err := e.customerSync(newFaults("CustomerLinks.MarkAnonymised")).SendAnonymise(ctx, client, conn,
				domain.CustomerAnonymisePayload{UserID: e.user, HubriseCustomerID: id})
			assert.ErrorIs(t, err, errStore, id)
		}
	})

	t.Run("a customer written elsewhere", func(t *testing.T) {
		busy := insertedUser(t, e.env, "busy@example.com")
		ok, err := e.mod.Leases.Acquire(ctx, "customer:"+busy.String(), "another-replica", e.clock.Now(), time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		_, err = cs.SendAnonymise(ctx, client, conn, domain.CustomerAnonymisePayload{UserID: busy, HubriseCustomerID: created.ID})
		assert.ErrorIs(t, err, application.ErrCustomerBusy)
	})
}

func (c *custEnv) customerLinkOf(user uuid.UUID) *domain.CustomerLink {
	c.t.Helper()
	l, err := c.mod.CustomerLinks.Get(c.t.Context(), user)
	require.NoError(c.t, err)
	return l
}

func TestWorkerSettlesCustomerRows(t *testing.T) {
	linkedEnv := func(t *testing.T) *custEnv {
		e := newCustEnv(t)
		e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
		e.placed(orderSpec{})
		e.drainAll()
		return e
	}
	profileRow := func(e *custEnv, payload string, ago time.Duration) int64 {
		var id int64
		require.NoError(e.t, e.db.GetContext(e.t.Context(), &id, `
			INSERT INTO hubrise_outbox (kind, dedupe_key, payload, state, next_attempt_at, created_at)
			VALUES ('customer', 'cust:' || gen_random_uuid()::text, $1::jsonb, 'pending', $2, $2) RETURNING id`, payload, e.clock.Now().Add(-ago)))
		return id
	}
	state := func(e *custEnv, id int64) domain.OutboxState {
		item, err := e.mod.Outbox.Get(e.t.Context(), id)
		require.NoError(e.t, err)
		return item.State
	}

	t.Run("an unreadable payload is parked", func(t *testing.T) {
		e := linkedEnv(t)
		bad := profileRow(e, `{"v":99}`, 0)
		nobody := profileRow(e, `{"v":1,"user_id":"00000000-0000-0000-0000-000000000000"}`, 0)
		anon := e.rawRowID("customer_anonymise", `{"v":1}`)
		e.drainAll()
		for _, id := range []int64{bad, nobody, anon} {
			assert.Equal(t, domain.OutboxParked, state(e, id))
		}
	})

	t.Run("an old update is skipped, the next order brings the customer up to date", func(t *testing.T) {
		e := linkedEnv(t)
		id := profileRow(e, `{"v":1,"user_id":"`+e.user.String()+`"}`, 25*time.Hour)
		e.srv.ClearRequests()
		e.drainAll()
		assert.Equal(t, domain.OutboxSkipped, state(e, id))
		assert.Empty(t, e.customerRequests(""))
	})

	t.Run("an update for a user without a customer is skipped", func(t *testing.T) {
		e := newCustEnv(t)
		id := profileRow(e, `{"v":1,"user_id":"`+e.user.String()+`"}`, 0)
		e.drainAll()
		assert.Equal(t, domain.OutboxSkipped, state(e, id))
	})

	t.Run("a customer HubRise refuses is parked", func(t *testing.T) {
		e := linkedEnv(t)
		e.exec(`UPDATE users SET email = 'not-an-email' WHERE id = $1`, e.user)
		require.NoError(t, e.push.Customers.ProfileChanged(t.Context(), e.user))
		e.drainAll()
		rows := e.customerRows(domain.OutboxCustomer)
		require.Len(t, rows, 1)
		assert.Equal(t, domain.OutboxParked, rows[0].State)
		require.NotNil(t, rows[0].LastError)
		assert.Contains(t, *rows[0].LastError, "email")
	})

	t.Run("a customer being written elsewhere is retried soon", func(t *testing.T) {
		e := linkedEnv(t)
		require.NoError(t, e.push.Customers.ProfileChanged(t.Context(), e.user))
		ok, err := e.mod.Leases.Acquire(t.Context(), "customer:"+e.user.String(), "another-replica", e.clock.Now(), time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		e.drain()
		row := e.customerRows(domain.OutboxCustomer)[0]
		assert.Equal(t, domain.OutboxPending, row.State)
		assert.Equal(t, 0, row.Attempts)
		assert.True(t, row.NextAttemptAt.After(e.clock.Now()))
	})

	t.Run("a refused connection pauses the pipeline", func(t *testing.T) {
		e := linkedEnv(t)
		require.NoError(t, e.push.Customers.ProfileChanged(t.Context(), e.user))
		e.srv.AddFault(hubrisetest.Fault{Method: http.MethodPatch, Path: customersPath, Status: 401, Times: 1})
		e.exec(`UPDATE users SET last_name = 'Hopper' WHERE id = $1`, e.user)
		e.drain()
		conn, err := e.mod.Connections.Get(t.Context())
		require.NoError(t, err)
		assert.NotNil(t, conn.AuthErrorAt)
		assert.Equal(t, domain.OutboxPending, e.customerRows(domain.OutboxCustomer)[0].State)
	})

	t.Run("an anonymisation HubRise refuses is parked, and a retry sends it", func(t *testing.T) {
		e := linkedEnv(t)
		require.NoError(t, e.users.DeleteMe(t.Context(), e.user.String()))
		e.srv.AddFault(hubrisetest.Fault{Method: http.MethodPost, Path: customersPath, Status: 422, Times: 1})
		e.drainAll()
		rows := e.customerRows(domain.OutboxCustomerAnonymise)
		require.Len(t, rows, 1)
		assert.Equal(t, domain.OutboxParked, rows[0].State)

		n, err := e.mod.Outbox.RetryParked(t.Context(), rows[0].ID, e.clock.Now())
		require.NoError(t, err)
		assert.EqualValues(t, 1, n)
		e.drainAll()
		assert.True(t, e.srv.Customers()[0].Anonymised)
	})

	t.Run("a customer that was never anonymised blocks nothing behind it", func(t *testing.T) {
		e := linkedEnv(t)
		first := "Grace"
		_, err := e.users.UpdateMe(t.Context(), e.user.String(), &first, nil, nil, nil, nil, nil, nil)
		require.NoError(t, err)
		e.exec(`UPDATE hubrise_outbox SET state = 'parked' WHERE kind = 'customer'`)
		require.NoError(t, e.users.DeleteMe(t.Context(), e.user.String()))
		e.drainAll()
		assert.True(t, e.srv.Customers()[0].Anonymised, "a parked profile row does not hold the erasure back")
	})
}

func (e *env) rawRowID(kind, payload string) int64 {
	e.t.Helper()
	var id int64
	require.NoError(e.t, e.db.GetContext(e.t.Context(), &id, `
		INSERT INTO hubrise_outbox (kind, dedupe_key, payload, state, next_attempt_at, created_at)
		VALUES ($1, 'cust:' || gen_random_uuid()::text, $2::jsonb, 'pending', $3, $3) RETURNING id`, kind, payload, e.clock.Now()))
	return id
}

func TestCustomerConfigDefaults(t *testing.T) {
	e := newCustEnv(t)
	// A tiny call timeout still gets a lease that outlasts the three calls of a write, and a
	// transient failure counts: with TransientAttempts 1 the first failure already sends a guest.
	cs := application.NewCustomerSync(application.CustomerDeps{
		Connections: e.mod.Connections, Links: e.mod.CustomerLinks, Source: e.mod.Customers, Outbox: e.mod.Outbox,
		Leases: e.mod.Leases, Clock: e.clock, Holder: "me",
		Config: application.CustomerConfig{CallTimeout: 10 * time.Minute, TransientAttempts: 1},
	})
	client, conn := e.sendFixture()
	e.srv.AddFault(hubrisetest.Fault{Method: http.MethodPost, Path: customersPath, Status: 500})
	id, err := cs.ForOrder(t.Context(), client, conn, e.payload(e.user), 1)
	require.NoError(t, err)
	assert.Empty(t, id)
}

// A deletion whose hook failed (the database was down, the process died) is final: nobody asks
// again, so the reconciler finds the account and queues the erasure.
func TestTheReconcilerQueuesTheErasureOfADeletionTheHookMissed(t *testing.T) {
	e := newCustEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	e.placed(orderSpec{})
	e.drainAll()
	cu := e.srv.Customers()[0]
	// The account is deleted without the hook ever running.
	e.exec(`UPDATE users SET deletion_requested_at = now(), first_name = 'Anonyme' WHERE id = $1`, e.user)
	require.Empty(t, e.customerRows(domain.OutboxCustomerAnonymise))

	require.NoError(t, e.push.Reconciler.RunOnce(t.Context()))
	rows := e.customerRows(domain.OutboxCustomerAnonymise)
	require.Len(t, rows, 1)
	var payload domain.CustomerAnonymisePayload
	require.NoError(t, json.Unmarshal(rows[0].Payload, &payload))
	assert.Equal(t, cu.ID, payload.HubriseCustomerID)

	require.NoError(t, e.push.Reconciler.RunOnce(t.Context()))
	assert.Len(t, e.customerRows(domain.OutboxCustomerAnonymise), 1, "queued once")

	e.drainAll()
	cu, _ = e.srv.Customer(cu.ID)
	assert.True(t, cu.Anonymised)
	require.NoError(t, e.push.Reconciler.RunOnce(t.Context()))
	assert.Len(t, e.customerRows(domain.OutboxCustomerAnonymise), 1, "and never again once erased")
}

func TestErasingMissedAccountsReportsFailures(t *testing.T) {
	setup := func(t *testing.T) *custEnv {
		e := newCustEnv(t)
		e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
		e.placed(orderSpec{})
		e.drainAll()
		e.exec(`UPDATE users SET deletion_requested_at = now() WHERE id = $1`, e.user)
		return e
	}
	t.Run("the list cannot be read", func(t *testing.T) {
		e := setup(t)
		cs := e.customerSync(newFaults("Source.DeletedNotErased"))
		require.Error(t, cs.EraseMissed(t.Context(), 10))
	})
	t.Run("an account cannot be queued", func(t *testing.T) {
		e := setup(t)
		cs := e.customerSync(newFaults("Outbox.Enqueue"))
		err := cs.EraseMissed(t.Context(), 10)
		require.Error(t, err)
		assert.Contains(t, err.Error(), e.user.String())
		assert.Empty(t, e.customerRows(domain.OutboxCustomerAnonymise))
		// The next run tries again.
		require.NoError(t, e.customerSync(newFaults()).EraseMissed(t.Context(), 10))
		assert.Len(t, e.customerRows(domain.OutboxCustomerAnonymise), 1)
	})
}

// A scrub that fails is reported, but the erasure of the HubRise customer is queued all the same; the
// reconciler repeats the scrub for the deleted accounts.
func TestAFailedScrubDoesNotStopTheErasureFromBeingQueued(t *testing.T) {
	e := newCustEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	e.placed(orderSpec{})
	e.drainAll()

	err := e.customerSync(newFaults("Outbox.ScrubCustomerData")).AccountDeleted(t.Context(), e.user)
	require.Error(t, err)
	assert.Len(t, e.customerRows(domain.OutboxCustomerAnonymise), 1)
}
