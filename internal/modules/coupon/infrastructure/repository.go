package infrastructure

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"tsb-service/internal/modules/coupon/domain"
	"tsb-service/pkg/db"
)

type CouponRepository struct {
	pool *db.DBPool
}

func NewCouponRepository(pool *db.DBPool) domain.CouponRepository {
	return &CouponRepository{pool: pool}
}

func (r *CouponRepository) FindByCode(ctx context.Context, code string) (*domain.Coupon, error) {
	var coupon domain.Coupon
	err := r.pool.ForContext(ctx).GetContext(ctx, &coupon,
		`SELECT id, code, discount_type, discount_value, min_order_amount, max_uses, used_count, is_active, valid_from, valid_until, created_at
		 FROM coupons WHERE code = $1`, code)
	if err != nil {
		return nil, fmt.Errorf("coupon not found: %w", err)
	}
	return &coupon, nil
}

func (r *CouponRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Coupon, error) {
	var coupon domain.Coupon
	err := r.pool.ForContext(ctx).GetContext(ctx, &coupon,
		`SELECT id, code, discount_type, discount_value, min_order_amount, max_uses, used_count, is_active, valid_from, valid_until, created_at
		 FROM coupons WHERE id = $1`, id)
	if err != nil {
		return nil, fmt.Errorf("coupon not found: %w", err)
	}
	return &coupon, nil
}

func (r *CouponRepository) FindAll(ctx context.Context) ([]*domain.Coupon, error) {
	var coupons []*domain.Coupon
	err := r.pool.ForContext(ctx).SelectContext(ctx, &coupons,
		`SELECT id, code, discount_type, discount_value, min_order_amount, max_uses, used_count, is_active, valid_from, valid_until, created_at
		 FROM coupons ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch coupons: %w", err)
	}
	return coupons, nil
}

func (r *CouponRepository) Save(ctx context.Context, coupon *domain.Coupon) error {
	err := r.pool.ForContext(ctx).QueryRowxContext(ctx,
		`INSERT INTO coupons (id, code, discount_type, discount_value, min_order_amount, max_uses, used_count, is_active, valid_from, valid_until)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 RETURNING created_at`,
		coupon.ID, coupon.Code, coupon.DiscountType, coupon.DiscountValue,
		coupon.MinOrderAmount, coupon.MaxUses, coupon.UsedCount, coupon.IsActive,
		coupon.ValidFrom, coupon.ValidUntil).Scan(&coupon.CreatedAt)
	if err != nil {
		return fmt.Errorf("failed to save coupon: %w", err)
	}
	return nil
}

func (r *CouponRepository) Update(ctx context.Context, coupon *domain.Coupon) error {
	_, err := r.pool.ForContext(ctx).ExecContext(ctx,
		`UPDATE coupons SET code = $2, discount_type = $3, discount_value = $4, min_order_amount = $5, max_uses = $6, is_active = $7, valid_from = $8, valid_until = $9
		 WHERE id = $1`,
		coupon.ID, coupon.Code, coupon.DiscountType, coupon.DiscountValue,
		coupon.MinOrderAmount, coupon.MaxUses, coupon.IsActive,
		coupon.ValidFrom, coupon.ValidUntil)
	if err != nil {
		return fmt.Errorf("failed to update coupon: %w", err)
	}
	return nil
}

func (r *CouponRepository) IncrementUsedCount(ctx context.Context, id uuid.UUID) error {
	_, err := r.pool.ForContext(ctx).ExecContext(ctx,
		`UPDATE coupons SET used_count = used_count + 1 WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("failed to increment coupon usage: %w", err)
	}
	return nil
}

func (r *CouponRepository) IncrementUsedCountAtomic(ctx context.Context, id uuid.UUID) (bool, error) {
	result, err := r.pool.ForContext(ctx).ExecContext(ctx,
		`UPDATE coupons SET used_count = used_count + 1
		 WHERE id = $1 AND is_active = true
		 AND (max_uses IS NULL OR used_count < max_uses)
		 AND (valid_from IS NULL OR valid_from <= NOW())
		 AND (valid_until IS NULL OR valid_until >= NOW())`, id)
	if err != nil {
		return false, fmt.Errorf("failed to increment coupon usage: %w", err)
	}
	rows, _ := result.RowsAffected()
	return rows > 0, nil
}
