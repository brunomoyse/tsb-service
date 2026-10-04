package repository

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/testhelpers"
	"tsb-service/internal/modules/order/domain"
)

// TestOrderRepositoryDatabaseFailures covers the branches taken when the database misbehaves:
// a closed connection for every entry point, and triggers that fail a later statement of a
// multi-statement transaction.
func TestOrderRepositoryDatabaseFailures(t *testing.T) {
	ctx := t.Context()
	repo := closedRepo(t)
	id := uuid.New()
	now := time.Now()
	one := 1
	kind := "PICKUP"

	t.Run("closed connection surfaces a wrapped error from every method", func(t *testing.T) {
		_, _, err := repo.Save(ctx, &domain.Order{}, &[]domain.OrderProductRaw{})
		assert.ErrorContains(t, err, "failed to begin transaction")
		assert.ErrorContains(t, repo.Update(ctx, &domain.Order{ID: id}), "failed to update order")
		_, err = repo.UpdateActiveOrdersLanguage(ctx, id, "fr")
		assert.ErrorContains(t, err, "failed to update active orders language")
		_, err = repo.HasActiveCouponOrder(ctx, id)
		assert.ErrorContains(t, err, "failed to check active coupon order")
		_, _, err = repo.FindByID(ctx, id)
		assert.ErrorContains(t, err, "failed to query order")
		_, err = repo.FindPaginated(ctx, 1, 10, nil)
		assert.ErrorContains(t, err, "failed to query orders")
		_, _, err = repo.FindFiltered(ctx, domain.OrderHistoryFilter{})
		assert.ErrorContains(t, err, "failed to query order history summary")
		_, err = repo.FindByOrderIDs(ctx, []string{id.String()})
		assert.ErrorContains(t, err, "failed to select order products")
		_, err = repo.FindByUserIDs(ctx, []string{id.String()})
		assert.Error(t, err)
		assert.ErrorContains(t, repo.InsertStatusHistory(ctx, id, domain.OrderStatusPending), "failed to insert status history")
		_, err = repo.CancelStaleTestOrders(ctx, time.Hour)
		assert.ErrorContains(t, err, "failed to cancel stale test orders")
		_, err = repo.FindStatusHistoryByOrderID(ctx, id)
		assert.ErrorContains(t, err, "failed to query status history")
		assert.ErrorContains(t, repo.DeleteOrder(ctx, id), "failed to begin transaction")
		_, err = repo.GetCustomerStats(ctx, &now, &now, &kind, &one)
		assert.ErrorContains(t, err, "failed to query customer stats")
	})

	e := newEnv(t)
	salmon := e.fx.SalmonSushi.ID

	failingTrigger := func(t *testing.T, table, event string, deferred bool) {
		t.Helper()
		testhelpers.FailTrigger(t, e.tdb.DB, table, "BEFORE", event, deferred)
	}

	t.Run("Save reports a failed commit and leaves nothing behind", func(t *testing.T) {
		failingTrigger(t, "orders", "INSERT", true)
		before := e.count(t, "orders")
		_, _, err := e.repo.Save(e.ctx, e.newOrder(domain.OrderStatusPending, nil), &[]domain.OrderProductRaw{})
		require.ErrorContains(t, err, "failed to commit transaction")
		assert.Equal(t, before, e.count(t, "orders"))
	})

	t.Run("DeleteOrder rolls back when removing the lines fails", func(t *testing.T) {
		saved := e.save(t, e.newOrder(domain.OrderStatusPending, nil), e.line(salmon, 1, "12.50", "12.50"))
		require.NoError(t, e.repo.InsertStatusHistory(e.ctx, saved.ID, domain.OrderStatusPending))
		failingTrigger(t, "order_product", "DELETE", false)
		err := e.repo.DeleteOrder(e.ctx, saved.ID)
		require.ErrorContains(t, err, "failed to delete order products")
		assert.Equal(t, 1, e.countWhere(t, "order_status_history", "order_id = $1", saved.ID), "history delete is rolled back")
	})

	t.Run("DeleteOrder reports a failure removing the history", func(t *testing.T) {
		saved := e.save(t, e.newOrder(domain.OrderStatusPending, nil))
		require.NoError(t, e.repo.InsertStatusHistory(e.ctx, saved.ID, domain.OrderStatusPending))
		failingTrigger(t, "order_status_history", "DELETE", false)
		require.ErrorContains(t, e.repo.DeleteOrder(e.ctx, saved.ID), "failed to delete status history")
	})

	t.Run("DeleteOrder reports a failure removing the order, and a failed commit", func(t *testing.T) {
		saved := e.save(t, e.newOrder(domain.OrderStatusPending, nil))
		failingTrigger(t, "orders", "DELETE", false)
		require.ErrorContains(t, e.repo.DeleteOrder(e.ctx, saved.ID), "failed to delete order")
		failingTrigger(t, "orders", "DELETE", true)
		require.ErrorContains(t, e.repo.DeleteOrder(e.ctx, saved.ID), "failed to commit transaction")
		assert.Equal(t, 1, e.countWhere(t, "orders", "id = $1", saved.ID))
	})
}

func TestOrderRepositoryLineQueryFailures(t *testing.T) {
	e := newEnv(t)
	saved := e.save(t, e.newOrder(domain.OrderStatusPending, nil), e.line(e.fx.SalmonSushi.ID, 1, "12.50", "12.50"))

	rename := func(t *testing.T, table string) {
		t.Helper()
		e.exec(t, `ALTER TABLE `+table+` RENAME TO `+table+`_gone`)
		t.Cleanup(func() { _, _ = e.tdb.DB.Exec(`ALTER TABLE ` + table + `_gone RENAME TO ` + table) })
	}

	t.Run("a failing selections query is reported by FindByID and FindByOrderIDs", func(t *testing.T) {
		rename(t, "order_product_choices")
		_, _, err := e.repo.FindByID(e.ctx, saved.ID)
		require.ErrorContains(t, err, "failed to query order product selections")
		_, err = e.repo.FindByOrderIDs(e.ctx, []string{saved.ID.String()})
		require.ErrorContains(t, err, "failed to select order product choices")
	})

	t.Run("a failing lines query is reported by FindByID", func(t *testing.T) {
		rename(t, "order_product")
		_, _, err := e.repo.FindByID(e.ctx, saved.ID)
		require.ErrorContains(t, err, "failed to query order products")
	})
}
