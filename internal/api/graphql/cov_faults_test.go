package graphql_test

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"

	addressApplication "tsb-service/internal/modules/address/application"
	addressDomain "tsb-service/internal/modules/address/domain"
	couponApplication "tsb-service/internal/modules/coupon/application"
	couponDomain "tsb-service/internal/modules/coupon/domain"
	orderApplication "tsb-service/internal/modules/order/application"
	orderDomain "tsb-service/internal/modules/order/domain"
	productApplication "tsb-service/internal/modules/product/application"
	productDomain "tsb-service/internal/modules/product/domain"
	restaurantApplication "tsb-service/internal/modules/restaurant/application"
	restaurantDomain "tsb-service/internal/modules/restaurant/domain"
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
	// getCalls / getFailsAfter: GetProduct fails once that many calls went through.
	getCalls      *atomic.Int32
	getFailsAfter int
	updateGroup   error
	createChoice  error
	updateChoice  error
	failPricing   bool
}

func (f faultyProducts) UpdateChoiceGroup(ctx context.Context, g *productDomain.ProductChoiceGroup) error {
	if f.updateGroup != nil {
		return f.updateGroup
	}
	return f.ProductService.UpdateChoiceGroup(ctx, g)
}

func (f faultyProducts) CreateChoice(ctx context.Context, c *productDomain.ProductChoice) error {
	if f.createChoice != nil {
		return f.createChoice
	}
	return f.ProductService.CreateChoice(ctx, c)
}

func (f faultyProducts) UpdateChoice(ctx context.Context, c *productDomain.ProductChoice) error {
	if f.updateChoice != nil {
		return f.updateChoice
	}
	return f.ProductService.UpdateChoice(ctx, c)
}

func (f faultyProducts) GetProductsForPricing(ctx context.Context, ids []string) ([]*productDomain.ProductOrderDetails, error) {
	if f.failPricing {
		return nil, errBoom
	}
	return f.ProductService.GetProductsForPricing(ctx, ids)
}

func (f faultyProducts) GetProduct(ctx context.Context, id uuid.UUID) (*productDomain.Product, error) {
	if f.nothing {
		return nil, nil
	}
	if f.getCalls != nil && int(f.getCalls.Add(1)) > f.getFailsAfter {
		return nil, errBoom
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
	createErr       error
	failStats       bool
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
	if f.createErr != nil {
		return nil, nil, f.createErr
	}
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

// faultyRestaurant fails the reads that follow a successful change of the schedule.
type faultyRestaurant struct {
	restaurantApplication.RestaurantService
	failList, failConfig, failWithOverrides bool
}

func (f faultyRestaurant) ListOverrides(ctx context.Context, from, to time.Time) ([]*restaurantDomain.ScheduleOverride, error) {
	if f.failList {
		return nil, errBoom
	}
	return f.RestaurantService.ListOverrides(ctx, from, to)
}

func (f faultyRestaurant) GetConfig(ctx context.Context) (*restaurantDomain.RestaurantConfig, error) {
	if f.failConfig {
		return nil, errBoom
	}
	return f.RestaurantService.GetConfig(ctx)
}

func (f faultyRestaurant) GetConfigWithOverrides(ctx context.Context) (*restaurantDomain.RestaurantConfig, map[string]*restaurantDomain.ScheduleOverride, error) {
	if f.failWithOverrides {
		return nil, nil, errBoom
	}
	return f.RestaurantService.GetConfigWithOverrides(ctx)
}

// faultyCouponStore makes CreateCoupon / UpdateCoupon / GetCoupon fail in the ways the resolver
// tells apart: a unique violation (the code is taken) or anything else.
type faultyCouponStore struct {
	couponApplication.CouponService
	createErrs []error // consumed one per CreateCoupon call, then delegated
	updateErr  error
	listErr    error
}

func (f *faultyCouponStore) CreateCoupon(ctx context.Context, c *couponDomain.Coupon) error {
	if len(f.createErrs) > 0 {
		err := f.createErrs[0]
		f.createErrs = f.createErrs[1:]
		if err != nil {
			return err
		}
	}
	return f.CouponService.CreateCoupon(ctx, c)
}

func (f *faultyCouponStore) UpdateCoupon(ctx context.Context, c *couponDomain.Coupon) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	return f.CouponService.UpdateCoupon(ctx, c)
}

func (f *faultyCouponStore) GetAllCoupons(ctx context.Context) ([]*couponDomain.Coupon, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.CouponService.GetAllCoupons(ctx)
}

// uniqueViolation is what Postgres answers when a coupon code is already taken.
func uniqueViolation() error { return &pq.Error{Code: "23505", Message: "duplicate key"} }

func (f *faultyOrders) GetCustomerStats(ctx context.Context, from, to *time.Time, orderType *string, minOrders *int) ([]*orderDomain.CustomerStatsRow, error) {
	if f.failStats {
		return nil, errBoom
	}
	return f.OrderService.GetCustomerStats(ctx, from, to, orderType, minOrders)
}

// faultyAddresses fails the cache-only lookup the User.address field uses.
type faultyAddresses struct {
	addressApplication.AddressService
}

func (faultyAddresses) GetByPlaceID(context.Context, string) (*addressDomain.Address, error) {
	return nil, errBoom
}
