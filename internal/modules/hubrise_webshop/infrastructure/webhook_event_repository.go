package infrastructure

import (
	"context"

	"tsb-service/internal/modules/hubrise_webshop/domain"
	"tsb-service/pkg/db"
)

type WebhookEventRepository struct {
	pool *db.DBPool
}

func NewWebhookEventRepository(pool *db.DBPool) *WebhookEventRepository {
	return &WebhookEventRepository{pool: pool}
}

var _ domain.WebhookEventRepository = (*WebhookEventRepository)(nil)

// Insert is idempotent: returns (true, nil) if the row was newly
// inserted, (false, nil) if the id already existed.
func (r *WebhookEventRepository) Insert(
	ctx context.Context,
	id, clientName, resourceType, eventType string,
	payload []byte,
) (bool, error) {
	const q = `
		INSERT INTO hubrise_webhook_events (id, client_name, resource_type, event_type, payload)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (id) DO NOTHING
	`
	res, err := r.pool.ForContext(ctx).ExecContext(ctx, q, id, clientName, resourceType, eventType, payload)
	if err != nil {
		return false, err
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return rowsAffected > 0, nil
}

func (r *WebhookEventRepository) MarkProcessed(ctx context.Context, id string) error {
	_, err := r.pool.ForContext(ctx).ExecContext(ctx,
		"UPDATE hubrise_webhook_events SET processed_at = now() WHERE id = $1", id)
	return err
}

func (r *WebhookEventRepository) MarkFailed(ctx context.Context, id, errMsg string) error {
	_, err := r.pool.ForContext(ctx).ExecContext(ctx,
		"UPDATE hubrise_webhook_events SET error_msg = $1 WHERE id = $2", errMsg, id)
	return err
}
