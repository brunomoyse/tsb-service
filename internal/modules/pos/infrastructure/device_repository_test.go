package infrastructure

import (
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/testhelpers"
	"tsb-service/pkg/db"
)

func TestDeviceRepository(t *testing.T) {
	tdb := testhelpers.SetupTestDatabase(t)
	repo := NewDeviceRepository(&db.DBPool{Customer: tdb.DB, Admin: tdb.DB})
	ctx := t.Context()

	insert := func(serial string, revoked bool, fcm *string) uuid.UUID {
		id := uuid.New()
		_, err := tdb.DB.ExecContext(ctx, `
			INSERT INTO pos_devices (id, serial_number, device_secret_hash, label, revoked_at, fcm_token)
			VALUES ($1, $2, 'abcd', $3, CASE WHEN $4 THEN now() END, $5)`, id, serial, "label-"+serial, revoked, fcm)
		require.NoError(t, err)
		return id
	}
	tokenA, tokenB := "tok-a", "tok-b"
	active := insert("SN-1", false, &tokenA)
	revoked := insert("SN-2", true, &tokenB)
	noToken := insert("SN-3", false, nil)

	t.Run("FindByID returns the device, unknown ids are sql.ErrNoRows", func(t *testing.T) {
		d, err := repo.FindByID(ctx, active)
		require.NoError(t, err)
		assert.Equal(t, "SN-1", d.SerialNumber)
		assert.Equal(t, "abcd", d.DeviceSecretHash)
		assert.Nil(t, d.RevokedAt)
		assert.Nil(t, d.LastSeenAt)
		require.NotNil(t, d.FCMToken)
		assert.Equal(t, "tok-a", *d.FCMToken)

		r, err := repo.FindByID(ctx, revoked)
		require.NoError(t, err)
		assert.NotNil(t, r.RevokedAt)

		_, err = repo.FindByID(ctx, uuid.New())
		assert.ErrorIs(t, err, sql.ErrNoRows)
	})

	t.Run("TouchLastSeen stamps the device", func(t *testing.T) {
		require.NoError(t, repo.TouchLastSeen(ctx, active))
		d, err := repo.FindByID(ctx, active)
		require.NoError(t, err)
		require.NotNil(t, d.LastSeenAt)
		assert.WithinDuration(t, time.Now(), *d.LastSeenAt, time.Minute)

		other, err := repo.FindByID(ctx, noToken)
		require.NoError(t, err)
		assert.Nil(t, other.LastSeenAt, "only the targeted device is touched")
	})

	t.Run("UpdateFCMToken updates active devices only", func(t *testing.T) {
		require.NoError(t, repo.UpdateFCMToken(ctx, noToken, "tok-new"))
		d, err := repo.FindByID(ctx, noToken)
		require.NoError(t, err)
		require.NotNil(t, d.FCMToken)
		assert.Equal(t, "tok-new", *d.FCMToken)
		assert.NotNil(t, d.FCMTokenUpdatedAt)

		require.NoError(t, repo.UpdateFCMToken(ctx, revoked, "tok-hijack"))
		r, err := repo.FindByID(ctx, revoked)
		require.NoError(t, err)
		assert.Equal(t, "tok-b", *r.FCMToken, "a revoked device keeps its old token")
	})

	t.Run("FindActiveFCMTokens skips revoked devices and devices without a token", func(t *testing.T) {
		tokens, err := repo.FindActiveFCMTokens(ctx)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"tok-a", "tok-new"}, tokens)
	})

	t.Run("a closed connection is reported by every method", func(t *testing.T) {
		conn, err := sqlx.Open("postgres", "host=127.0.0.1 port=1 user=x dbname=x sslmode=disable")
		require.NoError(t, err)
		require.NoError(t, conn.Close())
		closed := NewDeviceRepository(&db.DBPool{Customer: conn, Admin: conn})
		_, err = closed.FindByID(ctx, active)
		assert.Error(t, err)
		assert.Error(t, closed.TouchLastSeen(ctx, active))
		assert.Error(t, closed.UpdateFCMToken(ctx, active, "x"))
		_, err = closed.FindActiveFCMTokens(ctx)
		assert.Error(t, err)
	})
}
