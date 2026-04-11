package infrastructure

import (
	"context"
	"database/sql"
	"errors"

	"tsb-service/internal/modules/hubrise_webshop/domain"
	"tsb-service/pkg/db"
)

type CatalogSyncStateRepository struct {
	pool *db.DBPool
}

func NewCatalogSyncStateRepository(pool *db.DBPool) *CatalogSyncStateRepository {
	return &CatalogSyncStateRepository{pool: pool}
}

var _ domain.CatalogSyncStateRepository = (*CatalogSyncStateRepository)(nil)

func (r *CatalogSyncStateRepository) Get(ctx context.Context, clientName string) (*domain.CatalogSyncState, error) {
	const q = `
		SELECT client_name, last_pushed_version, last_pushed_at, last_push_status, last_error
		FROM hubrise_catalog_sync_state
		WHERE client_name = $1
	`
	var s domain.CatalogSyncState
	if err := r.pool.ForContext(ctx).GetContext(ctx, &s, q, clientName); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &s, nil
}

func (r *CatalogSyncStateRepository) Upsert(ctx context.Context, s *domain.CatalogSyncState) error {
	const q = `
		INSERT INTO hubrise_catalog_sync_state
			(client_name, last_pushed_version, last_pushed_at, last_push_status, last_error)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (client_name) DO UPDATE SET
			last_pushed_version = EXCLUDED.last_pushed_version,
			last_pushed_at = EXCLUDED.last_pushed_at,
			last_push_status = EXCLUDED.last_push_status,
			last_error = EXCLUDED.last_error
	`
	_, err := r.pool.ForContext(ctx).ExecContext(ctx, q,
		s.ClientName, s.LastPushedVersion, s.LastPushedAt, s.LastPushStatus, s.LastError,
	)
	return err
}
