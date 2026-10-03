package infrastructure

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/testhelpers"
	orderDomain "tsb-service/internal/modules/order/domain"
	orderInfra "tsb-service/internal/modules/order/infrastructure"
	"tsb-service/internal/modules/payment/domain"
	"tsb-service/pkg/db"
)

// newPaymentRepoDB starts a Postgres container with all migrations applied and
// returns the payment repository plus a helper creating orders (payments
// reference orders by foreign key).
func newPaymentRepoDB(t *testing.T) (domain.PaymentRepository, func(t *testing.T) uuid.UUID, *testhelpers.TestDatabase) {
	t.Helper()
	tdb := testhelpers.SetupTestDatabase(t)
	fixtures := testhelpers.SeedTestData(t, tdb.DB)
	pool := &db.DBPool{Customer: tdb.DB, Admin: tdb.DB}
	orders := orderInfra.NewOrderRepository(pool)

	newOrder := func(t *testing.T) uuid.UUID {
		t.Helper()
		o := &orderDomain.Order{
			UserID:          fixtures.RegularUser.ID,
			OrderStatus:     orderDomain.OrderStatusPending,
			OrderType:       orderDomain.OrderTypePickUp,
			IsOnlinePayment: true,
			Language:        "fr",
			TotalPrice:      decimal.RequireFromString("20.00"),
		}
		saved, _, err := orders.Save(t.Context(), o, &[]orderDomain.OrderProductRaw{})
		require.NoError(t, err)
		return saved.ID
	}
	return NewPaymentRepository(pool), newOrder, tdb
}

func samplePayment(orderID uuid.UUID, mollieID string) *domain.MolliePayment {
	resource, desc, cancel, hook := "payment", "Tokyo Sushi Bar", "https://x.test/checkout", "https://x.test/hook"
	country, profile, settlement := "BE", "pfl_1", "stl_1"
	created := time.Now().UTC().Truncate(time.Second)
	return &domain.MolliePayment{
		Resource:                        &resource,
		MolliePaymentID:                 mollieID,
		Status:                          domain.PaymentStatusOpen,
		Description:                     &desc,
		CancelURL:                       &cancel,
		WebhookURL:                      &hook,
		CountryCode:                     &country,
		RestrictPaymentMethodsToCountry: &country,
		ProfileID:                       &profile,
		SettlementID:                    &settlement,
		OrderID:                         orderID,
		IsCancelable:                    true,
		Metadata:                        []byte(`{"k":"v"}`),
		Links:                           []byte(`{"checkout":{"href":"https://mollie.test/c"}}`),
		CreatedAt:                       created,
		Amount:                          decimal.RequireFromString("20.05"),
		AmountRefunded:                  decimal.Zero,
		AmountRemaining:                 decimal.RequireFromString("20.05"),
		AmountCaptured:                  decimal.Zero,
		AmountChargedBack:               decimal.Zero,
		SettlementAmount:                decimal.Zero,
	}
}

func TestPaymentRepository(t *testing.T) {
	repo, newOrder, _ := newPaymentRepoDB(t)
	ctx := t.Context()

	t.Run("Save then find by external id and by order id", func(t *testing.T) {
		orderID := newOrder(t)
		p := samplePayment(orderID, "tr_save_1")
		require.NoError(t, repo.Save(ctx, p))
		require.NotEqual(t, uuid.Nil, p.ID, "Save must fill the generated id")

		byExt, err := repo.FindByExternalID(ctx, "tr_save_1")
		require.NoError(t, err)
		require.Equal(t, orderID, byExt.OrderID)
		require.Equal(t, domain.PaymentStatusOpen, byExt.Status)
		require.True(t, byExt.Amount.Equal(decimal.RequireFromString("20.05")), byExt.Amount.String())
		require.True(t, byExt.IsCancelable)
		require.JSONEq(t, `{"k":"v"}`, string(byExt.Metadata))
		require.Equal(t, "Tokyo Sushi Bar", *byExt.Description)

		byOrder, err := repo.FindByOrderID(ctx, orderID)
		require.NoError(t, err)
		require.Equal(t, "tr_save_1", byOrder.MolliePaymentID)
	})

	t.Run("lookup of an unknown payment wraps sql.ErrNoRows", func(t *testing.T) {
		// The webhook relies on this to tell a spoofed id (ack 200) from a
		// database outage (500, Mollie retries).
		_, err := repo.FindByExternalID(ctx, "tr_does_not_exist")
		require.True(t, errors.Is(err, sql.ErrNoRows), "got %v", err)
		_, err = repo.FindByOrderID(ctx, uuid.New())
		require.True(t, errors.Is(err, sql.ErrNoRows), "got %v", err)
	})

	t.Run("a failing lookup is not reported as not found", func(t *testing.T) {
		// A lookup that fails for another reason (here a cancelled context)
		// must NOT look like sql.ErrNoRows.
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		_, err := repo.FindByExternalID(cctx, "tr_save_1")
		require.Error(t, err)
		require.False(t, errors.Is(err, sql.ErrNoRows))
	})

	t.Run("duplicate mollie payment id is rejected", func(t *testing.T) {
		orderID := newOrder(t)
		require.NoError(t, repo.Save(ctx, samplePayment(orderID, "tr_dup")))
		require.Error(t, repo.Save(ctx, samplePayment(newOrder(t), "tr_dup")))
	})

	t.Run("payment for an unknown order is rejected", func(t *testing.T) {
		require.Error(t, repo.Save(ctx, samplePayment(uuid.New(), "tr_orphan")))
		_, err := repo.FindByExternalID(ctx, "tr_orphan")
		require.True(t, errors.Is(err, sql.ErrNoRows))
	})

	t.Run("RefreshStatus stores status and timestamps and returns the order id", func(t *testing.T) {
		orderID := newOrder(t)
		require.NoError(t, repo.Save(ctx, samplePayment(orderID, "tr_refresh")))
		paid := time.Now().UTC().Truncate(time.Second)

		got, err := repo.RefreshStatus(ctx, "tr_refresh", &domain.PaymentStatusUpdate{Status: domain.PaymentStatusPaid, PaidAt: &paid})
		require.NoError(t, err)
		require.Equal(t, orderID, *got)

		p, err := repo.FindByExternalID(ctx, "tr_refresh")
		require.NoError(t, err)
		require.Equal(t, domain.PaymentStatusPaid, p.Status)
		require.NotNil(t, p.PaidAt)
		require.True(t, p.PaidAt.Equal(paid))
		require.Nil(t, p.FailedAt)

		// expired later overwrites timestamps (they come from the Mollie fetch).
		exp := paid.Add(time.Hour)
		_, err = repo.RefreshStatus(ctx, "tr_refresh", &domain.PaymentStatusUpdate{Status: domain.PaymentStatusExpired, ExpiredAt: &exp})
		require.NoError(t, err)
		p, _ = repo.FindByExternalID(ctx, "tr_refresh")
		require.Equal(t, domain.PaymentStatusExpired, p.Status)
		require.NotNil(t, p.ExpiredAt)
	})

	t.Run("RefreshStatus of every status value is accepted by the schema", func(t *testing.T) {
		orderID := newOrder(t)
		require.NoError(t, repo.Save(ctx, samplePayment(orderID, "tr_all_status")))
		for _, st := range []domain.PaymentStatus{
			domain.PaymentStatusPending, domain.PaymentStatusAuthorized, domain.PaymentStatusPaid,
			domain.PaymentStatusFailed, domain.PaymentStatusCanceled, domain.PaymentStatusExpired, domain.PaymentStatusOpen,
		} {
			_, err := repo.RefreshStatus(ctx, "tr_all_status", &domain.PaymentStatusUpdate{Status: st})
			require.NoError(t, err, string(st))
		}
	})

	t.Run("RefreshStatus of an unknown payment errors", func(t *testing.T) {
		_, err := repo.RefreshStatus(ctx, "tr_nope", &domain.PaymentStatusUpdate{Status: domain.PaymentStatusPaid})
		require.Error(t, err)
	})

	t.Run("MarkAsRefund records the refunded amount", func(t *testing.T) {
		require.NoError(t, repo.Save(ctx, samplePayment(newOrder(t), "tr_refund")))
		require.NoError(t, repo.MarkAsRefund(ctx, "tr_refund", decimal.RequireFromString("20.05")))
		p, err := repo.FindByExternalID(ctx, "tr_refund")
		require.NoError(t, err)
		require.True(t, p.AmountRefunded.Equal(decimal.RequireFromString("20.05")))
	})

	t.Run("MarkAsRefund of an unknown payment errors", func(t *testing.T) {
		require.Error(t, repo.MarkAsRefund(ctx, "tr_nope", decimal.NewFromInt(1)))
	})

	t.Run("UpdateStatusByOrderID", func(t *testing.T) {
		orderID := newOrder(t)
		require.NoError(t, repo.Save(ctx, samplePayment(orderID, "tr_by_order")))
		p, err := repo.UpdateStatusByOrderID(ctx, orderID, domain.PaymentStatusCanceled)
		require.NoError(t, err)
		require.Equal(t, domain.PaymentStatusCanceled, p.Status)
		_, err = repo.UpdateStatusByOrderID(ctx, uuid.New(), domain.PaymentStatusCanceled)
		require.Error(t, err)
	})

	t.Run("FindByOrderIDs groups payments per order", func(t *testing.T) {
		o1, o2, o3 := newOrder(t), newOrder(t), newOrder(t)
		require.NoError(t, repo.Save(ctx, samplePayment(o1, "tr_g1a")))
		require.NoError(t, repo.Save(ctx, samplePayment(o1, "tr_g1b")))
		require.NoError(t, repo.Save(ctx, samplePayment(o2, "tr_g2")))

		got, err := repo.FindByOrderIDs(ctx, []string{o1.String(), o2.String(), o3.String()})
		require.NoError(t, err)
		require.Len(t, got[o1.String()], 2)
		require.Len(t, got[o2.String()], 1)
		require.Empty(t, got[o3.String()])

		empty, err := repo.FindByOrderIDs(ctx, nil)
		require.NoError(t, err)
		require.Empty(t, empty)
	})
}

func TestPaymentRepository_WithPaymentLock(t *testing.T) {
	repo, _, _ := newPaymentRepoDB(t)

	t.Run("same payment id is serialized across callers", func(t *testing.T) {
		var inside, maxInside, runs atomic.Int32
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				err := repo.WithPaymentLock(t.Context(), "tr_lock", func(context.Context) error {
					n := inside.Add(1)
					for {
						m := maxInside.Load()
						if n <= m || maxInside.CompareAndSwap(m, n) {
							break
						}
					}
					time.Sleep(15 * time.Millisecond)
					runs.Add(1)
					inside.Add(-1)
					return nil
				})
				require.NoError(t, err)
			})
		}
		wg.Wait()
		require.EqualValues(t, 8, runs.Load())
		require.EqualValues(t, 1, maxInside.Load(), "critical sections overlapped")
	})

	t.Run("different payment ids do not block each other", func(t *testing.T) {
		held := make(chan struct{})
		release := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			done <- repo.WithPaymentLock(t.Context(), "tr_lock_a", func(context.Context) error {
				close(held)
				<-release
				return nil
			})
		}()
		<-held
		other := make(chan error, 1)
		go func() {
			other <- repo.WithPaymentLock(t.Context(), "tr_lock_b", func(context.Context) error { return nil })
		}()
		select {
		case err := <-other:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Fatal("an unrelated payment was blocked by the lock")
		}
		close(release)
		require.NoError(t, <-done)
	})

	t.Run("fn error is returned and the lock is released", func(t *testing.T) {
		boom := errors.New("boom")
		require.ErrorIs(t, repo.WithPaymentLock(t.Context(), "tr_lock_err", func(context.Context) error { return boom }), boom)

		done := make(chan error, 1)
		go func() {
			done <- repo.WithPaymentLock(t.Context(), "tr_lock_err", func(context.Context) error { return nil })
		}()
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Fatal("lock still held after fn returned an error")
		}
	})

	t.Run("a waiter gives up when its context is cancelled", func(t *testing.T) {
		held := make(chan struct{})
		release := make(chan struct{})
		go func() {
			_ = repo.WithPaymentLock(t.Context(), "tr_lock_ctx", func(context.Context) error {
				close(held)
				<-release
				return nil
			})
		}()
		<-held
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		defer cancel()
		err := repo.WithPaymentLock(ctx, "tr_lock_ctx", func(context.Context) error {
			t.Error("fn must not run without the lock")
			return nil
		})
		require.Error(t, err)
		close(release)
	})
}
