package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// LiveActivityToken represents an APNs push token for an iOS Live Activity.
type LiveActivityToken struct {
	ID        uuid.UUID `db:"id"`
	OrderID   uuid.UUID `db:"order_id"`
	PushToken string    `db:"push_token"`
	CreatedAt time.Time `db:"created_at"`
	ExpiresAt time.Time `db:"expires_at"`
}

// DevicePushToken represents a device-level push token (APNs or FCM).
type DevicePushToken struct {
	ID          uuid.UUID `db:"id"`
	UserID      uuid.UUID `db:"user_id"`
	DeviceToken string    `db:"device_token"`
	Platform    string    `db:"platform"` // "ios" or "android"
	CreatedAt   time.Time `db:"created_at"`
	UpdatedAt   time.Time `db:"updated_at"`
}

// NotificationRepository defines the data access interface for push tokens.
type NotificationRepository interface {
	// Live Activity tokens (per-order)
	SaveLiveActivityToken(ctx context.Context, orderID uuid.UUID, pushToken string) error
	FindLiveActivityTokensByOrderID(ctx context.Context, orderID uuid.UUID) ([]string, error)
	DeleteLiveActivityTokensByOrderID(ctx context.Context, orderID uuid.UUID) error
	DeleteExpiredLiveActivityTokens(ctx context.Context) error

	// Device push tokens (per-user)
	SaveDeviceToken(ctx context.Context, userID uuid.UUID, deviceToken, platform string) error
	FindDeviceTokensByUserID(ctx context.Context, userID uuid.UUID) ([]DevicePushToken, error)
	DeleteDeviceToken(ctx context.Context, userID uuid.UUID, deviceToken string) error
}
