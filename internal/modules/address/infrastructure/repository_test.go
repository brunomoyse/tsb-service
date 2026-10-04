package infrastructure

import (
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/testhelpers"
	"tsb-service/internal/modules/address/domain"
	"tsb-service/pkg/db"
)

func sp(s string) *string { return &s }

func TestAddressCacheRepository(t *testing.T) {
	tdb := testhelpers.SetupTestDatabase(t)
	repo := NewAddressCacheRepository(&db.DBPool{Customer: tdb.DB, Admin: tdb.DB})
	ctx := t.Context()
	created := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)

	entry := func() *domain.AddressCache {
		return &domain.AddressCache{
			PlaceID: "place-1", FormattedAddress: "Rue Neuve 12, 4000 Liège", Lat: 50.64, Lng: 5.57,
			StreetName: sp("Rue Neuve"), HouseNumber: sp("12"), BoxNumber: sp("3B"), Postcode: sp("4000"), MunicipalityName: sp("Liège"),
			CountryCode: "BE", DistanceMeters: 2500, DurationSeconds: 420,
			RawPlaceDetails: []byte(`{"id":"place-1"}`), CreatedAt: created, RefreshedAt: created,
		}
	}

	t.Run("a miss is (nil, nil), not an error", func(t *testing.T) {
		got, err := repo.GetByPlaceID(ctx, "unknown")
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("Upsert inserts and GetByPlaceID returns every column", func(t *testing.T) {
		require.NoError(t, repo.Upsert(ctx, entry()))
		got, err := repo.GetByPlaceID(ctx, "place-1")
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "Rue Neuve 12, 4000 Liège", got.FormattedAddress)
		assert.InDelta(t, 50.64, got.Lat, 1e-9)
		assert.Equal(t, "Rue Neuve", *got.StreetName)
		assert.Equal(t, "3B", *got.BoxNumber)
		assert.Equal(t, "BE", got.CountryCode)
		assert.Equal(t, 2500, got.DistanceMeters)
		assert.Equal(t, 420, got.DurationSeconds)
		assert.JSONEq(t, `{"id":"place-1"}`, string(got.RawPlaceDetails))
		assert.True(t, created.Equal(got.CreatedAt))
	})

	t.Run("Upsert on an existing place refreshes it but keeps created_at", func(t *testing.T) {
		upd := entry()
		upd.FormattedAddress = "Rue Neuve 12 (updated)"
		upd.DistanceMeters = 3100
		upd.StreetName = nil
		upd.RefreshedAt = time.Now().UTC().Truncate(time.Second)
		upd.CreatedAt = time.Now().UTC() // must not overwrite the original
		require.NoError(t, repo.Upsert(ctx, upd))

		got, err := repo.GetByPlaceID(ctx, "place-1")
		require.NoError(t, err)
		assert.Equal(t, "Rue Neuve 12 (updated)", got.FormattedAddress)
		assert.Equal(t, 3100, got.DistanceMeters)
		assert.Nil(t, got.StreetName)
		assert.True(t, upd.RefreshedAt.Equal(got.RefreshedAt))
		assert.True(t, created.Equal(got.CreatedAt), "created_at is not rewritten on refresh")
	})

	t.Run("a closed connection is reported", func(t *testing.T) {
		conn, err := sqlx.Open("postgres", "host=127.0.0.1 port=1 user=x dbname=x sslmode=disable")
		require.NoError(t, err)
		require.NoError(t, conn.Close())
		closed := NewAddressCacheRepository(&db.DBPool{Customer: conn, Admin: conn})
		_, err = closed.GetByPlaceID(ctx, "place-1")
		assert.Error(t, err)
		assert.Error(t, closed.Upsert(ctx, entry()))
	})
}
