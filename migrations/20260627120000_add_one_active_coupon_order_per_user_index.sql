-- +goose NO TRANSACTION

-- +goose Up
-- Race-proof backstop for the single-use-coupon rule. The application's
-- HasActiveCouponOrder check can be defeated by two concurrent CreateOrder
-- calls (both read "no active coupon order", both insert). This partial unique
-- index lets at most one non-terminal coupon-bearing order exist per user, so
-- the second concurrent insert fails with a unique violation (mapped to a
-- friendly error by the resolver). Terminal states release the coupon and are
-- excluded from the predicate, mirroring HasActiveCouponOrder exactly.
-- Built CONCURRENTLY to avoid locking the orders table; requires running
-- outside a transaction (the NO TRANSACTION directive above).
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS one_active_coupon_order_per_user
  ON orders (user_id)
  WHERE coupon_code IS NOT NULL
    AND order_status NOT IN ('DELIVERED', 'PICKED_UP', 'CANCELLED', 'FAILED');

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS one_active_coupon_order_per_user;
