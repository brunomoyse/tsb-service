package infrastructure

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/testhelpers"
	orderDomain "tsb-service/internal/modules/order/domain"
	orderInfra "tsb-service/internal/modules/order/infrastructure"
	"tsb-service/pkg/db"
)

func TestNotificationRepository(t *testing.T) {
	tdb := testhelpers.SetupTestDatabase(t)
	fixtures := testhelpers.SeedTestData(t, tdb.DB)
	pool := &db.DBPool{Customer: tdb.DB, Admin: tdb.DB}
	repo := NewNotificationRepository(pool)
	orders := orderInfra.NewOrderRepository(pool)
	ctx := t.Context()

	user, admin := fixtures.RegularUser.ID, fixtures.AdminUser.ID

	newOrder := func(t *testing.T) uuid.UUID {
		t.Helper()
		saved, _, err := orders.Save(ctx, &orderDomain.Order{
			UserID:      user,
			OrderStatus: orderDomain.OrderStatusPending,
			OrderType:   orderDomain.OrderTypePickUp,
			Language:    "fr",
			TotalPrice:  decimal.RequireFromString("10.00"),
		}, &[]orderDomain.OrderProductRaw{})
		require.NoError(t, err)
		return saved.ID
	}

	t.Run("device tokens: save, find by user, upsert, delete", func(t *testing.T) {
		require.NoError(t, repo.SaveDeviceToken(ctx, user, "tok-a", "ios", "user"))
		require.NoError(t, repo.SaveDeviceToken(ctx, user, "tok-b", "android", "user"))
		require.NoError(t, repo.SaveDeviceToken(ctx, admin, "tok-admin", "android", "admin"))

		got, err := repo.FindDeviceTokensByUserID(ctx, user)
		require.NoError(t, err)
		require.Len(t, got, 2)
		byToken := map[string]string{}
		for _, tk := range got {
			require.Equal(t, user, tk.UserID)
			require.NotEqual(t, uuid.Nil, tk.ID)
			byToken[tk.DeviceToken] = tk.Platform
		}
		require.Equal(t, map[string]string{"tok-a": "ios", "tok-b": "android"}, byToken)

		// Re-registering the same (user, token) updates platform/role instead of duplicating.
		before := got[0]
		for _, tk := range got {
			if tk.DeviceToken == "tok-a" {
				before = tk
			}
		}
		require.NoError(t, repo.SaveDeviceToken(ctx, user, "tok-a", "android", "admin"))
		got, err = repo.FindDeviceTokensByUserID(ctx, user)
		require.NoError(t, err)
		require.Len(t, got, 2)
		for _, tk := range got {
			if tk.DeviceToken == "tok-a" {
				require.Equal(t, before.ID, tk.ID, "upsert keeps the row")
				require.Equal(t, "android", tk.Platform)
				require.Equal(t, "admin", tk.Role)
				require.True(t, tk.UpdatedAt.After(before.UpdatedAt), "updated_at must advance")
			}
		}

		require.NoError(t, repo.DeleteDeviceToken(ctx, user, "tok-a"))
		got, err = repo.FindDeviceTokensByUserID(ctx, user)
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Equal(t, "tok-b", got[0].DeviceToken)

		// Deleting an unknown token is a no-op, and scoped to the user.
		require.NoError(t, repo.DeleteDeviceToken(ctx, user, "nope"))
		require.NoError(t, repo.DeleteDeviceToken(ctx, user, "tok-admin"))
		adminTokens, err := repo.FindDeviceTokensByUserID(ctx, admin)
		require.NoError(t, err)
		require.Len(t, adminTokens, 1, "another user's token must not be deleted")
	})

	t.Run("device tokens: find by role", func(t *testing.T) {
		got, err := repo.FindDeviceTokensByRole(ctx, "admin")
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Equal(t, "tok-admin", got[0].DeviceToken)
		require.Equal(t, admin, got[0].UserID)

		none, err := repo.FindDeviceTokensByRole(ctx, "kitchen")
		require.NoError(t, err)
		require.Empty(t, none)
	})

	t.Run("live activity tokens: save, find, upsert extends expiry, clear", func(t *testing.T) {
		orderID := newOrder(t)
		other := newOrder(t)

		require.NoError(t, repo.SaveLiveActivityToken(ctx, orderID, "la-1"))
		require.NoError(t, repo.SaveLiveActivityToken(ctx, orderID, "la-2"))
		require.NoError(t, repo.SaveLiveActivityToken(ctx, other, "la-other"))

		got, err := repo.FindLiveActivityTokensByOrderID(ctx, orderID)
		require.NoError(t, err)
		require.Len(t, got, 2)
		for _, tk := range got {
			require.Equal(t, orderID, tk.OrderID)
			require.WithinDuration(t, time.Now().Add(12*time.Hour), tk.ExpiresAt, time.Minute)
		}

		// Upsert: shorten the expiry, then re-save and check it is pushed back to ~12h.
		_, err = tdb.DB.ExecContext(ctx, `UPDATE live_activity_tokens SET expires_at = now() + INTERVAL '1 hour' WHERE push_token = 'la-1'`)
		require.NoError(t, err)
		require.NoError(t, repo.SaveLiveActivityToken(ctx, orderID, "la-1"))
		var exp time.Time
		require.NoError(t, tdb.DB.GetContext(ctx, &exp, `SELECT expires_at FROM live_activity_tokens WHERE push_token = 'la-1'`))
		require.WithinDuration(t, time.Now().Add(12*time.Hour), exp, time.Minute)
		got, err = repo.FindLiveActivityTokensByOrderID(ctx, orderID)
		require.NoError(t, err)
		require.Len(t, got, 2, "upsert must not duplicate")

		require.NoError(t, repo.DeleteLiveActivityTokensByOrderID(ctx, orderID))
		got, err = repo.FindLiveActivityTokensByOrderID(ctx, orderID)
		require.NoError(t, err)
		require.Empty(t, got)
		still, err := repo.FindLiveActivityTokensByOrderID(ctx, other)
		require.NoError(t, err)
		require.Len(t, still, 1, "other order's tokens untouched")
	})

	t.Run("live activity tokens: expired tokens are hidden and purged", func(t *testing.T) {
		orderID := newOrder(t)
		require.NoError(t, repo.SaveLiveActivityToken(ctx, orderID, "fresh"))
		require.NoError(t, repo.SaveLiveActivityToken(ctx, orderID, "stale"))
		_, err := tdb.DB.ExecContext(ctx, `UPDATE live_activity_tokens SET expires_at = now() - INTERVAL '1 minute' WHERE push_token = 'stale'`)
		require.NoError(t, err)

		got, err := repo.FindLiveActivityTokensByOrderID(ctx, orderID)
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Equal(t, "fresh", got[0].PushToken)

		require.NoError(t, repo.DeleteExpiredLiveActivityTokens(ctx))
		var n int
		require.NoError(t, tdb.DB.GetContext(ctx, &n, `SELECT count(*) FROM live_activity_tokens WHERE push_token = 'stale'`))
		require.Zero(t, n)
		require.NoError(t, tdb.DB.GetContext(ctx, &n, `SELECT count(*) FROM live_activity_tokens WHERE push_token = 'fresh'`))
		require.Equal(t, 1, n, "non-expired token survives the purge")
	})

	t.Run("every method wraps database errors", func(t *testing.T) {
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		id := uuid.New()

		err := repo.SaveDeviceToken(cctx, id, "t", "ios", "user")
		require.ErrorContains(t, err, "save device token")
		_, err = repo.FindDeviceTokensByUserID(cctx, id)
		require.ErrorContains(t, err, "find device tokens")
		_, err = repo.FindDeviceTokensByRole(cctx, "admin")
		require.ErrorContains(t, err, "find device tokens by role")
		err = repo.DeleteDeviceToken(cctx, id, "t")
		require.ErrorContains(t, err, "delete device token")
		err = repo.SaveLiveActivityToken(cctx, id, "t")
		require.ErrorContains(t, err, "save live activity token")
		_, err = repo.FindLiveActivityTokensByOrderID(cctx, id)
		require.ErrorContains(t, err, "find live activity tokens")
		err = repo.DeleteLiveActivityTokensByOrderID(cctx, id)
		require.ErrorContains(t, err, "delete live activity tokens")
		err = repo.DeleteExpiredLiveActivityTokens(cctx)
		require.ErrorContains(t, err, "delete expired live activity tokens")

		// A live-activity token for a non-existent order violates the FK.
		err = repo.SaveLiveActivityToken(ctx, uuid.New(), "t")
		require.ErrorContains(t, err, "save live activity token")
	})
}
