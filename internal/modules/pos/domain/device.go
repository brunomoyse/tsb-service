package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Device is an enrolled POS handheld. Its shared HMAC secret is only kept as a
// SHA-256 hash; the plaintext is returned once at enrollment and stored on the
// device's EncryptedSharedPreferences.
type Device struct {
	ID               uuid.UUID  `db:"id"`
	SerialNumber     string     `db:"serial_number"`
	DeviceSecretHash string     `db:"device_secret_hash"`
	Label            string     `db:"label"`
	RegisteredBy     uuid.UUID  `db:"registered_by"`
	RegisteredAt     time.Time  `db:"registered_at"`
	LastSeenAt       *time.Time `db:"last_seen_at"`
	RevokedAt        *time.Time `db:"revoked_at"`
}

// RefreshToken is the server-side record for an opaque refresh token; the
// primary key is the SHA-256 of the token value so a DB leak cannot be used
// to mint access tokens.
type RefreshToken struct {
	TokenHash string     `db:"token_hash"`
	UserID    uuid.UUID  `db:"user_id"`
	DeviceID  uuid.UUID  `db:"device_id"`
	IssuedAt  time.Time  `db:"issued_at"`
	ExpiresAt time.Time  `db:"expires_at"`
	RevokedAt *time.Time `db:"revoked_at"`
}

// DeviceRepository is a narrow interface used by the service layer.
type DeviceRepository interface {
	FindBySerial(ctx context.Context, serial string) (*Device, error)
	FindByID(ctx context.Context, id uuid.UUID) (*Device, error)
	Insert(ctx context.Context, d *Device) error
	RotateSecret(ctx context.Context, id uuid.UUID, newHash, label string, registeredBy uuid.UUID) error
	TouchLastSeen(ctx context.Context, id uuid.UUID) error
	Revoke(ctx context.Context, id uuid.UUID) error
	ListActive(ctx context.Context) ([]Device, error)
}

type RefreshTokenRepository interface {
	Insert(ctx context.Context, t *RefreshToken) error
	FindByHash(ctx context.Context, hash string) (*RefreshToken, error)
	Revoke(ctx context.Context, hash string) error
	RevokeAllForUser(ctx context.Context, userID uuid.UUID) error
}

// PosUserRepository isolates the handful of user-table operations the POS
// module needs without reaching into the main user repository (which has many
// unrelated methods).
type PosUserRepository interface {
	FindByRRN(ctx context.Context, rrn string) (*PosUser, error)
	FindByID(ctx context.Context, id uuid.UUID) (*PosUser, error)
	UpdateRRN(ctx context.Context, userID uuid.UUID, rrn string) error
	UpdatePinHash(ctx context.Context, userID uuid.UUID, pinHash string) error
	IncrementFailedAttempts(ctx context.Context, userID uuid.UUID, lockedUntil *time.Time) error
	ResetFailedAttempts(ctx context.Context, userID uuid.UUID) error
}

// PosUser is the minimal projection used by the POS flow.
type PosUser struct {
	ID                  uuid.UUID  `db:"id"`
	FirstName           string     `db:"first_name"`
	LastName            string     `db:"last_name"`
	Email               string     `db:"email"`
	RRN                 *string    `db:"rrn"`
	PinHash             *string    `db:"pin_hash"`
	FailedPinAttempts   int        `db:"failed_pin_attempts"`
	PinLockedUntil      *time.Time `db:"pin_locked_until"`
	IsAdmin             bool       `db:"-"` // derived from Zitadel roles elsewhere
}
