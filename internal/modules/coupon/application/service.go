package application

import (
	"context"
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
	coupon, err := s.repo.FindByCode(ctx, code)
	if err != nil {
		return nil, decimal.Zero, fmt.Errorf("invalid or expired coupon")
	}

	userUsageCount, err := s.repo.GetUserUsageCount(ctx, coupon.ID, userID)
	if err != nil {
		return nil, decimal.Zero, fmt.Errorf("failed to check user usage: %w", err)
	}

	if err := coupon.Validate(orderAmount, userUsageCount); err != nil {
		return coupon, decimal.Zero, fmt.Errorf("invalid or expired coupon")
	}

	discount := coupon.CalculateDiscount(orderAmount)
	return coupon, discount, nil
}

func (s *couponService) IncrementUsage(ctx context.Context, id uuid.UUID) error {
	return s.repo.IncrementUsedCount(ctx, id)
}

func (s *couponService) IncrementUsageAtomic(ctx context.Context, id uuid.UUID, userID uuid.UUID) (bool, error) {
	return s.repo.RedeemAtomic(ctx, id, userID)
}

func (s *couponService) DecrementUsageAtomic(ctx context.Context, id uuid.UUID, userID uuid.UUID) error {
	log := logging.FromContext(ctx)

	if _, err := s.repo.DecrementUserUsageAtomic(ctx, id, userID); err != nil {
		log.Error("failed to decrement per-user coupon usage",
			zap.String("coupon_id", id.String()),
			zap.String("user_id", userID.String()),
			zap.Error(err))
		return fmt.Errorf("failed to decrement per-user coupon usage: %w", err)
	}

	if _, err := s.repo.DecrementUsedCountAtomic(ctx, id); err != nil {
		log.Error("failed to decrement global coupon usage",
			zap.String("coupon_id", id.String()),
			zap.Error(err))
		return fmt.Errorf("failed to decrement global coupon usage: %w", err)
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
