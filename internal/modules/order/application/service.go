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

func (s *orderService) CreateOrder(ctx context.Context, userID uuid.UUID, products []domain.PaymentLine, paymentMode domain.PaymentMode) (*domain.Order, error) {
	order := domain.NewOrder(userID, products, paymentMode)

	return s.repo.Save(ctx, s.mollieClient, &order)
}

func (s *orderService) GetOrdersByUserID(ctx context.Context, userID uuid.UUID) ([]*domain.Order, error) {
	return s.repo.FindByUserID(ctx, userID)
}
