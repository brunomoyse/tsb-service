package application_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/testhelpers"
	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/internal/modules/hubrise/infrastructure"
	hubtest "tsb-service/internal/modules/hubrise/testhelpers"
	"tsb-service/pkg/hubrise"
	"tsb-service/pkg/hubrise/hubrisetest"
	"tsb-service/pkg/timezone"
)

// pubAudit records the audit calls of the publisher.
type pubAudit struct {
	mu    sync.Mutex
	calls []pubAuditCall
}

type pubAuditCall struct {
	action  string
	success bool
	details map[string]any
}

func (a *pubAudit) record(_ context.Context, action string, success bool, details map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, pubAuditCall{action, success, details})
}

func (a *pubAudit) all() []pubAuditCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]pubAuditCall(nil), a.calls...)
}

// penv is a migrated database with a connected installation in PUBLISH mode, a HubRise mock holding an
// empty catalog, and the publisher wired to both.
type penv struct {
	t      *testing.T
	db     *sqlx.DB
	srv    *hubrisetest.Server
	clock  *hubtest.FakeClock
	pub    *application.CatalogPublisher
	conns  domain.ConnectionRepository
	links  domain.CatalogLinkRepository
	state  domain.PublishStateRepository
	items  domain.PublishedItemRepository
	notes  *catNotifier
	audits *pubAudit
	cat    uuid.UUID
}

func newPubEnv(t *testing.T, seed ...hubtest.SeedOption) *penv {
	t.Helper()
	td := testhelpers.SetupTestDatabase(t)
	clock := hubtest.NewFakeClock(time.Date(2026, 10, 26, 12, 0, 0, 0, timezone.Location))
	srv := hubrisetest.New(t, hubrisetest.WithClock(clock.Now))
	require.NoError(t, srv.ReplaceCatalogRaw(json.RawMessage(`{}`)))
	opts := append([]hubtest.SeedOption{hubtest.WithToken(hubrisetest.Token), hubtest.WithLocation(hubrisetest.LocationID, hubrisetest.CatalogID), hubtest.WithPublishCatalog()}, seed...)
	hubtest.SeedConnection(t, td.DB, opts...)
	e := &penv{t: t, db: td.DB, srv: srv, clock: clock, notes: &catNotifier{}, audits: &pubAudit{}}
	e.conns = infrastructure.NewConnectionRepository(td.DB)
	e.links = infrastructure.NewCatalogLinkRepository(td.DB)
	e.state = infrastructure.NewPublishStateRepository(td.DB)
	e.items = infrastructure.NewPublishedItemRepository(td.DB)
	factory := &application.ClientFactory{BaseURL: srv.APIBaseURL(), Codec: hubtest.TestCodec(t), Limiter: hubrise.NewLimiter(hubrise.Limits{}), HTTP: srv.HTTPClient()}
	e.pub = &application.CatalogPublisher{
		Connections: e.conns, Links: e.links, State: e.state, Items: e.items,
		Local: infrastructure.NewLocalCatalogRepository(td.DB), Clock: clock, Leases: infrastructure.NewLeaseRepository(td.DB),
		NewClient: func(c *domain.Connection) (application.PublishAPI, error) { return factory.Client(c) },
		Notifier:  e.notes, Audit: e.audits.record, Holder: "replica-a",
	}
	e.cat = e.category(1, "Makis")
	return e
}

func (e *penv) exec(query string, args ...any) {
	e.t.Helper()
	_, err := e.db.ExecContext(e.t.Context(), query, args...)
	require.NoError(e.t, err)
}

func (e *penv) category(order int, fr string) uuid.UUID {
	e.t.Helper()
	id := uuid.New()
	e.exec(`INSERT INTO product_categories (id, "order", slug) VALUES ($1, $2, $3)`, id, order, "c-"+id.String()[:6])
	e.exec(`INSERT INTO product_category_translations (product_category_id, language, name) VALUES ($1, 'fr', $2)`, id, fr)
	return id
}

func (e *penv) product(code, fr, price string) uuid.UUID {
	e.t.Helper()
	id := uuid.New()
	var c any
	if code != "" {
		c = code
	}
	e.exec(`INSERT INTO products (id, category_id, price, code, slug, vat_category, is_visible, is_available) VALUES ($1, $2, $3, $4, $5, 'food', true, true)`,
		id, e.cat, price, c, "s-"+id.String()[:8])
	e.exec(`INSERT INTO product_translations (product_id, language, name) VALUES ($1, 'fr', $2)`, id, fr)
	return id
}

func (e *penv) group(product uuid.UUID, fr string, choices ...string) uuid.UUID {
	e.t.Helper()
	id := uuid.New()
	e.exec(`INSERT INTO product_choice_groups (id, product_id, min_selections, max_selections, sort_order) VALUES ($1, $2, 0, 1, 0)`, id, product)
	e.exec(`INSERT INTO product_choice_group_translations (product_choice_group_id, locale, name) VALUES ($1, 'fr', $2)`, id, fr)
	for i, c := range choices {
		cid := uuid.New()
		e.exec(`INSERT INTO product_choices (id, product_id, choice_group_id, price_modifier, sort_order) VALUES ($1, $2, $3, 0, $4)`, cid, product, id, i)
		e.exec(`INSERT INTO product_choice_translations (product_choice_id, locale, name) VALUES ($1, 'fr', $2)`, cid, c)
	}
	return id
}

// edit changes the menu the way the product service does and tells the publisher.
func (e *penv) edit(query string, args ...any) {
	e.t.Helper()
	e.exec(query, args...)
	_, err := e.state.MarkDirty(e.t.Context(), e.clock.Now())
	require.NoError(e.t, err)
}

func (e *penv) puts() int { return e.srv.CatalogPuts() }

func (e *penv) gets() int { return len(e.srv.RequestsTo("GET", "/v1/catalogs/")) }

func (e *penv) publisherState() *domain.PublishState {
	e.t.Helper()
	s, err := e.state.Get(e.t.Context())
	require.NoError(e.t, err)
	return s
}

// confirmFirst previews and confirms the first publish, as the dashboard does.
func (e *penv) confirmFirst() *application.PublishOutcome {
	e.t.Helper()
	prev, err := e.pub.Preview(e.t.Context(), false)
	require.NoError(e.t, err)
	out, err := e.pub.PublishNow(e.t.Context(), application.PublishRequest{PlanHash: prev.Plan.PlanHash})
	require.NoError(e.t, err)
	return out
}

// tick advances the clock and lets the publisher run once.
func (e *penv) tick(advance time.Duration) bool {
	e.t.Helper()
	e.clock.Advance(advance)
	ran, err := e.pub.Tick(e.t.Context())
	require.NoError(e.t, err)
	return ran
}

func TestNothingRunsBeforeTheFirstPublishIsConfirmed(t *testing.T) {
	e := newPubEnv(t)
	e.product("M1", "Maki", "8.90")
	_, err := e.state.MarkDirty(t.Context(), e.clock.Now())
	require.NoError(t, err)
	assert.False(t, e.tick(5*time.Minute), "a dirty publisher waits for the confirmation")
	assert.Zero(t, len(e.srv.Requests()), "no HTTP call at all")

	prev, err := e.pub.Preview(t.Context(), false)
	require.NoError(t, err)
	assert.True(t, prev.FirstPublish)
	assert.Equal(t, 2, prev.Plan.Count(application.ActionPublishAdd), "a category and a product")
	assert.Zero(t, e.puts(), "a preview writes nothing")
	assert.Equal(t, 1, e.gets())
}

func TestFirstPublishNeedsThePlanHashOfThePreview(t *testing.T) {
	e := newPubEnv(t)
	e.product("M1", "Maki", "8.90")
	prev, err := e.pub.Preview(t.Context(), false)
	require.NoError(t, err)

	_, err = e.pub.PublishNow(t.Context(), application.PublishRequest{})
	assert.ErrorIs(t, err, domain.ErrPublishConfirmRequired)
	_, err = e.pub.PublishNow(t.Context(), application.PublishRequest{PlanHash: "stale"})
	assert.ErrorIs(t, err, domain.ErrPreviewStale)
	assert.Zero(t, e.puts())
	assert.Nil(t, e.publisherState().FirstPublishConfirmedAt)
	assert.Nil(t, e.publisherState().LeaseHolder, "a refused publish gives the lease back")

	out, err := e.pub.PublishNow(t.Context(), application.PublishRequest{PlanHash: prev.Plan.PlanHash})
	require.NoError(t, err)
	assert.True(t, out.Published)
	assert.Equal(t, 1, e.puts())
	st := e.publisherState()
	assert.NotNil(t, st.FirstPublishConfirmedAt)
	assert.NotNil(t, st.LastPublishedAt)
	assert.NotNil(t, st.LastPublishedLocalHash)
	assert.NotNil(t, st.LastPublishedRemoteHash)
	assert.False(t, st.Dirty())
	assert.Nil(t, st.LeaseHolder)

	// The links carry the refs HubRise now has; the items are what the PUT contained.
	n, err := e.items.Count(t.Context(), domain.EntityProduct)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	var origin string
	require.NoError(t, e.db.GetContext(t.Context(), &origin, `SELECT origin FROM hubrise_catalog_links WHERE hubrise_key = 'M1'`))
	assert.Equal(t, "published", origin)
	var hid *string
	require.NoError(t, e.db.GetContext(t.Context(), &hid, `SELECT hubrise_id FROM hubrise_catalog_links WHERE hubrise_key = 'M1'`))
	require.NotNil(t, hid, "the HubRise id comes back from the PUT")
	assert.NotEmpty(t, *hid)
	require.Len(t, e.notes.all(), 1)

	calls := e.audits.all()
	require.Len(t, calls, 1)
	assert.Equal(t, application.AuditPublish, calls[0].action)
	assert.True(t, calls[0].success)
	assert.Equal(t, "manual", calls[0].details["trigger"])
	assert.NotContains(t, calls[0].details, "token")
}

func TestEditsArePublishedAfterTheDebounceInOneRead(t *testing.T) {
	e := newPubEnv(t)
	id := e.product("M1", "Maki", "8.90")
	e.confirmFirst()
	e.clock.Advance(time.Minute)
	e.srv.ClearRequests()
	e.notes.reset()

	e.edit(`UPDATE products SET price = 9.50 WHERE id = $1`, id)
	e.clock.Advance(5 * time.Second)
	e.edit(`UPDATE product_translations SET name = 'Maki saumon' WHERE product_id = $1`, id)
	assert.False(t, e.tick(5*time.Second), "10 s after the last change, not 5")
	assert.False(t, e.tick(4*time.Second))
	assert.Zero(t, len(e.srv.Requests()))

	assert.True(t, e.tick(2*time.Second), "the burst is published once")
	assert.Equal(t, 1, e.gets())
	assert.Equal(t, 2, e.puts()) // the first publish and this one
	assert.False(t, e.publisherState().Dirty())
	assert.Equal(t, []uuid.UUID{id}, e.notes.all())
	assert.False(t, e.tick(time.Minute), "nothing left to do")

	sku := asList(asList(unmarshalMap(t, e.srv.CatalogData())["products"])[0]["skus"])[0]
	assert.Equal(t, "9.50 EUR", sku["price"])
}

func unmarshalMap(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	return m
}

func TestAnEditThatChangesNothingPublishedCostsNoHttpCall(t *testing.T) {
	e := newPubEnv(t)
	id := e.product("M1", "Maki", "8.90")
	e.confirmFirst()
	e.srv.ClearRequests()

	e.edit(`UPDATE products SET is_available = false WHERE id = $1`, id)
	assert.True(t, e.tick(time.Minute), "the run happens")
	assert.Zero(t, len(e.srv.Requests()), "availability is not published: neither a read nor a write")
	assert.False(t, e.publisherState().Dirty(), "and the flag is cleared")
}

func TestPublishesAreAtLeastThirtySecondsApart(t *testing.T) {
	e := newPubEnv(t)
	id := e.product("M1", "Maki", "8.90")
	e.confirmFirst()
	e.clock.Advance(domain.PublishMinInterval + time.Second)
	e.edit(`UPDATE products SET price = 9 WHERE id = $1`, id)
	assert.True(t, e.tick(domain.PublishDebounce+time.Second))
	e.edit(`UPDATE products SET price = 10 WHERE id = $1`, id)
	assert.False(t, e.tick(domain.PublishDebounce+time.Second), "11 s after the previous run started")
	assert.True(t, e.tick(domain.PublishMinInterval))
	assert.Equal(t, 3, e.puts())
}

func TestTwoReplicasPublishOnce(t *testing.T) {
	e := newPubEnv(t)
	id := e.product("M1", "Maki", "8.90")
	e.confirmFirst()
	other := *e.pub
	other.Holder = "replica-b"
	e.edit(`UPDATE products SET price = 9 WHERE id = $1`, id)
	e.clock.Advance(time.Minute)
	var wg sync.WaitGroup
	var ran [2]bool
	for i, pub := range []*application.CatalogPublisher{e.pub, &other} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := pub.Tick(t.Context())
			assert.NoError(t, err)
			ran[i] = r
		}()
	}
	wg.Wait()
	assert.NotEqual(t, ran[0], ran[1], "exactly one replica claimed the run")
	assert.Equal(t, 2, e.puts())
}

func TestAnEditDuringARunKeepsTheDirtyFlag(t *testing.T) {
	e := newPubEnv(t)
	id := e.product("M1", "Maki", "8.90")
	e.confirmFirst()
	e.edit(`UPDATE products SET price = 9 WHERE id = $1`, id)
	e.clock.Advance(time.Minute)
	// The edit lands while the PUT is in flight: the mock delays it.
	e.srv.AddFault(hubrisetest.Fault{Method: "PUT", Times: 1, Delay: 300 * time.Millisecond})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := e.pub.Tick(t.Context())
		assert.NoError(t, err)
	}()
	require.Eventually(t, func() bool { return e.publisherState().LeaseHolder != nil }, 5*time.Second, 5*time.Millisecond)
	e.edit(`UPDATE products SET price = 10 WHERE id = $1`, id)
	<-done
	assert.True(t, e.publisherState().Dirty(), "the newer edit is not lost")
	e.srv.ClearFaults()
	assert.True(t, e.tick(time.Minute))
	sku := asList(asList(unmarshalMap(t, e.srv.CatalogData())["products"])[0]["skus"])[0]
	assert.Equal(t, "10.00 EUR", sku["price"])
}

func TestRemovalsAboveTheThresholdWaitForConfirmation(t *testing.T) {
	e := newPubEnv(t)
	var ids []uuid.UUID
	for _, c := range []string{"A", "B", "C", "D", "E", "F", "G", "H"} {
		ids = append(ids, e.product(c, "Maki "+c, "5"))
	}
	e.confirmFirst()

	// 4 of 8: more than 30 % but fewer than 5 products, so no confirmation.
	for _, id := range ids[:4] {
		e.edit(`DELETE FROM products WHERE id = $1`, id)
	}
	assert.True(t, e.tick(time.Minute))
	assert.Equal(t, 2, e.puts())
	assert.Len(t, remoteSkus(unmarshalMap(t, e.srv.CatalogData())), 4)

	// 4 more of the 4 left: all of them... keep one so the menu is not empty.
	e2 := newPubEnv(t)
	var ids2 []uuid.UUID
	for _, c := range []string{"A", "B", "C", "D", "E", "F", "G", "H"} {
		ids2 = append(ids2, e2.product(c, "Maki "+c, "5"))
	}
	e2.confirmFirst()
	for _, id := range ids2[:5] {
		e2.edit(`DELETE FROM products WHERE id = $1`, id)
	}
	assert.True(t, e2.tick(time.Minute))
	st := e2.publisherState()
	assert.Equal(t, domain.PublishRemovalsNeedConfirm, st.ErrorCode())
	assert.NotNil(t, st.BlockedAt)
	assert.Equal(t, 1, e2.puts(), "nothing was removed")
	assert.False(t, e2.tick(time.Hour), "a blocked publisher does not retry")

	prev, err := e2.pub.Preview(t.Context(), false)
	require.NoError(t, err)
	assert.True(t, prev.Plan.RemovalsNeedConfirm)
	_, err = e2.pub.PublishNow(t.Context(), application.PublishRequest{PlanHash: prev.Plan.PlanHash})
	assert.ErrorIs(t, err, domain.ErrPublishConfirmRequired)
	_, err = e2.pub.PublishNow(t.Context(), application.PublishRequest{ConfirmRemovals: true})
	assert.ErrorIs(t, err, domain.ErrPublishConfirmRequired, "the confirmation names the plan")
	out, err := e2.pub.PublishNow(t.Context(), application.PublishRequest{ConfirmRemovals: true, PlanHash: prev.Plan.PlanHash})
	require.NoError(t, err)
	assert.True(t, out.Published)
	assert.Len(t, remoteSkus(unmarshalMap(t, e2.srv.CatalogData())), 3)
	assert.Empty(t, e2.publisherState().LastError)
}

func TestAnEmptyMenuIsNeverPublished(t *testing.T) {
	e := newPubEnv(t)
	id := e.product("M1", "Maki", "8.90")
	e.confirmFirst()
	e.edit(`DELETE FROM products WHERE id = $1`, id)
	assert.True(t, e.tick(time.Minute))
	assert.Equal(t, domain.PublishInvalidMenu, e.publisherState().ErrorCode())
	assert.Equal(t, 1, e.puts(), "the EPOS menu is still there")
	_, err := e.pub.PublishNow(t.Context(), application.PublishRequest{})
	assert.ErrorIs(t, err, domain.ErrEmptyMenu)
}

func TestBlockersStopThePublish(t *testing.T) {
	e := newPubEnv(t)
	p := e.product("M1", "Maki", "8.90")
	e.group(p, "Sauce") // a group without a choice cannot be uploaded
	_, err := e.pub.PublishNow(t.Context(), application.PublishRequest{PlanHash: "x"})
	var blocked *domain.PublishBlockedError
	require.ErrorAs(t, err, &blocked)
	assert.ErrorIs(t, err, domain.ErrPublishBlocked)
	assert.Zero(t, e.puts())
}

func TestPreconditionsOfAPublish(t *testing.T) {
	e := newPubEnv(t)
	e.product("M1", "Maki", "8.90")
	ctx := t.Context()

	e.exec(`UPDATE hubrise_connection SET scope = $1`, hubtest.DefaultScope)
	_, err := e.pub.Preview(ctx, false)
	assert.ErrorIs(t, err, domain.ErrReconnectRequired)
	_, err = e.pub.PublishNow(ctx, application.PublishRequest{})
	assert.ErrorIs(t, err, domain.ErrReconnectRequired)
	e.exec(`UPDATE hubrise_connection SET scope = $1`, hubtest.WriteScope)

	e.exec(`UPDATE hubrise_connection SET publish_catalog = false`)
	_, err = e.pub.Preview(ctx, false)
	assert.ErrorIs(t, err, domain.ErrNotPublishMode)
	e.exec(`UPDATE hubrise_connection SET publish_catalog = true`)

	e.exec(`UPDATE hubrise_connection SET enabled = false`)
	_, err = e.pub.PublishNow(ctx, application.PublishRequest{})
	assert.ErrorIs(t, err, domain.ErrNotEnabled)
	e.exec(`UPDATE hubrise_connection SET enabled = true, relink_required_at = now()`)
	_, err = e.pub.PublishNow(ctx, application.PublishRequest{})
	assert.ErrorIs(t, err, domain.ErrRelinkRequired)
	e.exec(`UPDATE hubrise_connection SET relink_required_at = NULL, auth_error_at = now()`)
	_, err = e.pub.Preview(ctx, false)
	assert.ErrorIs(t, err, hubrise.KindUnauthorized)
	e.exec(`UPDATE hubrise_connection SET auth_error_at = NULL, catalog_id = NULL`)
	_, err = e.pub.Preview(ctx, false)
	assert.ErrorIs(t, err, domain.ErrNotConnected)
	e.exec(`UPDATE hubrise_connection SET access_token_enc = NULL`)
	_, err = e.pub.Preview(ctx, false)
	assert.ErrorIs(t, err, domain.ErrNotConnected)
	assert.Zero(t, len(e.srv.Requests()), "no precondition reaches HubRise")
}

func TestNoConnectionIsNotConnected(t *testing.T) {
	e := newPubEnv(t)
	e.exec(`DELETE FROM hubrise_connection`)
	_, err := e.pub.Preview(t.Context(), false)
	assert.ErrorIs(t, err, domain.ErrNotConnected)
	_, err = e.pub.PublishNow(t.Context(), application.PublishRequest{})
	assert.ErrorIs(t, err, domain.ErrNotConnected)
	require.NoError(t, e.pub.Scan(t.Context()))
}

func TestABusyLeaseQueuesTheInteractivePublish(t *testing.T) {
	e := newPubEnv(t)
	id := e.product("M1", "Maki", "8.90")
	e.confirmFirst()
	// Another replica is publishing.
	_, ok, err := e.state.ClaimNow(t.Context(), "replica-b:x", e.clock.Now())
	require.NoError(t, err)
	require.True(t, ok)
	e.edit(`UPDATE products SET price = 9 WHERE id = $1`, id)
	out, err := e.pub.PublishNow(t.Context(), application.PublishRequest{})
	require.NoError(t, err)
	assert.True(t, out.Queued)
	assert.False(t, out.Published)
	assert.True(t, e.publisherState().Dirty())

	// Before the first publish is confirmed there is nothing to queue.
	e2 := newPubEnv(t)
	e2.product("M1", "Maki", "8.90")
	_, ok, err = e2.state.ClaimNow(t.Context(), "replica-b:x", e2.clock.Now())
	require.NoError(t, err)
	require.True(t, ok)
	_, err = e2.pub.PublishNow(t.Context(), application.PublishRequest{PlanHash: "x"})
	assert.ErrorIs(t, err, domain.ErrPublishBusy)
}

func TestAnAccountCatalogIsRefused(t *testing.T) {
	e := newPubEnv(t)
	e.product("M1", "Maki", "8.90")
	e.srv.SetCatalogLevel(hubrisetest.LevelAccount)
	_, err := e.pub.Preview(t.Context(), false)
	assert.ErrorIs(t, err, domain.ErrAccountCatalog)
	_, err = e.pub.PublishNow(t.Context(), application.PublishRequest{PlanHash: "x"})
	assert.ErrorIs(t, err, domain.ErrAccountCatalog)
	assert.Equal(t, domain.PublishAccountCatalog, e.publisherState().ErrorCode())
	assert.Zero(t, e.puts())
}

// faulty publishes with one scripted HubRise failure and returns the state afterwards.
func (e *penv) faulty(f hubrisetest.Fault) (*domain.PublishState, error) {
	e.t.Helper()
	e.srv.AddFault(f)
	prev := e.firstPlan()
	_, err := e.pub.PublishNow(e.t.Context(), application.PublishRequest{PlanHash: prev})
	e.srv.ClearFaults()
	return e.publisherState(), err
}

func (e *penv) firstPlan() string {
	e.t.Helper()
	prev, err := e.pub.Preview(e.t.Context(), false)
	require.NoError(e.t, err)
	return prev.Plan.PlanHash
}

func TestErrorsOfTheCatalogWrite(t *testing.T) {
	t.Run("403 means the scope is missing and pauses nothing else", func(t *testing.T) {
		e := newPubEnv(t)
		e.product("M1", "Maki", "8.90")
		st, err := e.faulty(hubrisetest.Fault{Method: "PUT", Status: 403, Times: 1})
		assert.ErrorIs(t, err, domain.ErrReconnectRequired, "the admin is asked to reconnect")
		assert.Equal(t, domain.PublishWriteScopeMissing, st.ErrorCode())
		assert.NotNil(t, st.BlockedAt)
		assert.Nil(t, e.conn().AuthErrorAt, "no global pause")
	})
	t.Run("401 with a working token is an account catalog", func(t *testing.T) {
		e := newPubEnv(t)
		e.product("M1", "Maki", "8.90")
		st, err := e.faulty(hubrisetest.Fault{Method: "PUT", Status: 401, Times: 1})
		assert.ErrorIs(t, err, domain.ErrAccountCatalog)
		assert.Equal(t, domain.PublishAccountCatalog, st.ErrorCode())
		assert.Nil(t, e.conn().AuthErrorAt)
	})
	t.Run("401 with a refused token pauses the integration", func(t *testing.T) {
		e := newPubEnv(t)
		e.product("M1", "Maki", "8.90")
		e.srv.AddFault(hubrisetest.Fault{Method: "GET", Path: "/v1/location", Status: 401})
		st, err := e.faulty(hubrisetest.Fault{Method: "PUT", Status: 401, Times: 1})
		assert.ErrorIs(t, err, hubrise.KindUnauthorized)
		assert.Equal(t, domain.PublishAuth, st.ErrorCode())
		assert.NotNil(t, e.conn().AuthErrorAt)
		assert.Nil(t, st.BlockedAt, "waits for a reconnect, not for an edit")
	})
	t.Run("404 means the catalog is gone", func(t *testing.T) {
		e := newPubEnv(t)
		e.product("M1", "Maki", "8.90")
		st, err := e.faulty(hubrisetest.Fault{Method: "PUT", Status: 404, Times: 1})
		assert.ErrorIs(t, err, hubrise.KindNotFound)
		assert.Equal(t, domain.PublishCatalogNotFound, st.ErrorCode())
		assert.NotNil(t, st.BlockedAt)
	})
	t.Run("422 is a rejection that waits for the next edit", func(t *testing.T) {
		e := newPubEnv(t)
		id := e.product("M1", "Maki", "8.90")
		st, err := e.faulty(hubrisetest.Fault{Method: "PUT", Status: 422, Times: 1})
		assert.ErrorIs(t, err, hubrise.KindInvalid)
		assert.Equal(t, domain.PublishRejected, st.ErrorCode())
		assert.NotNil(t, st.BlockedAt)
		assert.NotEmpty(t, st.LastError)
		// Only an edit unblocks it.
		e.clock.Advance(time.Hour)
		e.edit(`UPDATE products SET price = 9 WHERE id = $1`, id)
		assert.Nil(t, e.publisherState().BlockedAt)
	})
	t.Run("429 reschedules without burning an attempt", func(t *testing.T) {
		e := newPubEnv(t)
		e.product("M1", "Maki", "8.90")
		st, err := e.faulty(hubrisetest.Fault{Method: "PUT", Status: 429, Times: 1, RetryAfter: 45 * time.Second})
		var he *hubrise.Error
		require.ErrorAs(t, err, &he)
		assert.Equal(t, hubrise.KindRateLimited, he.Kind)
		assert.Equal(t, domain.PublishUnavailable, st.ErrorCode())
		assert.Zero(t, st.Attempts)
		require.NotNil(t, st.NextAttemptAt)
		assert.WithinDuration(t, e.clock.Now().Add(45*time.Second), *st.NextAttemptAt, time.Second)
	})
	t.Run("5xx backs off, doubling", func(t *testing.T) {
		e := newPubEnv(t)
		e.product("M1", "Maki", "8.90")
		e.confirmFirst()
		e.exec(`UPDATE products SET price = 9`)
		wants := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 15 * time.Minute, 15 * time.Minute}
		e.srv.AddFault(hubrisetest.Fault{Method: "PUT", Status: 500})
		_, err := e.state.MarkDirty(t.Context(), e.clock.Now())
		require.NoError(t, err)
		e.clock.Advance(time.Minute)
		for i, want := range wants {
			ran, err := e.pub.Tick(t.Context())
			require.NoError(t, err)
			require.True(t, ran, "attempt %d", i+1)
			st := e.publisherState()
			assert.Equal(t, domain.PublishUnavailable, st.ErrorCode())
			assert.Equal(t, i+1, st.Attempts)
			require.NotNil(t, st.NextAttemptAt)
			assert.WithinDuration(t, e.clock.Now().Add(want), *st.NextAttemptAt, time.Second, "backoff %d", i+1)
			assert.False(t, e.tick(want-time.Second), "not due yet")
			e.clock.Advance(2 * time.Second)
		}
		e.srv.ClearFaults()
		assert.True(t, e.tick(0))
		st := e.publisherState()
		assert.Empty(t, st.LastError)
		assert.Zero(t, st.Attempts)
	})
}

func TestErrorsOfTheCatalogRead(t *testing.T) {
	cases := []struct {
		name   string
		status int
		code   domain.PublishErrorCode
		block  bool
		auth   bool
	}{
		{"401 pauses the integration", 401, domain.PublishAuth, false, true},
		{"403 pauses the integration", 403, domain.PublishAuth, false, true},
		{"404 means the catalog is gone", 404, domain.PublishCatalogNotFound, true, false},
		{"500 is an outage", 500, domain.PublishUnavailable, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newPubEnv(t)
			e.product("M1", "Maki", "8.90")
			e.srv.AddFault(hubrisetest.Fault{Method: "GET", Path: "/v1/catalogs/", Status: tc.status, Times: 1})
			_, err := e.pub.PublishNow(t.Context(), application.PublishRequest{PlanHash: "x"})
			require.Error(t, err)
			st := e.publisherState()
			assert.Equal(t, tc.code, st.ErrorCode())
			assert.Equal(t, tc.block, st.BlockedAt != nil)
			assert.Equal(t, tc.auth, e.conn().AuthErrorAt != nil)
			assert.Zero(t, e.puts())
		})
	}
	t.Run("429 on the read waits", func(t *testing.T) {
		e := newPubEnv(t)
		e.product("M1", "Maki", "8.90")
		e.srv.AddFault(hubrisetest.Fault{Method: "GET", Path: "/v1/catalogs/", Status: 429, Times: 1, RetryAfter: time.Minute})
		_, err := e.pub.Preview(t.Context(), false)
		assert.ErrorIs(t, err, hubrise.KindRateLimited)
	})
}

func (e *penv) conn() *domain.Connection {
	e.t.Helper()
	c, err := e.conns.Get(e.t.Context())
	require.NoError(e.t, err)
	return c
}

func TestAPublishLostToAnotherReplicaIsReported(t *testing.T) {
	e := newPubEnv(t)
	e.product("M1", "Maki", "8.90")
	prev := e.firstPlan()
	// The lease expires while the PUT is slow and another replica takes it over. Wait for the PUT to
	// be in flight: the publisher renews its lease up to that point, so moving the clock earlier
	// would only let it renew at the new time.
	putStarted := make(chan struct{})
	e.srv.AddFault(hubrisetest.Fault{Method: "PUT", Times: 1, Delay: 500 * time.Millisecond, OnStart: func() { close(putStarted) }})
	done := make(chan error, 1)
	go func() {
		_, err := e.pub.PublishNow(t.Context(), application.PublishRequest{PlanHash: prev})
		done <- err
	}()
	select {
	case <-putStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("the publish never reached its PUT")
	}
	require.NotNil(t, e.publisherState().LeaseHolder)
	e.clock.Advance(domain.PublishLeaseTTL + time.Second)
	_, ok, err := e.state.ClaimNow(t.Context(), "replica-b:x", e.clock.Now())
	require.NoError(t, err)
	require.True(t, ok)
	assert.ErrorIs(t, <-done, domain.ErrPublishBusy)
}

func TestScanMarksTheMenuDirtyWhenItDriftedFromWhatWasPublished(t *testing.T) {
	e := newPubEnv(t)
	id := e.product("M1", "Maki", "8.90")
	ctx := t.Context()
	require.NoError(t, e.pub.Scan(ctx), "before the first publish there is nothing to compare")
	assert.False(t, e.publisherState().Dirty())

	e.confirmFirst()
	require.NoError(t, e.pub.Scan(ctx))
	assert.False(t, e.publisherState().Dirty(), "in step")

	e.exec(`UPDATE products SET is_available = false WHERE id = $1`, id)
	require.NoError(t, e.pub.Scan(ctx))
	assert.False(t, e.publisherState().Dirty(), "availability is not part of the menu hash")

	e.exec(`UPDATE products SET price = 9 WHERE id = $1`, id) // a writer that bypassed the hook
	require.NoError(t, e.pub.Scan(ctx))
	assert.True(t, e.publisherState().Dirty())
	seq := e.publisherState().ChangeSeq
	require.NoError(t, e.pub.Scan(ctx))
	assert.Equal(t, seq, e.publisherState().ChangeSeq, "an already dirty publisher is left alone")

	e.exec(`UPDATE hubrise_connection SET publish_catalog = false`)
	e.exec(`UPDATE hubrise_catalog_publish SET dirty_since = NULL`)
	require.NoError(t, e.pub.Scan(ctx))
	assert.False(t, e.publisherState().Dirty(), "not in PUBLISH mode")
}

func TestARunWithoutWriteScopeBlocksTheBackgroundPublish(t *testing.T) {
	e := newPubEnv(t)
	id := e.product("M1", "Maki", "8.90")
	e.confirmFirst()
	e.exec(`UPDATE hubrise_connection SET scope = $1`, hubtest.DefaultScope)
	e.edit(`UPDATE products SET price = 9 WHERE id = $1`, id)
	assert.True(t, e.tick(time.Minute))
	assert.Equal(t, domain.PublishWriteScopeMissing, e.publisherState().ErrorCode())
	assert.Equal(t, 1, e.puts())
}

func TestRunStopsWithItsContext(t *testing.T) {
	e := newPubEnv(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { e.pub.Run(ctx); close(done) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestForeignItemsAreKeptUnlessRemoveForeign(t *testing.T) {
	e := newPubEnv(t)
	e.product("M1", "Maki", "8.90")
	require.NoError(t, e.srv.ReplaceCatalogRaw(json.RawMessage(`{"categories":[{"ref":"epos","name":"EPOS"}],"products":[{"ref":"EPOS-ONLY","category_ref":"epos","name":"Epos","skus":[{"ref":"EPOS-ONLY","price":"3.00 EUR"}]}]}`)))
	prev, err := e.pub.Preview(t.Context(), false)
	require.NoError(t, err)
	assert.Len(t, prev.Plan.Foreign, 2)
	out, err := e.pub.PublishNow(t.Context(), application.PublishRequest{PlanHash: prev.Plan.PlanHash})
	require.NoError(t, err)
	assert.True(t, out.Published)
	assert.Contains(t, remoteSkus(unmarshalMap(t, e.srv.CatalogData())), "EPOS-ONLY")

	// removeForeign changes the plan, so the old hash is stale.
	_, err = e.pub.PublishNow(t.Context(), application.PublishRequest{PlanHash: prev.Plan.PlanHash, RemoveForeign: true})
	assert.ErrorIs(t, err, domain.ErrPreviewStale)
	prev2, err := e.pub.Preview(t.Context(), true)
	require.NoError(t, err)
	_, err = e.pub.PublishNow(t.Context(), application.PublishRequest{PlanHash: prev2.Plan.PlanHash, RemoveForeign: true})
	require.NoError(t, err)
	assert.NotContains(t, remoteSkus(unmarshalMap(t, e.srv.CatalogData())), "EPOS-ONLY")
}

func TestMenuChangedMarksTheMenuDirtyOnlyWhenTheDashboardOwnsIt(t *testing.T) {
	e := newPubEnv(t)
	ctx := t.Context()
	before := e.publisherState().ChangeSeq

	require.NoError(t, e.pub.MenuChanged(ctx))
	st := e.publisherState()
	assert.Equal(t, before+1, st.ChangeSeq)
	assert.True(t, st.Dirty())

	for _, off := range []string{`publish_catalog = false`, `publish_catalog = false, sync_catalog = true`} {
		e.exec(`UPDATE hubrise_catalog_publish SET dirty_since = NULL, change_seq = 100`)
		e.exec(`UPDATE hubrise_connection SET ` + off)
		require.NoError(t, e.pub.MenuChanged(ctx))
		st = e.publisherState()
		assert.Equal(t, int64(100), st.ChangeSeq, "%s: an import or standalone shop does not publish", off)
		assert.False(t, st.Dirty())
	}

	e.exec(`DELETE FROM hubrise_connection`)
	require.NoError(t, e.pub.MenuChanged(ctx), "no connection at all is fine")

	p := *e.pub
	p.Connections = pubFaultyConns{ConnectionRepository: e.conns, get: true}
	assert.ErrorIs(t, p.MenuChanged(ctx), errPubBoom)
}

func TestMenuChangedFailureToMarkIsReturned(t *testing.T) {
	e := newPubEnv(t)
	p := *e.pub
	p.State = &pubFaultyState{PublishStateRepository: e.pub.State, fail: map[string]bool{"MarkDirty": true}}
	assert.ErrorIs(t, p.MenuChanged(t.Context()), errPubBoom)
}

// A failed write leaves the first publish unconfirmed: the admin previewed one plan, and the next
// run (after the reconnect) may plan something else.
func TestAFailedFirstWriteDoesNotKeepTheConfirmation(t *testing.T) {
	e := newPubEnv(t)
	e.product("M1", "Maki", "8.90")
	st, err := e.faulty(hubrisetest.Fault{Method: "PUT", Status: 403, Times: 1})
	require.ErrorIs(t, err, domain.ErrReconnectRequired)
	assert.Nil(t, st.FirstPublishConfirmedAt)
	assert.Zero(t, e.puts())

	_, err = e.pub.PublishNow(t.Context(), application.PublishRequest{})
	assert.ErrorIs(t, err, domain.ErrPublishConfirmRequired, "a new preview is needed")
}

// A background run on a connection that cannot publish parks with a reason instead of failing every tick.
func TestABackgroundRunWithoutTheWriteScopeIsParked(t *testing.T) {
	e := armed(t)
	e.exec(`UPDATE hubrise_connection SET scope = 'location[orders.write]'`)
	e.clock.Advance(time.Minute)
	ran, err := e.pub.Tick(t.Context())
	require.NoError(t, err)
	assert.True(t, ran)
	st := e.publisherState()
	assert.Equal(t, domain.PublishWriteScopeMissing, st.ErrorCode())
	assert.NotNil(t, st.BlockedAt)
	assert.Nil(t, st.LeaseHolder)
	ran, err = e.pub.Tick(t.Context())
	require.NoError(t, err)
	assert.False(t, ran, "and it stays parked")
}

// HubRise changes every id on a PUT: the ids of the SKUs a deal line uses, as a GET shows them, must not
// travel back.
func TestDealLinesAreSentBackByRefOnly(t *testing.T) {
	e := newPubEnv(t)
	e.product("M1", "Maki", "8.90")
	require.NoError(t, e.srv.ReplaceCatalogRaw(json.RawMessage(`{"categories":[{"ref":"old","name":"Old"}],
	  "products":[{"ref":"OLD1","category_ref":"old","name":"Old one","skus":[{"ref":"OLD1","price":"1.00 EUR"}]}],
	  "deals":[{"ref":"d","name":"Deal","lines":[{"label":"l","pricing_effect":"fixed_price","pricing_value":"1.00 EUR","skus":[{"ref":"OLD1"}]}]}]}`)))
	var before struct {
		Deals []struct {
			Lines []struct {
				Skus []map[string]any `json:"skus"`
			} `json:"lines"`
		} `json:"deals"`
	}
	require.NoError(t, json.Unmarshal(e.srv.CatalogData(), &before))
	require.NotEmpty(t, before.Deals[0].Lines[0].Skus[0]["id"], "a GET shows the id of a deal sku")

	out := e.confirmFirst()
	require.True(t, out.Published, "the PUT is accepted: no stale id travelled")
	assert.Equal(t, 1, e.puts())
	var after map[string]any
	require.NoError(t, json.Unmarshal(e.srv.CatalogData(), &after))
	assert.Len(t, asList(after["deals"]), 1, "the deal survives")
}
