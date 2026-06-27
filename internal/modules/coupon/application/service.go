package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"

	"tsb-service/internal/modules/coupon/domain"
	"tsb-service/pkg/logging"
)

type CouponService interface {
	ValidateCoupon(ctx context.Context, code string, orderAmount decimal.Decimal, userID uuid.UUID) (*domain.Coupon, decimal.Decimal, error)
	IncrementUsage(ctx context.Context, id uuid.UUID) error
	// IncrementUsageAtomic atomically increments and returns false if the coupon is no longer valid.
	IncrementUsageAtomic(ctx context.Context, id uuid.UUID, userID uuid.UUID) (bool, error)
	// DecrementUsageAtomic rolls back a previous IncrementUsageAtomic (best-effort).
	// Used when order creation or payment initiation fails after reservation, and on
	// payment-failed webhooks. Returns an error only for hard DB failures; missing rows
	// (e.g. counter already at zero) are treated as no-ops.
	DecrementUsageAtomic(ctx context.Context, id uuid.UUID, userID uuid.UUID) error
	GetAllCoupons(ctx context.Context) ([]*domain.Coupon, error)
	GetCoupon(ctx context.Context, id uuid.UUID) (*domain.Coupon, error)
	GetCouponByCode(ctx context.Context, code string) (*domain.Coupon, error)
	CreateCoupon(ctx context.Context, coupon *domain.Coupon) error
	UpdateCoupon(ctx context.Context, coupon *domain.Coupon) error
}

type couponService struct {
	repo domain.CouponRepository
}

func NewCouponService(repo domain.CouponRepository) CouponService {
	return &couponService{repo: repo}
}

func (s *couponService) ValidateCoupon(ctx context.Context, code string, orderAmount decimal.Decimal, userID uuid.UUID) (*domain.Coupon, decimal.Decimal, error) {
	// Daily brute-force guard: block before any lookup once the user has spent
	// their failed attempts for the day (Europe/Brussels). Fail-open if the
	// counter read itself errors — never lock a user out on infra failure.
	if attempts, err := s.repo.CountFailedCouponAttemptsToday(ctx, userID); err != nil {
		logging.FromContext(ctx).Error("failed to read daily coupon attempts",
			zap.String("user_id", userID.String()), zap.Error(err))
	} else if attempts >= domain.MaxFailedCouponAttemptsPerDay {
		return nil, decimal.Zero, &domain.DailyAttemptLimitError{}
	}

	coupon, err := s.repo.FindByCode(ctx, code)
	if err != nil {
		s.recordFailedAttempt(ctx, userID)
		return nil, decimal.Zero, fmt.Errorf("invalid or expired coupon")
	}

	userUsageCount, err := s.repo.GetUserUsageCount(ctx, coupon.ID, userID)
	if err != nil {
		return nil, decimal.Zero, fmt.Errorf("failed to check user usage: %w", err)
	}

	if err := coupon.Validate(orderAmount, userUsageCount); err != nil {
		// Surface the actionable "minimum order amount not met" message so the
		// customer knows how to proceed; keep existence/expiry/limit failures
		// generic to avoid leaking coupon state to enumeration attempts.
		// A valid-but-min-not-met code is not enumeration, so it doesn't count
		// toward the daily limit; every other failure does.
		var minErr *domain.MinOrderNotMetError
		if errors.As(err, &minErr) {
			return coupon, decimal.Zero, minErr
		}
		s.recordFailedAttempt(ctx, userID)
		return coupon, decimal.Zero, fmt.Errorf("invalid or expired coupon")
	}

	discount := coupon.CalculateDiscount(orderAmount)
	return coupon, discount, nil
}

// recordFailedAttempt increments the user's daily failed-attempt counter,
// best-effort: a write failure is logged but never propagated, so a counter
// outage can't break the customer's checkout flow.
func (s *couponService) recordFailedAttempt(ctx context.Context, userID uuid.UUID) {
	if err := s.repo.RecordFailedCouponAttempt(ctx, userID); err != nil {
		logging.FromContext(ctx).Error("failed to record coupon attempt",
			zap.String("user_id", userID.String()), zap.Error(err))
	}
}

func (s *couponService) IncrementUsage(ctx context.Context, id uuid.UUID) error {
	return s.repo.IncrementUsedCount(ctx, id)
}

func (s *couponService) IncrementUsageAtomic(ctx context.Context, id uuid.UUID, userID uuid.UUID) (bool, error) {
	return s.repo.RedeemAtomic(ctx, id, userID)
}

func (s *couponService) DecrementUsageAtomic(ctx context.Context, id uuid.UUID, userID uuid.UUID) error {
	if err := s.repo.DecrementUsageAtomic(ctx, id, userID); err != nil {
		logging.FromContext(ctx).Error("failed to decrement coupon usage",
			zap.String("coupon_id", id.String()),
			zap.String("user_id", userID.String()),
			zap.Error(err))
		return fmt.Errorf("failed to decrement coupon usage: %w", err)
	}
	return nil
}

func (s *couponService) GetCouponByCode(ctx context.Context, code string) (*domain.Coupon, error) {
	return s.repo.FindByCode(ctx, code)
}

func (s *couponService) GetAllCoupons(ctx context.Context) ([]*domain.Coupon, error) {
	return s.repo.FindAll(ctx)
}

func (s *couponService) GetCoupon(ctx context.Context, id uuid.UUID) (*domain.Coupon, error) {
	return s.repo.FindByID(ctx, id)
}

func (s *couponService) CreateCoupon(ctx context.Context, coupon *domain.Coupon) error {
	return s.repo.Save(ctx, coupon)
}

func (s *couponService) UpdateCoupon(ctx context.Context, coupon *domain.Coupon) error {
	return s.repo.Update(ctx, coupon)
}
