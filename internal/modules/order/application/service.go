package application

import (
	"context"
	"fmt"
	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/google/uuid"
	"time"
	"tsb-service/internal/modules/order/domain"
	productDomain "tsb-service/internal/modules/product/domain"
	"tsb-service/pkg/sse"
)

type OrderService interface {
	CreateOrder(ctx context.Context, order *domain.Order, orderProducts *[]domain.OrderProductRaw) (*domain.Order, *[]domain.OrderProductRaw, error)
	GetPaginatedOrders(ctx context.Context, page int, limit int, userID *uuid.UUID) ([]*domain.Order, error)
	UpdateOrderStatus(ctx context.Context, orderID uuid.UUID, status domain.OrderStatus) error
	GetOrderByID(ctx context.Context, orderID uuid.UUID) (*domain.Order, *[]domain.OrderProductRaw, error)
	GetOrderProductsByOrderIDs(ctx context.Context, orderIDs []string) (map[string][]*domain.OrderProductRaw, error)

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

	// Construct an event payload for the orderCreated event.
	eventPayload := fmt.Sprintf(
		`{"event": "orderCreated", "orderID": "%s", "timestamp": "%s"}`,
		order.ID.String(),
		time.Now().Format(time.RFC3339),
	)

	go func() {
		time.Sleep(1 * time.Second)
		// Trigger the event sending via the SSE hub.
		sse.Hub.Broadcast(eventPayload)
	}()

	return order, orderProducts, nil
}

func (s *orderService) GetPaginatedOrders(ctx context.Context, page int, limit int, userID *uuid.UUID) ([]*domain.Order, error) {
	return s.repo.FindPaginated(ctx, page, limit, userID)
}

func (s *orderService) UpdateOrderStatus(ctx context.Context, orderID uuid.UUID, newStatus domain.OrderStatus) error {
	// Retrieve the order
	order, _, err := s.repo.FindByID(ctx, orderID)
	if err != nil {
		return err
	}

	// Update the status in the order struct
	order.OrderStatus = newStatus

	// Update the order in the repository
	if err := s.repo.Update(ctx, order); err != nil {
		return err
	}

	// Construct an event payload (as JSON)
	eventPayload := fmt.Sprintf(
		`{"event": "orderUpdated", "orderID": "%s", "timestamp": "%s"}`,
		orderID,
		time.Now().Format(time.RFC3339),
	)

	go func() {
		time.Sleep(1 * time.Second)
		// Trigger the event sending via the SSE hub.
		sse.Hub.Broadcast(eventPayload)
	}()

	return nil
}

func (s *orderService) GetOrderByID(ctx context.Context, orderID uuid.UUID) (*domain.Order, *[]domain.OrderProductRaw, error) {
	return s.repo.FindByID(ctx, orderID)
}

func (s *orderService) GetOrderProductsByOrderIDs(ctx context.Context, orderIDs []string) (map[string][]*domain.OrderProductRaw, error) {
	return s.repo.FindByOrderIDs(ctx, orderIDs)
}

func (s *orderService) BatchGetOrdersByUserIDs(ctx context.Context, userIDs []string) (map[string][]*domain.Order, error) {
	return s.repo.FindByUserIDs(ctx, userIDs)
}
