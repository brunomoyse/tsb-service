package infrastructure

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/testhelpers"
	"tsb-service/pkg/db"
)

func TestPaymentRepository_DatabaseFailures(t *testing.T) {
	t.Run("a closed connection surfaces a wrapped error from every method", func(t *testing.T) {
		conn := testhelpers.ClosedDB(t)
		repo := NewPaymentRepository(&db.DBPool{Customer: conn, Admin: conn})
		ctx := t.Context()

		assert.ErrorContains(t, repo.Save(ctx, samplePayment(uuid.New(), "tr_x")), "failed to begin transaction")
		assert.ErrorContains(t, repo.MarkAsRefund(ctx, "tr_x", decimal.NewFromInt(1)), "failed to mark payment as refunded")
		assert.ErrorContains(t, repo.WithPaymentLock(ctx, "tr_x", func(context.Context) error {
			t.Fatal("the critical section must not run without the lock")
			return nil
		}), "acquire connection for payment lock")
		_, err := repo.FindByOrderIDs(ctx, []string{uuid.NewString()})
		assert.ErrorContains(t, err, "failed to find payments by order IDs")
	})

	t.Run("Save reports a failed commit and stores nothing", func(t *testing.T) {
		repo, newOrder, tdb := newPaymentRepoDB(t)
		testhelpers.FailCommitTrigger(t, tdb.DB, "mollie_payments", "INSERT")

		err := repo.Save(t.Context(), samplePayment(newOrder(t), "tr_commit"))

		require.ErrorContains(t, err, "failed to commit transaction")
		_, err = repo.FindByExternalID(t.Context(), "tr_commit")
		require.Error(t, err, "a payment whose transaction failed to commit must not exist")
	})
}
