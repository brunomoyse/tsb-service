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
	FindByOrderIDs(ctx context.Context, orderIDs []uuid.UUID) (map[uuid.UUID][]OrderProductRaw, error)
}
