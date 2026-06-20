package domain

import (
	"crypto/rand"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// NormalizeCode canonicalises a coupon code so lookups and uniqueness are
// case- and whitespace-insensitive (e.g. " summer " and "SUMMER" match).
func NormalizeCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

// codeAlphabet excludes visually ambiguous characters (0/O, 1/I) so a printed
// coupon code is easy to read back and type.
const codeAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"

// generatedCodeLength is the number of random characters after the prefix.
const generatedCodeLength = 6

// GenerateCode returns a random, human-readable coupon code (e.g. "TSB-7F3K9Q").
// Callers should retry on a unique-constraint violation; the alphabet/length
// give ~10^9 combinations so collisions are rare but possible.
func GenerateCode() (string, error) {
	buf := make([]byte, generatedCodeLength)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate coupon code: %w", err)
	}
	out := make([]byte, generatedCodeLength)
	for i, b := range buf {
		out[i] = codeAlphabet[int(b)%len(codeAlphabet)]
	}
	return "TSB-" + string(out), nil
}

// MinOrderNotMetError signals that the order amount is below the coupon's
// minimum. It is the one validation failure whose message is safe (and useful)
// to surface to the customer, since they already hold a valid code.
type MinOrderNotMetError struct {
	Required decimal.Decimal
}

func (e *MinOrderNotMetError) Error() string {
	return fmt.Sprintf("minimum order amount of %s not met", e.Required.String())
}

// MaxFailedCouponAttemptsPerDay caps how many failed coupon validations a single
// user may make per calendar day, to block brute-force code enumeration.
const MaxFailedCouponAttemptsPerDay = 5

// DailyAttemptLimitError signals the user has exhausted their daily coupon
// validation attempts (brute-force guard).
type DailyAttemptLimitError struct{}

func (e *DailyAttemptLimitError) Error() string {
	return "too many coupon attempts today, please try again tomorrow"
}

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
		return &MinOrderNotMetError{Required: *c.MinOrderAmount}
	}

	return nil
}

// CalculateDiscount returns the discount amount for the given order amount.
func (c *Coupon) CalculateDiscount(orderAmount decimal.Decimal) decimal.Decimal {
	switch c.DiscountType {
	case DiscountTypePercentage:
		// e.g. 10% → orderAmount * 10 / 100. Clamp to the order amount as a
		// defense-in-depth guard: a misconfigured >100% coupon must never
		// produce a discount larger than the order itself.
		discount := orderAmount.Mul(c.DiscountValue).Div(decimal.NewFromInt(100)).Round(2)
		if discount.GreaterThan(orderAmount) {
			return orderAmount
		}
		return discount
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
