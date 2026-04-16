package infrastructure

import (
	"context"
	"time"

	"github.com/google/uuid"

	"tsb-service/internal/modules/pos/domain"
	"tsb-service/pkg/db"
)

type DeviceRepository struct {
	pool *db.DBPool
}

func NewDeviceRepository(pool *db.DBPool) domain.DeviceRepository {
	return &DeviceRepository{pool: pool}
}

func (r *DeviceRepository) FindBySerial(ctx context.Context, serial string) (*domain.Device, error) {
	var d domain.Device
	const q = `SELECT * FROM pos_devices WHERE serial_number = $1`
	if err := r.pool.ForContext(ctx).GetContext(ctx, &d, q, serial); err != nil {
		return nil, err
	}
	return &d, nil
}

func (r *DeviceRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Device, error) {
	var d domain.Device
	const q = `SELECT * FROM pos_devices WHERE id = $1`
	if err := r.pool.ForContext(ctx).GetContext(ctx, &d, q, id); err != nil {
		return nil, err
	}
	return &d, nil
}

func (r *DeviceRepository) Insert(ctx context.Context, d *domain.Device) error {
	const q = `
		INSERT INTO pos_devices (serial_number, device_secret_hash, label, registered_by)
		VALUES ($1, $2, $3, $4)
		RETURNING id, registered_at`
	return r.pool.ForContext(ctx).QueryRowxContext(
		ctx, q, d.SerialNumber, d.DeviceSecretHash, d.Label, d.RegisteredBy,
	).Scan(&d.ID, &d.RegisteredAt)
}

func (r *DeviceRepository) RotateSecret(ctx context.Context, id uuid.UUID, newHash, label string, registeredBy uuid.UUID) error {
	const q = `
		UPDATE pos_devices
		SET device_secret_hash = $2,
		    label              = $3,
		    registered_by      = $4,
		    registered_at      = now(),
		    revoked_at         = NULL
		WHERE id = $1`
	_, err := r.pool.ForContext(ctx).ExecContext(ctx, q, id, newHash, label, registeredBy)
	return err
}

func (r *DeviceRepository) TouchLastSeen(ctx context.Context, id uuid.UUID) error {
	const q = `UPDATE pos_devices SET last_seen_at = now() WHERE id = $1`
	_, err := r.pool.ForContext(ctx).ExecContext(ctx, q, id)
	return err
}

func (r *DeviceRepository) Revoke(ctx context.Context, id uuid.UUID) error {
	const q = `UPDATE pos_devices SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`
	_, err := r.pool.ForContext(ctx).ExecContext(ctx, q, id)
	return err
}

func (r *DeviceRepository) ListActive(ctx context.Context) ([]domain.Device, error) {
	const q = `SELECT * FROM pos_devices ORDER BY registered_at DESC`
	var out []domain.Device
	if err := r.pool.ForContext(ctx).SelectContext(ctx, &out, q); err != nil {
		return nil, err
	}
	return out, nil
}

// ---- Refresh tokens

type RefreshTokenRepository struct {
	pool *db.DBPool
}

func NewRefreshTokenRepository(pool *db.DBPool) domain.RefreshTokenRepository {
	return &RefreshTokenRepository{pool: pool}
}

func (r *RefreshTokenRepository) Insert(ctx context.Context, t *domain.RefreshToken) error {
	const q = `
		INSERT INTO pos_refresh_tokens (token_hash, user_id, device_id, expires_at)
		VALUES ($1, $2, $3, $4)
		RETURNING issued_at`
	return r.pool.ForContext(ctx).QueryRowxContext(
		ctx, q, t.TokenHash, t.UserID, t.DeviceID, t.ExpiresAt,
	).Scan(&t.IssuedAt)
}

func (r *RefreshTokenRepository) FindByHash(ctx context.Context, hash string) (*domain.RefreshToken, error) {
	var t domain.RefreshToken
	const q = `SELECT * FROM pos_refresh_tokens WHERE token_hash = $1`
	if err := r.pool.ForContext(ctx).GetContext(ctx, &t, q, hash); err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *RefreshTokenRepository) Revoke(ctx context.Context, hash string) error {
	const q = `UPDATE pos_refresh_tokens SET revoked_at = now() WHERE token_hash = $1 AND revoked_at IS NULL`
	_, err := r.pool.ForContext(ctx).ExecContext(ctx, q, hash)
	return err
}

func (r *RefreshTokenRepository) RevokeAllForUser(ctx context.Context, userID uuid.UUID) error {
	const q = `UPDATE pos_refresh_tokens SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`
	_, err := r.pool.ForContext(ctx).ExecContext(ctx, q, userID)
	return err
}

// ---- POS user repository

type PosUserRepository struct {
	pool *db.DBPool
}

func NewPosUserRepository(pool *db.DBPool) domain.PosUserRepository {
	return &PosUserRepository{pool: pool}
}

func (r *PosUserRepository) FindByRRN(ctx context.Context, rrn string) (*domain.PosUser, error) {
	var u domain.PosUser
	const q = `
		SELECT id, first_name, last_name, email, rrn, pin_hash,
		       failed_pin_attempts, pin_locked_until
		FROM users
		WHERE rrn = $1`
	if err := r.pool.ForContext(ctx).GetContext(ctx, &u, q, rrn); err != nil {
		return nil, err
	}
	return &u, nil
}

func (r *PosUserRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.PosUser, error) {
	var u domain.PosUser
	const q = `
		SELECT id, first_name, last_name, email, rrn, pin_hash,
		       failed_pin_attempts, pin_locked_until
		FROM users
		WHERE id = $1`
	if err := r.pool.ForContext(ctx).GetContext(ctx, &u, q, id); err != nil {
		return nil, err
	}
	return &u, nil
}

func (r *PosUserRepository) UpdateRRN(ctx context.Context, userID uuid.UUID, rrn string) error {
	const q = `UPDATE users SET rrn = $2, updated_at = now() WHERE id = $1`
	_, err := r.pool.ForContext(ctx).ExecContext(ctx, q, userID, rrn)
	return err
}

func (r *PosUserRepository) UpdatePinHash(ctx context.Context, userID uuid.UUID, pinHash string) error {
	const q = `
		UPDATE users
		SET pin_hash = $2,
		    pin_updated_at = now(),
		    failed_pin_attempts = 0,
		    pin_locked_until = NULL,
		    updated_at = now()
		WHERE id = $1`
	_, err := r.pool.ForContext(ctx).ExecContext(ctx, q, userID, pinHash)
	return err
}

func (r *PosUserRepository) IncrementFailedAttempts(ctx context.Context, userID uuid.UUID, lockedUntil *time.Time) error {
	const q = `
		UPDATE users
		SET failed_pin_attempts = failed_pin_attempts + 1,
		    pin_locked_until = COALESCE($2, pin_locked_until)
		WHERE id = $1`
	_, err := r.pool.ForContext(ctx).ExecContext(ctx, q, userID, lockedUntil)
	return err
}

func (r *PosUserRepository) ResetFailedAttempts(ctx context.Context, userID uuid.UUID) error {
	const q = `UPDATE users SET failed_pin_attempts = 0, pin_locked_until = NULL WHERE id = $1`
	_, err := r.pool.ForContext(ctx).ExecContext(ctx, q, userID)
	return err
}

// ---- POS staff repository

type StaffRepository struct {
	pool *db.DBPool
}

func NewStaffRepository(pool *db.DBPool) domain.StaffRepository {
	return &StaffRepository{pool: pool}
}

func (r *StaffRepository) Insert(ctx context.Context, s *domain.Staff) error {
	const q = `
		INSERT INTO pos_staff (display_name, rrn_hash, pin_hash)
		VALUES ($1, $2, $3)
		RETURNING id, created_at`
	return r.pool.ForContext(ctx).QueryRowxContext(ctx, q, s.DisplayName, s.RRNHash, s.PinHash).Scan(&s.ID, &s.CreatedAt)
}

func (r *StaffRepository) FindByRRNHash(ctx context.Context, rrnHash string) (*domain.Staff, error) {
	var s domain.Staff
	const q = `SELECT * FROM pos_staff WHERE rrn_hash = $1`
	if err := r.pool.ForContext(ctx).GetContext(ctx, &s, q, rrnHash); err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *StaffRepository) List(ctx context.Context) ([]domain.Staff, error) {
	const q = `SELECT * FROM pos_staff ORDER BY display_name`
	var out []domain.Staff
	if err := r.pool.ForContext(ctx).SelectContext(ctx, &out, q); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *StaffRepository) Delete(ctx context.Context, id uuid.UUID) error {
	const q = `DELETE FROM pos_staff WHERE id = $1`
	_, err := r.pool.ForContext(ctx).ExecContext(ctx, q, id)
	return err
}

func (r *StaffRepository) IncrementFailedAttempts(ctx context.Context, id uuid.UUID, lockedUntil *time.Time) error {
	const q = `
		UPDATE pos_staff
		SET failed_pin_attempts = failed_pin_attempts + 1,
		    pin_locked_until = COALESCE($2, pin_locked_until)
		WHERE id = $1`
	_, err := r.pool.ForContext(ctx).ExecContext(ctx, q, id, lockedUntil)
	return err
}

func (r *StaffRepository) ResetFailedAttempts(ctx context.Context, id uuid.UUID) error {
	const q = `UPDATE pos_staff SET failed_pin_attempts = 0, pin_locked_until = NULL WHERE id = $1`
	_, err := r.pool.ForContext(ctx).ExecContext(ctx, q, id)
	return err
}
