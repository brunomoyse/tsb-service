package application

import (
	"context"

	"tsb-service/internal/modules/payment/domain"
	"tsb-service/pkg/db"
)

type contextKey string

const (
	orderPaymentLoaderKey contextKey = "orderPaymentLoader"
)

type OrderPaymentLoader struct {
	Loader *db.TypedLoader[*domain.MolliePayment]
}

// AttachDataLoaders attaches all necessary DataLoaders for payments to the context.
func AttachDataLoaders(ctx context.Context, ps PaymentService) context.Context {
	ctx = context.WithValue(ctx, orderPaymentLoaderKey, NewOrderPaymentLoader(ps))

	return ctx
}

// NewOrderPaymentLoader creates a new Order -> Payment loader.
func NewOrderPaymentLoader(ps PaymentService) *OrderPaymentLoader {
	return &OrderPaymentLoader{
		Loader: db.NewTypedLoader[*domain.MolliePayment](
			func(ctx context.Context, orderIDs []string) (map[string][]*domain.MolliePayment, error) {
				return ps.BatchGetPaymentsByOrderIDs(ctx, orderIDs)
			},
			"failed to fetch categories",
		),
	}
}

// GetOrderPaymentLoader reads the loader from context.
func GetOrderPaymentLoader(ctx context.Context) *OrderPaymentLoader {
	loader, ok := ctx.Value(orderPaymentLoaderKey).(*OrderPaymentLoader)
	if !ok {
		return nil
	}
	return loader
}
