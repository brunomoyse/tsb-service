package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Device is the single enrolled POS handheld. Its shared HMAC secret is only
// kept as a SHA-256 hash; the plaintext lives in the device's BuildConfig
// (baked at APK build time).
type Device struct {
	ID                uuid.UUID  `db:"id"`
	SerialNumber      string     `db:"serial_number"`
	DeviceSecretHash  string     `db:"device_secret_hash"`
	Label             string     `db:"label"`
	RegisteredBy      *uuid.UUID `db:"registered_by"`
	RegisteredAt      time.Time  `db:"registered_at"`
	LastSeenAt        *time.Time `db:"last_seen_at"`
	RevokedAt         *time.Time `db:"revoked_at"`
	FCMToken          *string    `db:"fcm_token"`
	FCMTokenUpdatedAt *time.Time `db:"fcm_token_updated_at"`
}

// DeviceRepository is a narrow interface used by the service layer.
type DeviceRepository interface {
	FindByID(ctx context.Context, id uuid.UUID) (*Device, error)
	TouchLastSeen(ctx context.Context, id uuid.UUID) error
	UpdateFCMToken(ctx context.Context, deviceID uuid.UUID, token string) error
	FindActiveFCMTokens(ctx context.Context) ([]string, error)
}
