package application

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/notification/domain"
)

// recordingRepo records the arguments each repository method receives and
// returns canned results/errors.
type recordingRepo struct {
	calls []string
	args  []any
	err   error

	deviceTokens []domain.DevicePushToken
	laTokens     []domain.LiveActivityToken
}

func (r *recordingRepo) record(name string, args ...any) {
	r.calls = append(r.calls, name)
	r.args = append(r.args, args)
}

func (r *recordingRepo) SaveDeviceToken(_ context.Context, userID uuid.UUID, deviceToken, platform, role string) error {
	r.record("SaveDeviceToken", userID, deviceToken, platform, role)
	return r.err
}

func (r *recordingRepo) FindDeviceTokensByUserID(_ context.Context, userID uuid.UUID) ([]domain.DevicePushToken, error) {
	r.record("FindDeviceTokensByUserID", userID)
	return r.deviceTokens, r.err
}

func (r *recordingRepo) FindDeviceTokensByRole(_ context.Context, role string) ([]domain.DevicePushToken, error) {
	r.record("FindDeviceTokensByRole", role)
	return r.deviceTokens, r.err
}

func (r *recordingRepo) DeleteDeviceToken(_ context.Context, userID uuid.UUID, deviceToken string) error {
	r.record("DeleteDeviceToken", userID, deviceToken)
	return r.err
}

func (r *recordingRepo) SaveLiveActivityToken(_ context.Context, orderID uuid.UUID, pushToken string) error {
	r.record("SaveLiveActivityToken", orderID, pushToken)
	return r.err
}

func (r *recordingRepo) FindLiveActivityTokensByOrderID(_ context.Context, orderID uuid.UUID) ([]domain.LiveActivityToken, error) {
	r.record("FindLiveActivityTokensByOrderID", orderID)
	return r.laTokens, r.err
}

func (r *recordingRepo) DeleteLiveActivityTokensByOrderID(_ context.Context, orderID uuid.UUID) error {
	r.record("DeleteLiveActivityTokensByOrderID", orderID)
	return r.err
}

func (r *recordingRepo) DeleteExpiredLiveActivityTokens(_ context.Context) error {
	r.record("DeleteExpiredLiveActivityTokens")
	return r.err
}

func TestNotificationServiceDelegatesToRepository(t *testing.T) {
	ctx := context.Background()
	userID, orderID := uuid.New(), uuid.New()
	boom := errors.New("db down")

	tokens := []domain.DevicePushToken{{ID: uuid.New(), UserID: userID, DeviceToken: "tok", Platform: "ios", Role: "user"}}
	la := []domain.LiveActivityToken{{ID: uuid.New(), OrderID: orderID, PushToken: "la"}}

	tests := []struct {
		name     string
		call     func(s NotificationService) (any, error)
		wantCall string
		wantArgs []any
		want     any
	}{
		{"RegisterDeviceToken", func(s NotificationService) (any, error) {
			return nil, s.RegisterDeviceToken(ctx, userID, "tok", "ios", "user")
		}, "SaveDeviceToken", []any{userID, "tok", "ios", "user"}, nil},
		{"GetDeviceTokens", func(s NotificationService) (any, error) { return s.GetDeviceTokens(ctx, userID) },
			"FindDeviceTokensByUserID", []any{userID}, tokens},
		{"GetAdminDeviceTokens queries the admin role", func(s NotificationService) (any, error) { return s.GetAdminDeviceTokens(ctx) },
			"FindDeviceTokensByRole", []any{"admin"}, tokens},
		{"UnregisterDeviceToken", func(s NotificationService) (any, error) {
			return nil, s.UnregisterDeviceToken(ctx, userID, "tok")
		}, "DeleteDeviceToken", []any{userID, "tok"}, nil},
		{"RegisterLiveActivityToken", func(s NotificationService) (any, error) {
			return nil, s.RegisterLiveActivityToken(ctx, orderID, "la")
		}, "SaveLiveActivityToken", []any{orderID, "la"}, nil},
		{"GetLiveActivityTokens", func(s NotificationService) (any, error) { return s.GetLiveActivityTokens(ctx, orderID) },
			"FindLiveActivityTokensByOrderID", []any{orderID}, la},
		{"ClearLiveActivityTokens", func(s NotificationService) (any, error) {
			return nil, s.ClearLiveActivityTokens(ctx, orderID)
		}, "DeleteLiveActivityTokensByOrderID", []any{orderID}, nil},
		{"PurgeExpiredLiveActivityTokens", func(s NotificationService) (any, error) {
			return nil, s.PurgeExpiredLiveActivityTokens(ctx)
		}, "DeleteExpiredLiveActivityTokens", []any(nil), nil},
	}

	for _, tc := range tests {
		t.Run(tc.name+" success", func(t *testing.T) {
			repo := &recordingRepo{deviceTokens: tokens, laTokens: la}
			got, err := tc.call(NewNotificationService(repo))
			require.NoError(t, err)
			require.Equal(t, []string{tc.wantCall}, repo.calls)
			require.Equal(t, []any{tc.wantArgs}, repo.args)
			if tc.want != nil {
				require.Equal(t, tc.want, got)
			}
		})
		t.Run(tc.name+" propagates repository errors", func(t *testing.T) {
			repo := &recordingRepo{err: boom}
			_, err := tc.call(NewNotificationService(repo))
			require.ErrorIs(t, err, boom)
		})
	}
}
