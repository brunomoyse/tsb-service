package application

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"tsb-service/internal/modules/coupon/domain"
)

type CouponService interface {
	ValidateCoupon(ctx context.Context, code string, orderAmount decimal.Decimal) (*domain.Coupon, decimal.Decimal, error)
	IncrementUsage(ctx context.Context, id uuid.UUID) error
	// IncrementUsageAtomic atomically increments and returns false if the coupon is no longer valid.
	IncrementUsageAtomic(ctx context.Context, id uuid.UUID) (bool, error)
	GetAllCoupons(ctx context.Context) ([]*domain.Coupon, error)
	GetCoupon(ctx context.Context, id uuid.UUID) (*domain.Coupon, error)
	CreateCoupon(ctx context.Context, coupon *domain.Coupon) error
	UpdateCoupon(ctx context.Context, coupon *domain.Coupon) error
}

type couponService struct {
	repo domain.CouponRepository
}

func NewCouponService(repo domain.CouponRepository) CouponService {
	return &couponService{repo: repo}
}

func (s *couponService) ValidateCoupon(ctx context.Context, code string, orderAmount decimal.Decimal) (*domain.Coupon, decimal.Decimal, error) {
	coupon, err := s.repo.FindByCode(ctx, code)
	if err != nil {
		return nil, decimal.Zero, fmt.Errorf("invalid or expired coupon")
	}

	if err := coupon.Validate(orderAmount); err != nil {
		return coupon, decimal.Zero, fmt.Errorf("invalid or expired coupon")
	}

	discount := coupon.CalculateDiscount(orderAmount)
	return coupon, discount, nil
}

func (s *couponService) IncrementUsage(ctx context.Context, id uuid.UUID) error {
	return s.repo.IncrementUsedCount(ctx, id)
}

func (s *couponService) IncrementUsageAtomic(ctx context.Context, id uuid.UUID) (bool, error) {
	return s.repo.IncrementUsedCountAtomic(ctx, id)
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
