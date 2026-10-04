package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/coupon/domain"
)

// scriptedRepo is a CouponRepository whose answers and failures are set per test; it records writes.
type scriptedRepo struct {
	coupon        *domain.Coupon
	findErr       error
	attempts      int
	attemptsErr   error
	usage         int
	recordErr     error
	recorded      int
	findCalls     int
	usageCalls    int
	incremented   []uuid.UUID
	incrementErr  error
	redeemed      bool
	redeemErr     error
	redeemArgs    [2]uuid.UUID
	decrementErr  error
	decrementArgs [2]uuid.UUID
	all           []*domain.Coupon
	allErr        error
	saved, update *domain.Coupon
	writeErr      error
	lastCode      string
}

func (r *scriptedRepo) FindByCode(_ context.Context, code string) (*domain.Coupon, error) {
	r.findCalls++
	r.lastCode = code
	return r.coupon, r.findErr
}
func (r *scriptedRepo) FindByID(context.Context, uuid.UUID) (*domain.Coupon, error) {
	return r.coupon, r.findErr
}
func (r *scriptedRepo) FindAll(context.Context) ([]*domain.Coupon, error) { return r.all, r.allErr }
func (r *scriptedRepo) Save(_ context.Context, c *domain.Coupon) error {
	r.saved = c
	return r.writeErr
}
func (r *scriptedRepo) Update(_ context.Context, c *domain.Coupon) error {
	r.update = c
	return r.writeErr
}
func (r *scriptedRepo) IncrementUsedCount(_ context.Context, id uuid.UUID) error {
	r.incremented = append(r.incremented, id)
	return r.incrementErr
}
func (r *scriptedRepo) RedeemAtomic(_ context.Context, c, u uuid.UUID) (bool, error) {
	r.redeemArgs = [2]uuid.UUID{c, u}
	return r.redeemed, r.redeemErr
}
func (r *scriptedRepo) GetUserUsageCount(context.Context, uuid.UUID, uuid.UUID) (int, error) {
	r.usageCalls++
	return r.usage, nil
}
func (r *scriptedRepo) CountFailedCouponAttemptsToday(context.Context, uuid.UUID) (int, error) {
	return r.attempts, r.attemptsErr
}
func (r *scriptedRepo) RecordFailedCouponAttempt(context.Context, uuid.UUID) error {
	r.recorded++
	return r.recordErr
}
func (r *scriptedRepo) DecrementUsageAtomic(_ context.Context, c, u uuid.UUID) error {
	r.decrementArgs = [2]uuid.UUID{c, u}
	return r.decrementErr
}

func percentCoupon() *domain.Coupon {
	return &domain.Coupon{ID: uuid.New(), Code: "TSB-10", DiscountType: domain.DiscountTypePercentage, DiscountValue: decimal.NewFromInt(10), IsActive: true}
}

func TestValidateCoupon_Success(t *testing.T) {
	c := percentCoupon()
	repo := &scriptedRepo{coupon: c}
	got, discount, err := NewCouponService(repo).ValidateCoupon(t.Context(), " tsb-10 ", decimal.NewFromInt(50), uuid.New())
	require.NoError(t, err)
	assert.Same(t, c, got)
	assert.True(t, discount.Equal(decimal.NewFromInt(5)))
	assert.Zero(t, repo.recorded, "a successful validation costs no attempt")
	assert.Equal(t, " tsb-10 ", repo.lastCode, "the raw code reaches the repository, which normalises it")
}

func TestValidateCoupon_DailyAttemptLimit(t *testing.T) {
	t.Run("at the limit the lookup is not even attempted", func(t *testing.T) {
		repo := &scriptedRepo{coupon: percentCoupon(), attempts: domain.MaxFailedCouponAttemptsPerDay}
		_, discount, err := NewCouponService(repo).ValidateCoupon(t.Context(), "TSB-10", decimal.NewFromInt(50), uuid.New())
		_, ok := errors.AsType[*domain.DailyAttemptLimitError](err)
		assert.True(t, ok, "err = %v", err)
		assert.True(t, discount.IsZero())
		assert.Zero(t, repo.findCalls)
		assert.Zero(t, repo.recorded)
	})

	t.Run("one below the limit still validates", func(t *testing.T) {
		repo := &scriptedRepo{coupon: percentCoupon(), attempts: domain.MaxFailedCouponAttemptsPerDay - 1}
		_, _, err := NewCouponService(repo).ValidateCoupon(t.Context(), "TSB-10", decimal.NewFromInt(50), uuid.New())
		assert.NoError(t, err)
	})

	t.Run("an unreadable counter fails open", func(t *testing.T) {
		repo := &scriptedRepo{coupon: percentCoupon(), attemptsErr: errors.New("db down")}
		_, discount, err := NewCouponService(repo).ValidateCoupon(t.Context(), "TSB-10", decimal.NewFromInt(50), uuid.New())
		require.NoError(t, err, "never lock a customer out because of an infrastructure failure")
		assert.True(t, discount.Equal(decimal.NewFromInt(5)))
	})
}

func TestValidateCoupon_Refusals(t *testing.T) {
	now := time.Now()
	cases := map[string]*domain.Coupon{
		"inactive": {IsActive: false},
		"expired":  {IsActive: true, ValidUntil: &[]time.Time{now.Add(-time.Hour)}[0]},
		"future":   {IsActive: true, ValidFrom: &[]time.Time{now.Add(time.Hour)}[0]},
		"used up":  {IsActive: true, MaxUses: &[]int{1}[0], UsedCount: 1},
	}
	for name, c := range cases {
		t.Run(name+" is a generic refusal that costs an attempt", func(t *testing.T) {
			c.ID, c.DiscountType, c.DiscountValue = uuid.New(), domain.DiscountTypeFixed, decimal.NewFromInt(5)
			repo := &scriptedRepo{coupon: c}
			got, discount, err := NewCouponService(repo).ValidateCoupon(t.Context(), "X", decimal.NewFromInt(50), uuid.New())
			require.Error(t, err)
			assert.Equal(t, "invalid or expired coupon", err.Error(), "the reason is not leaked")
			assert.Same(t, c, got)
			assert.True(t, discount.IsZero())
			assert.Equal(t, 1, repo.recorded)
		})
	}

	t.Run("per-user cap reached", func(t *testing.T) {
		c := percentCoupon()
		c.MaxUsesPerUser = &[]int{2}[0]
		repo := &scriptedRepo{coupon: c, usage: 2}
		_, _, err := NewCouponService(repo).ValidateCoupon(t.Context(), "X", decimal.NewFromInt(50), uuid.New())
		assert.EqualError(t, err, "invalid or expired coupon")
		assert.Equal(t, 1, repo.recorded)
	})

	t.Run("a minimum not met is shown to the customer and is not counted as a guess", func(t *testing.T) {
		c := percentCoupon()
		min := decimal.NewFromInt(30)
		c.MinOrderAmount = &min
		repo := &scriptedRepo{coupon: c}
		got, discount, err := NewCouponService(repo).ValidateCoupon(t.Context(), "X", decimal.NewFromInt(10), uuid.New())
		minErr, ok := errors.AsType[*domain.MinOrderNotMetError](err)
		require.True(t, ok, "err = %v", err)
		assert.True(t, minErr.Required.Equal(min))
		assert.Same(t, c, got)
		assert.True(t, discount.IsZero())
		assert.Zero(t, repo.recorded)
	})

	t.Run("a failure to record the attempt does not change the refusal", func(t *testing.T) {
		repo := &scriptedRepo{findErr: fmt.Errorf("coupon not found: %w", sql.ErrNoRows), recordErr: errors.New("db down")}
		_, _, err := NewCouponService(repo).ValidateCoupon(t.Context(), "X", decimal.NewFromInt(10), uuid.New())
		assert.EqualError(t, err, "invalid or expired coupon")
		assert.Equal(t, 1, repo.recorded)
	})
}

func TestCouponServiceUsageAndCRUD(t *testing.T) {
	ctx := t.Context()
	id, user := uuid.New(), uuid.New()
	c := percentCoupon()
	boom := errors.New("db down")

	t.Run("redeem passes the ids through and reports exhaustion as false", func(t *testing.T) {
		repo := &scriptedRepo{redeemed: true}
		ok, err := NewCouponService(repo).IncrementUsageAtomic(ctx, id, user)
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Equal(t, [2]uuid.UUID{id, user}, repo.redeemArgs)

		repo = &scriptedRepo{redeemed: false}
		ok, err = NewCouponService(repo).IncrementUsageAtomic(ctx, id, user)
		require.NoError(t, err)
		assert.False(t, ok)

		repo = &scriptedRepo{redeemErr: boom}
		_, err = NewCouponService(repo).IncrementUsageAtomic(ctx, id, user)
		assert.ErrorIs(t, err, boom)
	})

	t.Run("rolling a redemption back wraps hard failures", func(t *testing.T) {
		repo := &scriptedRepo{}
		require.NoError(t, NewCouponService(repo).DecrementUsageAtomic(ctx, id, user))
		assert.Equal(t, [2]uuid.UUID{id, user}, repo.decrementArgs)

		repo = &scriptedRepo{decrementErr: boom}
		err := NewCouponService(repo).DecrementUsageAtomic(ctx, id, user)
		require.ErrorIs(t, err, boom)
		assert.ErrorContains(t, err, "failed to decrement coupon usage")
	})

	t.Run("plain increment, lookups and writes are delegated", func(t *testing.T) {
		repo := &scriptedRepo{coupon: c, all: []*domain.Coupon{c}}
		svc := NewCouponService(repo)

		require.NoError(t, svc.IncrementUsage(ctx, id))
		assert.Equal(t, []uuid.UUID{id}, repo.incremented)
		repo.incrementErr = boom
		assert.ErrorIs(t, svc.IncrementUsage(ctx, id), boom)

		got, err := svc.GetCouponByCode(ctx, "CODE")
		require.NoError(t, err)
		assert.Same(t, c, got)
		got, err = svc.GetCoupon(ctx, id)
		require.NoError(t, err)
		assert.Same(t, c, got)
		all, err := svc.GetAllCoupons(ctx)
		require.NoError(t, err)
		assert.Equal(t, []*domain.Coupon{c}, all)

		require.NoError(t, svc.CreateCoupon(ctx, c))
		assert.Same(t, c, repo.saved)
		require.NoError(t, svc.UpdateCoupon(ctx, c))
		assert.Same(t, c, repo.update)

		repo.writeErr, repo.allErr, repo.findErr = boom, boom, boom
		assert.ErrorIs(t, svc.CreateCoupon(ctx, c), boom)
		assert.ErrorIs(t, svc.UpdateCoupon(ctx, c), boom)
		_, err = svc.GetAllCoupons(ctx)
		assert.ErrorIs(t, err, boom)
		_, err = svc.GetCoupon(ctx, id)
		assert.ErrorIs(t, err, boom)
		_, err = svc.GetCouponByCode(ctx, "CODE")
		assert.ErrorIs(t, err, boom)
	})
}
