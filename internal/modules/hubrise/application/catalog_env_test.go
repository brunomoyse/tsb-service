package application_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
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

// notifier records the products an import announced.
type notifier struct {
	mu    sync.Mutex
	calls [][]uuid.UUID
}

func (n *notifier) ProductsChanged(_ context.Context, ids []uuid.UUID) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls = append(n.calls, ids)
}

func (n *notifier) all() []uuid.UUID {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []uuid.UUID
	for _, c := range n.calls {
		out = append(out, c...)
	}
	return out
}

func (n *notifier) reset() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls = nil
}

// imageStore is the image uploader of the tests.
type imageStore struct {
	mu       sync.Mutex
	uploaded map[uuid.UUID][]byte
	names    map[uuid.UUID]string
	err      error
}

func (s *imageStore) UploadProductImage(_ context.Context, id uuid.UUID, filename string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	if s.uploaded == nil {
		s.uploaded, s.names = map[uuid.UUID][]byte{}, map[uuid.UUID]string{}
	}
	s.uploaded[id], s.names[id] = data, filename
	return nil
}

// env is a migrated database with a connected, enabled, catalog-syncing installation, a HubRise mock
// and the catalog service wired to both.
type env struct {
	t      *testing.T
	db     *sqlx.DB
	srv    *hubrisetest.Server
	clock  *hubtest.FakeClock
	svc    *application.CatalogService
	guard  *application.Guard
	conns  domain.ConnectionRepository
	links  domain.CatalogLinkRepository
	leases domain.LeaseRepository
	local  domain.LocalCatalogRepository
	notes  *notifier
	images *imageStore
}

type envOption func(*envConfig)

type envConfig struct {
	features [5]bool // push, status, inventory, catalog, customers
}

func newEnv(t *testing.T, opts ...envOption) *env {
	t.Helper()
	cfg := envConfig{features: [5]bool{true, true, true, true, true}}
	for _, o := range opts {
		o(&cfg)
	}
	td := testhelpers.SetupTestDatabase(t)
	clock := hubtest.NewFakeClock(time.Date(2026, 10, 26, 12, 0, 0, 0, timezone.Location))
	srv := hubrisetest.New(t, hubrisetest.WithClock(clock.Now))
	hubtest.SeedConnection(t, td.DB, hubtest.WithToken(hubrisetest.Token), hubtest.WithLocation(hubrisetest.LocationID, hubrisetest.CatalogID),
		hubtest.WithFeatures(cfg.features[0], cfg.features[1], cfg.features[2], cfg.features[3], cfg.features[4]))

	e := &env{t: t, db: td.DB, srv: srv, clock: clock, notes: &notifier{}, images: &imageStore{}}
	e.conns = infrastructure.NewConnectionRepository(td.DB)
	e.links = infrastructure.NewCatalogLinkRepository(td.DB)
	e.leases = infrastructure.NewLeaseRepository(td.DB)
	e.local = infrastructure.NewLocalCatalogRepository(td.DB)
	factory := &application.ClientFactory{BaseURL: srv.APIBaseURL(), Codec: hubtest.TestCodec(t), Limiter: hubrise.NewLimiter(hubrise.Limits{}), HTTP: srv.HTTPClient()}
	e.svc = &application.CatalogService{
		Connections: e.conns, Links: e.links, Leases: e.leases, Local: e.local, Clock: clock,
		NewClient: factory.CatalogAPI, Notifier: e.notes, Images: e.images, Holder: "test-instance",
	}
	e.guard = &application.Guard{Connections: e.conns, Links: e.links}
	return e
}

// ---- the HubRise side ----

// menu builds catalog data in the upload form the mock accepts.
type menu struct{ d hubrise.CatalogData }

func newMenu() *menu { return &menu{} }

func (m *menu) category(ref, name string) *menu {
	m.d.Categories = append(m.d.Categories, hubrise.Category{Ref: ref, Name: name})
	return m
}

// product adds a food product (6 % to take away, 12 % to eat in) with the given SKUs.
func (m *menu) product(ref, category, name string, skus ...hubrise.Sku) *menu {
	p := product(ref, category, name, skus...)
	p.TaxRate = &hubrise.TaxRate{Delivery: "6", Collection: "6", EatIn: "12"}
	m.d.Products = append(m.d.Products, p)
	return m
}

func (m *menu) list(ref, name string, min int, max *int, options ...hubrise.Option) *menu {
	m.d.OptionLists = append(m.d.OptionLists, hubrise.OptionList{Ref: ref, Name: name, MinSelections: min, MaxSelections: max, Options: options})
	return m
}

func option(ref, name, price string) hubrise.Option {
	return hubrise.Option{Ref: ref, Name: name, Price: money(price)}
}

// codeSku is a SKU with a menu code.
func codeSku(ref, price, code string, lists ...string) hubrise.Sku {
	s := sku(ref, price, lists...)
	s.CustomFields = []byte(`{"code":"` + code + `"}`)
	return s
}

func (e *env) publish(m *menu) {
	e.t.Helper()
	require.NoError(e.t, e.srv.ReplaceCatalog(m.d))
}

// ---- the shop side ----

func (e *env) sync(mode domain.SyncMode) *application.SyncResult {
	e.t.Helper()
	res, err := e.svc.Sync(e.t.Context(), application.SyncRequest{Mode: mode})
	require.NoError(e.t, err)
	return res
}

func (e *env) fullImport() *application.SyncResult {
	e.t.Helper()
	res := e.sync(domain.SyncFull)
	require.True(e.t, res.Applied)
	require.Empty(e.t, res.Result.Failed)
	return res
}

type productRow struct {
	ID          uuid.UUID `db:"id"`
	CategoryID  uuid.UUID `db:"category_id"`
	Price       string    `db:"price"`
	Code        *string   `db:"code"`
	Slug        string    `db:"slug"`
	VatCategory string    `db:"vat_category"`
	Visible     bool      `db:"is_visible"`
	Available   bool      `db:"is_available"`
	Discount    bool      `db:"is_discountable"`
	Halal       bool      `db:"is_halal"`
}

// product finds the product linked to a SKU ref.
func (e *env) product(sku string) productRow {
	e.t.Helper()
	link, err := e.links.FindByKey(e.t.Context(), domain.EntityProduct, sku)
	require.NoError(e.t, err, "SKU %s is linked", sku)
	return e.productByID(link.LocalID())
}

func (e *env) productByID(id uuid.UUID) productRow {
	e.t.Helper()
	var p productRow
	require.NoError(e.t, e.db.GetContext(e.t.Context(), &p,
		`SELECT id, category_id, price::text AS price, code, slug, vat_category, is_visible, is_available, is_discountable, is_halal FROM products WHERE id = $1`, id))
	return p
}

type textRow struct {
	Language    string  `db:"language"`
	Name        string  `db:"name"`
	Description *string `db:"description"`
	Source      string  `db:"source"`
}

func (e *env) productTexts(id uuid.UUID) map[string]textRow {
	e.t.Helper()
	var rows []textRow
	require.NoError(e.t, e.db.SelectContext(e.t.Context(), &rows, `SELECT language, name, description, source FROM product_translations WHERE product_id = $1`, id))
	out := map[string]textRow{}
	for _, r := range rows {
		out[r.Language] = r
	}
	return out
}

func (e *env) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	require.NoError(e.t, e.db.GetContext(e.t.Context(), &n, query, args...))
	return n
}

func (e *env) exec(query string, args ...any) {
	e.t.Helper()
	_, err := e.db.ExecContext(e.t.Context(), query, args...)
	require.NoError(e.t, err)
}

func (e *env) connection() *domain.Connection {
	e.t.Helper()
	c, err := e.conns.Get(e.t.Context())
	require.NoError(e.t, err)
	return c
}

func (e *env) setting(patch domain.SettingsPatch) {
	e.t.Helper()
	_, err := e.conns.UpdateSettings(e.t.Context(), patch)
	require.NoError(e.t, err)
}

// failingLocal fails one batch (the one that contains the key) and passes the rest through.
type failingLocal struct {
	domain.LocalCatalogRepository
	failOn string
}

func (f failingLocal) InBatch(ctx context.Context, fn func(context.Context, domain.CatalogTx) error) error {
	return f.LocalCatalogRepository.InBatch(ctx, func(ctx context.Context, tx domain.CatalogTx) error {
		err := fn(ctx, tx)
		if err != nil {
			return err
		}
		if f.failOn != "" {
			if _, lerr := tx.Links().FindByKey(ctx, domain.EntityProduct, f.failOn); lerr == nil {
				return errors.New("injected batch failure")
			}
		}
		return nil
	})
}
