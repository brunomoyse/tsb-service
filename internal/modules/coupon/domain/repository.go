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
	// RedeemAtomic performs the full redemption under a single transaction:
	// takes a row lock on the coupon, re-validates activity/window/global-cap,
	// increments per-user usage (bounded by max_uses_per_user), and increments
	// the global counter — committing all three or none. Returns true on
	// successful redemption, false when the coupon is no longer available
	// (expired, exhausted, per-user cap reached, or deactivated).
	RedeemAtomic(ctx context.Context, couponID, userID uuid.UUID) (bool, error)
	// GetUserUsageCount returns how many times a specific user has used a coupon.
	GetUserUsageCount(ctx context.Context, couponID, userID uuid.UUID) (int, error)
	// DecrementUsedCountAtomic rolls back a previous global increment.
	// Guards against used_count going negative.
	DecrementUsedCountAtomic(ctx context.Context, id uuid.UUID) (bool, error)
	// DecrementUserUsageAtomic rolls back a previous per-user increment.
	// Guards against used_count going negative.
	DecrementUserUsageAtomic(ctx context.Context, couponID, userID uuid.UUID) (bool, error)
}
