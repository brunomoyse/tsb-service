package infrastructure

import (
	"context"
	"database/sql"
	"errors"
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
		`SELECT id, code, discount_type, discount_value, min_order_amount, max_uses, max_uses_per_user, used_count, is_active, valid_from, valid_until, created_at
		 FROM coupons WHERE code = $1`, code)
	if err != nil {
		return nil, fmt.Errorf("coupon not found: %w", err)
	}
	return &coupon, nil
}

func (r *CouponRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Coupon, error) {
	var coupon domain.Coupon
	err := r.pool.ForContext(ctx).GetContext(ctx, &coupon,
		`SELECT id, code, discount_type, discount_value, min_order_amount, max_uses, max_uses_per_user, used_count, is_active, valid_from, valid_until, created_at
		 FROM coupons WHERE id = $1`, id)
	if err != nil {
		return nil, fmt.Errorf("coupon not found: %w", err)
	}
	return &coupon, nil
}

func (r *CouponRepository) FindAll(ctx context.Context) ([]*domain.Coupon, error) {
	var coupons []*domain.Coupon
	err := r.pool.ForContext(ctx).SelectContext(ctx, &coupons,
		`SELECT id, code, discount_type, discount_value, min_order_amount, max_uses, max_uses_per_user, used_count, is_active, valid_from, valid_until, created_at
		 FROM coupons ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch coupons: %w", err)
	}
	return coupons, nil
}

func (r *CouponRepository) Save(ctx context.Context, coupon *domain.Coupon) error {
	err := r.pool.ForContext(ctx).QueryRowxContext(ctx,
		`INSERT INTO coupons (id, code, discount_type, discount_value, min_order_amount, max_uses, max_uses_per_user, used_count, is_active, valid_from, valid_until)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		 RETURNING created_at`,
		coupon.ID, coupon.Code, coupon.DiscountType, coupon.DiscountValue,
		coupon.MinOrderAmount, coupon.MaxUses, coupon.MaxUsesPerUser, coupon.UsedCount, coupon.IsActive,
		coupon.ValidFrom, coupon.ValidUntil).Scan(&coupon.CreatedAt)
	if err != nil {
		return fmt.Errorf("failed to save coupon: %w", err)
	}
	return nil
}

func (r *CouponRepository) Update(ctx context.Context, coupon *domain.Coupon) error {
	_, err := r.pool.ForContext(ctx).ExecContext(ctx,
		`UPDATE coupons SET code = $2, discount_type = $3, discount_value = $4, min_order_amount = $5, max_uses = $6, max_uses_per_user = $7, is_active = $8, valid_from = $9, valid_until = $10
		 WHERE id = $1`,
		coupon.ID, coupon.Code, coupon.DiscountType, coupon.DiscountValue,
		coupon.MinOrderAmount, coupon.MaxUses, coupon.MaxUsesPerUser, coupon.IsActive,
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

// RedeemAtomic serializes concurrent redemptions on the same coupon via a
// SELECT ... FOR UPDATE row lock, then atomically validates and increments
// both the per-user and global counters. All three steps (lock + per-user
// bump + global bump) commit together or not at all, so a failed global
// increment cannot leave a dangling per-user increment behind — the race
// the previous two-step implementation allowed.
func (r *CouponRepository) RedeemAtomic(ctx context.Context, couponID, userID uuid.UUID) (bool, error) {
	tx, err := r.pool.ForContext(ctx).BeginTxx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin redeem tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var maxUsesPerUser sql.NullInt32
	// Lock the coupon row and re-check the global validity window in one shot.
	// Concurrent redemptions for the same coupon serialize here; Postgres
	// returns zero rows (and we treat it as "no longer available") once the
	// cap is exhausted.
	err = tx.QueryRowxContext(ctx,
		`SELECT max_uses_per_user
		 FROM coupons
		 WHERE id = $1
		   AND is_active = true
		   AND (max_uses IS NULL OR used_count < max_uses)
		   AND (valid_from IS NULL OR valid_from <= NOW())
		   AND (valid_until IS NULL OR valid_until >= NOW())
		 FOR UPDATE`, couponID).Scan(&maxUsesPerUser)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("lock coupon: %w", err)
	}

	// Per-user bump. The WHERE on the DO UPDATE ensures we never exceed
	// max_uses_per_user; a zero-row result means this user has hit their cap.
	var perUserResult sql.Result
	if maxUsesPerUser.Valid {
		perUserResult, err = tx.ExecContext(ctx,
			`INSERT INTO coupon_users (coupon_id, user_id, used_count)
			 VALUES ($1, $2, 1)
			 ON CONFLICT (coupon_id, user_id) DO UPDATE SET used_count = coupon_users.used_count + 1
			 WHERE coupon_users.used_count < $3`,
			couponID, userID, maxUsesPerUser.Int32)
	} else {
		perUserResult, err = tx.ExecContext(ctx,
			`INSERT INTO coupon_users (coupon_id, user_id, used_count)
			 VALUES ($1, $2, 1)
			 ON CONFLICT (coupon_id, user_id) DO UPDATE SET used_count = coupon_users.used_count + 1`,
			couponID, userID)
	}
	if err != nil {
		return false, fmt.Errorf("per-user redeem: %w", err)
	}
	if rows, _ := perUserResult.RowsAffected(); rows == 0 {
		return false, nil
	}

	// Global bump. We already re-checked the cap under the row lock, but
	// double-gate here so the counter update itself cannot overshoot if a
	// future writer slips in under a different code path.
	globalResult, err := tx.ExecContext(ctx,
		`UPDATE coupons SET used_count = used_count + 1
		 WHERE id = $1 AND (max_uses IS NULL OR used_count < max_uses)`, couponID)
	if err != nil {
		return false, fmt.Errorf("global redeem: %w", err)
	}
	if rows, _ := globalResult.RowsAffected(); rows == 0 {
		return false, nil
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit redeem: %w", err)
	}
	return true, nil
}

func (r *CouponRepository) DecrementUsedCountAtomic(ctx context.Context, id uuid.UUID) (bool, error) {
	result, err := r.pool.ForContext(ctx).ExecContext(ctx,
		`UPDATE coupons SET used_count = used_count - 1
		 WHERE id = $1 AND used_count > 0`, id)
	if err != nil {
		return false, fmt.Errorf("failed to decrement coupon usage: %w", err)
	}
	rows, _ := result.RowsAffected()
	return rows > 0, nil
}

func (r *CouponRepository) GetUserUsageCount(ctx context.Context, couponID, userID uuid.UUID) (int, error) {
	var count int
	err := r.pool.ForContext(ctx).GetContext(ctx, &count,
		`SELECT used_count FROM coupon_users WHERE coupon_id = $1 AND user_id = $2`, couponID, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("failed to get user usage count: %w", err)
	}
	return count, nil
}

func (r *CouponRepository) DecrementUserUsageAtomic(ctx context.Context, couponID, userID uuid.UUID) (bool, error) {
	result, err := r.pool.ForContext(ctx).ExecContext(ctx,
		`UPDATE coupon_users SET used_count = used_count - 1
		 WHERE coupon_id = $1 AND user_id = $2 AND used_count > 0`,
		couponID, userID)
	if err != nil {
		return false, fmt.Errorf("failed to decrement user usage: %w", err)
	}
	rows, _ := result.RowsAffected()
	return rows > 0, nil
}
