package infrastructure

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/pkg/db"
)

func TestPaymentRepository_DatabaseFailures(t *testing.T) {
	t.Run("a closed connection surfaces a wrapped error from every method", func(t *testing.T) {
		conn, err := sqlx.Open("postgres", "host=127.0.0.1 port=1 user=x dbname=x sslmode=disable")
		require.NoError(t, err)
		require.NoError(t, conn.Close())
		repo := NewPaymentRepository(&db.DBPool{Customer: conn, Admin: conn})
		ctx := t.Context()

		assert.ErrorContains(t, repo.Save(ctx, samplePayment(uuid.New(), "tr_x")), "failed to begin transaction")
		assert.ErrorContains(t, repo.MarkAsRefund(ctx, "tr_x", decimal.NewFromInt(1)), "failed to mark payment as refunded")
		assert.ErrorContains(t, repo.WithPaymentLock(ctx, "tr_x", func(context.Context) error {
			t.Fatal("the critical section must not run without the lock")
			return nil
		}), "acquire connection for payment lock")
		_, err = repo.FindByOrderIDs(ctx, []string{uuid.NewString()})
		assert.ErrorContains(t, err, "failed to find payments by order IDs")
	})

	t.Run("Save reports a failed commit and stores nothing", func(t *testing.T) {
		repo, newOrder, tdb := newPaymentRepoDB(t)
		_, err := tdb.DB.ExecContext(t.Context(), `CREATE FUNCTION fail_it() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'forced failure'; END $$`)
		require.NoError(t, err)
		_, err = tdb.DB.ExecContext(t.Context(), `CREATE CONSTRAINT TRIGGER fail_trg AFTER INSERT ON mollie_payments DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_it()`)
		require.NoError(t, err)

		err = repo.Save(t.Context(), samplePayment(newOrder(t), "tr_commit"))

		require.ErrorContains(t, err, "failed to commit transaction")
		_, err = repo.FindByExternalID(t.Context(), "tr_commit")
		require.Error(t, err, "a payment whose transaction failed to commit must not exist")
	})
}
