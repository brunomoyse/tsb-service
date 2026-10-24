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
	db *sqlx.DB // nil on a store bound to a transaction
	q  queryer
}

// queryer is what both *sqlx.DB and *sqlx.Tx offer, so the same repository code runs on the pool
// or inside a transaction.
type queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	GetContext(ctx context.Context, dest any, query string, args ...any) error
	SelectContext(ctx context.Context, dest any, query string, args ...any) error
}

func newStore(db *sqlx.DB) store { return store{db: db, q: db} }

func newTxStore(tx *sqlx.Tx) store { return store{q: tx} }

func (s store) exec(ctx context.Context, what, query string, args ...any) error {
	_, err := s.execN(ctx, what, query, args...)
	return err
}

// execN runs a statement and returns the number of rows it changed.
func (s store) execN(ctx context.Context, what, query string, args ...any) (int64, error) {
	res, err := s.q.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("hubrise: %s: %w", what, err)
	}
	n, _ := res.RowsAffected() // lib/pq always reports it
	return n, nil
}

// get scans one row into dest, or returns domain.ErrNotFound.
func (s store) get(ctx context.Context, what string, dest any, query string, args ...any) error {
	if err := s.q.GetContext(ctx, dest, query, args...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrNotFound
		}
		return fmt.Errorf("hubrise: %s: %w", what, err)
	}
	return nil
}

// sel scans all rows into dest (a pointer to a slice).
func (s store) sel(ctx context.Context, what string, dest any, query string, args ...any) error {
	if err := s.q.SelectContext(ctx, dest, query, args...); err != nil {
		return fmt.Errorf("hubrise: %s: %w", what, err)
	}
	return nil
}

// affected reports whether a statement changed at least one row.
func (s store) affected(ctx context.Context, what, query string, args ...any) (bool, error) {
	n, err := s.execN(ctx, what, query, args...)
	return n > 0, err
}

// isNoRows reports whether err is "no rows in result set".
func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// inTx runs fn on a store bound to one transaction and commits it, or rolls everything back when fn
// fails. On a store that is already bound to a transaction fn simply runs in it.
func (s store) inTx(ctx context.Context, what string, fn func(tx store) error) error {
	if s.db == nil {
		return fn(s)
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("hubrise: %s: %w", what, err)
	}
	defer func() { _ = tx.Rollback() }() // a no-op once committed
	if err := fn(newTxStore(tx)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("hubrise: %s: commit: %w", what, err)
	}
	return nil
}
