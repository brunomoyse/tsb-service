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

// ──────────────────── Live Activity Tokens ────────────────────

func (r *notificationRepository) SaveLiveActivityToken(ctx context.Context, orderID uuid.UUID, pushToken string) error {
	query := `
		INSERT INTO live_activity_tokens (order_id, push_token)
		VALUES ($1, $2)
		ON CONFLICT (order_id, push_token) DO UPDATE SET created_at = now()
	`
	_, err := r.pool.ForContext(ctx).ExecContext(ctx, query, orderID, pushToken)
	if err != nil {
		return fmt.Errorf("save live activity token: %w", err)
	}
	return nil
}

func (r *notificationRepository) FindLiveActivityTokensByOrderID(ctx context.Context, orderID uuid.UUID) ([]string, error) {
	query := `
		SELECT push_token FROM live_activity_tokens
		WHERE order_id = $1 AND expires_at > now()
	`
	var tokens []string
	if err := r.pool.ForContext(ctx).SelectContext(ctx, &tokens, query, orderID); err != nil {
		return nil, fmt.Errorf("find live activity tokens: %w", err)
	}
	return tokens, nil
}

func (r *notificationRepository) DeleteLiveActivityTokensByOrderID(ctx context.Context, orderID uuid.UUID) error {
	query := `DELETE FROM live_activity_tokens WHERE order_id = $1`
	_, err := r.pool.ForContext(ctx).ExecContext(ctx, query, orderID)
	if err != nil {
		return fmt.Errorf("delete live activity tokens: %w", err)
	}
	return nil
}

func (r *notificationRepository) DeleteExpiredLiveActivityTokens(ctx context.Context) error {
	query := `DELETE FROM live_activity_tokens WHERE expires_at <= now()`
	_, err := r.pool.ForContext(ctx).ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("delete expired live activity tokens: %w", err)
	}
	return nil
}

// ──────────────────── Device Push Tokens ────────────────────

func (r *notificationRepository) SaveDeviceToken(ctx context.Context, userID uuid.UUID, deviceToken, platform string) error {
	query := `
		INSERT INTO device_push_tokens (user_id, device_token, platform)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id, device_token) DO UPDATE SET
			platform = EXCLUDED.platform,
			updated_at = now()
	`
	_, err := r.pool.ForContext(ctx).ExecContext(ctx, query, userID, deviceToken, platform)
	if err != nil {
		return fmt.Errorf("save device token: %w", err)
	}
	return nil
}

func (r *notificationRepository) FindDeviceTokensByUserID(ctx context.Context, userID uuid.UUID) ([]domain.DevicePushToken, error) {
	query := `
		SELECT id, user_id, device_token, platform, created_at, updated_at
		FROM device_push_tokens
		WHERE user_id = $1
	`
	var tokens []domain.DevicePushToken
	if err := r.pool.ForContext(ctx).SelectContext(ctx, &tokens, query, userID); err != nil {
		return nil, fmt.Errorf("find device tokens: %w", err)
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
