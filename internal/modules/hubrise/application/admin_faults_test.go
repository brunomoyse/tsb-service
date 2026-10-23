package application_test

import (
	"context"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise"
	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/modules/hubrise/domain"
	hubtest "tsb-service/internal/modules/hubrise/testhelpers"
	pkghubrise "tsb-service/pkg/hubrise"
	"tsb-service/pkg/hubrise/hubrisetest"
)

// This file sweeps fault injection over the storage calls of the connection management, the way
// catalog_faults_test.go does for the importer: every repository call is counted, run N fails the
// Nth one, and the operation must report it. A few calls are deliberately best effort (purging old
// states, the inventory baseline after a settings change): a failure there is logged and the
// operation still succeeds.

type adminFaults struct {
	failAt  int
	calls   int
	hit     bool
	hitName string
}

func (f *adminFaults) tick(name string) error {
	f.calls++
	if f.calls == f.failAt {
		f.hit, f.hitName = true, name
		return errInjected
	}
	return nil
}

type adminConns struct {
	domain.ConnectionRepository
	f *adminFaults
}

func (c adminConns) Get(ctx context.Context) (*domain.Connection, error) {
	if err := c.f.tick("Connections.Get"); err != nil {
		return nil, err
	}
	return c.ConnectionRepository.Get(ctx)
}

func (c adminConns) Connect(ctx context.Context, p domain.ConnectParams) (*domain.Connection, error) {
	if err := c.f.tick("Connections.Connect"); err != nil {
		return nil, err
	}
	return c.ConnectionRepository.Connect(ctx, p)
}

func (c adminConns) Disconnect(ctx context.Context) error {
	if err := c.f.tick("Connections.Disconnect"); err != nil {
		return err
	}
	return c.ConnectionRepository.Disconnect(ctx)
}

func (c adminConns) UpdateSettings(ctx context.Context, p domain.SettingsPatch) (*domain.Connection, error) {
	if err := c.f.tick("Connections.UpdateSettings"); err != nil {
		return nil, err
	}
	return c.ConnectionRepository.UpdateSettings(ctx, p)
}

func (c adminConns) SetCallbackRegistered(ctx context.Context, at *time.Time) error {
	if err := c.f.tick("Connections.SetCallbackRegistered"); err != nil {
		return err
	}
	return c.ConnectionRepository.SetCallbackRegistered(ctx, at)
}

func (c adminConns) SetRelinkRequired(ctx context.Context, at *time.Time) error {
	if err := c.f.tick("Connections.SetRelinkRequired"); err != nil {
		return err
	}
	return c.ConnectionRepository.SetRelinkRequired(ctx, at)
}

func (c adminConns) ResetInventoryBaseline(ctx context.Context) error {
	if err := c.f.tick("Connections.ResetInventoryBaseline"); err != nil {
		return err
	}
	return c.ConnectionRepository.ResetInventoryBaseline(ctx)
}

func (c adminConns) SetInventoryBaseline(ctx context.Context, at time.Time) error {
	if err := c.f.tick("Connections.SetInventoryBaseline"); err != nil {
		return err
	}
	return c.ConnectionRepository.SetInventoryBaseline(ctx, at)
}

type adminStates struct {
	domain.OAuthStateRepository
	f *adminFaults
}

func (s adminStates) Create(ctx context.Context, state string, admin uuid.UUID, exp time.Time) error {
	if err := s.f.tick("OAuthStates.Create"); err != nil {
		return err
	}
	return s.OAuthStateRepository.Create(ctx, state, admin, exp)
}

func (s adminStates) Consume(ctx context.Context, state string, now time.Time) (uuid.UUID, bool, error) {
	if err := s.f.tick("OAuthStates.Consume"); err != nil {
		return uuid.Nil, false, err
	}
	return s.OAuthStateRepository.Consume(ctx, state, now)
}

func (s adminStates) PurgeExpired(ctx context.Context, now time.Time) (int64, error) {
	if err := s.f.tick("OAuthStates.PurgeExpired"); err != nil {
		return 0, err
	}
	return s.OAuthStateRepository.PurgeExpired(ctx, now)
}

type adminOrders struct {
	domain.OrderLinkRepository
	f *adminFaults
}

func (o adminOrders) Get(ctx context.Context, id uuid.UUID) (*domain.OrderLink, error) {
	if err := o.f.tick("Orders.Get"); err != nil {
		return nil, err
	}
	return o.OrderLinkRepository.Get(ctx, id)
}

func (o adminOrders) CountNeedsAttention(ctx context.Context) (int, error) {
	if err := o.f.tick("Orders.CountNeedsAttention"); err != nil {
		return 0, err
	}
	return o.OrderLinkRepository.CountNeedsAttention(ctx)
}

type adminOutbox struct {
	domain.OutboxRepository
	f *adminFaults
}

func (o adminOutbox) Counts(ctx context.Context) (domain.OutboxCounts, error) {
	if err := o.f.tick("Outbox.Counts"); err != nil {
		return domain.OutboxCounts{}, err
	}
	return o.OutboxRepository.Counts(ctx)
}

func (o adminOutbox) List(ctx context.Context, s *domain.OutboxState, limit int) ([]domain.OutboxItem, error) {
	if err := o.f.tick("Outbox.List"); err != nil {
		return nil, err
	}
	return o.OutboxRepository.List(ctx, s, limit)
}

func (o adminOutbox) RetryParked(ctx context.Context, id int64, now time.Time) (int64, error) {
	if err := o.f.tick("Outbox.RetryParked"); err != nil {
		return 0, err
	}
	return o.OutboxRepository.RetryParked(ctx, id, now)
}

func (o adminOutbox) RetryAllParked(ctx context.Context, now time.Time) (int64, error) {
	if err := o.f.tick("Outbox.RetryAllParked"); err != nil {
		return 0, err
	}
	return o.OutboxRepository.RetryAllParked(ctx, now)
}

type adminLinks struct {
	domain.CatalogLinkRepository
	f *adminFaults
}

func (l adminLinks) CountUnlinkedProducts(ctx context.Context) (int, error) {
	if err := l.f.tick("Links.CountUnlinkedProducts"); err != nil {
		return 0, err
	}
	return l.CatalogLinkRepository.CountUnlinkedProducts(ctx)
}

func (l adminLinks) LinkedProductIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	if err := l.f.tick("Links.LinkedProductIDs"); err != nil {
		return nil, err
	}
	return l.CatalogLinkRepository.LinkedProductIDs(ctx, ids)
}

func (l adminLinks) DeleteAll(ctx context.Context) (int64, error) {
	if err := l.f.tick("Links.DeleteAll"); err != nil {
		return 0, err
	}
	return l.CatalogLinkRepository.DeleteAll(ctx)
}

func (l adminLinks) Count(ctx context.Context, retired bool) (int, error) {
	if err := l.f.tick("Links.Count"); err != nil {
		return 0, err
	}
	return l.CatalogLinkRepository.Count(ctx, retired)
}

// faultyAdmin is an admin whose storage calls go through the fault counter, including the ones of
// its catalog service (the relink and preview checks it runs).
func (r *adminRig) faultyAdmin(f *adminFaults) *application.Admin {
	r.t.Helper()
	conns := adminConns{r.mod.Connections, f}
	links := adminLinks{r.mod.CatalogLinks, f}
	svc := *r.catalog.Service
	svc.Connections, svc.Links = conns, links
	pooled := application.NewPooledClients(r.mod.Codec, r.srv.APIBaseURL(), r.srv.HTTPClient(), nil)
	return application.NewAdmin(application.AdminDeps{
		Config: application.AdminConfig{RedirectURL: adminAPIBase + "/x", CallbackURL: adminCallback},
		OAuth:  r.srv.NewOAuth(r.t), Connections: conns, OAuthStates: adminStates{r.mod.OAuthStates, f},
		Links: links, Orders: adminOrders{r.mod.Orders, f}, Outbox: adminOutbox{r.mod.Outbox, f},
		Codec: r.mod.Codec, Clients: pooled, Catalog: &svc, Inventory: r.inventory.Service, Clock: r.clock,
		TokenClient: func(token string) (*pkghubrise.Client, error) {
			return pkghubrise.New(r.srv.APIBaseURL(), token, pkghubrise.WithHTTPClient(r.srv.HTTPClient()))
		},
	})
}

// sweepAdmin runs scenario with the Nth storage call failing, for N = 1, 2, ... until a run reaches
// no such call, and returns how many calls it swept. prepare builds the state of every run;
// tolerated names the calls whose failure the operation absorbs.
func sweepAdmin(t *testing.T, r *adminRig, tolerated []string, prepare func(), scenario func(*application.Admin) error) int {
	t.Helper()
	runs := 0
	for failAt := 1; failAt < 100; failAt++ {
		r.exec(`DELETE FROM hubrise_orders; DELETE FROM hubrise_outbox; DELETE FROM hubrise_catalog_links;
			DELETE FROM hubrise_oauth_states; DELETE FROM hubrise_job_leases; DELETE FROM hubrise_connection`)
		prepare()
		f := &adminFaults{failAt: failAt}
		err := scenario(r.faultyAdmin(f))
		if !f.hit {
			require.NoError(t, err, "a run that reaches no injected call succeeds")
			return runs
		}
		runs++
		if slices.Contains(tolerated, f.hitName) {
			assert.NoError(t, err, "%s is best effort", f.hitName)
			continue
		}
		require.ErrorIs(t, err, errInjected, "failing storage call #%d (%s) must surface", failAt, f.hitName)
	}
	t.Fatal("the sweep did not end")
	return runs
}

// reconnect resets the database to a connected, enabled installation with one link and one parked row.
func (r *adminRig) reconnect(opts ...hubtest.SeedOption) {
	r.t.Helper()
	seed := append([]hubtest.SeedOption{
		hubtest.WithToken(hubrisetest.Token), hubtest.WithLocation(hubrisetest.LocationID, hubrisetest.CatalogID),
		hubtest.WithPushOrdersSince(r.clock.Now().Add(-time.Hour)), hubtest.WithCustomerList("", ""),
	}, opts...)
	hubtest.SeedConnection(r.t, r.db, seed...)
	r.link(r.fx.SalmonSushi.ID, "MAKI-SAUMON-6")
	r.exec(`INSERT INTO hubrise_outbox (kind, dedupe_key, payload, state) VALUES ('inventory', 'inv:sku:Z', '{}', 'parked')`)
}

func TestEveryStorageFailureOfTheStatusSurfaces(t *testing.T) {
	r := newAdminRig(t)
	runs := sweepAdmin(t, r, nil, func() { r.reconnect() }, func(a *application.Admin) error {
		_, err := a.Status(t.Context())
		return err
	})
	assert.Equal(t, 4, runs, "the connection, the backlog, the flagged orders and the unlinked products")
}

func TestEveryStorageFailureOfStartingAConnectionSurfaces(t *testing.T) {
	r := newAdminRig(t)
	runs := sweepAdmin(t, r, []string{"OAuthStates.PurgeExpired"}, func() {}, func(a *application.Admin) error {
		_, err := a.StartConnection(t.Context(), uuid.New())
		return err
	})
	assert.Equal(t, 2, runs)
}

func TestEveryStorageFailureOfCompletingAConnectionSurfaces(t *testing.T) {
	r := newAdminRig(t)
	var state, code string
	runs := sweepAdmin(t, r, nil, func() {
		r.reconnect()
		state, code = r.authorize(uuid.New())
	}, func(a *application.Admin) error {
		_, err := a.CompleteConnection(t.Context(), state, code)
		return err
	})
	assert.GreaterOrEqual(t, runs, 4, "consume, previous, connect, callback stamp, then the relink checks")
}

// A reconnect to another location must never leave the new ids enabled over the old links, whatever
// storage call fails after the connection was stored; and the old location's token is only given up
// once everything succeeded.
func TestAFailedReconnectToAnotherLocationNeverLeavesTheOldLinksActive(t *testing.T) {
	r := newAdminRig(t)
	r.srv.SetOAuthTarget(&hubrisetest.OAuthTarget{LocationID: "loc-2", LocationName: "Other", CatalogID: "cat-2", CatalogName: "Other menu"})
	stored := 0
	for failAt := 1; failAt < 100; failAt++ {
		r.exec(`DELETE FROM hubrise_orders; DELETE FROM hubrise_outbox; DELETE FROM hubrise_catalog_links;
			DELETE FROM hubrise_oauth_states; DELETE FROM hubrise_job_leases; DELETE FROM hubrise_connection`)
		r.reconnect(hubtest.WithToken(hubrisetest.EPOSToken))
		state, code := r.authorize(uuid.New())
		f := &adminFaults{failAt: failAt}
		res, err := r.faultyAdmin(f).CompleteConnection(t.Context(), state, code)
		if !f.hit {
			require.NoError(t, err)
			assert.True(t, res.RelinkRequired)
			break
		}
		require.ErrorIs(t, err, errInjected, "%s", f.hitName)
		assert.False(t, r.srv.TokenRevoked(hubrisetest.EPOSToken), "failing %s: the old token is kept until the reconnect succeeded", f.hitName)
		c := r.conn()
		if *c.LocationID == "loc-2" { // the new ids were stored
			stored++
			assert.False(t, c.Enabled, "failing %s: the new ids must not be live over the old links", f.hitName)
			assert.NotNil(t, c.RelinkRequiredAt, "failing %s", f.hitName)
			assert.False(t, c.SyncCatalog)
			assert.False(t, c.SyncInventory)
		}
	}
	assert.Positive(t, stored, "some failures land after the connection was stored")
}

func TestEveryStorageFailureOfADisconnectSurfaces(t *testing.T) {
	r := newAdminRig(t)
	runs := sweepAdmin(t, r, nil, func() { r.reconnect() }, func(a *application.Admin) error {
		return a.Disconnect(t.Context(), true)
	})
	assert.GreaterOrEqual(t, runs, 3, "read, disconnect, forget the links")
}

func TestEveryStorageFailureOfASettingsChangeSurfaces(t *testing.T) {
	r := newAdminRig(t)
	yes := true
	runs := sweepAdmin(t, r, []string{"Connections.ResetInventoryBaseline"}, func() {
		r.reconnect(hubtest.WithFeatures(false, false, false, false, false))
		// A fresh dry run of the current catalog, so enabling catalog sync is allowed.
		_, err := r.catalog.Service.Sync(t.Context(), application.SyncRequest{Mode: domain.SyncDryRun})
		require.NoError(t, err)
		r.exec(`UPDATE hubrise_connection SET enabled = FALSE`)
	}, func(a *application.Admin) error {
		_, err := a.UpdateSettings(t.Context(), domain.SettingsPatch{Enabled: &yes, SyncCatalog: &yes, SyncInventory: &yes, PushOrders: &yes})
		return err
	})
	assert.GreaterOrEqual(t, runs, 3)
}

func TestEveryStorageFailureOfTheOutboxOperationsSurfaces(t *testing.T) {
	r := newAdminRig(t)
	id := int64(1)
	for name, scenario := range map[string]func(*application.Admin) error{
		"list":      func(a *application.Admin) error { _, err := a.Outbox(t.Context(), nil, 10); return err },
		"retry one": func(a *application.Admin) error { _, err := a.RetryOutbox(t.Context(), &id); return err },
		"retry all": func(a *application.Admin) error { _, err := a.RetryOutbox(t.Context(), nil); return err },
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, 1, sweepAdmin(t, r, nil, func() { r.reconnect() }, scenario))
		})
	}
}

func TestEveryStorageFailureOfTheOrderAndProductLookupsSurfaces(t *testing.T) {
	r := newAdminRig(t)
	order := r.createOrder()
	t.Run("order", func(t *testing.T) {
		assert.Equal(t, 1, sweepAdmin(t, r, nil, func() { r.reconnect() },
			func(a *application.Admin) error { _, err := a.Order(t.Context(), order); return err }))
	})
	t.Run("managed products", func(t *testing.T) {
		assert.Equal(t, 2, sweepAdmin(t, r, nil, func() {
			r.reconnect(hubtest.WithFeatures(true, true, true, true, true))
		}, func(a *application.Admin) error {
			_, err := a.ManagedProducts(t.Context(), []uuid.UUID{r.fx.SalmonSushi.ID})
			return err
		}))
	})
	t.Run("inventory sync", func(t *testing.T) {
		assert.Equal(t, 1, sweepAdmin(t, r, nil, func() {
			r.reconnect()
			r.exec(`UPDATE hubrise_connection SET inventory_baseline_at = now()`)
		}, func(a *application.Admin) error {
			_, err := a.SyncInventory(t.Context())
			return err
		}))
	})
}

func TestAnUnreadableTokenDoesNotStopADisconnect(t *testing.T) {
	r := newAdminRig(t)
	r.exec(`UPDATE hubrise_connection SET access_token_enc = '\x00010203'`)

	require.NoError(t, r.admin.Disconnect(t.Context(), false))

	assert.False(t, r.conn().Connected())
	assert.Equal(t, false, r.audit.all()[0].Details["revoked"])
}

func TestAFailingCallbackDeletionDoesNotStopADisconnect(t *testing.T) {
	r := newAdminRig(t)
	r.srv.AddFault(hubrisetest.Fault{Method: "DELETE", Path: "/v1/callback", Status: 500})

	require.NoError(t, r.admin.Disconnect(t.Context(), false))

	assert.False(t, r.conn().Connected())
}

func TestAnUnreadableTokenOfTheOldLocationDoesNotStopAReconnect(t *testing.T) {
	r := newAdminRig(t, withSeed(hubtest.WithLocation("old-loc", "old-cat")))
	r.exec(`UPDATE hubrise_connection SET access_token_enc = '\x00010203'`)
	state, code := r.authorize(uuid.New())

	res, err := r.admin.CompleteConnection(t.Context(), state, code)
	require.NoError(t, err)
	assert.False(t, res.RelinkRequired)
	assert.Equal(t, hubrisetest.Token, r.tokenOf(r.conn()))
}

func TestTheDriftCheckerOfTheCatalogServiceReportsTheDrift(t *testing.T) {
	r := newAdminRig(t)
	check := r.catalog.Service.DriftChecker()
	require.NoError(t, check.CheckDrift(t.Context()))
}

func TestATokenHubRiseDoesNotKnowIsStillDisconnected(t *testing.T) {
	r := newAdminRig(t)
	enc, err := r.mod.Codec.Encrypt("a-token-the-mock-never-issued")
	require.NoError(t, err)
	r.exec(`UPDATE hubrise_connection SET access_token_enc = $1`, enc)

	require.NoError(t, r.admin.Disconnect(t.Context(), false))

	assert.False(t, r.conn().Connected())
	assert.Equal(t, false, r.audit.all()[0].Details["revoked"], "the revocation was refused and the audit says so")
}

func TestAConnectionWhoseTokenClientCannotBeBuiltIsAFailedAuthorization(t *testing.T) {
	r := newAdminRig(t, withoutConnection())
	broken := application.NewAdmin(application.AdminDeps{
		Config: application.AdminConfig{RedirectURL: adminAPIBase + "/x", CallbackURL: adminCallback},
		OAuth:  r.srv.NewOAuth(t), Connections: r.mod.Connections, OAuthStates: r.mod.OAuthStates, Links: r.mod.CatalogLinks,
		Orders: r.mod.Orders, Outbox: r.mod.Outbox, Codec: r.mod.Codec, Catalog: r.catalog.Service, Clock: r.clock,
		TokenClient: func(string) (*pkghubrise.Client, error) { return nil, errInjected },
	})
	authURL, err := broken.StartConnection(t.Context(), uuid.New())
	require.NoError(t, err)
	u, _ := url.Parse(authURL)

	_, err = broken.CompleteConnection(t.Context(), u.Query().Get("state"), r.srv.IssueAuthCode())

	require.ErrorIs(t, err, domain.ErrAuthorizationFailed)
	assert.NotContains(t, err.Error(), hubrisetest.Token)
}

func TestTheLocationNameIsReadFromHubRiseWhenTheTokenResponseOmitsIt(t *testing.T) {
	r := newAdminRig(t, withoutConnection())
	r.srv.SetOAuthTarget(&hubrisetest.OAuthTarget{LocationID: hubrisetest.LocationID, CatalogID: hubrisetest.CatalogID})
	state, code := r.authorize(uuid.New())

	_, err := r.admin.CompleteConnection(t.Context(), state, code)

	require.NoError(t, err)
	require.NotNil(t, r.conn().LocationName)
	assert.NotEmpty(t, *r.conn().LocationName, "asked HubRise for it")
}

func TestABaselineThatCannotRunNowDoesNotUndoTheSettingsChange(t *testing.T) {
	yes := true
	r := newAdminRig(t, withSeed(hubtest.WithFeatures(true, true, false, false, true)))
	ok, err := r.mod.Leases.Acquire(t.Context(), application.LeaseInventorySync, "someone-else", r.clock.Now(), time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	updated, err := r.admin.UpdateSettings(t.Context(), domain.SettingsPatch{SyncInventory: &yes})

	require.NoError(t, err)
	assert.True(t, updated.SyncInventory)
	assert.Nil(t, r.conn().InventoryBaselineAt, "the next inventory sync pushes the baseline")
}

func TestNewCatalogReportsAConnectionItCannotOpenAClientFor(t *testing.T) {
	e := newEnv(t)
	catalog := e.mod.NewCatalog(e.push, hubrise.CatalogOptions{Clients: failingClients{}})

	_, err := catalog.Service.Sync(t.Context(), application.SyncRequest{Mode: domain.SyncDryRun})

	assert.ErrorIs(t, err, errInjected)
}

type failingClients struct{}

func (failingClients) Client(context.Context, *domain.Connection) (*pkghubrise.Client, error) {
	return nil, errInjected
}

func TestNewAdminRejectsABadAuthorizationServerAddress(t *testing.T) {
	e := newEnv(t)
	broken := *e.mod
	broken.Config.OAuthBaseURL = "nonsense"
	_, err := broken.NewAdmin(e.push, e.mod.NewCatalog(e.push, hubrise.CatalogOptions{}), hubrise.AdminOptions{APIBaseURL: adminAPIBase})
	assert.Error(t, err)
}
