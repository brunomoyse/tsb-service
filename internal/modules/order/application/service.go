package application

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	couponApplication "tsb-service/internal/modules/coupon/application"
	"tsb-service/internal/modules/order/domain"
	"tsb-service/pkg/logging"
)

type OrderService interface {
	CreateOrder(ctx context.Context, order *domain.Order, orderProducts *[]domain.OrderProductRaw) (*domain.Order, *[]domain.OrderProductRaw, error)
	GetPaginatedOrders(ctx context.Context, page int, limit int, userID *uuid.UUID) ([]*domain.Order, error)
	UpdateOrder(ctx context.Context, orderID uuid.UUID, newStatus *domain.OrderStatus, estimatedReadyTime *time.Time, cancellationReason *domain.OrderCancellationReason) error
	GetOrderByID(ctx context.Context, orderID uuid.UUID) (*domain.Order, *[]domain.OrderProductRaw, error)
	GetStatusHistory(ctx context.Context, orderID uuid.UUID) ([]*domain.OrderStatusHistory, error)

	DeleteOrder(ctx context.Context, orderID uuid.UUID) error
	BatchGetOrderProductsByOrderIDs(ctx context.Context, orderIDs []string) (map[string][]*domain.OrderProductRaw, error)
	BatchGetOrdersByUserIDs(ctx context.Context, userIDs []string) (map[string][]*domain.Order, error)
	UpdateActiveOrdersLanguage(ctx context.Context, userID uuid.UUID, language string) ([]*domain.Order, error)
	GetCustomerStats(ctx context.Context, startDate, endDate *time.Time, orderType *string, minOrders *int) ([]*domain.CustomerStatsRow, error)
	GetOrderHistory(ctx context.Context, filter domain.OrderHistoryFilter) ([]*domain.Order, *domain.OrderHistorySummary, error)
	// CancelStaleTestOrders auto-cancels store-review test orders older than
	// olderThan and returns how many were cancelled. TEMPORARY (revert after launch).
	CancelStaleTestOrders(ctx context.Context, olderThan time.Duration) (int, error)
}

type orderService struct {
	repo          domain.OrderRepository
	couponService couponApplication.CouponService
}

func NewOrderService(repo domain.OrderRepository, couponService couponApplication.CouponService) OrderService {
	return &orderService{
		repo:          repo,
		couponService: couponService,
	}
}

func (s *orderService) CreateOrder(ctx context.Context, o *domain.Order, op *[]domain.OrderProductRaw) (*domain.Order, *[]domain.OrderProductRaw, error) {
	order, orderProducts, err := s.repo.Save(ctx, o, op)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to save order: %w", err)
	}

	// Record initial status in history
	if err := s.repo.InsertStatusHistory(ctx, order.ID, order.OrderStatus); err != nil {
		logging.FromContext(ctx).Error("failed to record initial status history", zap.String("order_id", order.ID.String()), zap.Error(err))
	}

	return order, orderProducts, nil
}

func (s *orderService) DeleteOrder(ctx context.Context, orderID uuid.UUID) error {
	return s.repo.DeleteOrder(ctx, orderID)
}

func (s *orderService) GetPaginatedOrders(ctx context.Context, page int, limit int, userID *uuid.UUID) ([]*domain.Order, error) {
	return s.repo.FindPaginated(ctx, page, limit, userID)
}

func (s *orderService) UpdateOrder(ctx context.Context, orderID uuid.UUID, newStatus *domain.OrderStatus, estimatedReadyTime *time.Time, cancellationReason *domain.OrderCancellationReason) error {
	// Retrieve the order
	order, _, err := s.repo.FindByID(ctx, orderID)
	if err != nil {
		return err
	}

	oldStatus := order.OrderStatus

	// Check if there a new status
	if newStatus != nil {
		order.OrderStatus = *newStatus
	}

	// Check if there is a new estimated ready time
	if estimatedReadyTime != nil {
		order.EstimatedReadyTime = estimatedReadyTime
	}

	// Persist cancellation reason only when transitioning to CANCELLED.
	if cancellationReason != nil && order.OrderStatus == domain.OrderStatusCanceled {
		order.CancellationReason = cancellationReason
	}

	if err := s.repo.Update(ctx, order); err != nil {
		return err
	}

	// Record status change in history
	if order.OrderStatus != oldStatus {
		if err := s.repo.InsertStatusHistory(ctx, order.ID, order.OrderStatus); err != nil {
			logging.FromContext(ctx).Error("failed to record status history", zap.String("order_id", order.ID.String()), zap.String("status", string(order.OrderStatus)), zap.Error(err))
		}
	}

	// Roll back coupon usage when the order transitions into CANCELED. This is
	// the single source of cancel-time rollback: it covers cash orders, admin
	// and POS cancellations, and the payment-failed webhook (which cancels via
	// this method). The transition guard (oldStatus != CANCELED) makes it
	// idempotent, so a duplicate cancellation never double-decrements.
	if s.couponService != nil &&
		order.OrderStatus == domain.OrderStatusCanceled && oldStatus != domain.OrderStatusCanceled &&
		order.CouponCode != nil && *order.CouponCode != "" {
		if coupon, cErr := s.couponService.GetCouponByCode(ctx, *order.CouponCode); cErr != nil || coupon == nil {
			logging.FromContext(ctx).Error("failed to fetch coupon for rollback on cancellation",
				zap.String("order_id", order.ID.String()), zap.String("coupon_code", *order.CouponCode), zap.Error(cErr))
		} else if dErr := s.couponService.DecrementUsageAtomic(ctx, coupon.ID, order.UserID); dErr != nil {
			logging.FromContext(ctx).Error("failed to roll back coupon on cancellation",
				zap.String("order_id", order.ID.String()), zap.String("coupon_code", *order.CouponCode), zap.Error(dErr))
		}
	}

	return nil
}

func (s *orderService) CancelStaleTestOrders(ctx context.Context, olderThan time.Duration) (int, error) {
	ids, err := s.repo.CancelStaleTestOrders(ctx, olderThan)
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := s.repo.InsertStatusHistory(ctx, id, domain.OrderStatusCanceled); err != nil {
			logging.FromContext(ctx).Warn("failed to record auto-cancel status history",
				zap.String("order_id", id.String()), zap.Error(err))
		}
	}
	return len(ids), nil
}

func (s *orderService) GetOrderByID(ctx context.Context, orderID uuid.UUID) (*domain.Order, *[]domain.OrderProductRaw, error) {
	return s.repo.FindByID(ctx, orderID)
}

func (s *orderService) GetStatusHistory(ctx context.Context, orderID uuid.UUID) ([]*domain.OrderStatusHistory, error) {
	return s.repo.FindStatusHistoryByOrderID(ctx, orderID)
}

func (s *orderService) BatchGetOrderProductsByOrderIDs(ctx context.Context, orderIDs []string) (map[string][]*domain.OrderProductRaw, error) {
	return s.repo.FindByOrderIDs(ctx, orderIDs)
}

func (s *orderService) UpdateActiveOrdersLanguage(ctx context.Context, userID uuid.UUID, language string) ([]*domain.Order, error) {
	return s.repo.UpdateActiveOrdersLanguage(ctx, userID, language)
}

func (s *orderService) BatchGetOrdersByUserIDs(ctx context.Context, userIDs []string) (map[string][]*domain.Order, error) {
	return s.repo.FindByUserIDs(ctx, userIDs)
}

func (s *orderService) GetCustomerStats(ctx context.Context, startDate, endDate *time.Time, orderType *string, minOrders *int) ([]*domain.CustomerStatsRow, error) {
	return s.repo.GetCustomerStats(ctx, startDate, endDate, orderType, minOrders)
}

func (s *orderService) GetOrderHistory(ctx context.Context, filter domain.OrderHistoryFilter) ([]*domain.Order, *domain.OrderHistorySummary, error) {
	return s.repo.FindFiltered(ctx, filter)
}
