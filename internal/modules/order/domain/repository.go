package domain

import (
	"context"

	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/google/uuid"
)

type OrderRepository interface {
	Save(ctx context.Context, client *mollie.Client, order *Order) (*Order, error)
	Update(ctx context.Context, order *Order) error
	FindByUserID(ctx context.Context, userID uuid.UUID) ([]*Order, error)
	FindByID(ctx context.Context, orderID uuid.UUID) (*Order, error)
	FindPaginated(ctx context.Context, page int, limit int) ([]*Order, error)
	OrderFillPrices(ctx context.Context, order *Order) (*Order, error)
}
