package infrastructure_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/internal/modules/hubrise/infrastructure"
	hubtest "tsb-service/internal/modules/hubrise/testhelpers"
)

func sp(s string) *string { return &s }

func TestConnectionRepository(t *testing.T) {
	e := newEnv(t)
	repo := infrastructure.NewConnectionRepository(e.db)
	ctx := t.Context()
	now := time.Now().Truncate(time.Microsecond)

	t.Run("nothing connected yet", func(t *testing.T) {
		_, err := repo.Get(ctx)
		assert.ErrorIs(t, err, domain.ErrNotFound)
		// Every update of the single row says so when there is no row.
		for name, err := range map[string]error{
			"disconnect":       repo.Disconnect(ctx),
			"auth error":       repo.RecordAuthError(ctx, now, "x"),
			"clear auth":       repo.ClearAuthError(ctx),
			"record error":     repo.RecordError(ctx, "x"),
			"touch":            repo.TouchLastEvent(ctx, now),
			"callback":         repo.SetCallbackRegistered(ctx, &now),
			"catalog sync":     repo.RecordCatalogSync(ctx, "h", now),
			"catalog preview":  repo.RecordCatalogPreview(ctx, "h", now),
			"update available": repo.SetCatalogUpdateAvailable(ctx, &now),
			"baseline":         repo.SetInventoryBaseline(ctx, now),
			"relink":           repo.SetRelinkRequired(ctx, &now),
		} {
			assert.ErrorIs(t, err, domain.ErrNotFound, name)
		}
		_, err = repo.UpdateSettings(ctx, domain.SettingsPatch{})
		assert.ErrorIs(t, err, domain.ErrNotFound)
	})

	t.Run("connect creates the row with default settings", func(t *testing.T) {
		since := now.Add(-time.Minute)
		c, err := repo.Connect(ctx, domain.ConnectParams{
			AccessTokenEnc: []byte("sealed"), AccountID: "acc", AccountName: "Account", LocationID: "loc", LocationName: "Location",
			CatalogID: "cat", CatalogName: "Catalog", CustomerListID: "cl", CustomerListName: "Customers",
			Scope: "location[orders.write]", PushOrdersSince: since,
		})
		require.NoError(t, err)
		assert.True(t, c.Connected())
		assert.False(t, c.Enabled, "a fresh connection starts disabled")
		assert.Equal(t, []byte("sealed"), c.AccessTokenEnc)
		assert.Equal(t, "loc", *c.LocationID)
		assert.Equal(t, "Customers", *c.CustomerListName)
		assert.False(t, c.PushOrders || c.SyncStatus || c.SyncInventory || c.SyncCustomers, "every feature starts off")
		assert.False(t, c.SyncCatalog || c.CatalogAutoImport || c.AutoRefundOnCancel || c.ImportImages)
		assert.Equal(t, "fr", c.CatalogLanguage)
		assert.WithinDuration(t, since, *c.PushOrdersSince, time.Millisecond)
		assert.Nil(t, c.VariantRef)
		assert.Equal(t, "DELIVERY", c.DeliveryChargeRefOrDefault())

		got, err := repo.Get(ctx)
		require.NoError(t, err)
		assert.Equal(t, *c, *got)
	})

	t.Run("update settings patches only what is given", func(t *testing.T) {
		yes, no := true, false
		since := now
		c, err := repo.UpdateSettings(ctx, domain.SettingsPatch{
			Enabled: &yes, PushOrders: &no, SyncStatus: &no, SyncInventory: &no, SyncCatalog: &yes, SyncCustomers: &no,
			CatalogAutoImport: &yes, AutoRefundOnCancel: &yes, ImportImages: &yes,
			CatalogLanguage: sp("nl"), VariantRef: sp("eat_in"), ChannelName: sp("webshop"),
			DeliveryChargeRef: sp("DLV"), PaymentFeeChargeRef: sp("FEE"), PushOrdersSince: &since,
		})
		require.NoError(t, err)
		assert.True(t, c.Enabled && c.SyncCatalog && c.CatalogAutoImport && c.AutoRefundOnCancel && c.ImportImages)
		assert.False(t, c.PushOrders || c.SyncStatus || c.SyncInventory || c.SyncCustomers)
		assert.Equal(t, "nl", c.CatalogLanguage)
		assert.Equal(t, "eat_in", *c.VariantRef)
		assert.Equal(t, "DLV", c.DeliveryChargeRefOrDefault())
		assert.Equal(t, "FEE", c.PaymentFeeChargeRefOrDefault())
		assert.WithinDuration(t, since, *c.PushOrdersSince, time.Millisecond)

		// An empty patch changes nothing.
		same, err := repo.UpdateSettings(ctx, domain.SettingsPatch{})
		require.NoError(t, err)
		c.UpdatedAt = same.UpdatedAt
		assert.Equal(t, *c, *same)

		// An empty string clears a text option; nil leaves it.
		cleared, err := repo.UpdateSettings(ctx, domain.SettingsPatch{VariantRef: sp(""), ChannelName: sp("")})
		require.NoError(t, err)
		assert.Nil(t, cleared.VariantRef)
		assert.Nil(t, cleared.ChannelName)
		assert.Equal(t, "DLV", *cleared.DeliveryChargeRef)
	})

	t.Run("bookkeeping stamps", func(t *testing.T) {
		require.NoError(t, repo.RecordAuthError(ctx, now, "401 from HubRise"))
		c, _ := repo.Get(ctx)
		assert.WithinDuration(t, now, *c.AuthErrorAt, time.Millisecond)
		assert.Equal(t, "401 from HubRise", *c.LastError)
		assert.False(t, c.Active(), "an auth error pauses the pipeline")

		require.NoError(t, repo.ClearAuthError(ctx))
		c, _ = repo.Get(ctx)
		assert.Nil(t, c.AuthErrorAt)
		assert.Nil(t, c.LastError)

		require.NoError(t, repo.RecordError(ctx, "boom"))
		c, _ = repo.Get(ctx)
		assert.Equal(t, "boom", *c.LastError)
		require.NoError(t, repo.RecordError(ctx, ""))
		c, _ = repo.Get(ctx)
		assert.Nil(t, c.LastError)

		require.NoError(t, repo.TouchLastEvent(ctx, now))
		require.NoError(t, repo.SetCallbackRegistered(ctx, &now))
		require.NoError(t, repo.SetCatalogUpdateAvailable(ctx, &now))
		require.NoError(t, repo.RecordCatalogPreview(ctx, "preview-hash", now))
		require.NoError(t, repo.SetInventoryBaseline(ctx, now))
		require.NoError(t, repo.SetRelinkRequired(ctx, &now))
		c, _ = repo.Get(ctx)
		assert.WithinDuration(t, now, *c.LastEventAt, time.Millisecond)
		assert.WithinDuration(t, now, *c.CallbackRegisteredAt, time.Millisecond)
		assert.WithinDuration(t, now, *c.CatalogUpdateAvailableAt, time.Millisecond)
		assert.Equal(t, "preview-hash", *c.CatalogPreviewHash)
		assert.WithinDuration(t, now, *c.CatalogPreviewAt, time.Millisecond)
		assert.WithinDuration(t, now, *c.InventoryBaselineAt, time.Millisecond)
		assert.WithinDuration(t, now, *c.RelinkRequiredAt, time.Millisecond)

		// Importing a catalog records its hash and clears the "update available" banner.
		require.NoError(t, repo.RecordCatalogSync(ctx, "catalog-hash", now))
		c, _ = repo.Get(ctx)
		assert.Equal(t, "catalog-hash", *c.CatalogHash)
		assert.WithinDuration(t, now, *c.LastCatalogSyncAt, time.Millisecond)
		assert.Nil(t, c.CatalogUpdateAvailableAt)

		require.NoError(t, repo.SetCallbackRegistered(ctx, nil))
		require.NoError(t, repo.SetCatalogUpdateAvailable(ctx, nil))
		require.NoError(t, repo.SetRelinkRequired(ctx, nil))
		c, _ = repo.Get(ctx)
		assert.Nil(t, c.CallbackRegisteredAt)
		assert.Nil(t, c.CatalogUpdateAvailableAt)
		assert.Nil(t, c.RelinkRequiredAt)
	})

	t.Run("reconnect keeps the settings and clears the errors", func(t *testing.T) {
		require.NoError(t, repo.RecordAuthError(ctx, now, "revoked"))
		before, _ := repo.Get(ctx)
		c, err := repo.Connect(ctx, domain.ConnectParams{AccessTokenEnc: []byte("sealed-2"), LocationID: "loc-2"})
		require.NoError(t, err)
		assert.Equal(t, []byte("sealed-2"), c.AccessTokenEnc)
		assert.Equal(t, "loc-2", *c.LocationID)
		assert.Nil(t, c.CatalogID, "ids the token response did not carry are cleared")
		assert.Nil(t, c.AuthErrorAt)
		assert.Nil(t, c.LastError)
		assert.Equal(t, before.Enabled, c.Enabled, "the runtime switch is kept")
		assert.Equal(t, "nl", c.CatalogLanguage, "settings are kept")
		assert.True(t, c.SyncCatalog)
		assert.WithinDuration(t, *before.PushOrdersSince, *c.PushOrdersSince, time.Millisecond, "no stamp given: the old one is kept")
	})

	t.Run("disconnect", func(t *testing.T) {
		require.NoError(t, repo.SetCallbackRegistered(ctx, &now))
		require.NoError(t, repo.Disconnect(ctx))
		c, err := repo.Get(ctx)
		require.NoError(t, err)
		assert.False(t, c.Connected())
		assert.False(t, c.Enabled)
		assert.Nil(t, c.CallbackRegisteredAt)
		assert.Equal(t, "loc-2", *c.LocationID, "ids and settings stay")
		assert.True(t, c.SyncCatalog)
	})

	t.Run("reconnect that needs a relink switches the integration off in the same statement", func(t *testing.T) {
		on := true
		_, err := repo.UpdateSettings(ctx, domain.SettingsPatch{Enabled: &on, SyncCatalog: &on, SyncInventory: &on})
		require.NoError(t, err)
		c, err := repo.Connect(ctx, domain.ConnectParams{AccessTokenEnc: []byte("sealed-3"), LocationID: "loc-3", RelinkRequiredAt: &now})
		require.NoError(t, err)
		assert.Equal(t, "loc-3", *c.LocationID)
		assert.False(t, c.Enabled)
		assert.False(t, c.SyncCatalog)
		assert.False(t, c.SyncInventory)
		require.NotNil(t, c.RelinkRequiredAt)
		assert.WithinDuration(t, now, *c.RelinkRequiredAt, time.Millisecond)

		c, err = repo.Connect(ctx, domain.ConnectParams{AccessTokenEnc: []byte("sealed-4"), LocationID: "loc-3"})
		require.NoError(t, err)
		assert.NotNil(t, c.RelinkRequiredAt, "a plain reconnect does not lift the flag")
		require.NoError(t, repo.SetRelinkRequired(ctx, nil))
	})
}

func TestSeedConnection(t *testing.T) {
	e := newEnv(t)
	repo := infrastructure.NewConnectionRepository(e.db)
	codec := hubtest.TestCodec(t)

	seeded := hubtest.SeedConnection(t, e.db)
	assert.Equal(t, hubtest.DefaultToken, seeded.Token)
	c, err := repo.Get(t.Context())
	require.NoError(t, err)
	assert.True(t, c.Active())
	assert.Equal(t, hubtest.DefaultLocationID, *c.LocationID)
	assert.Equal(t, hubtest.DefaultCatalogID, *c.CatalogID)
	assert.Equal(t, hubtest.DefaultCustomerListID, *c.CustomerListID)
	tok, err := c.AccessToken(codec)
	require.NoError(t, err)
	assert.Equal(t, hubtest.DefaultToken, tok.Reveal())
	assert.True(t, c.PushOrders && c.SyncStatus && c.SyncInventory && c.SyncCustomers)
	assert.False(t, c.SyncCatalog)
	assert.Nil(t, c.PushOrdersSince)

	// Seeding again replaces the row, clearing an auth error, and honours the options.
	require.NoError(t, repo.RecordAuthError(t.Context(), time.Now(), "revoked"))
	since := time.Now().Add(-time.Hour).Truncate(time.Microsecond)
	seeded = hubtest.SeedConnection(t, e.db,
		hubtest.WithToken("other"), hubtest.WithEnabled(false), hubtest.WithFeatures(false, true, false, true, false),
		hubtest.WithPushOrdersSince(since), hubtest.WithLocation("loc-9", "cat-9"))
	assert.Equal(t, "other", seeded.Token)
	assert.Equal(t, "loc-9", seeded.LocationID)
	assert.Equal(t, "cat-9", seeded.CatalogID)
	c, err = repo.Get(t.Context())
	require.NoError(t, err)
	assert.False(t, c.Enabled)
	assert.Nil(t, c.AuthErrorAt)
	assert.False(t, c.PushOrders || c.SyncInventory || c.SyncCustomers)
	assert.True(t, c.SyncStatus && c.SyncCatalog)
	assert.WithinDuration(t, since, *c.PushOrdersSince, time.Millisecond)
	tok, _ = c.AccessToken(codec)
	assert.Equal(t, "other", tok.Reveal())
}
