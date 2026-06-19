package application

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	couponDomain "tsb-service/internal/modules/coupon/domain"
	"tsb-service/internal/modules/order/domain"
)

// fakeOrderRepo implements domain.OrderRepository. Only FindByID/Update/
// InsertStatusHistory carry behaviour for these tests; the rest are stubs.
type fakeOrderRepo struct {
	order        *domain.Order
	updatedOrder *domain.Order
}

func (f *fakeOrderRepo) Save(_ context.Context, o *domain.Order, op *[]domain.OrderProductRaw) (*domain.Order, *[]domain.OrderProductRaw, error) {
	return o, op, nil
}

func (f *fakeOrderRepo) Update(_ context.Context, o *domain.Order) error {
	f.updatedOrder = o
	return nil
}

func (f *fakeOrderRepo) FindByID(_ context.Context, _ uuid.UUID) (*domain.Order, *[]domain.OrderProductRaw, error) {
	// Return a copy so the service mutates its own instance, mirroring the real repo.
	cp := *f.order
	return &cp, nil, nil
}

func (f *fakeOrderRepo) FindPaginated(_ context.Context, _ int, _ int, _ *uuid.UUID) ([]*domain.Order, error) {
	return nil, nil
}

func (f *fakeOrderRepo) FindFiltered(_ context.Context, _ domain.OrderHistoryFilter) ([]*domain.Order, *domain.OrderHistorySummary, error) {
	return nil, nil, nil
}

func (f *fakeOrderRepo) FindByOrderIDs(_ context.Context, _ []string) (map[string][]*domain.OrderProductRaw, error) {
	return nil, nil
}

func (f *fakeOrderRepo) FindByUserIDs(_ context.Context, _ []string) (map[string][]*domain.Order, error) {
	return nil, nil
}

func (f *fakeOrderRepo) UpdateActiveOrdersLanguage(_ context.Context, _ uuid.UUID, _ string) ([]*domain.Order, error) {
	return nil, nil
}

func (f *fakeOrderRepo) InsertStatusHistory(_ context.Context, _ uuid.UUID, _ domain.OrderStatus) error {
	return nil
}

func (f *fakeOrderRepo) CancelStaleTestOrders(_ context.Context, _ time.Duration) ([]uuid.UUID, error) {
	return nil, nil
}

func (f *fakeOrderRepo) FindStatusHistoryByOrderID(_ context.Context, _ uuid.UUID) ([]*domain.OrderStatusHistory, error) {
	return nil, nil
}

func (f *fakeOrderRepo) DeleteOrder(_ context.Context, _ uuid.UUID) error { return nil }

func (f *fakeOrderRepo) GetCustomerStats(_ context.Context, _, _ *time.Time, _ *string, _ *int) ([]*domain.CustomerStatsRow, error) {
	return nil, nil
}

// fakeCouponService implements couponApplication.CouponService and records the
// (couponID, userID) of every DecrementUsageAtomic call.
type fakeCouponService struct {
	coupon         *couponDomain.Coupon
	getByCodeErr   error
	decrementCalls [][2]uuid.UUID
}

func (f *fakeCouponService) GetCouponByCode(_ context.Context, _ string) (*couponDomain.Coupon, error) {
	if f.getByCodeErr != nil {
		return nil, f.getByCodeErr
	}
	return f.coupon, nil
}

func (f *fakeCouponService) DecrementUsageAtomic(_ context.Context, id uuid.UUID, userID uuid.UUID) error {
	f.decrementCalls = append(f.decrementCalls, [2]uuid.UUID{id, userID})
	return nil
}

func (f *fakeCouponService) ValidateCoupon(context.Context, string, decimal.Decimal, uuid.UUID) (*couponDomain.Coupon, decimal.Decimal, error) {
	panic("unused")
}
func (f *fakeCouponService) IncrementUsage(context.Context, uuid.UUID) error { return nil }
func (f *fakeCouponService) IncrementUsageAtomic(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return true, nil
}
func (f *fakeCouponService) GetAllCoupons(context.Context) ([]*couponDomain.Coupon, error) {
	return nil, nil
}
func (f *fakeCouponService) GetCoupon(context.Context, uuid.UUID) (*couponDomain.Coupon, error) {
	return nil, nil
}
func (f *fakeCouponService) CreateCoupon(context.Context, *couponDomain.Coupon) error { return nil }
func (f *fakeCouponService) UpdateCoupon(context.Context, *couponDomain.Coupon) error { return nil }

func strPtr(s string) *string { return &s }

func TestUpdateOrderCouponRollback(t *testing.T) {
	canceled := domain.OrderStatusCanceled
	couponID := uuid.New()
	userID := uuid.New()

	newOrder := func(status domain.OrderStatus, code *string) *domain.Order {
		return &domain.Order{ID: uuid.New(), UserID: userID, OrderStatus: status, CouponCode: code}
	}

	t.Run("cancelling an order with a coupon rolls back usage once", func(t *testing.T) {
		repo := &fakeOrderRepo{order: newOrder(domain.OrderStatusConfirmed, strPtr("TOKYO10"))}
		coupons := &fakeCouponService{coupon: &couponDomain.Coupon{ID: couponID}}
		svc := NewOrderService(repo, coupons)

		if err := svc.UpdateOrder(context.Background(), repo.order.ID, &canceled, nil, nil); err != nil {
			t.Fatalf("UpdateOrder: %v", err)
		}
		if len(coupons.decrementCalls) != 1 {
			t.Fatalf("expected 1 rollback, got %d", len(coupons.decrementCalls))
		}
		if coupons.decrementCalls[0] != [2]uuid.UUID{couponID, userID} {
			t.Fatalf("rollback used wrong ids: %v", coupons.decrementCalls[0])
		}
	})

	t.Run("re-cancelling an already-cancelled order does not roll back again", func(t *testing.T) {
		repo := &fakeOrderRepo{order: newOrder(domain.OrderStatusCanceled, strPtr("TOKYO10"))}
		coupons := &fakeCouponService{coupon: &couponDomain.Coupon{ID: couponID}}
		svc := NewOrderService(repo, coupons)

		if err := svc.UpdateOrder(context.Background(), repo.order.ID, &canceled, nil, nil); err != nil {
			t.Fatalf("UpdateOrder: %v", err)
		}
		if len(coupons.decrementCalls) != 0 {
			t.Fatalf("expected no rollback on no-op transition, got %d", len(coupons.decrementCalls))
		}
	})

	t.Run("cancelling an order without a coupon rolls back nothing", func(t *testing.T) {
		repo := &fakeOrderRepo{order: newOrder(domain.OrderStatusConfirmed, nil)}
		coupons := &fakeCouponService{coupon: &couponDomain.Coupon{ID: couponID}}
		svc := NewOrderService(repo, coupons)

		if err := svc.UpdateOrder(context.Background(), repo.order.ID, &canceled, nil, nil); err != nil {
			t.Fatalf("UpdateOrder: %v", err)
		}
		if len(coupons.decrementCalls) != 0 {
			t.Fatalf("expected no rollback without a coupon, got %d", len(coupons.decrementCalls))
		}
	})
}
