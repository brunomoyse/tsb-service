package infrastructure

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"tsb-service/internal/modules/notification/domain"
	"tsb-service/pkg/db"
)

type notificationRepository struct {
	pool *db.DBPool
}

// NewNotificationRepository constructs a NotificationRepository backed by PostgreSQL.
func NewNotificationRepository(pool *db.DBPool) domain.NotificationRepository {
	return &notificationRepository{pool: pool}
}

// ──────────────────── Device Push Tokens ────────────────────

func (r *notificationRepository) SaveDeviceToken(ctx context.Context, userID uuid.UUID, deviceToken, platform, role string) error {
	query := `
		INSERT INTO device_push_tokens (user_id, device_token, platform, role)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id, device_token) DO UPDATE SET
			platform = EXCLUDED.platform,
			role = EXCLUDED.role,
			updated_at = now()
	`
	_, err := r.pool.ForContext(ctx).ExecContext(ctx, query, userID, deviceToken, platform, role)
	if err != nil {
		return fmt.Errorf("save device token: %w", err)
	}
	return nil
}

func (r *notificationRepository) FindDeviceTokensByUserID(ctx context.Context, userID uuid.UUID) ([]domain.DevicePushToken, error) {
	query := `
		SELECT id, user_id, device_token, platform, role, created_at, updated_at
		FROM device_push_tokens
		WHERE user_id = $1
	`
	var tokens []domain.DevicePushToken
	if err := r.pool.ForContext(ctx).SelectContext(ctx, &tokens, query, userID); err != nil {
		return nil, fmt.Errorf("find device tokens: %w", err)
	}
	return tokens, nil
}

func (r *notificationRepository) FindDeviceTokensByRole(ctx context.Context, role string) ([]domain.DevicePushToken, error) {
	query := `
		SELECT id, user_id, device_token, platform, role, created_at, updated_at
		FROM device_push_tokens
		WHERE role = $1
	`
	var tokens []domain.DevicePushToken
	if err := r.pool.ForContext(ctx).SelectContext(ctx, &tokens, query, role); err != nil {
		return nil, fmt.Errorf("find device tokens by role: %w", err)
	}
	return tokens, nil
}

func (r *notificationRepository) DeleteDeviceToken(ctx context.Context, userID uuid.UUID, deviceToken string) error {
	query := `DELETE FROM device_push_tokens WHERE user_id = $1 AND device_token = $2`
	_, err := r.pool.ForContext(ctx).ExecContext(ctx, query, userID, deviceToken)
	if err != nil {
		return fmt.Errorf("delete device token: %w", err)
	}
	return nil
}
