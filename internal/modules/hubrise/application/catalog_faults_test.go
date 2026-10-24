package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/pkg/hubrise"
	"tsb-service/pkg/hubrise/hubrisetest"
)

// This file sweeps fault injection over the storage calls of the catalog code: every repository
// call is counted, and run N fails the Nth call. Whatever the scenario, a failed call must surface
// as an error (or a failed batch), never as a panic or a silent success, and the catSweep ends when a
// run completes without reaching its Nth call. That drives every error branch of the importer.

var errInjected = errors.New("injected storage failure")

type catFaults struct {
	failAt int
	calls  int
	hit    bool
}

func (f *catFaults) tick() error {
	f.calls++
	if f.calls == f.failAt {
		f.hit = true
		return errInjected
	}
	return nil
}

type catFaultyConns struct {
	domain.ConnectionRepository
	f *catFaults
}

func (c catFaultyConns) Get(ctx context.Context) (*domain.Connection, error) {
	if err := c.f.tick(); err != nil {
		return nil, err
	}
	return c.ConnectionRepository.Get(ctx)
}

func (c catFaultyConns) UpdateSettings(ctx context.Context, p domain.SettingsPatch) (*domain.Connection, error) {
	if err := c.f.tick(); err != nil {
		return nil, err
	}
	return c.ConnectionRepository.UpdateSettings(ctx, p)
}

func (c catFaultyConns) RecordAuthError(ctx context.Context, at time.Time, m string) error {
	if err := c.f.tick(); err != nil {
		return err
	}
	return c.ConnectionRepository.RecordAuthError(ctx, at, m)
}

func (c catFaultyConns) RecordError(ctx context.Context, m string) error {
	if err := c.f.tick(); err != nil {
		return err
	}
	return c.ConnectionRepository.RecordError(ctx, m)
}

func (c catFaultyConns) RecordCatalogSync(ctx context.Context, h string, at time.Time) error {
	if err := c.f.tick(); err != nil {
		return err
	}
	return c.ConnectionRepository.RecordCatalogSync(ctx, h, at)
}

func (c catFaultyConns) RecordCatalogPreview(ctx context.Context, h string, at time.Time) error {
	if err := c.f.tick(); err != nil {
		return err
	}
	return c.ConnectionRepository.RecordCatalogPreview(ctx, h, at)
}

func (c catFaultyConns) SetCatalogUpdateAvailable(ctx context.Context, at *time.Time) error {
	if err := c.f.tick(); err != nil {
		return err
	}
	return c.ConnectionRepository.SetCatalogUpdateAvailable(ctx, at)
}

func (c catFaultyConns) SetRelinkRequired(ctx context.Context, at *time.Time) error {
	if err := c.f.tick(); err != nil {
		return err
	}
	return c.ConnectionRepository.SetRelinkRequired(ctx, at)
}

type catFaultyLinks struct {
	domain.CatalogLinkRepository
	f *catFaults
}

func (l catFaultyLinks) Upsert(ctx context.Context, link *domain.CatalogLink) error {
	if err := l.f.tick(); err != nil {
		return err
	}
	return l.CatalogLinkRepository.Upsert(ctx, link)
}

func (l catFaultyLinks) FindByKey(ctx context.Context, t domain.EntityType, key string) (*domain.CatalogLink, error) {
	if err := l.f.tick(); err != nil {
		return nil, err
	}
	return l.CatalogLinkRepository.FindByKey(ctx, t, key)
}

func (l catFaultyLinks) FindByLocal(ctx context.Context, t domain.EntityType, id uuid.UUID) (*domain.CatalogLink, error) {
	if err := l.f.tick(); err != nil {
		return nil, err
	}
	return l.CatalogLinkRepository.FindByLocal(ctx, t, id)
}

func (l catFaultyLinks) ListByType(ctx context.Context, t domain.EntityType, retired bool) ([]domain.CatalogLink, error) {
	if err := l.f.tick(); err != nil {
		return nil, err
	}
	return l.CatalogLinkRepository.ListByType(ctx, t, retired)
}

func (l catFaultyLinks) Count(ctx context.Context, retired bool) (int, error) {
	if err := l.f.tick(); err != nil {
		return 0, err
	}
	return l.CatalogLinkRepository.Count(ctx, retired)
}

func (l catFaultyLinks) SetRetired(ctx context.Context, id int64, at *time.Time) error {
	if err := l.f.tick(); err != nil {
		return err
	}
	return l.CatalogLinkRepository.SetRetired(ctx, id, at)
}

func (l catFaultyLinks) SetCatalogDisabled(ctx context.Context, id int64, disabled bool) error {
	if err := l.f.tick(); err != nil {
		return err
	}
	return l.CatalogLinkRepository.SetCatalogDisabled(ctx, id, disabled)
}

func (l catFaultyLinks) SetImageMD5(ctx context.Context, id int64, md5 string) error {
	if err := l.f.tick(); err != nil {
		return err
	}
	return l.CatalogLinkRepository.SetImageMD5(ctx, id, md5)
}

func (l catFaultyLinks) Delete(ctx context.Context, id int64) error {
	if err := l.f.tick(); err != nil {
		return err
	}
	return l.CatalogLinkRepository.Delete(ctx, id)
}

func (l catFaultyLinks) DeleteAll(ctx context.Context) (int64, error) {
	if err := l.f.tick(); err != nil {
		return 0, err
	}
	return l.CatalogLinkRepository.DeleteAll(ctx)
}

type catFaultyLeases struct {
	domain.LeaseRepository
	f *catFaults
}

func (l catFaultyLeases) Acquire(ctx context.Context, name, holder string, now time.Time, ttl time.Duration) (bool, error) {
	if err := l.f.tick(); err != nil {
		return false, err
	}
	return l.LeaseRepository.Acquire(ctx, name, holder, now, ttl)
}

type catFaultyLocal struct {
	domain.LocalCatalogRepository
	f *catFaults
}

func (l catFaultyLocal) Snapshot(ctx context.Context) (*domain.LocalSnapshot, error) {
	if err := l.f.tick(); err != nil {
		return nil, err
	}
	return l.LocalCatalogRepository.Snapshot(ctx)
}

func (l catFaultyLocal) InBatch(ctx context.Context, fn func(context.Context, domain.CatalogTx) error) error {
	return l.LocalCatalogRepository.InBatch(ctx, func(ctx context.Context, tx domain.CatalogTx) error {
		return fn(ctx, catFaultyTx{CatalogTx: tx, f: l.f})
	})
}

type catFaultyTx struct {
	domain.CatalogTx
	f *catFaults
}

func (t catFaultyTx) Links() domain.CatalogLinkRepository {
	return catFaultyLinks{CatalogLinkRepository: t.CatalogTx.Links(), f: t.f}
}

func (t catFaultyTx) CreateCategory(ctx context.Context, c domain.NewCategory) (uuid.UUID, error) {
	if err := t.f.tick(); err != nil {
		return uuid.Nil, err
	}
	return t.CatalogTx.CreateCategory(ctx, c)
}

func (t catFaultyTx) CreateProduct(ctx context.Context, p domain.NewProduct) (uuid.UUID, error) {
	if err := t.f.tick(); err != nil {
		return uuid.Nil, err
	}
	return t.CatalogTx.CreateProduct(ctx, p)
}

func (t catFaultyTx) PatchProduct(ctx context.Context, id uuid.UUID, p domain.ProductPatch) error {
	if err := t.f.tick(); err != nil {
		return err
	}
	return t.CatalogTx.PatchProduct(ctx, id, p)
}

func (t catFaultyTx) CreateGroup(ctx context.Context, g domain.NewGroup) (uuid.UUID, error) {
	if err := t.f.tick(); err != nil {
		return uuid.Nil, err
	}
	return t.CatalogTx.CreateGroup(ctx, g)
}

func (t catFaultyTx) PatchGroup(ctx context.Context, id uuid.UUID, p domain.GroupPatch) error {
	if err := t.f.tick(); err != nil {
		return err
	}
	return t.CatalogTx.PatchGroup(ctx, id, p)
}

func (t catFaultyTx) CreateChoice(ctx context.Context, c domain.NewChoice) (uuid.UUID, error) {
	if err := t.f.tick(); err != nil {
		return uuid.Nil, err
	}
	return t.CatalogTx.CreateChoice(ctx, c)
}

func (t catFaultyTx) PatchChoice(ctx context.Context, id uuid.UUID, p domain.ChoicePatch) error {
	if err := t.f.tick(); err != nil {
		return err
	}
	return t.CatalogTx.PatchChoice(ctx, id, p)
}

func (t catFaultyTx) UpsertTexts(ctx context.Context, target domain.TextTarget, owner uuid.UUID, texts []domain.TextWrite) error {
	if err := t.f.tick(); err != nil {
		return err
	}
	return t.CatalogTx.UpsertTexts(ctx, target, owner, texts)
}

// inject points the service and the guard at faulty repositories and returns the fault counter.
func (e *cenv) inject(failAt int) *catFaults {
	f := &catFaults{failAt: failAt}
	e.svc.Connections = catFaultyConns{ConnectionRepository: e.conns, f: f}
	e.svc.Links = catFaultyLinks{CatalogLinkRepository: e.links, f: f}
	e.svc.Leases = catFaultyLeases{LeaseRepository: e.leases, f: f}
	e.svc.Local = catFaultyLocal{LocalCatalogRepository: e.local, f: f}
	e.guard.Connections = e.svc.Connections
	e.guard.Links = e.svc.Links
	return f
}

// restore puts the real repositories back.
func (e *cenv) restore() {
	e.svc.Connections, e.svc.Links, e.svc.Leases, e.svc.Local = e.conns, e.links, e.leases, e.local
	e.guard.Connections, e.guard.Links = e.conns, e.links
}

// reset empties the menu and the links, and the import bookkeeping of the connection.
func (e *cenv) reset() {
	e.exec(`TRUNCATE hubrise_catalog_links, product_choices, product_choice_groups, products, product_categories RESTART IDENTITY CASCADE`)
	e.exec(`UPDATE hubrise_connection SET catalog_hash = NULL, catalog_preview_hash = NULL, catalog_preview_at = NULL,
		catalog_update_available_at = NULL, relink_required_at = NULL, last_error = NULL, auth_error_at = NULL,
		location_id = $1, enabled = TRUE, sync_catalog = TRUE`, hubrisetest.LocationID)
	e.exec(`DELETE FROM hubrise_job_leases`)
	e.clock.Advance(2 * time.Minute) // the mock limits catalog reads to 10 a minute
}

// catSweep runs scenario with the Nth storage call failing, for N = 1, 2, ... until a run reaches no
// such call. prepare builds the state the scenario starts from (with the real repositories); the
// scenario returns the error of the operation under test. Every injected run must report a failure.
func catSweep(t *testing.T, e *cenv, prepare func(), scenario func() error) int {
	t.Helper()
	runs := 0
	for failAt := 1; failAt < 400; failAt++ {
		e.restore()
		e.reset()
		prepare()
		f := e.inject(failAt)
		err := scenario()
		e.restore()
		if !f.hit {
			require.NoError(t, err, "a run that reached no injected call succeeds")
			return runs
		}
		runs++
		require.Error(t, err, "failing storage call #%d must surface", failAt)
	}
	t.Fatal("the catSweep did not end")
	return runs
}

// syncErr runs a sync and reports a failed batch as an error, like the user of the service sees it.
func (e *cenv) syncErr(req application.SyncRequest) func() error {
	return func() error {
		res, err := e.svc.Sync(e.t.Context(), req)
		if err != nil {
			return err
		}
		if res.Result != nil && len(res.Result.Failed) > 0 {
			return res.Result.Failed[0].Err
		}
		for _, w := range res.Warnings { // an image that could not be stored is a warning, not an error
			if w.Code == domain.WarnImage {
				return errors.New(w.Message)
			}
		}
		return nil
	}
}

func TestEveryStorageFailureOfAFullImportSurfaces(t *testing.T) {
	e := newCatEnv(t)
	e.publish(sushiMenu())
	runs := catSweep(t, e, func() {}, e.syncErr(application.SyncRequest{Mode: domain.SyncFull}))
	assert.Greater(t, runs, 40)
}

func TestEveryStorageFailureOfAnUpdateImportSurfaces(t *testing.T) {
	e := newCatEnv(t)
	e.setting(domain.SettingsPatch{ImportImages: on()})
	e.srv.AddImage("img-catSweep", "image/png", []byte("png-catSweep"))
	prepare := func() {
		e.publish(sushiMenu())
		e.fullImport()
		changed := sushiMenu()
		changed.d.Products[0].Name = "Maki au saumon"
		changed.d.Products[0].Skus[0].Price = money("6.20")
		changed.d.Products[0].Skus[0].CustomFields = []byte(`{"code":"M9","vat_category":"beverage"}`)
		changed.d.Products[1].CategoryRef = "MAKI"
		changed.d.Products[0].ImageIDs = []string{"img-catSweep"}
		changed.d.OptionLists[0].MinSelections = 0
		changed.d.OptionLists[0].Options[1].Price = money("0.70")
		changed.d.OptionLists[1].Options[0].Restrictions = &hubrise.Restrictions{Enabled: ptr(false)}
		e.publish(changed)
	}
	runs := catSweep(t, e, prepare, e.syncErr(application.SyncRequest{Mode: domain.SyncFull}))
	assert.Greater(t, runs, 30)
}

func TestEveryStorageFailureOfARetireAndReviveSurfaces(t *testing.T) {
	e := newCatEnv(t)
	reduced := newMenu().category("MAKI", "Makis").
		list("SAUCE", "Sauce", 1, ptr(1), option("SOJA", "Soja", "0")).
		product("SAUMON", "MAKI", "Maki saumon", codeSku("SAUMON-6", "5.50", "M1", "SAUCE"))
	retire := func() {
		e.publish(sushiMenu())
		e.fullImport()
		e.publish(reduced)
	}
	assert.Greater(t, catSweep(t, e, retire, e.syncErr(application.SyncRequest{Mode: domain.SyncFull, ConfirmRetire: true})), 15)

	revive := func() {
		retire()
		e.fullImport()
		e.publish(sushiMenu())
	}
	assert.Greater(t, catSweep(t, e, revive, e.syncErr(application.SyncRequest{Mode: domain.SyncFull})), 15)
}

func TestEveryStorageFailureOfALinkImportSurfaces(t *testing.T) {
	e := newCatEnv(t)
	prepare := func() {
		cat := newLocalCategory(t, e, 3, "Makis")
		p := e.localProduct(cat, "M1", "Maki saumon", "5")
		g := e.localGroup(p, "Sauce")
		e.localChoice(g, p, "Soja")
		e.publish(sushiMenu())
	}
	assert.Greater(t, catSweep(t, e, prepare, e.syncErr(application.SyncRequest{Mode: domain.SyncLinkOnly})), 10)
	assert.Greater(t, catSweep(t, e, prepare, e.syncErr(application.SyncRequest{Mode: domain.SyncFull})), 20)
	assert.Greater(t, catSweep(t, e, prepare, e.syncErr(application.SyncRequest{Mode: domain.SyncDryRun})), 3)
}

func TestEveryStorageFailureOfAScheduledMassRetirementSurfaces(t *testing.T) {
	e := newCatEnv(t)
	prepare := func() {
		e.publish(bigMenu(8))
		e.fullImport()
		e.publish(bigMenu(1))
	}
	assert.Greater(t, catSweep(t, e, prepare, e.syncErr(application.SyncRequest{Mode: domain.SyncFull, Scheduled: true})), 3)
	// Clearing the pending review after the confirmed import.
	prepare2 := func() {
		prepare()
		_, err := e.svc.Sync(t.Context(), application.SyncRequest{Mode: domain.SyncFull, Scheduled: true})
		require.NoError(t, err)
	}
	assert.Greater(t, catSweep(t, e, prepare2, e.syncErr(application.SyncRequest{Mode: domain.SyncFull, ConfirmRetire: true})), 10)
}

func TestEveryStorageFailureOfTheSafetyChecksSurfaces(t *testing.T) {
	e := newCatEnv(t)
	ctx := t.Context()
	prepare := func() {
		e.publish(sushiMenu())
		e.fullImport()
		e.sync(domain.SyncDryRun)
	}
	assert.Greater(t, catSweep(t, e, prepare, func() error { return e.svc.CheckEnableSyncCatalog(ctx) }), 0)
	assert.Greater(t, catSweep(t, e, prepare, func() error { return e.svc.CheckEnable(ctx) }), 0)

	staleFlag := func() {
		prepare()
		e.exec(`DELETE FROM hubrise_catalog_links`)
		e.exec(`UPDATE hubrise_connection SET relink_required_at = now()`)
	}
	assert.Greater(t, catSweep(t, e, staleFlag, func() error { return e.svc.CheckEnable(ctx) }), 1)

	forget := func() {
		prepare()
		e.exec(`UPDATE hubrise_connection SET relink_required_at = now()`)
	}
	assert.Greater(t, catSweep(t, e, forget, func() error { _, err := e.svc.ForgetLinks(ctx); return err }), 1)

	var before *domain.Connection
	relink := func() {
		prepare()
		before = e.connection()
		e.exec(`UPDATE hubrise_connection SET location_id = 'elsewhere'`)
	}
	assert.Greater(t, catSweep(t, e, relink, func() error { _, err := e.svc.AfterConnect(ctx, before); return err }), 3)
}

func TestEveryStorageFailureOfTheDriftCheckSurfaces(t *testing.T) {
	e := newCatEnv(t)
	ctx := t.Context()
	manual := func() {
		e.publish(sushiMenu())
		e.fullImport()
		changed := sushiMenu()
		changed.d.Products[1].Skus[0].Price = money("3.30")
		e.publish(changed)
	}
	assert.Greater(t, catSweep(t, e, manual, func() error { _, err := e.svc.CheckDrift(ctx); return err }), 1)

	banner := func() {
		e.publish(sushiMenu())
		e.fullImport()
		e.exec(`UPDATE hubrise_connection SET catalog_update_available_at = now()`)
	}
	assert.Greater(t, catSweep(t, e, banner, func() error { _, err := e.svc.CheckDrift(ctx); return err }), 1)

	auto := func() {
		manual()
		e.setting(domain.SettingsPatch{CatalogAutoImport: on()})
	}
	assert.Greater(t, catSweep(t, e, auto, func() error {
		_, err := e.svc.CheckDrift(ctx)
		if err == nil { // a failed batch is recorded on the connection, not returned
			if conn, gerr := e.conns.Get(ctx); gerr == nil && conn.LastError != nil {
				return errors.New(*conn.LastError)
			}
		}
		return err
	}), 10)
}

func TestEveryStorageFailureOfTheManualLinksSurfaces(t *testing.T) {
	e := newCatEnv(t)
	ctx := t.Context()
	var a, b uuid.UUID
	prepare := func() {
		cat := newLocalCategory(t, e, 1, "Plats")
		a = e.localProduct(cat, "", "Riz", "3")
		b = e.localProduct(cat, "", "Nems", "3")
		require.NoError(t, e.svc.LinkProduct(ctx, a, strp("SKU-A")))
	}
	assert.Greater(t, catSweep(t, e, prepare, func() error { return e.svc.LinkProduct(ctx, a, strp("SKU-B")) }), 2, "re-point")
	assert.Greater(t, catSweep(t, e, prepare, func() error { return e.svc.LinkProduct(ctx, a, nil) }), 1, "unlink")
	assert.Greater(t, catSweep(t, e, prepare, func() error { return e.svc.LinkProduct(ctx, b, strp("SKU-C")) }), 1, "link")
}

func strp(s string) *string { return &s }

func TestEveryStorageFailureOfTheGuardSurfaces(t *testing.T) {
	e := newCatEnv(t)
	ctx := t.Context()
	var g *guarded
	prepare := func() {
		e.publish(sushiMenu())
		e.fullImport()
		g = &guarded{e: e, product: e.product("SAUMON-6")}
		gl, _ := e.links.FindByKey(ctx, domain.EntityChoiceGroup, "SAUMON-6|SAUCE")
		cl, _ := e.links.FindByKey(ctx, domain.EntityChoice, "SAUMON-6|SAUCE|SOJA")
		g.group, g.choice = gl.LocalID(), cl.LocalID()
	}
	// The guard reports storage failures as errors that are not ManagedError (the edit must fail closed).
	check := func(fn func() error) func() error {
		return func() error {
			err := fn()
			var m *domain.ManagedError
			if errors.As(err, &m) {
				return nil // a lock, the storage was readable
			}
			return err
		}
	}
	edit := func() domain.ProductFields { f := g.fields(); f.Price = dec("1"); return f }
	assert.Greater(t, catSweep(t, e, prepare, check(func() error { return e.guard.CheckProduct(ctx, g.fields(), edit()) })), 1)
	assert.Greater(t, catSweep(t, e, prepare, check(func() error {
		return e.guard.CheckChoiceGroup(ctx, &domain.GroupFields{ID: g.group}, &domain.GroupFields{ID: g.group, Min: 3})
	})), 1)
	assert.Greater(t, catSweep(t, e, prepare, check(func() error {
		return e.guard.CheckChoiceGroup(ctx, nil, &domain.GroupFields{ProductID: g.product.ID})
	})), 1)
	assert.Greater(t, catSweep(t, e, prepare, check(func() error { return e.guard.CheckChoiceGroup(ctx, &domain.GroupFields{ID: g.group}, nil) })), 1)
	assert.Greater(t, catSweep(t, e, prepare, check(func() error {
		return e.guard.CheckChoice(ctx, &domain.ChoiceFields{ID: g.choice}, &domain.ChoiceFields{ID: g.choice, Sort: 3})
	})), 1)
	assert.Greater(t, catSweep(t, e, prepare, check(func() error { return e.guard.CheckChoice(ctx, nil, &domain.ChoiceFields{GroupID: g.group}) })), 1)
	assert.Greater(t, catSweep(t, e, prepare, check(func() error { return e.guard.CheckChoice(ctx, &domain.ChoiceFields{ID: g.choice}, nil) })), 1)
}
