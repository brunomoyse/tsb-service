package application

import (
	"context"
	"fmt"
	"time"
	"tsb-service/internal/modules/order/domain"
	"tsb-service/pkg/sse"

	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/google/uuid"
)

type OrderService interface {
	CreateOrder(ctx context.Context, userID uuid.UUID, products []domain.PaymentLine, paymentMode domain.PaymentMode) (*domain.Order, error)
	GetOrdersByUserID(ctx context.Context, userID uuid.UUID) ([]*domain.Order, error)
	GetPaginatedOrders(ctx context.Context, page int, limit int) ([]*domain.Order, error)
	UpdateOrderStatus(ctx context.Context, orderID uuid.UUID, status domain.OrderStatus) error
	GetOrderByID(ctx context.Context, orderID uuid.UUID) (*domain.Order, error)
}

type orderService struct {
	repo         domain.OrderRepository
	mollieClient *mollie.Client
}

func NewOrderService(repo domain.OrderRepository, mollieClient *mollie.Client) OrderService {
	return &orderService{
		repo:         repo,
		mollieClient: mollieClient,
	}
}

func (s *orderService) CreateOrder(ctx context.Context, userID uuid.UUID, paymentLines []domain.PaymentLine, paymentMode domain.PaymentMode) (*domain.Order, error) {
	order := domain.NewOrder(userID, paymentLines)

	// Load product prices from DB
	updatedOrder, err := s.repo.OrderFillPrices(ctx, &order)
	if err != nil {
		return nil, err
	}

	savedOrder, err := s.repo.Save(ctx, s.mollieClient, updatedOrder)
	if err != nil {
		return nil, err
	}

	// Construct an event payload for the orderCreated event.
	eventPayload := fmt.Sprintf(
		`{"event": "orderCreated", "orderID": "%s", "timestamp": "%s"}`,
		savedOrder.ID.String(),
		time.Now().Format(time.RFC3339),
	)
	// Broadcast the event to all connected SSE clients.
	sse.Hub.Broadcast(eventPayload)

	return savedOrder, nil
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
	order.Status = newStatus

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
