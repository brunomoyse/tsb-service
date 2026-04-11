package infrastructure

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"tsb-service/internal/modules/hubrise_webshop/domain"
	"tsb-service/pkg/db"
)

type ConnectionRepository struct {
	pool *db.DBPool
}

func NewConnectionRepository(pool *db.DBPool) *ConnectionRepository {
	return &ConnectionRepository{pool: pool}
}

var _ domain.ConnectionRepository = (*ConnectionRepository)(nil)

func (r *ConnectionRepository) GetByClient(ctx context.Context, clientName string) (*domain.Connection, error) {
	const q = `
		SELECT id, client_name, location_id, account_id, catalog_id, customer_list_id,
		       access_token, scope, created_at, updated_at
		FROM hubrise_connections
		WHERE client_name = $1
	`
	var row domain.Connection
	if err := r.pool.ForContext(ctx).GetContext(ctx, &row, q, clientName); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

func (r *ConnectionRepository) Upsert(ctx context.Context, c *domain.Connection) error {
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	c.UpdatedAt = time.Now()
	const q = `
		INSERT INTO hubrise_connections
			(id, client_name, location_id, account_id, catalog_id, customer_list_id,
			 access_token, scope, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (client_name) DO UPDATE SET
			location_id = EXCLUDED.location_id,
			account_id = EXCLUDED.account_id,
			catalog_id = EXCLUDED.catalog_id,
			customer_list_id = EXCLUDED.customer_list_id,
			access_token = EXCLUDED.access_token,
			scope = EXCLUDED.scope,
			updated_at = EXCLUDED.updated_at
	`
	_, err := r.pool.ForContext(ctx).ExecContext(ctx, q,
		c.ID, c.ClientName, c.LocationID, c.AccountID, c.CatalogID, c.CustomerListID,
		c.AccessToken, c.Scope, c.UpdatedAt,
	)
	return err
}

func (r *ConnectionRepository) Delete(ctx context.Context, clientName string) error {
	_, err := r.pool.ForContext(ctx).ExecContext(ctx, "DELETE FROM hubrise_connections WHERE client_name = $1", clientName)
	return err
}

// Compile-time check that we don't accidentally shadow sqlx.
var _ = sqlx.DB{}
