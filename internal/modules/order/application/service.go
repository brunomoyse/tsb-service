package application

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"tsb-service/internal/modules/order/domain"
	"tsb-service/pkg/logging"
)

type OrderService interface {
	CreateOrder(ctx context.Context, order *domain.Order, orderProducts *[]domain.OrderProductRaw) (*domain.Order, *[]domain.OrderProductRaw, error)
	GetPaginatedOrders(ctx context.Context, page int, limit int, userID *uuid.UUID) ([]*domain.Order, error)
	UpdateOrder(ctx context.Context, orderID uuid.UUID, newStatus *domain.OrderStatus, estimatedReadyTime *time.Time) error
	GetOrderByID(ctx context.Context, orderID uuid.UUID) (*domain.Order, *[]domain.OrderProductRaw, error)
	GetStatusHistory(ctx context.Context, orderID uuid.UUID) ([]*domain.OrderStatusHistory, error)

	BatchGetOrderProductsByOrderIDs(ctx context.Context, orderIDs []string) (map[string][]*domain.OrderProductRaw, error)
	BatchGetOrdersByUserIDs(ctx context.Context, userIDs []string) (map[string][]*domain.Order, error)
}

type orderService struct {
	repo domain.OrderRepository
}

func NewOrderService(repo domain.OrderRepository) OrderService {
	return &orderService{
		repo: repo,
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

func (s *orderService) GetPaginatedOrders(ctx context.Context, page int, limit int, userID *uuid.UUID) ([]*domain.Order, error) {
	return s.repo.FindPaginated(ctx, page, limit, userID)
}

func (s *orderService) UpdateOrder(ctx context.Context, orderID uuid.UUID, newStatus *domain.OrderStatus, estimatedReadyTime *time.Time) error {
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

	if err := s.repo.Update(ctx, order); err != nil {
		return err
	}

	// Record status change in history
	if order.OrderStatus != oldStatus {
		if err := s.repo.InsertStatusHistory(ctx, order.ID, order.OrderStatus); err != nil {
			logging.FromContext(ctx).Error("failed to record status history", zap.String("order_id", order.ID.String()), zap.String("status", string(order.OrderStatus)), zap.Error(err))
		}
	}

	return nil
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

func (s *orderService) BatchGetOrdersByUserIDs(ctx context.Context, userIDs []string) (map[string][]*domain.Order, error) {
	return s.repo.FindByUserIDs(ctx, userIDs)
}
