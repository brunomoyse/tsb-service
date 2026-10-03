package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"tsb-service/internal/modules/coupon/domain"
)

type fakeRepo struct {
	domain.CouponRepository // unused methods panic if called
	findErr                 error
	coupon                  *domain.Coupon
	usageErr                error
	recorded                int
}

func (f *fakeRepo) CountFailedCouponAttemptsToday(context.Context, uuid.UUID) (int, error) {
	return 0, nil
}
func (f *fakeRepo) FindByCode(context.Context, string) (*domain.Coupon, error) {
	return f.coupon, f.findErr
}
func (f *fakeRepo) GetUserUsageCount(context.Context, uuid.UUID, uuid.UUID) (int, error) {
	return 0, f.usageErr
}
func (f *fakeRepo) RecordFailedCouponAttempt(context.Context, uuid.UUID) error {
	f.recorded++
	return nil
}

func validCoupon() *domain.Coupon {
	return &domain.Coupon{
		ID: uuid.New(), DiscountType: domain.DiscountTypeFixed, DiscountValue: decimal.NewFromInt(5),
		IsActive: true,
	}
}

func TestValidateCoupon_InfrastructureFailuresAreNotRefusals(t *testing.T) {
	boom := errors.New("connection refused")
	cases := map[string]*fakeRepo{
		"usage count fails": {coupon: validCoupon(), usageErr: boom},
		"lookup fails":      {findErr: fmt.Errorf("coupon not found: %w", boom)},
	}
	for name, repo := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := NewCouponService(repo).ValidateCoupon(t.Context(), "X", decimal.NewFromInt(20), uuid.New())
			if _, ok := errors.AsType[*domain.CheckFailedError](err); !ok {
				t.Fatalf("err = %v, want *CheckFailedError", err)
			}
			if !errors.Is(err, boom) {
				t.Error("the cause must stay reachable for logs / Sentry")
			}
			if repo.recorded != 0 {
				t.Error("a server fault must not burn one of the customer's daily attempts")
			}
		})
	}
}

func TestValidateCoupon_UnknownCodeIsARefusalThatCounts(t *testing.T) {
	repo := &fakeRepo{findErr: fmt.Errorf("coupon not found: %w", sql.ErrNoRows)}
	_, _, err := NewCouponService(repo).ValidateCoupon(t.Context(), "NOPE", decimal.NewFromInt(20), uuid.New())
	var checkErr *domain.CheckFailedError
	if err == nil || errors.As(err, &checkErr) {
		t.Fatalf("err = %v, want a plain refusal", err)
	}
	if repo.recorded != 1 {
		t.Errorf("recorded = %d, want 1", repo.recorded)
	}
}
