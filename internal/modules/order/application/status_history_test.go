package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	couponDomain "tsb-service/internal/modules/coupon/domain"
	"tsb-service/internal/modules/order/domain"
)

// histRepo records status history rows and can fail selected calls.
type histRepo struct {
	fakeOrderRepo
	history    []domain.OrderStatus
	historyErr error
	findErr    error
	updateErr  error
	saveErr    error
}

func (h *histRepo) Save(ctx context.Context, o *domain.Order, op *[]domain.OrderProductRaw) (*domain.Order, *[]domain.OrderProductRaw, error) {
	if h.saveErr != nil {
		return nil, nil, h.saveErr
	}
	return h.fakeOrderRepo.Save(ctx, o, op)
}

func (h *histRepo) FindByID(ctx context.Context, id uuid.UUID) (*domain.Order, *[]domain.OrderProductRaw, error) {
	if h.findErr != nil {
		return nil, nil, h.findErr
	}
	return h.fakeOrderRepo.FindByID(ctx, id)
}

func (h *histRepo) Update(ctx context.Context, o *domain.Order) error {
	if h.updateErr != nil {
		return h.updateErr
	}
	return h.fakeOrderRepo.Update(ctx, o)
}

func (h *histRepo) InsertStatusHistory(_ context.Context, _ uuid.UUID, s domain.OrderStatus) error {
	h.history = append(h.history, s)
	return h.historyErr
}

func newHistRepo(status domain.OrderStatus, code *string) *histRepo {
	return &histRepo{fakeOrderRepo: fakeOrderRepo{order: &domain.Order{
		ID: uuid.New(), UserID: uuid.New(), OrderStatus: status, CouponCode: code,
	}}}
}

func TestCreateOrderRecordsInitialStatus(t *testing.T) {
	repo := newHistRepo(domain.OrderStatusPending, nil)
	svc := NewOrderService(repo, nil)

	got, _, err := svc.CreateOrder(t.Context(), repo.order, &[]domain.OrderProductRaw{})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != repo.order.ID {
		t.Fatal("wrong order returned")
	}
	if len(repo.history) != 1 || repo.history[0] != domain.OrderStatusPending {
		t.Fatalf("history = %v, want [PENDING]", repo.history)
	}

	t.Run("history failure does not fail the order", func(t *testing.T) {
		repo := newHistRepo(domain.OrderStatusPending, nil)
		repo.historyErr = errors.New("db down")
		if _, _, err := NewOrderService(repo, nil).CreateOrder(t.Context(), repo.order, nil); err != nil {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("save failure is returned and writes no history", func(t *testing.T) {
		repo := newHistRepo(domain.OrderStatusPending, nil)
		repo.saveErr = errors.New("db down")
		if _, _, err := NewOrderService(repo, nil).CreateOrder(t.Context(), repo.order, nil); err == nil {
			t.Fatal("want error")
		}
		if len(repo.history) != 0 {
			t.Fatalf("history = %v", repo.history)
		}
	})
}

func TestUpdateOrderStatusHistory(t *testing.T) {
	confirmed, canceled := domain.OrderStatusConfirmed, domain.OrderStatusCanceled

	t.Run("a status change writes exactly one history row", func(t *testing.T) {
		repo := newHistRepo(domain.OrderStatusPending, nil)
		if err := NewOrderService(repo, nil).UpdateOrder(t.Context(), repo.order.ID, &confirmed, nil, nil); err != nil {
			t.Fatal(err)
		}
		if len(repo.history) != 1 || repo.history[0] != domain.OrderStatusConfirmed {
			t.Fatalf("history = %v", repo.history)
		}
		if repo.updatedOrder == nil || repo.updatedOrder.OrderStatus != domain.OrderStatusConfirmed {
			t.Fatal("order not persisted with the new status")
		}
	})

	t.Run("same status writes no history row", func(t *testing.T) {
		repo := newHistRepo(domain.OrderStatusConfirmed, nil)
		if err := NewOrderService(repo, nil).UpdateOrder(t.Context(), repo.order.ID, &confirmed, nil, nil); err != nil {
			t.Fatal(err)
		}
		if len(repo.history) != 0 {
			t.Fatalf("history = %v", repo.history)
		}
	})

	t.Run("nil status with a new ready time writes no history row", func(t *testing.T) {
		repo := newHistRepo(domain.OrderStatusConfirmed, nil)
		eta := time.Now().Add(20 * time.Minute)
		if err := NewOrderService(repo, nil).UpdateOrder(t.Context(), repo.order.ID, nil, &eta, nil); err != nil {
			t.Fatal(err)
		}
		if len(repo.history) != 0 {
			t.Fatalf("history = %v", repo.history)
		}
		if repo.updatedOrder.EstimatedReadyTime == nil || !repo.updatedOrder.EstimatedReadyTime.Equal(eta) {
			t.Fatal("ready time not persisted")
		}
		if repo.updatedOrder.OrderStatus != domain.OrderStatusConfirmed {
			t.Fatal("status must be untouched")
		}
	})

	t.Run("cancellation reason is stored only when cancelling", func(t *testing.T) {
		reason := domain.OrderCancellationReasonOutOfStock

		repo := newHistRepo(domain.OrderStatusPending, nil)
		if err := NewOrderService(repo, nil).UpdateOrder(t.Context(), repo.order.ID, &canceled, nil, &reason); err != nil {
			t.Fatal(err)
		}
		if repo.updatedOrder.CancellationReason == nil || *repo.updatedOrder.CancellationReason != reason {
			t.Fatal("reason not stored on cancel")
		}

		repo = newHistRepo(domain.OrderStatusPending, nil)
		if err := NewOrderService(repo, nil).UpdateOrder(t.Context(), repo.order.ID, &confirmed, nil, &reason); err != nil {
			t.Fatal(err)
		}
		if repo.updatedOrder.CancellationReason != nil {
			t.Fatal("reason must be ignored when not cancelling")
		}
	})

	t.Run("order lookup failure is returned, nothing written", func(t *testing.T) {
		repo := newHistRepo(domain.OrderStatusPending, nil)
		repo.findErr = errors.New("db down")
		if err := NewOrderService(repo, nil).UpdateOrder(t.Context(), repo.order.ID, &canceled, nil, nil); err == nil {
			t.Fatal("want error")
		}
		if repo.updatedOrder != nil || len(repo.history) != 0 {
			t.Fatal("must not write when the order cannot be loaded")
		}
	})

	t.Run("update failure is returned, no history row and no coupon rollback", func(t *testing.T) {
		repo := newHistRepo(domain.OrderStatusPending, new("TOKYO10"))
		repo.updateErr = errors.New("db down")
		coupons := &fakeCouponService{coupon: &couponDomain.Coupon{ID: uuid.New()}}
		if err := NewOrderService(repo, coupons).UpdateOrder(t.Context(), repo.order.ID, &canceled, nil, nil); err == nil {
			t.Fatal("want error")
		}
		if len(repo.history) != 0 || len(coupons.decrementCalls) != 0 {
			t.Fatalf("history=%v rollbacks=%d", repo.history, len(coupons.decrementCalls))
		}
	})

	t.Run("history failure does not undo the update or the coupon rollback", func(t *testing.T) {
		repo := newHistRepo(domain.OrderStatusPending, new("TOKYO10"))
		repo.historyErr = errors.New("db down")
		coupons := &fakeCouponService{coupon: &couponDomain.Coupon{ID: uuid.New()}}
		if err := NewOrderService(repo, coupons).UpdateOrder(t.Context(), repo.order.ID, &canceled, nil, nil); err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(coupons.decrementCalls) != 1 {
			t.Fatalf("rollbacks = %d, want 1", len(coupons.decrementCalls))
		}
	})
}

func TestUpdateOrderCouponRollbackFailuresNeverFailTheCancellation(t *testing.T) {
	canceled := domain.OrderStatusCanceled

	t.Run("coupon lookup error", func(t *testing.T) {
		repo := newHistRepo(domain.OrderStatusPending, new("TOKYO10"))
		coupons := &fakeCouponService{getByCodeErr: errors.New("db down")}
		if err := NewOrderService(repo, coupons).UpdateOrder(t.Context(), repo.order.ID, &canceled, nil, nil); err != nil {
			t.Fatalf("err = %v", err)
		}
		if repo.updatedOrder.OrderStatus != domain.OrderStatusCanceled {
			t.Fatal("the order must still be cancelled")
		}
	})

	t.Run("coupon no longer exists", func(t *testing.T) {
		repo := newHistRepo(domain.OrderStatusPending, new("GONE"))
		coupons := &fakeCouponService{} // returns nil, nil
		if err := NewOrderService(repo, coupons).UpdateOrder(t.Context(), repo.order.ID, &canceled, nil, nil); err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(coupons.decrementCalls) != 0 {
			t.Fatal("nothing to decrement")
		}
	})

	t.Run("empty coupon code is ignored", func(t *testing.T) {
		repo := newHistRepo(domain.OrderStatusPending, new(""))
		coupons := &fakeCouponService{coupon: &couponDomain.Coupon{ID: uuid.New()}}
		if err := NewOrderService(repo, coupons).UpdateOrder(t.Context(), repo.order.ID, &canceled, nil, nil); err != nil {
			t.Fatal(err)
		}
		if len(coupons.decrementCalls) != 0 {
			t.Fatal("empty code must not trigger a rollback")
		}
	})

	t.Run("nil coupon service", func(t *testing.T) {
		repo := newHistRepo(domain.OrderStatusPending, new("TOKYO10"))
		if err := NewOrderService(repo, nil).UpdateOrder(t.Context(), repo.order.ID, &canceled, nil, nil); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("only the transition into CANCELLED rolls back, not FAILED or others", func(t *testing.T) {
		for _, st := range []domain.OrderStatus{domain.OrderStatusConfirmed, domain.OrderStatusFailed, domain.OrderStatusPreparing} {
			repo := newHistRepo(domain.OrderStatusPending, new("TOKYO10"))
			coupons := &fakeCouponService{coupon: &couponDomain.Coupon{ID: uuid.New()}}
			if err := NewOrderService(repo, coupons).UpdateOrder(t.Context(), repo.order.ID, &st, nil, nil); err != nil {
				t.Fatal(err)
			}
			if len(coupons.decrementCalls) != 0 {
				t.Fatalf("%s must not release the coupon", st)
			}
		}
	})
}

func TestCancelStaleTestOrdersHistory(t *testing.T) {
	a := domain.CancelledOrderRef{ID: uuid.New(), UserID: uuid.New()}
	b := domain.CancelledOrderRef{ID: uuid.New(), UserID: uuid.New()}

	t.Run("one CANCELLED history row per order", func(t *testing.T) {
		repo := newHistRepo(domain.OrderStatusPending, nil)
		repo.staleTestOrders = []domain.CancelledOrderRef{a, b}
		n, err := NewOrderService(repo, nil).CancelStaleTestOrders(t.Context(), time.Minute)
		if err != nil || n != 2 {
			t.Fatalf("n=%d err=%v", n, err)
		}
		if len(repo.history) != 2 || repo.history[0] != domain.OrderStatusCanceled || repo.history[1] != domain.OrderStatusCanceled {
			t.Fatalf("history = %v", repo.history)
		}
	})

	t.Run("nothing stale", func(t *testing.T) {
		repo := newHistRepo(domain.OrderStatusPending, nil)
		n, err := NewOrderService(repo, nil).CancelStaleTestOrders(t.Context(), time.Minute)
		if err != nil || n != 0 || len(repo.history) != 0 {
			t.Fatalf("n=%d err=%v history=%v", n, err, repo.history)
		}
	})

	t.Run("history failure does not stop the sweep", func(t *testing.T) {
		repo := newHistRepo(domain.OrderStatusPending, nil)
		repo.historyErr = errors.New("db down")
		repo.staleTestOrders = []domain.CancelledOrderRef{a, b}
		n, err := NewOrderService(repo, nil).CancelStaleTestOrders(t.Context(), time.Minute)
		if err != nil || n != 2 {
			t.Fatalf("n=%d err=%v", n, err)
		}
	})
}

func TestOrderServiceReadPassthroughs(t *testing.T) {
	repo := newHistRepo(domain.OrderStatusPending, nil)
	svc := NewOrderService(repo, nil)

	got, _, err := svc.GetOrderByID(t.Context(), repo.order.ID)
	if err != nil || got.ID != repo.order.ID {
		t.Fatalf("GetOrderByID: %v %v", got, err)
	}
	repo.findErr = errors.New("db down")
	if _, _, err := svc.GetOrderByID(t.Context(), repo.order.ID); err == nil {
		t.Fatal("want error")
	}
	if h, err := svc.GetStatusHistory(t.Context(), repo.order.ID); err != nil || h != nil {
		t.Fatalf("GetStatusHistory: %v %v", h, err)
	}
	if err := svc.DeleteOrder(t.Context(), repo.order.ID); err != nil {
		t.Fatal(err)
	}
	if ok, err := svc.HasActiveCouponOrder(t.Context(), uuid.New()); err != nil || ok {
		t.Fatalf("HasActiveCouponOrder: %v %v", ok, err)
	}
}
