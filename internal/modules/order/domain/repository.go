package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// OrderHistoryFilter holds filter parameters for querying order history.
type OrderHistoryFilter struct {
	Page      int
	Limit     int
	StartDate *time.Time
	EndDate   *time.Time
	Status    *OrderStatus
	OrderType *OrderType
	Search    *string // customer name search (via JOIN on users)
}

// OrderHistorySummary holds aggregate stats for filtered orders.
type OrderHistorySummary struct {
	TotalOrders  int    `db:"total_orders"`
	TotalRevenue string `db:"total_revenue"`
	AverageOrder string `db:"average_order"`
}

type OrderRepository interface {
	Save(ctx context.Context, order *Order, orderProducts *[]OrderProductRaw) (*Order, *[]OrderProductRaw, error)
	Update(ctx context.Context, order *Order) error
	FindByID(ctx context.Context, orderID uuid.UUID) (*Order, *[]OrderProductRaw, error)
	FindPaginated(ctx context.Context, page int, limit int, userID *uuid.UUID) ([]*Order, error)
	FindFiltered(ctx context.Context, filter OrderHistoryFilter) ([]*Order, *OrderHistorySummary, error)
	FindByOrderIDs(ctx context.Context, orderIDs []string) (map[string][]*OrderProductRaw, error)
	FindByUserIDs(ctx context.Context, userIDs []string) (map[string][]*Order, error)
	// UpdateActiveOrdersLanguage sets the language on all the user's non-terminal
	// orders and returns the affected orders (id, user, status, type, language) so
	// callers can re-push their Live Activities in the new language.
	UpdateActiveOrdersLanguage(ctx context.Context, userID uuid.UUID, language string) ([]*Order, error)
	InsertStatusHistory(ctx context.Context, orderID uuid.UUID, status OrderStatus) error
	FindStatusHistoryByOrderID(ctx context.Context, orderID uuid.UUID) ([]*OrderStatusHistory, error)
	DeleteOrder(ctx context.Context, orderID uuid.UUID) error
	GetCustomerStats(ctx context.Context, startDate, endDate *time.Time, orderType *string, minOrders *int) ([]*CustomerStatsRow, error)
}
