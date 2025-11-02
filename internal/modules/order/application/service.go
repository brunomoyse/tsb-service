package application

import (
	"context"
	"fmt"
	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/google/uuid"
	"time"
	"tsb-service/internal/modules/order/domain"
	productDomain "tsb-service/internal/modules/product/domain"
)

type OrderService interface {
	CreateOrder(ctx context.Context, order *domain.Order, orderProducts *[]domain.OrderProductRaw) (*domain.Order, *[]domain.OrderProductRaw, error)
	GetPaginatedOrders(ctx context.Context, page int, limit int, userID *uuid.UUID) ([]*domain.Order, error)
	UpdateOrder(ctx context.Context, orderID uuid.UUID, newStatus *domain.OrderStatus, estimatedReadyTime *time.Time) error
	GetOrderByID(ctx context.Context, orderID uuid.UUID) (*domain.Order, *[]domain.OrderProductRaw, error)

	// Platform order methods
	CreatePlatformOrder(ctx context.Context, order *domain.Order) (*domain.Order, error)
	CreatePlatformOrderWithProducts(ctx context.Context, order *domain.Order, orderProducts []domain.OrderProductRaw) (*domain.Order, error)
	UpdatePlatformOrderStatus(ctx context.Context, platformOrderID string, source domain.OrderSource, newStatus domain.OrderStatus) (*domain.Order, error)
	GetOrderByPlatformID(ctx context.Context, platformOrderID string, source domain.OrderSource) (*domain.Order, error)

	BatchGetOrderProductsByOrderIDs(ctx context.Context, orderIDs []string) (map[string][]*domain.OrderProductRaw, error)
	BatchGetOrdersByUserIDs(ctx context.Context, userIDs []string) (map[string][]*domain.Order, error)
}

type orderService struct {
	repo         domain.OrderRepository
	productRepo  productDomain.ProductRepository
	mollieClient *mollie.Client
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

	// Check if there a new status
	if newStatus != nil {
		order.OrderStatus = *newStatus
	}

	// Check if there is a new estimated ready time
	if estimatedReadyTime != nil {
		order.EstimatedReadyTime = estimatedReadyTime
	}

	return s.repo.Update(ctx, order)
}

func (s *orderService) GetOrderByID(ctx context.Context, orderID uuid.UUID) (*domain.Order, *[]domain.OrderProductRaw, error) {
	return s.repo.FindByID(ctx, orderID)
}

func (s *orderService) BatchGetOrderProductsByOrderIDs(ctx context.Context, orderIDs []string) (map[string][]*domain.OrderProductRaw, error) {
	return s.repo.FindByOrderIDs(ctx, orderIDs)
}

func (s *orderService) BatchGetOrdersByUserIDs(ctx context.Context, userIDs []string) (map[string][]*domain.Order, error) {
	return s.repo.FindByUserIDs(ctx, userIDs)
}

// CreatePlatformOrder creates an order from a platform (Deliveroo, Uber, etc.)
func (s *orderService) CreatePlatformOrder(ctx context.Context, order *domain.Order) (*domain.Order, error) {
	// Save platform order without order products (platform orders manage their own items)
	createdOrder, _, err := s.repo.Save(ctx, order, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to save platform order: %w", err)
	}
	return createdOrder, nil
}

// CreatePlatformOrderWithProducts creates an order from a platform with mapped order products
func (s *orderService) CreatePlatformOrderWithProducts(ctx context.Context, order *domain.Order, orderProducts []domain.OrderProductRaw) (*domain.Order, error) {
	// Save platform order with order products
	createdOrder, _, err := s.repo.Save(ctx, order, &orderProducts)
	if err != nil {
		return nil, fmt.Errorf("failed to save platform order with products: %w", err)
	}
	return createdOrder, nil
}

// UpdatePlatformOrderStatus updates the status of a platform order
func (s *orderService) UpdatePlatformOrderStatus(ctx context.Context, platformOrderID string, source domain.OrderSource, newStatus domain.OrderStatus) (*domain.Order, error) {
	// Get the order by platform ID
	order, err := s.repo.FindByPlatformOrderID(ctx, platformOrderID, source)
	if err != nil {
		return nil, fmt.Errorf("failed to find platform order: %w", err)
	}

	// Update status
	order.OrderStatus = newStatus
	order.UpdatedAt = time.Now()

	// Save
	err = s.repo.Update(ctx, order)
	if err != nil {
		return nil, fmt.Errorf("failed to update platform order: %w", err)
	}

	return order, nil
}

// GetOrderByPlatformID retrieves an order by its platform-specific order ID
func (s *orderService) GetOrderByPlatformID(ctx context.Context, platformOrderID string, source domain.OrderSource) (*domain.Order, error) {
	return s.repo.FindByPlatformOrderID(ctx, platformOrderID, source)
}
