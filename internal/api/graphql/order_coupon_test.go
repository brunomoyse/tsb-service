package graphql_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// insertCouponOrder inserts a pickup order owned by userID with the given
// coupon code and status, for exercising the one-active-coupon-order rule.
func insertCouponOrder(t *testing.T, tc *TestContext, userID uuid.UUID, code, status string) {
	t.Helper()
	const q = `
		INSERT INTO orders (user_id, order_type, total_price, coupon_code, order_status)
		VALUES ($1, 'PICKUP', 10.00, $2, $3)
	`
	_, err := tc.DB.DB.ExecContext(t.Context(), q, userID, code, status)
	require.NoError(t, err)
}

func TestHasActiveCouponOrder(t *testing.T) {
	tc := setupTestContext(t)
	user := tc.Fixtures.RegularUser.ID

	// No orders at all → no active coupon order.
	has, err := tc.Resolver.OrderService.HasActiveCouponOrder(t.Context(), user)
	require.NoError(t, err)
	assert.False(t, has)

	// An order without a coupon doesn't count.
	insertTestOrder(t, tc, user)
	has, err = tc.Resolver.OrderService.HasActiveCouponOrder(t.Context(), user)
	require.NoError(t, err)
	assert.False(t, has)

	// A terminal (cancelled) coupon order doesn't count.
	insertCouponOrder(t, tc, user, "TOKYO10", "CANCELLED")
	has, err = tc.Resolver.OrderService.HasActiveCouponOrder(t.Context(), user)
	require.NoError(t, err)
	assert.False(t, has)

	// A non-terminal coupon order counts.
	insertCouponOrder(t, tc, user, "TOKYO10", "PENDING")
	has, err = tc.Resolver.OrderService.HasActiveCouponOrder(t.Context(), user)
	require.NoError(t, err)
	assert.True(t, has)

	// Scoped per user: another user is unaffected.
	other := tc.Fixtures.AdminUser.ID
	has, err = tc.Resolver.OrderService.HasActiveCouponOrder(t.Context(), other)
	require.NoError(t, err)
	assert.False(t, has)
}
