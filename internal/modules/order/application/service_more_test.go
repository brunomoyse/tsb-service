package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	couponDomain "tsb-service/internal/modules/coupon/domain"
	"tsb-service/internal/modules/order/domain"
)

// recordingRepo returns canned data and remembers the arguments of the batch/listing calls.
type recordingRepo struct {
	fakeOrderRepo
	err error

	languageUser uuid.UUID
	languageArg  string
	languageRes  []*domain.Order
	userIDsArg   []string
	byUser       map[string][]*domain.Order
	orderIDsArg  []string
	byOrder      map[string][]*domain.OrderProductRaw
	statsArgs    [4]any
	stats        []*domain.CustomerStatsRow
	filterArg    domain.OrderHistoryFilter
	history      []*domain.Order
	summary      *domain.OrderHistorySummary
	paginated    []*domain.Order
	pageArgs     [3]any
	hasCoupon    bool
	staleErr     error
}

func (r *recordingRepo) UpdateActiveOrdersLanguage(_ context.Context, u uuid.UUID, l string) ([]*domain.Order, error) {
	r.languageUser, r.languageArg = u, l
	return r.languageRes, r.err
}

func (r *recordingRepo) FindByUserIDs(_ context.Context, ids []string) (map[string][]*domain.Order, error) {
	r.userIDsArg = ids
	return r.byUser, r.err
}

func (r *recordingRepo) FindByOrderIDs(_ context.Context, ids []string) (map[string][]*domain.OrderProductRaw, error) {
	r.orderIDsArg = ids
	return r.byOrder, r.err
}

func (r *recordingRepo) GetCustomerStats(_ context.Context, s, e *time.Time, t *string, m *int) ([]*domain.CustomerStatsRow, error) {
	r.statsArgs = [4]any{s, e, t, m}
	return r.stats, r.err
}

func (r *recordingRepo) FindFiltered(_ context.Context, f domain.OrderHistoryFilter) ([]*domain.Order, *domain.OrderHistorySummary, error) {
	r.filterArg = f
	return r.history, r.summary, r.err
}

func (r *recordingRepo) FindPaginated(_ context.Context, p, l int, u *uuid.UUID) ([]*domain.Order, error) {
	r.pageArgs = [3]any{p, l, u}
	return r.paginated, r.err
}

func (r *recordingRepo) HasActiveCouponOrder(context.Context, uuid.UUID) (bool, error) {
	return r.hasCoupon, r.err
}

func (r *recordingRepo) CancelStaleTestOrders(context.Context, time.Duration) ([]domain.CancelledOrderRef, error) {
	return nil, r.staleErr
}

func TestOrderServiceDelegatesToTheRepository(t *testing.T) {
	ctx := t.Context()
	uid := uuid.New()
	o := &domain.Order{ID: uuid.New()}
	repo := &recordingRepo{
		languageRes: []*domain.Order{o},
		byUser:      map[string][]*domain.Order{uid.String(): {o}},
		byOrder:     map[string][]*domain.OrderProductRaw{o.ID.String(): {{Quantity: 2}}},
		stats:       []*domain.CustomerStatsRow{{TotalOrders: 3}},
		history:     []*domain.Order{o},
		summary:     &domain.OrderHistorySummary{TotalOrders: 1},
		paginated:   []*domain.Order{o},
		hasCoupon:   true,
	}
	svc := NewOrderService(repo, nil)

	langOrders, err := svc.UpdateActiveOrdersLanguage(ctx, uid, "nl")
	require.NoError(t, err)
	assert.Equal(t, []*domain.Order{o}, langOrders)
	assert.Equal(t, uid, repo.languageUser)
	assert.Equal(t, "nl", repo.languageArg)

	byUser, err := svc.BatchGetOrdersByUserIDs(ctx, []string{uid.String()})
	require.NoError(t, err)
	assert.Equal(t, repo.byUser, byUser)
	assert.Equal(t, []string{uid.String()}, repo.userIDsArg)

	byOrder, err := svc.BatchGetOrderProductsByOrderIDs(ctx, []string{o.ID.String()})
	require.NoError(t, err)
	assert.Equal(t, repo.byOrder, byOrder)
	assert.Equal(t, []string{o.ID.String()}, repo.orderIDsArg)

	start, end, typ, min := time.Now(), time.Now().Add(time.Hour), "PICKUP", 2
	stats, err := svc.GetCustomerStats(ctx, &start, &end, &typ, &min)
	require.NoError(t, err)
	assert.Equal(t, 3, stats[0].TotalOrders)
	assert.Equal(t, [4]any{&start, &end, &typ, &min}, repo.statsArgs)

	filter := domain.OrderHistoryFilter{Page: 2, Limit: 5}
	hist, sum, err := svc.GetOrderHistory(ctx, filter)
	require.NoError(t, err)
	assert.Equal(t, []*domain.Order{o}, hist)
	assert.Equal(t, 1, sum.TotalOrders)
	assert.Equal(t, filter, repo.filterArg)

	page, err := svc.GetPaginatedOrders(ctx, 3, 7, &uid)
	require.NoError(t, err)
	assert.Equal(t, []*domain.Order{o}, page)
	assert.Equal(t, [3]any{3, 7, &uid}, repo.pageArgs)

	has, err := svc.HasActiveCouponOrder(ctx, uid)
	require.NoError(t, err)
	assert.True(t, has)
}

func TestOrderServicePropagatesRepositoryErrors(t *testing.T) {
	ctx := t.Context()
	boom := errors.New("db down")
	repo := &recordingRepo{err: boom, staleErr: boom}
	svc := NewOrderService(repo, nil)

	_, err := svc.UpdateActiveOrdersLanguage(ctx, uuid.New(), "fr")
	assert.ErrorIs(t, err, boom)
	_, err = svc.BatchGetOrdersByUserIDs(ctx, nil)
	assert.ErrorIs(t, err, boom)
	_, err = svc.BatchGetOrderProductsByOrderIDs(ctx, nil)
	assert.ErrorIs(t, err, boom)
	_, err = svc.GetCustomerStats(ctx, nil, nil, nil, nil)
	assert.ErrorIs(t, err, boom)
	_, _, err = svc.GetOrderHistory(ctx, domain.OrderHistoryFilter{})
	assert.ErrorIs(t, err, boom)
	_, err = svc.GetPaginatedOrders(ctx, 1, 1, nil)
	assert.ErrorIs(t, err, boom)
	_, err = svc.HasActiveCouponOrder(ctx, uuid.New())
	assert.ErrorIs(t, err, boom)

	n, err := svc.CancelStaleTestOrders(ctx, time.Hour)
	assert.ErrorIs(t, err, boom)
	assert.Zero(t, n)
}

type failingDecrementCoupons struct {
	fakeCouponService
	err error
}

func (f *failingDecrementCoupons) DecrementUsageAtomic(ctx context.Context, id, u uuid.UUID) error {
	_ = f.fakeCouponService.DecrementUsageAtomic(ctx, id, u)
	return f.err
}

func TestUpdateOrderCancellationSurvivesACouponRollbackFailure(t *testing.T) {
	repo := newHistRepo(domain.OrderStatusConfirmed, new("TOKYO10"))
	coupons := &failingDecrementCoupons{fakeCouponService: fakeCouponService{coupon: &couponDomain.Coupon{ID: uuid.New()}}, err: errors.New("db down")}
	canceled := domain.OrderStatusCanceled

	err := NewOrderService(repo, coupons).UpdateOrder(t.Context(), repo.order.ID, &canceled, nil, nil)

	require.NoError(t, err, "the cancellation already happened; a coupon failure is only logged")
	assert.Equal(t, domain.OrderStatusCanceled, repo.updatedOrder.OrderStatus)
	assert.Len(t, coupons.decrementCalls, 1)
}

func TestOrderDataLoaders(t *testing.T) {
	uid := uuid.New()
	o := &domain.Order{ID: uuid.New(), UserID: uid}
	repo := &recordingRepo{
		byUser:  map[string][]*domain.Order{uid.String(): {o}},
		byOrder: map[string][]*domain.OrderProductRaw{o.ID.String(): {{Quantity: 4}}},
	}
	svc := NewOrderService(repo, nil)

	t.Run("loaders attached to the context resolve through the service", func(t *testing.T) {
		ctx := AttachDataLoaders(t.Context(), svc)

		orders, err := GetUserOrderLoader(ctx).Loader.Load(ctx, uid.String())
		require.NoError(t, err)
		assert.Equal(t, []*domain.Order{o}, orders)

		none, err := GetUserOrderLoader(ctx).Loader.Load(ctx, uuid.NewString())
		require.NoError(t, err)
		assert.Empty(t, none)

		items, err := GetOrderItemLoader(ctx).Loader.Load(ctx, o.ID.String())
		require.NoError(t, err)
		require.Len(t, items, 1)
		assert.Equal(t, int64(4), items[0].Quantity)
	})

	t.Run("a repository failure becomes a loader error naming what failed", func(t *testing.T) {
		failing := NewOrderService(&recordingRepo{err: errors.New("db down")}, nil)
		ctx := AttachDataLoaders(t.Context(), failing)

		_, err := GetUserOrderLoader(ctx).Loader.Load(ctx, uid.String())
		require.ErrorContains(t, err, "db down")
		_, err = GetOrderItemLoader(ctx).Loader.Load(ctx, o.ID.String())
		require.ErrorContains(t, err, "failed to fetch order items")
	})

	t.Run("a context without loaders yields nil", func(t *testing.T) {
		assert.Nil(t, GetUserOrderLoader(t.Context()))
		assert.Nil(t, GetOrderItemLoader(t.Context()))
	})
}

