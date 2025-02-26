package service

import (
	"context"

	"tsb-service/internal/order"
	"tsb-service/internal/order/repository"

	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/google/uuid"
)

// OrderService defines the business operations for orders.
type OrderService interface {
	// CreateOrder creates a new order with a Mollie payment.
	CreateOrder(ctx context.Context, client *mollie.Client, form order.CreateOrderForm, currentUserLang string, currentUserId uuid.UUID) (*order.Order, error)
	// GetOrdersForUser returns all orders for a given user.
	GetOrdersForUser(ctx context.Context, userId uuid.UUID) ([]order.Order, error)
	// GetOrderById returns a single order by its ID.
	GetOrderById(ctx context.Context, orderId uuid.UUID) (*order.Order, error)
	// UpdateOrderStatus updates the order's status based on the Mollie payment status.
	UpdateOrderStatus(ctx context.Context, paymentID string, paymentStatus string) error
}

type orderService struct {
	repo repository.OrderRepository
}

// NewOrderService creates a new OrderService instance with the provided repository.
func NewOrderService(repo repository.OrderRepository) OrderService {
	return &orderService{repo: repo}
}

// CreateOrder creates an order by delegating to the repository.
func (s *orderService) CreateOrder(ctx context.Context, client *mollie.Client, form order.CreateOrderForm, currentUserLang string, currentUserId uuid.UUID) (*order.Order, error) {
	return s.repo.CreateOrder(ctx, client, form, currentUserLang, currentUserId)
}

// GetOrdersForUser retrieves all orders for a specific user.
func (s *orderService) GetOrdersForUser(ctx context.Context, userId uuid.UUID) ([]order.Order, error) {
	return s.repo.GetOrdersForUser(ctx, userId)
}

// GetOrderById retrieves an order by its ID.
func (s *orderService) GetOrderById(ctx context.Context, orderId uuid.UUID) (*order.Order, error) {
	return s.repo.GetOrderById(ctx, orderId)
}

// UpdateOrderStatus updates the order status based on payment feedback.
func (s *orderService) UpdateOrderStatus(ctx context.Context, paymentID string, paymentStatus string) error {
	return s.repo.UpdateOrderStatus(ctx, paymentID, paymentStatus)
}