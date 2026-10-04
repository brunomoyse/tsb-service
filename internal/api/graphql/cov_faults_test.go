package graphql_test

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	couponApplication "tsb-service/internal/modules/coupon/application"
	orderApplication "tsb-service/internal/modules/order/application"
	orderDomain "tsb-service/internal/modules/order/domain"
	productApplication "tsb-service/internal/modules/product/application"
	productDomain "tsb-service/internal/modules/product/domain"
	userApplication "tsb-service/internal/modules/user/application"
	userDomain "tsb-service/internal/modules/user/domain"
)

// Service wrappers that fail one chosen operation and delegate everything else to the real
// service. They reach the error branches a healthy database never produces (the resolvers map
// each of those to a stable error or a log line).

type faultyUsers struct {
	userApplication.UserService
	failGet bool
}

func (f faultyUsers) GetUserByID(ctx context.Context, id string) (*userDomain.User, error) {
	if f.failGet {
		return nil, errBoom
	}
	return f.UserService.GetUserByID(ctx, id)
}

type faultyProducts struct {
	productApplication.ProductService
	failInvoiceNames  bool
	emptyInvoiceNames bool
	// nothing makes the lookups of one product / category answer (nil, nil).
	nothing bool
}

func (f faultyProducts) GetProduct(ctx context.Context, id uuid.UUID) (*productDomain.Product, error) {
	if f.nothing {
		return nil, nil
	}
	return f.ProductService.GetProduct(ctx, id)
}

func (f faultyProducts) GetCategory(ctx context.Context, id uuid.UUID) (*productDomain.Category, error) {
	if f.nothing {
		return nil, nil
	}
	return f.ProductService.GetCategory(ctx, id)
}

func (f faultyProducts) GetCategoryBySlug(ctx context.Context, slug string) (*productDomain.Category, error) {
	if f.nothing {
		return nil, nil
	}
	return f.ProductService.GetCategoryBySlug(ctx, slug)
}

func (f faultyProducts) GetChoiceByID(ctx context.Context, id uuid.UUID) (*productDomain.ProductChoice, error) {
	if f.nothing {
		return nil, nil
	}
	return f.ProductService.GetChoiceByID(ctx, id)
}

func (f faultyProducts) GetProductNamesForInvoice(ctx context.Context, ids []string) ([]*productDomain.ProductOrderDetails, error) {
	if f.failInvoiceNames {
		return nil, errBoom
	}
	if f.emptyInvoiceNames {
		return nil, nil
	}
	return f.ProductService.GetProductNamesForInvoice(ctx, ids)
}

// faultyOrders fails or alters chosen order operations. getFailsAfter lets that many GetOrderByID
// calls through and fails the following ones (-1: never).
type faultyOrders struct {
	orderApplication.OrderService
	failUpdate      bool
	failCreate      bool
	getFailsAfter   int
	getCalls        *atomic.Int32
	failDelete      bool
	wrongItems      bool
	failLanguage    bool
	failStatusHist  bool
	failOrdersQuery bool
	nilOrder        bool
	failHistory     bool
}

func newFaultyOrders(base orderApplication.OrderService) *faultyOrders {
	return &faultyOrders{OrderService: base, getFailsAfter: -1, getCalls: &atomic.Int32{}}
}

func (f *faultyOrders) UpdateOrder(ctx context.Context, id uuid.UUID, s *orderDomain.OrderStatus, eta *time.Time, reason *orderDomain.OrderCancellationReason) error {
	if f.failUpdate {
		return errBoom
	}
	return f.OrderService.UpdateOrder(ctx, id, s, eta, reason)
}

func (f *faultyOrders) GetOrderByID(ctx context.Context, id uuid.UUID) (*orderDomain.Order, *[]orderDomain.OrderProductRaw, error) {
	if f.nilOrder {
		return nil, nil, nil
	}
	n := f.getCalls.Add(1)
	if f.getFailsAfter >= 0 && int(n) > f.getFailsAfter {
		return nil, nil, errBoom
	}
	return f.OrderService.GetOrderByID(ctx, id)
}

func (f *faultyOrders) DeleteOrder(ctx context.Context, id uuid.UUID) error {
	if f.failDelete {
		return errBoom
	}
	return f.OrderService.DeleteOrder(ctx, id)
}

// CreateOrder returns the saved order with its lines pointing at a product the pricer never saw.
func (f *faultyOrders) CreateOrder(ctx context.Context, o *orderDomain.Order, items *[]orderDomain.OrderProductRaw) (*orderDomain.Order, *[]orderDomain.OrderProductRaw, error) {
	if f.failCreate {
		return nil, nil, errBoom
	}
	order, saved, err := f.OrderService.CreateOrder(ctx, o, items)
	if err != nil || !f.wrongItems {
		return order, saved, err
	}
	changed := make([]orderDomain.OrderProductRaw, len(*saved))
	copy(changed, *saved)
	for i := range changed {
		changed[i].ProductID = uuid.New()
	}
	return order, &changed, nil
}

func (f *faultyOrders) GetStatusHistory(ctx context.Context, id uuid.UUID) ([]*orderDomain.OrderStatusHistory, error) {
	if f.failStatusHist {
		return nil, errBoom
	}
	return f.OrderService.GetStatusHistory(ctx, id)
}

func (f *faultyOrders) GetPaginatedOrders(ctx context.Context, page, limit int, user *uuid.UUID) ([]*orderDomain.Order, error) {
	if f.failOrdersQuery {
		return nil, errBoom
	}
	return f.OrderService.GetPaginatedOrders(ctx, page, limit, user)
}

type faultyCoupons struct {
	couponApplication.CouponService
	incrementErr    error
	incrementDenied bool
	decrementErr    error
}

func (f faultyCoupons) IncrementUsageAtomic(ctx context.Context, id, user uuid.UUID) (bool, error) {
	if f.incrementErr != nil {
		return false, f.incrementErr
	}
	if f.incrementDenied {
		return false, nil
	}
	return f.CouponService.IncrementUsageAtomic(ctx, id, user)
}

func (f faultyCoupons) DecrementUsageAtomic(ctx context.Context, id, user uuid.UUID) error {
	if f.decrementErr != nil {
		return f.decrementErr
	}
	return f.CouponService.DecrementUsageAtomic(ctx, id, user)
}

func (f *faultyOrders) GetOrderHistory(ctx context.Context, filter orderDomain.OrderHistoryFilter) ([]*orderDomain.Order, *orderDomain.OrderHistorySummary, error) {
	if f.failHistory {
		return nil, nil, errBoom
	}
	return f.OrderService.GetOrderHistory(ctx, filter)
}
