package application

import (
	"context"
	"tsb-service/internal/modules/order/domain"

	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/google/uuid"
)

type OrderService interface {
	CreateOrder(ctx context.Context, userID uuid.UUID, products []domain.PaymentLine, paymentMode domain.PaymentMode) (*domain.Order, error)
	GetOrdersByUserID(ctx context.Context, userID uuid.UUID) ([]*domain.Order, error)
	GetPaginatedOrders(ctx context.Context, page int, limit int) ([]*domain.Order, error)
	UpdateOrderStatus(ctx context.Context, orderID uuid.UUID, status domain.OrderStatus) error
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

	return s.repo.Save(ctx, s.mollieClient, updatedOrder)
}

func (s *orderService) GetOrdersByUserID(ctx context.Context, userID uuid.UUID) ([]*domain.Order, error) {
	return s.repo.FindByUserID(ctx, userID)
}

func (s *orderService) GetPaginatedOrders(ctx context.Context, page int, limit int) ([]*domain.Order, error) {
	return s.repo.FindPaginated(ctx, page, limit)
}

func (s *orderService) UpdateOrderStatus(ctx context.Context, orderID uuid.UUID, status domain.OrderStatus) error {
	order, err := s.repo.FindByID(ctx, orderID)
	if err != nil {
		return err
	}

	order.Status = status

	return s.repo.Update(ctx, order)
}
