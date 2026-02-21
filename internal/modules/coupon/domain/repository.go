package domain

import (
	"context"

	"github.com/google/uuid"
)

type CouponRepository interface {
	FindByCode(ctx context.Context, code string) (*Coupon, error)
	FindByID(ctx context.Context, id uuid.UUID) (*Coupon, error)
	FindAll(ctx context.Context) ([]*Coupon, error)
	Save(ctx context.Context, coupon *Coupon) error
	Update(ctx context.Context, coupon *Coupon) error
	IncrementUsedCount(ctx context.Context, id uuid.UUID) error
	// IncrementUsedCountAtomic atomically increments usage only if the coupon is still valid.
	// Returns true if the increment succeeded, false if the coupon is no longer valid/available.
	IncrementUsedCountAtomic(ctx context.Context, id uuid.UUID) (bool, error)
}
