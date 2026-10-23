// Package infrastructure implements the HubRise repositories with sqlx on the admin pool. The
// hubrise tables get no per-role GRANTs, so only the admin connection reaches them.
package infrastructure

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jmoiron/sqlx"

	"tsb-service/internal/modules/hubrise/domain"
)

// store is the thin query layer shared by the repositories: it wraps errors with the name of the
// operation and maps "no rows" to domain.ErrNotFound.
type store struct {
	db *sqlx.DB
}

func (s store) exec(ctx context.Context, what, query string, args ...any) error {
	_, err := s.execN(ctx, what, query, args...)
	return err
}

// execN runs a statement and returns the number of rows it changed.
func (s store) execN(ctx context.Context, what, query string, args ...any) (int64, error) {
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("hubrise: %s: %w", what, err)
	}
	n, _ := res.RowsAffected() // lib/pq always reports it
	return n, nil
}

// get scans one row into dest, or returns domain.ErrNotFound.
func (s store) get(ctx context.Context, what string, dest any, query string, args ...any) error {
	if err := s.db.GetContext(ctx, dest, query, args...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrNotFound
		}
		return fmt.Errorf("hubrise: %s: %w", what, err)
	}
	return nil
}

// sel scans all rows into dest (a pointer to a slice).
func (s store) sel(ctx context.Context, what string, dest any, query string, args ...any) error {
	if err := s.db.SelectContext(ctx, dest, query, args...); err != nil {
		return fmt.Errorf("hubrise: %s: %w", what, err)
	}
	return nil
}

// affected reports whether a statement changed at least one row.
func (s store) affected(ctx context.Context, what, query string, args ...any) (bool, error) {
	n, err := s.execN(ctx, what, query, args...)
	return n > 0, err
}
