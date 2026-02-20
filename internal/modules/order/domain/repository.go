package domain

import (
	"context"

	"github.com/google/uuid"
)

type OrderRepository interface {
	Save(ctx context.Context, order *Order, orderProducts *[]OrderProductRaw) (*Order, *[]OrderProductRaw, error)
	Update(ctx context.Context, order *Order) error
	FindByID(ctx context.Context, orderID uuid.UUID) (*Order, *[]OrderProductRaw, error)
	FindPaginated(ctx context.Context, page int, limit int, userID *uuid.UUID) ([]*Order, error)
	FindByOrderIDs(ctx context.Context, orderIDs []string) (map[string][]*OrderProductRaw, error)
	FindByUserIDs(ctx context.Context, userIDs []string) (map[string][]*Order, error)
	InsertStatusHistory(ctx context.Context, orderID uuid.UUID, status OrderStatus) error
	FindStatusHistoryByOrderID(ctx context.Context, orderID uuid.UUID) ([]*OrderStatusHistory, error)
}
