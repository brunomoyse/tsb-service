package application

import (
	"context"
	"fmt"
	"time"
	"tsb-service/internal/modules/order/domain"
	productDomain "tsb-service/internal/modules/product/domain"
	"tsb-service/pkg/sse"

	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/google/uuid"
)

type OrderService interface {
	CreateOrder(ctx context.Context, order *domain.Order, orderProducts *[]domain.OrderProduct) (*domain.Order, *[]domain.OrderProduct, error)
	GetOrdersByUserID(ctx context.Context, userID uuid.UUID) ([]*domain.Order, error)
	GetPaginatedOrders(ctx context.Context, page int, limit int) ([]*domain.Order, error)
	UpdateOrderStatus(ctx context.Context, orderID uuid.UUID, status domain.OrderStatus) error
	GetOrderByID(ctx context.Context, orderID uuid.UUID) (*domain.Order, error)
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

func (s *orderService) CreateOrder(ctx context.Context, o *domain.Order, op *[]domain.OrderProduct) (*domain.Order, *[]domain.OrderProduct, error) {

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
	// Broadcast the event to all connected SSE clients.
	sse.Hub.Broadcast(eventPayload)

	return order, orderProducts, nil
}

func (s *orderService) GetOrdersByUserID(ctx context.Context, userID uuid.UUID) ([]*domain.Order, error) {
	return s.repo.FindByUserID(ctx, userID)
}

func (s *orderService) GetPaginatedOrders(ctx context.Context, page int, limit int) ([]*domain.Order, error) {
	return s.repo.FindPaginated(ctx, page, limit)
}

func (s *orderService) UpdateOrderStatus(ctx context.Context, orderID uuid.UUID, newStatus domain.OrderStatus) error {
	// Retrieve the order
	order, err := s.repo.FindByID(ctx, orderID)
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
		`{"event": "orderStatusUpdated", "orderID": "%s", "newStatus": "%s", "timestamp": "%s"}`,
		orderID.String(),
		newStatus,
		time.Now().Format(time.RFC3339),
	)

	// Trigger the event sending via the SSE hub.
	sse.Hub.Broadcast(eventPayload)

	return nil
}

func (s *orderService) GetOrderByID(ctx context.Context, orderID uuid.UUID) (*domain.Order, error) {
	return s.repo.FindByID(ctx, orderID)
}
