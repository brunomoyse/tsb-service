package domain

import (
	"context"

	"github.com/google/uuid"
)

type OrderRepository interface {
	Save(ctx context.Context, order *Order, orderProducts *[]OrderProductRaw) (*Order, *[]OrderProductRaw, error)
	Update(ctx context.Context, order *Order) error
	FindByUserID(ctx context.Context, userID uuid.UUID) ([]*Order, error)
	FindByID(ctx context.Context, orderID uuid.UUID) (*Order, error)
	FindPaginated(ctx context.Context, page int, limit int) ([]*Order, error)
}
