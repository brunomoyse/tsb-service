package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// DevicePushToken represents a device-level push token (APNs or FCM).
type DevicePushToken struct {
	ID          uuid.UUID `db:"id"`
	UserID      uuid.UUID `db:"user_id"`
	DeviceToken string    `db:"device_token"`
	Platform    string    `db:"platform"` // "ios" or "android"
	Role        string    `db:"role"`     // "user" or "admin"
	CreatedAt   time.Time `db:"created_at"`
	UpdatedAt   time.Time `db:"updated_at"`
}

// NotificationRepository defines the data access interface for push tokens.
type NotificationRepository interface {
	// Device push tokens (per-user)
	SaveDeviceToken(ctx context.Context, userID uuid.UUID, deviceToken, platform, role string) error
	FindDeviceTokensByUserID(ctx context.Context, userID uuid.UUID) ([]DevicePushToken, error)
	FindDeviceTokensByRole(ctx context.Context, role string) ([]DevicePushToken, error)
	DeleteDeviceToken(ctx context.Context, userID uuid.UUID, deviceToken string) error
}
