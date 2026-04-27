package infrastructure

import (
	"context"

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

func (r *DeviceRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Device, error) {
	var d domain.Device
	const q = `SELECT * FROM pos_devices WHERE id = $1`
	if err := r.pool.ForContext(ctx).GetContext(ctx, &d, q, id); err != nil {
		return nil, err
	}
	return &d, nil
}

func (r *DeviceRepository) TouchLastSeen(ctx context.Context, id uuid.UUID) error {
	const q = `UPDATE pos_devices SET last_seen_at = now() WHERE id = $1`
	_, err := r.pool.ForContext(ctx).ExecContext(ctx, q, id)
	return err
}

func (r *DeviceRepository) UpdateFCMToken(ctx context.Context, deviceID uuid.UUID, token string) error {
	const q = `UPDATE pos_devices SET fcm_token = $2, fcm_token_updated_at = now() WHERE id = $1 AND revoked_at IS NULL`
	_, err := r.pool.ForContext(ctx).ExecContext(ctx, q, deviceID, token)
	return err
}

func (r *DeviceRepository) FindActiveFCMTokens(ctx context.Context) ([]string, error) {
	const q = `SELECT fcm_token FROM pos_devices WHERE revoked_at IS NULL AND fcm_token IS NOT NULL`
	var tokens []string
	if err := r.pool.ForContext(ctx).SelectContext(ctx, &tokens, q); err != nil {
		return nil, err
	}
	return tokens, nil
}
