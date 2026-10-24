package application_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/modules/hubrise/domain"
	hubtest "tsb-service/internal/modules/hubrise/testhelpers"
)

func newPublishRig(t *testing.T) *adminRig {
	t.Helper()
	return newAdminRig(t, withSeed(hubtest.WithPublishCatalog()))
}

func (r *adminRig) publisherState() *domain.PublishState {
	r.t.Helper()
	s, err := r.mod.PublishState.Get(r.t.Context())
	require.NoError(r.t, err)
	return s
}

// confirmFirstPublish plays the dashboard: preview, then publish with the plan hash.
func (r *adminRig) confirmFirstPublish() *application.PublishOutcome {
	r.t.Helper()
	prev, err := r.admin.PreviewPublish(r.t.Context(), false)
	require.NoError(r.t, err)
	out, err := r.admin.PublishCatalog(r.t.Context(), application.PublishRequest{PlanHash: prev.Plan.PlanHash})
	require.NoError(r.t, err)
	return out
}

func TestSwitchingToPublishTurnsTheImportOffAndAwaitsTheFirstPublish(t *testing.T) {
	r := newAdminRig(t, withSeed(hubtest.WithFeatures(true, true, true, true, true), hubtest.WithScope(hubtest.WriteScope)))
	r.exec(`UPDATE hubrise_connection SET catalog_auto_import = true`)
	require.Equal(t, domain.DirectionImport, r.conn().CatalogDirection())

	conn, err := r.admin.SetCatalogDirection(t.Context(), domain.DirectionPublish, false)
	require.NoError(t, err)
	assert.Equal(t, domain.DirectionPublish, conn.CatalogDirection())
	assert.False(t, conn.SyncCatalog)
	assert.False(t, conn.CatalogAutoImport)
	st := r.publisherState()
	assert.Nil(t, st.FirstPublishConfirmedAt)
	assert.True(t, st.Dirty(), "awaiting the first publish")

	status, err := r.admin.Status(t.Context())
	require.NoError(t, err)
	assert.Equal(t, domain.DirectionPublish, status.Direction)
	assert.True(t, status.Publish.Enabled)
	assert.True(t, status.Publish.AwaitingFirstPublish)
	assert.False(t, status.Publish.Active, "nothing runs before the confirmation")
	assert.False(t, status.Publish.NeedsReconnectForWrite)
	assert.Equal(t, 1, len(r.audit.all()), "the switch is audited")
	assert.Equal(t, application.AuditDirection, r.audit.all()[0].Action)

	// Asking again changes nothing and is not audited twice.
	_, err = r.admin.SetCatalogDirection(t.Context(), domain.DirectionPublish, false)
	require.NoError(t, err)
	assert.Equal(t, 1, len(r.audit.all()))
}

func TestSwitchingToPublishNeedsAConnectionWithWriteScope(t *testing.T) {
	t.Run("an old authorization has no catalog.write", func(t *testing.T) {
		r := newAdminRig(t)
		_, err := r.admin.SetCatalogDirection(t.Context(), domain.DirectionPublish, false)
		assert.ErrorIs(t, err, domain.ErrReconnectRequired)
		assert.Equal(t, domain.DirectionNone, r.conn().CatalogDirection())
		status, err := r.admin.Status(t.Context())
		require.NoError(t, err)
		assert.True(t, status.Publish.NeedsReconnectForWrite)
	})
	t.Run("a pending relink", func(t *testing.T) {
		r := newAdminRig(t, withSeed(hubtest.WithScope(hubtest.WriteScope)))
		r.exec(`UPDATE hubrise_connection SET relink_required_at = now()`)
		_, err := r.admin.SetCatalogDirection(t.Context(), domain.DirectionPublish, false)
		assert.ErrorIs(t, err, domain.ErrRelinkRequired)
	})
	t.Run("not connected", func(t *testing.T) {
		r := newAdminRig(t, withoutConnection())
		_, err := r.admin.SetCatalogDirection(t.Context(), domain.DirectionPublish, false)
		assert.ErrorIs(t, err, domain.ErrNotConnected)
	})
	t.Run("an unknown direction", func(t *testing.T) {
		r := newAdminRig(t)
		_, err := r.admin.SetCatalogDirection(t.Context(), "SIDEWAYS", false)
		var bad *domain.InvalidSettingError
		assert.ErrorAs(t, err, &bad)
	})
}

func TestLeavingPublishWithUnpublishedEditsNeedsAcknowledgement(t *testing.T) {
	r := newPublishRig(t)
	_, err := r.admin.SetCatalogDirection(t.Context(), domain.DirectionPublish, false)
	require.NoError(t, err)
	r.confirmFirstPublish()
	assert.False(t, r.publisherState().Dirty())

	// Clean: leaving needs nothing.
	_, err = r.admin.SetCatalogDirection(t.Context(), domain.DirectionNone, false)
	require.NoError(t, err)
	_, err = r.admin.SetCatalogDirection(t.Context(), domain.DirectionPublish, false)
	require.NoError(t, err)
	r.confirmFirstPublish()

	_, err = r.mod.PublishState.MarkDirty(t.Context(), r.clock.Now())
	require.NoError(t, err)
	_, err = r.admin.SetCatalogDirection(t.Context(), domain.DirectionNone, false)
	assert.ErrorIs(t, err, domain.ErrPublishPendingChanges)
	assert.Equal(t, domain.DirectionPublish, r.conn().CatalogDirection(), "nothing changed")

	conn, err := r.admin.SetCatalogDirection(t.Context(), domain.DirectionNone, true)
	require.NoError(t, err)
	assert.Equal(t, domain.DirectionNone, conn.CatalogDirection())
	assert.False(t, r.publisherState().Dirty(), "the warnings and the dirty flag are gone")
}

func TestSwitchingToImportStillNeedsTheDryRun(t *testing.T) {
	r := newPublishRig(t)
	_, err := r.admin.SetCatalogDirection(t.Context(), domain.DirectionPublish, false)
	require.NoError(t, err)
	_, err = r.admin.SetCatalogDirection(t.Context(), domain.DirectionImport, true)
	assert.ErrorIs(t, err, domain.ErrPreviewRequired)
	assert.Equal(t, domain.DirectionPublish, r.conn().CatalogDirection())

	_, err = r.admin.PreviewCatalog(t.Context(), domain.SyncDryRun)
	require.NoError(t, err, "a dry run keeps working in PUBLISH mode")
	conn, err := r.admin.SetCatalogDirection(t.Context(), domain.DirectionImport, true)
	require.NoError(t, err)
	assert.True(t, conn.SyncCatalog)
	assert.False(t, conn.PublishCatalog)
}

func TestTheTwoDirectionsExcludeEachOtherInTheSettings(t *testing.T) {
	r := newPublishRig(t)
	_, err := r.admin.SetCatalogDirection(t.Context(), domain.DirectionPublish, false)
	require.NoError(t, err)
	yes := true
	_, err = r.admin.UpdateSettings(t.Context(), domain.SettingsPatch{SyncCatalog: &yes})
	assert.ErrorIs(t, err, domain.ErrDirectionConflict)
	assert.False(t, r.conn().SyncCatalog)

	_, err = r.admin.UpdateSettings(t.Context(), domain.SettingsPatch{PublishImages: &yes})
	require.NoError(t, err)
	assert.True(t, r.conn().PublishImages)

	old := newAdminRig(t)
	_, err = old.admin.UpdateSettings(t.Context(), domain.SettingsPatch{PublishImages: &yes})
	assert.ErrorIs(t, err, domain.ErrReconnectRequired, "images are written with catalog.write")
}

func TestAFullImportIsRefusedWhileTheDashboardOwnsTheMenu(t *testing.T) {
	r := newPublishRig(t)
	_, err := r.admin.SetCatalogDirection(t.Context(), domain.DirectionPublish, false)
	require.NoError(t, err)
	r.srv.ClearRequests()

	_, err = r.admin.SyncCatalog(t.Context(), domain.SyncFull, false)
	assert.ErrorIs(t, err, domain.ErrPublishMode)
	_, err = r.admin.PreviewCatalog(t.Context(), domain.SyncFull)
	assert.ErrorIs(t, err, domain.ErrPublishMode)
	assert.Empty(t, r.srv.Requests(), "refused before any HubRise call")

	_, err = r.admin.PreviewCatalog(t.Context(), domain.SyncLinkOnly)
	assert.NoError(t, err)
	res, err := r.admin.SyncCatalog(t.Context(), domain.SyncLinkOnly, false)
	require.NoError(t, err, "linking existing SKUs gives them refs before the first publish")
	assert.True(t, res.Applied)
	_, err = r.admin.SyncCatalog(t.Context(), domain.SyncDryRun, false)
	assert.NoError(t, err)
}

func TestDriftOfAPublishedCatalogOnlyFlagsAWarning(t *testing.T) {
	r := newPublishRig(t)
	_, err := r.admin.SetCatalogDirection(t.Context(), domain.DirectionPublish, false)
	require.NoError(t, err)
	r.confirmFirstPublish()
	productsBefore := r.count(`SELECT count(*) FROM products`)

	out, err := r.catalog.Service.CheckDrift(t.Context())
	require.NoError(t, err)
	assert.Equal(t, &application.DriftOutcome{}, out)
	assert.Nil(t, r.publisherState().ExternalChangeAt, "nobody touched the catalog")

	// Somebody edits the catalog in the HubRise back office.
	cat := r.srv.Catalog()
	cat.Data.Products[0].Name = "Edited in the back office"
	require.NoError(t, r.srv.ReplaceCatalog(*cat.Data))
	_, err = r.catalog.Service.CheckDrift(t.Context())
	require.NoError(t, err)
	assert.NotNil(t, r.publisherState().ExternalChangeAt)
	assert.Equal(t, productsBefore, r.count(`SELECT count(*) FROM products`), "never imported")
	assert.Nil(t, r.conn().CatalogUpdateAvailableAt, "no 'update available' banner in PUBLISH mode")

	require.NoError(t, r.admin.AcknowledgeExternalChange(t.Context()))
	assert.Nil(t, r.publisherState().ExternalChangeAt)
}

// driftJob builds the event-driven drift job of the rig.
func (r *adminRig) driftJob() *application.CatalogDrift {
	return application.NewCatalogDrift(application.CatalogDriftDeps{
		Connections: r.mod.Connections, Events: r.mod.Events, Leases: r.mod.Leases, Checker: r.catalog.Drift,
		Publish: r.catalog.Publisher, Clock: r.clock,
	})
}

func (r *adminRig) catalogEvent(id, eventType string, receivedAgo time.Duration) {
	r.t.Helper()
	r.exec(`INSERT INTO hubrise_events (id, resource_type, event_type, payload, received_at) VALUES ($1, 'catalog', $2, '{}', $3)`,
		id, eventType, r.clock.Now().Add(-receivedAgo))
}

func TestCatalogEventsOfAPublishedCatalogCostNoHubriseCall(t *testing.T) {
	r := newPublishRig(t)
	_, err := r.admin.SetCatalogDirection(t.Context(), domain.DirectionPublish, false)
	require.NoError(t, err)
	r.confirmFirstPublish()
	job := r.driftJob()
	r.srv.ClearRequests()

	// The echo of our own publish is ignored.
	r.catalogEvent("echo", "update", time.Minute)
	ran, err := job.CheckEvents(t.Context())
	require.NoError(t, err)
	assert.True(t, ran)
	assert.Nil(t, r.publisherState().ExternalChangeAt, "an event right after our own publish is its echo")

	// An edit later is an external change.
	r.clock.Advance(10 * time.Minute)
	r.catalogEvent("edit", "update", time.Minute)
	ran, err = job.CheckEvents(t.Context())
	require.NoError(t, err)
	assert.True(t, ran)
	assert.NotNil(t, r.publisherState().ExternalChangeAt)
	assert.Empty(t, r.srv.Requests(), "no GET of the catalog: the 10 a minute bucket is not spent")
	assert.Equal(t, 0, r.count(`SELECT count(*) FROM hubrise_events WHERE state = 'pending'`))

	// Too fresh: the debounce waits.
	r.catalogEvent("fresh", "update", time.Second)
	ran, err = job.CheckEvents(t.Context())
	require.NoError(t, err)
	assert.False(t, ran)
	r.exec(`DELETE FROM hubrise_events`)

	// A deletion stops publishing.
	r.catalogEvent("gone", "delete", time.Minute)
	_, err = job.CheckEvents(t.Context())
	require.NoError(t, err)
	st := r.publisherState()
	assert.Equal(t, domain.PublishCatalogNotFound, st.ErrorCode())
	assert.NotNil(t, st.BlockedAt)
}

func TestCatalogEventsWithoutAPublisherAreJustConsumed(t *testing.T) {
	r := newPublishRig(t)
	_, err := r.admin.SetCatalogDirection(t.Context(), domain.DirectionPublish, false)
	require.NoError(t, err)
	job := application.NewCatalogDrift(application.CatalogDriftDeps{
		Connections: r.mod.Connections, Events: r.mod.Events, Leases: r.mod.Leases, Checker: r.catalog.Drift, Clock: r.clock,
	})
	r.catalogEvent("edit", "update", time.Minute)
	ran, err := job.CheckEvents(t.Context())
	require.NoError(t, err)
	assert.True(t, ran)
	assert.Nil(t, r.publisherState().ExternalChangeAt)
}

func TestDriftOfAPublishedCatalogWithoutAPublisherDoesNothing(t *testing.T) {
	r := newPublishRig(t)
	_, err := r.admin.SetCatalogDirection(t.Context(), domain.DirectionPublish, false)
	require.NoError(t, err)
	r.catalog.Service.Publish = nil
	r.srv.ClearRequests()
	out, err := r.catalog.Service.CheckDrift(t.Context())
	require.NoError(t, err)
	assert.Equal(t, &application.DriftOutcome{}, out)
	assert.Empty(t, r.srv.Requests())
}

func TestForgettingLinksForgetsWhatWasPublished(t *testing.T) {
	r := newPublishRig(t)
	_, err := r.admin.SetCatalogDirection(t.Context(), domain.DirectionPublish, false)
	require.NoError(t, err)
	r.confirmFirstPublish()
	require.NotZero(t, r.count(`SELECT count(*) FROM hubrise_published_items`))

	n, err := r.admin.ForgetLinks(t.Context())
	require.NoError(t, err)
	assert.Positive(t, n)
	assert.Zero(t, r.count(`SELECT count(*) FROM hubrise_published_items`))
	st := r.publisherState()
	assert.Nil(t, st.FirstPublishConfirmedAt, "the next publish is a first one again")
	assert.True(t, st.Dirty())

	// Outside PUBLISH only the items go.
	_, err = r.admin.SetCatalogDirection(t.Context(), domain.DirectionNone, true)
	require.NoError(t, err)
	r.exec(`INSERT INTO hubrise_published_items (entity_type, hubrise_key, hubrise_ref, content_hash) VALUES ('product', 'X', 'X', 'h')`)
	_, err = r.admin.ForgetLinks(t.Context())
	require.NoError(t, err)
	assert.Zero(t, r.count(`SELECT count(*) FROM hubrise_published_items`))
}

func TestProductPublishInfoFollowsTheEdits(t *testing.T) {
	plain := newAdminRig(t)
	infos, err := plain.admin.ProductPublishInfos(t.Context(), []uuid.UUID{plain.fx.SalmonSushi.ID})
	require.NoError(t, err)
	assert.Empty(t, infos, "nothing to show outside PUBLISH mode")

	r := newPublishRig(t)
	ids := []uuid.UUID{r.fx.SalmonSushi.ID}

	infos, err = r.admin.ProductPublishInfos(t.Context(), ids)
	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.Equal(t, application.ProductNotPublished, infos[ids[0]].State)
	assert.Empty(t, infos[ids[0]].Ref)

	r.confirmFirstPublish()
	infos, err = r.admin.ProductPublishInfos(t.Context(), ids)
	require.NoError(t, err)
	got := infos[ids[0]]
	assert.Equal(t, application.ProductPublished, got.State)
	assert.NotEmpty(t, got.Ref)
	assert.NotNil(t, got.PublishedAt)
	all, err := r.admin.ProductPublishInfos(t.Context(), nil)
	require.NoError(t, err)
	assert.Greater(t, len(all), 1, "no ids means every product")

	r.exec(`UPDATE products SET price = price + 1 WHERE id = $1`, ids[0])
	_, err = r.mod.PublishState.MarkDirty(t.Context(), r.clock.Now())
	require.NoError(t, err)
	infos, err = r.admin.ProductPublishInfos(t.Context(), ids)
	require.NoError(t, err)
	assert.Equal(t, application.ProductPending, infos[ids[0]].State)
	status, err := r.admin.Status(t.Context())
	require.NoError(t, err)
	assert.True(t, status.Publish.Pending)
	assert.Equal(t, 1, status.Publish.PendingProducts)
	assert.Positive(t, status.Publish.PublishedProducts)
	assert.NotNil(t, status.Publish.PendingSince)
}

func TestPublishStatusReportsErrorsAndTheNextRun(t *testing.T) {
	r := newPublishRig(t)
	_, err := r.admin.SetCatalogDirection(t.Context(), domain.DirectionPublish, false)
	require.NoError(t, err)
	r.confirmFirstPublish()
	_, err = r.mod.PublishState.MarkDirty(t.Context(), r.clock.Now())
	require.NoError(t, err)
	status, err := r.admin.Status(t.Context())
	require.NoError(t, err)
	assert.True(t, status.Publish.Active)
	assert.NotNil(t, status.Publish.NextPublishAt)
	assert.NotNil(t, status.Publish.LastPublishedAt)
	assert.NotNil(t, status.Publish.FirstPublishConfirmedAt)
	assert.False(t, status.Publish.AwaitingFirstPublish)

	require.NoError(t, r.mod.PublishState.Block(t.Context(), "", domain.PublishAccountCatalog, "account catalog", r.clock.Now()))
	status, err = r.admin.Status(t.Context())
	require.NoError(t, err)
	assert.False(t, status.Publish.Active, "a blocked publisher is not active")
	assert.Nil(t, status.Publish.NextPublishAt)
	assert.True(t, status.Publish.AccountCatalogDetected)
	require.NotNil(t, status.Publish.LastError)
	assert.Equal(t, domain.PublishAccountCatalog, status.Publish.LastErrorCode)
	assert.NotNil(t, status.Publish.LastErrorAt)
}

func TestAdminWithoutAPublisherIsStandalone(t *testing.T) {
	d := application.AdminDeps{}
	a := application.NewAdmin(d)
	_, err := a.PreviewPublish(t.Context(), false)
	assert.ErrorIs(t, err, domain.ErrNotPublishMode)
	_, err = a.PublishCatalog(t.Context(), application.PublishRequest{})
	assert.ErrorIs(t, err, domain.ErrNotPublishMode)
	assert.NoError(t, a.AcknowledgeExternalChange(t.Context()))
	infos, err := a.ProductPublishInfos(t.Context(), nil)
	require.NoError(t, err)
	assert.Empty(t, infos)
}

// pubFaultyEvents fails the reads of the event store the publisher's drift job makes.
type pubFaultyEvents struct {
	domain.EventRepository
	types, collapse bool
}

func (f pubFaultyEvents) PendingEventTypes(ctx context.Context, resource string, before time.Time) ([]string, error) {
	if f.types {
		return nil, errPubBoom
	}
	return f.EventRepository.PendingEventTypes(ctx, resource, before)
}

func (f pubFaultyEvents) CollapsePending(ctx context.Context, resource string, before, now time.Time) (int64, error) {
	if f.collapse {
		return 0, errPubBoom
	}
	return f.EventRepository.CollapsePending(ctx, resource, before, now)
}

func TestCatalogEventsOfAPublishedCatalogThatCannotBeConsumedAreReported(t *testing.T) {
	for name, faulty := range map[string]func(domain.EventRepository) domain.EventRepository{
		"the types": func(e domain.EventRepository) domain.EventRepository {
			return pubFaultyEvents{EventRepository: e, types: true}
		},
		"the collapse": func(e domain.EventRepository) domain.EventRepository {
			return pubFaultyEvents{EventRepository: e, collapse: true}
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newPublishRig(t)
			_, err := r.admin.SetCatalogDirection(t.Context(), domain.DirectionPublish, false)
			require.NoError(t, err)
			job := application.NewCatalogDrift(application.CatalogDriftDeps{
				Connections: r.mod.Connections, Events: faulty(r.mod.Events), Leases: r.mod.Leases, Checker: r.catalog.Drift, Clock: r.clock,
				Publish: r.catalog.Publisher,
			})
			r.catalogEvent("edit", "update", time.Minute)
			_, err = job.CheckEvents(t.Context())
			assert.ErrorIs(t, err, errPubBoom)
		})
	}
}

func TestTheStatusFailsWhenThePublisherCannotBeRead(t *testing.T) {
	r := newPublishRig(t)
	r.exec(`ALTER TABLE hubrise_catalog_publish RENAME TO hubrise_catalog_publish_away`)
	_, err := r.admin.Status(t.Context())
	assert.Error(t, err)
}

func TestForgettingLinksReportsWhatItCannotForget(t *testing.T) {
	r := newPublishRig(t)
	r.exec(`ALTER TABLE hubrise_published_items RENAME TO hubrise_published_items_away`)
	_, err := r.admin.ForgetLinks(t.Context())
	assert.Error(t, err)
}

func TestThePublisherCannotBuildAClientForAnUnreadableToken(t *testing.T) {
	r := newPublishRig(t)
	r.exec(`UPDATE hubrise_connection SET access_token_enc = decode('00', 'hex')`)
	_, err := r.catalog.Publisher.Preview(t.Context(), false)
	assert.Error(t, err)
}
