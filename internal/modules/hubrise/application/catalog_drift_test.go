package application_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/modules/hubrise/domain"
	hubtest "tsb-service/internal/modules/hubrise/testhelpers"
	"tsb-service/pkg/hubrise"
	"tsb-service/pkg/hubrise/hubrisetest"
)

func on() *bool { v := true; return &v }

func TestDriftOfAnInactiveConnectionDoesNothing(t *testing.T) {
	e := newCatEnv(t)
	e.setting(domain.SettingsPatch{Enabled: offSwitch()})
	out, err := e.svc.CheckDrift(t.Context())
	require.NoError(t, err)
	assert.Equal(t, &application.DriftOutcome{}, out)
	assert.Empty(t, e.srv.Requests())
}

func TestDriftNotesAChangedCatalogForTheDashboard(t *testing.T) {
	e := newCatEnv(t)
	e.publish(sushiMenu())
	e.fullImport()

	out, err := e.svc.CheckDrift(t.Context())
	require.NoError(t, err)
	assert.False(t, out.Changed, "nothing new")

	changed := sushiMenu()
	changed.d.Products[1].Skus[0].Price = money("3.20")
	e.publish(changed)
	out, err = e.svc.CheckDrift(t.Context())
	require.NoError(t, err)
	assert.True(t, out.Changed)
	assert.False(t, out.Imported, "manual import mode: the dashboard offers it")
	first := e.connection().CatalogUpdateAvailableAt
	require.NotNil(t, first)

	// The stamp is kept while the catalog stays ahead.
	e.clock.Advance(time.Hour)
	_, err = e.svc.CheckDrift(t.Context())
	require.NoError(t, err)
	assert.True(t, e.connection().CatalogUpdateAvailableAt.Equal(*first))

	// Importing by hand brings the shop level; the next check clears the banner.
	e.fullImport()
	out, err = e.svc.CheckDrift(t.Context())
	require.NoError(t, err)
	assert.False(t, out.Changed)
	assert.Nil(t, e.connection().CatalogUpdateAvailableAt)
}

func TestDriftImportsAutomaticallyWhenAsked(t *testing.T) {
	e := newCatEnv(t)
	e.setting(domain.SettingsPatch{CatalogAutoImport: on()})
	e.publish(sushiMenu())

	out, err := e.svc.CheckDrift(t.Context())

	require.NoError(t, err)
	assert.True(t, out.Changed)
	assert.True(t, out.Imported)
	assert.Equal(t, 3, e.count(`SELECT count(*) FROM products`))
}

func TestDriftNeverAppliesAMassRetirement(t *testing.T) {
	e := newCatEnv(t)
	e.setting(domain.SettingsPatch{CatalogAutoImport: on()})
	e.publish(bigMenu(8))
	e.fullImport()
	e.publish(bigMenu(1))

	out, err := e.svc.CheckDrift(t.Context())

	require.NoError(t, err)
	assert.True(t, out.PendingReview)
	assert.False(t, out.Imported)
	assert.Equal(t, 8, e.count(`SELECT count(*) FROM products WHERE is_visible`))
}

func TestDriftReportsErrors(t *testing.T) {
	e := newCatEnv(t)
	e.setting(domain.SettingsPatch{CatalogAutoImport: on()})
	e.publish(sushiMenu())
	e.srv.AddFault(hubrisetest.Fault{Method: "GET", Path: "/v1/catalogs", Status: 500, Times: 1})
	_, err := e.svc.CheckDrift(t.Context())
	require.Error(t, err)

	// The fetch works, the import fails on the lease.
	ok, err := e.leases.Acquire(t.Context(), "catalog_import", "other", e.clock.Now(), time.Hour)
	require.NoError(t, err)
	require.True(t, ok)
	out, err := e.svc.CheckDrift(t.Context())
	assert.ErrorIs(t, err, domain.ErrImportInProgress)
	assert.True(t, out.Changed)
}

func TestAFailedBatchOfAnAutomaticImportIsNotRecordedAsDone(t *testing.T) {
	e := newCatEnv(t)
	e.publish(sushiMenu())
	e.svc.Local = failingLocal{LocalCatalogRepository: e.local, failOn: "SAUMON-6"}
	res, err := e.svc.Sync(t.Context(), application.SyncRequest{Mode: domain.SyncFull})
	require.NoError(t, err)
	require.NotEmpty(t, res.Result.Failed)
	assert.Nil(t, e.connection().CatalogHash)
}

// corruptAPI serves an image whose bytes do not match the checksum HubRise reported.
type corruptAPI struct{ application.CatalogAPI }

func (c corruptAPI) GetImageData(ctx context.Context, catalogID, imageID string) (*hubrise.ImageData, error) {
	img, err := c.CatalogAPI.GetImageData(ctx, catalogID, imageID)
	if err != nil {
		return nil, err
	}
	img.Data = append(img.Data, '!')
	return img, nil
}

func TestAnImageWhoseChecksumDisagreesIsNotStored(t *testing.T) {
	e := newCatEnv(t)
	e.setting(domain.SettingsPatch{ImportImages: on()})
	e.srv.AddImage("img", "image/png", []byte("png"))
	m := newMenu().category("C", "Carte").product("P1", "C", "Un", sku("S1", "3"))
	m.d.Products[0].ImageIDs = []string{"img"}
	e.publish(m)
	inner := e.svc.NewClient
	e.svc.NewClient = func(c *domain.Connection) (application.CatalogAPI, error) {
		api, err := inner(c)
		return corruptAPI{api}, err
	}

	res := e.fullImport()

	require.Len(t, res.Warnings, 1)
	assert.Contains(t, res.Warnings[0].Message, "checksum mismatch")
	assert.Empty(t, e.images.uploaded)
}

func TestClientFactoryFailures(t *testing.T) {
	e := newCatEnv(t)
	good := &application.ClientFactory{BaseURL: e.srv.APIBaseURL(), Codec: hubtest.TestCodec(t), HTTP: e.srv.HTTPClient()}
	conn := e.connection()
	c, err := good.Client(conn)
	require.NoError(t, err)
	assert.NotNil(t, c)
	api, err := good.CatalogAPI(conn)
	require.NoError(t, err)
	assert.NotNil(t, api)

	conn.AccessTokenEnc = []byte("not a sealed token")
	_, err = good.Client(conn)
	assert.Error(t, err)

	conn = e.connection()
	conn.AccessTokenEnc = nil
	_, err = good.Client(conn)
	assert.ErrorIs(t, err, domain.ErrNotConnected)

	bad := &application.ClientFactory{BaseURL: "", Codec: hubtest.TestCodec(t)}
	_, err = bad.Client(e.connection())
	assert.ErrorContains(t, err, "build client")

	// Without an HTTP client the library default is used.
	plain := &application.ClientFactory{BaseURL: e.srv.APIBaseURL(), Codec: hubtest.TestCodec(t)}
	_, err = plain.Client(e.connection())
	assert.NoError(t, err)
}

func TestUtilsImageUploaderSendsTheImageToTheFileService(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("FILE_SERVICE_URL", srv.URL)
	id := uuid.New()

	err := application.UtilsImageUploader{}.UploadProductImage(t.Context(), id, id.String()+".png", []byte("png"))

	require.NoError(t, err)
	assert.NotEmpty(t, gotPath)

	t.Setenv("FILE_SERVICE_URL", "")
	assert.Error(t, application.UtilsImageUploader{}.UploadProductImage(t.Context(), id, "x.png", nil), "no file service configured")
}

// errConns fails the bookkeeping calls named by its flags.
type errConns struct {
	domain.ConnectionRepository
	authErr, recordErr bool
}

func (c errConns) RecordAuthError(ctx context.Context, at time.Time, m string) error {
	if c.authErr {
		return errInjected
	}
	return c.ConnectionRepository.RecordAuthError(ctx, at, m)
}

func (c errConns) RecordError(ctx context.Context, m string) error {
	if c.recordErr {
		return errInjected
	}
	return c.ConnectionRepository.RecordError(ctx, m)
}

func TestBookkeepingFailuresDoNotHideTheRealError(t *testing.T) {
	e := newCatEnv(t)
	e.publish(sushiMenu())
	e.svc.Connections = errConns{ConnectionRepository: e.conns, authErr: true}
	e.srv.AddFault(hubrisetest.Fault{Method: "GET", Path: "/v1/catalogs", Status: 401, Times: 1})
	_, err := e.svc.Sync(t.Context(), application.SyncRequest{Mode: domain.SyncDryRun})
	assert.ErrorIs(t, err, hubrise.KindUnauthorized, "the 401 is returned even when it cannot be recorded")

	e.svc.Connections = errConns{ConnectionRepository: e.conns, recordErr: true}
	e.svc.Local = failingLocal{LocalCatalogRepository: e.local, failOn: "COLA-33"}
	_, err = e.svc.Sync(t.Context(), application.SyncRequest{Mode: domain.SyncFull})
	assert.ErrorIs(t, err, errInjected)
}

func TestImageDownloadFailureAndUnknownTypesAreHandled(t *testing.T) {
	e := newCatEnv(t)
	e.setting(domain.SettingsPatch{ImportImages: on(), VariantRef: strPtr("EAT_IN")})
	e.srv.AddImage("img-odd", "application/x-odd", []byte("odd"))
	e.srv.AddImage("img-down", "image/png", []byte("down"))
	m := newMenu().category("C", "Carte").product("P1", "C", "Un", sku("S1", "3")).product("P2", "C", "Deux", sku("S2", "3"))
	m.d.Products[0].ImageIDs = []string{"img-odd"}
	m.d.Products[1].ImageIDs = []string{"img-down"}
	e.publish(m)
	e.srv.AddFault(hubrisetest.Fault{Method: "GET", Path: "/v1/catalogs/" + hubrisetest.CatalogID + "/images/img-down/data", Status: 500})

	res := e.fullImport()

	assert.Contains(t, e.images.names[e.product("S1").ID], ".jpg", "an unknown media type falls back to jpg")
	var failed []string
	for _, w := range res.Warnings {
		if w.Code == domain.WarnImage {
			failed = append(failed, w.Key)
		}
	}
	assert.Equal(t, []string{"S2"}, failed)
}

func TestReconnectComparesMissingIdsAsEmpty(t *testing.T) {
	e := newCatEnv(t)
	e.publish(sushiMenu())
	e.fullImport()
	prev := e.connection()
	prev.LocationID, prev.CatalogID = nil, nil
	required, err := e.svc.AfterConnect(t.Context(), prev)
	require.NoError(t, err)
	assert.True(t, required, "no known location before, one now, and links exist")
}
