package hubrise_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/testhelpers"
	"tsb-service/internal/modules/hubrise"
	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/internal/modules/hubrise/infrastructure"
	hubtest "tsb-service/internal/modules/hubrise/testhelpers"
)

// TestMain removes the package's shared PostgreSQL container once its tests are done.
func TestMain(m *testing.M) { os.Exit(testhelpers.Main(m)) }

func TestNewModuleUnconfiguredIsNil(t *testing.T) {
	// No database is needed: nothing is built when HubRise is not available.
	m, err := hubrise.NewModule(hubrise.Config{}, nil, nil)
	require.NoError(t, err)
	assert.Nil(t, m)
	assert.False(t, m.Enabled(), "Enabled is safe on a nil Module")

	partial := hubrise.Config{ClientID: "id", ClientSecret: "secret"}
	m, err = hubrise.NewModule(partial, nil, nil)
	require.NoError(t, err)
	assert.Nil(t, m, "a half configured deployment stays standalone")
}

func TestNewModuleBadKey(t *testing.T) {
	_, err := hubrise.NewModule(hubrise.Config{ClientID: "id", ClientSecret: "s", TokenKey: []byte("short")}, nil, nil)
	assert.Error(t, err)
}

func TestNewModuleWiresTheStorageLayer(t *testing.T) {
	td := testhelpers.SetupTestDatabase(t)
	cfg, err := hubrise.LoadConfig(env(validEnv()))
	require.NoError(t, err)

	clock := hubtest.NewFakeClock(time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC))
	m, err := hubrise.NewModule(cfg, td.DB, clock)
	require.NoError(t, err)
	require.NotNil(t, m)
	assert.True(t, m.Enabled())
	assert.Same(t, clock, m.Clock)

	// Default clock.
	def, err := hubrise.NewModule(cfg, td.DB, nil)
	require.NoError(t, err)
	assert.IsType(t, infrastructure.SystemClock{}, def.Clock)

	// The codec uses the configured key: a token seeded with the test codec opens through the module.
	hubtest.SeedConnection(t, td.DB)
	conn, err := m.Connections.Get(t.Context())
	require.NoError(t, err)
	tok, err := conn.AccessToken(m.Codec)
	require.NoError(t, err)
	assert.Equal(t, hubtest.DefaultToken, tok.Reveal())

	// Every repository is wired to the database.
	_, err = m.CatalogLinks.Count(t.Context(), true)
	require.NoError(t, err)
	_, err = m.Outbox.Counts(t.Context())
	require.NoError(t, err)
	_, err = m.Orders.CountNeedsAttention(t.Context())
	require.NoError(t, err)
	_, err = m.Events.OldestPendingReceivedAt(t.Context(), "order")
	require.NoError(t, err)
	_, err = m.Foreign.IsForeign(t.Context(), "x")
	require.NoError(t, err)
	ok, err := m.Leases.Acquire(t.Context(), "job", "me", clock.Now(), time.Minute)
	require.NoError(t, err)
	assert.True(t, ok)
	_, _, err = m.OAuthStates.Consume(t.Context(), "none", clock.Now())
	require.NoError(t, err)
	_, err = m.CustomerLinks.Get(t.Context(), uuid.New())
	assert.ErrorIs(t, err, domain.ErrNotFound)
	_, err = m.Customers.Profile(t.Context(), uuid.New())
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestNewPushBuildsTheOrderPushOnTheModuleStores(t *testing.T) {
	td := testhelpers.SetupTestDatabase(t)
	cfg, err := hubrise.LoadConfig(env(validEnv()))
	require.NoError(t, err)
	m, err := hubrise.NewModule(cfg, td.DB, hubtest.NewFakeClock(time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)))
	require.NoError(t, err)
	require.NotNil(t, m.Source)

	// The defaults: pooled clients on the configured API, no optional port.
	push := m.NewPush(hubrise.PushOptions{})
	require.NotNil(t, push.Pusher)
	require.NotNil(t, push.Worker)
	require.NotNil(t, push.Reconciler)
	require.NotNil(t, push.Customers)

	// Nothing is connected: the customer hooks do nothing either.
	require.NoError(t, push.Customers.ProfileChanged(t.Context(), uuid.New()))
	require.NoError(t, push.Customers.AccountDeleted(t.Context(), uuid.New()))

	// Nothing is connected: the hooks do nothing and the worker has nothing to claim.
	require.NoError(t, push.Pusher.OrderReady(t.Context(), uuid.New()))
	n, err := push.Worker.DrainOnce(t.Context())
	require.NoError(t, err)
	assert.Zero(t, n)
}

type noImages struct{}

func (noImages) ProductImage(context.Context, uuid.UUID, string) (*application.SourceImage, error) {
	return nil, nil
}

func TestTheCatalogPublisherReadsTheImagesFromTheConfiguredBucket(t *testing.T) {
	td := testhelpers.SetupTestDatabase(t)
	cfg, err := hubrise.LoadConfig(env(validEnv()))
	require.NoError(t, err)
	clients := application.NewPooledClients(nil, "http://hubrise.invalid/v1", nil, nil)

	m, err := hubrise.NewModule(cfg, td.DB, nil)
	require.NoError(t, err)
	assert.Nil(t, m.NewCatalog(nil, hubrise.CatalogOptions{Clients: clients}).Publisher.ImageSource, "no bucket, no images")

	cfg.ImageBaseURL = "https://bucket.example/shop"
	m, err = hubrise.NewModule(cfg, td.DB, nil)
	require.NoError(t, err)
	pub := m.NewCatalog(nil, hubrise.CatalogOptions{Clients: clients}).Publisher
	assert.Equal(t, application.HTTPProductImages{BaseURL: "https://bucket.example/shop"}, pub.ImageSource)
	assert.NotNil(t, pub.ImageStamps)

	own := noImages{}
	pub = m.NewCatalog(nil, hubrise.CatalogOptions{Clients: clients, ProductImages: own}).Publisher
	assert.Equal(t, own, pub.ImageSource, "the caller's source wins")
}
