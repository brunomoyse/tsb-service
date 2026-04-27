package domain

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type DiscountType string

const (
	DiscountTypePercentage DiscountType = "percentage"
	DiscountTypeFixed      DiscountType = "fixed"
)

type Status string

const (
	StatusActive    Status = "active"
	StatusInactive  Status = "inactive"
	StatusScheduled Status = "scheduled"
	StatusExpired   Status = "expired"
	StatusExhausted Status = "exhausted"
)

type Coupon struct {
	ID             uuid.UUID       `db:"id"`
	Code           string          `db:"code"`
	DiscountType   DiscountType    `db:"discount_type"`
	DiscountValue  decimal.Decimal `db:"discount_value"`
	MinOrderAmount *decimal.Decimal `db:"min_order_amount"`
	MaxUses        *int            `db:"max_uses"`
	MaxUsesPerUser *int            `db:"max_uses_per_user"`
	UsedCount      int             `db:"used_count"`
	IsActive       bool            `db:"is_active"`
	ValidFrom      *time.Time      `db:"valid_from"`
	ValidUntil     *time.Time      `db:"valid_until"`
	CreatedAt      time.Time       `db:"created_at"`
}

// Status returns the effective status of the coupon, combining the admin
// IsActive flag with validity window and global usage limits.
// Admin intent (IsActive=false) takes precedence so the dashboard can
// distinguish a manually disabled coupon from an expired/exhausted one.
func (c *Coupon) Status() Status {
	if !c.IsActive {
		return StatusInactive
	}
	now := time.Now()
	if c.ValidFrom != nil && now.Before(*c.ValidFrom) {
		return StatusScheduled
	}
	if c.ValidUntil != nil && now.After(*c.ValidUntil) {
		return StatusExpired
	}
	if c.MaxUses != nil && c.UsedCount >= *c.MaxUses {
		return StatusExhausted
	}
	return StatusActive
}

// Validate checks whether the coupon can be applied to an order with the given amount.
// userUsageCount is the number of times the current user has already used this coupon.
func (c *Coupon) Validate(orderAmount decimal.Decimal, userUsageCount int) error {
	if !c.IsActive {
		return fmt.Errorf("coupon is not active")
	}

	now := time.Now()
	if c.ValidFrom != nil && now.Before(*c.ValidFrom) {
		return fmt.Errorf("coupon is not yet valid")
	}
	if c.ValidUntil != nil && now.After(*c.ValidUntil) {
		return fmt.Errorf("coupon has expired")
	}

	if c.MaxUses != nil && c.UsedCount >= *c.MaxUses {
		return fmt.Errorf("coupon usage limit reached")
	}

	if c.MaxUsesPerUser != nil && userUsageCount >= *c.MaxUsesPerUser {
		return fmt.Errorf("coupon per-user usage limit reached")
	}

	if c.MinOrderAmount != nil && orderAmount.LessThan(*c.MinOrderAmount) {
		return fmt.Errorf("minimum order amount of %s not met", c.MinOrderAmount.String())
	}

	return nil
}

// CalculateDiscount returns the discount amount for the given order amount.
func (c *Coupon) CalculateDiscount(orderAmount decimal.Decimal) decimal.Decimal {
	switch c.DiscountType {
	case DiscountTypePercentage:
		// e.g. 10% → orderAmount * 10 / 100
		return orderAmount.Mul(c.DiscountValue).Div(decimal.NewFromInt(100)).Round(2)
	case DiscountTypeFixed:
		// Fixed discount capped at order amount
		if c.DiscountValue.GreaterThan(orderAmount) {
			return orderAmount
		}
		return c.DiscountValue
	default:
		return decimal.Zero
	}
}
