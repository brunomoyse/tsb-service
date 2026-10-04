package infrastructure

import (
	"database/sql"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"tsb-service/pkg/timezone/timezonetest"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/testhelpers"
	"tsb-service/internal/modules/coupon/domain"
	"tsb-service/pkg/db"
)

func intp(n int) *int { return &n }

func timep(t time.Time) *time.Time { return &t }

type env struct {
	tdb  *testhelpers.TestDatabase
	repo domain.CouponRepository
}

func newEnv(t *testing.T) *env {
	t.Helper()
	tdb := testhelpers.SetupTestDatabase(t)
	return &env{tdb: tdb, repo: NewCouponRepository(&db.DBPool{Customer: tdb.DB, Admin: tdb.DB})}
}

func (e *env) coupon(t *testing.T, mutate func(*domain.Coupon)) *domain.Coupon {
	t.Helper()
	c := &domain.Coupon{
		ID: uuid.New(), Code: "C-" + uuid.NewString()[:8], DiscountType: domain.DiscountTypeFixed,
		DiscountValue: decimal.NewFromInt(5), IsActive: true,
	}
	if mutate != nil {
		mutate(c)
	}
	require.NoError(t, e.repo.Save(t.Context(), c))
	return c
}

func (e *env) user(t *testing.T, label string) uuid.UUID {
	t.Helper()
	id, _ := testhelpers.SeedCustomer(t, e.tdb.DB, label)
	return id
}

func (e *env) usedCount(t *testing.T, id uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, e.tdb.DB.GetContext(t.Context(), &n, `SELECT used_count FROM coupons WHERE id = $1`, id))
	return n
}

func TestCouponRepositoryCRUD(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()

	t.Run("Save fills created_at and a coupon round-trips with every field", func(t *testing.T) {
		min := decimal.RequireFromString("25.50")
		from, until := time.Now().Add(-time.Hour).UTC().Truncate(time.Second), time.Now().Add(24*time.Hour).UTC().Truncate(time.Second)
		c := e.coupon(t, func(c *domain.Coupon) {
			c.Code = "ROUNDTRIP"
			c.DiscountType = domain.DiscountTypePercentage
			c.DiscountValue = decimal.RequireFromString("12.50")
			c.MinOrderAmount = &min
			c.MaxUses = intp(100)
			c.MaxUsesPerUser = intp(2)
			c.ValidFrom, c.ValidUntil = timep(from), timep(until)
		})
		assert.False(t, c.CreatedAt.IsZero())

		got, err := e.repo.FindByID(ctx, c.ID)
		require.NoError(t, err)
		assert.Equal(t, "ROUNDTRIP", got.Code)
		assert.Equal(t, domain.DiscountTypePercentage, got.DiscountType)
		assert.True(t, got.DiscountValue.Equal(decimal.RequireFromString("12.5")))
		require.NotNil(t, got.MinOrderAmount)
		assert.True(t, got.MinOrderAmount.Equal(min))
		assert.Equal(t, 100, *got.MaxUses)
		assert.Equal(t, 2, *got.MaxUsesPerUser)
		assert.True(t, got.IsActive)
		assert.True(t, from.Equal(*got.ValidFrom))
		assert.True(t, until.Equal(*got.ValidUntil))
		assert.Zero(t, got.UsedCount)
	})

	t.Run("Save rejects a duplicate code and an invalid discount", func(t *testing.T) {
		e.coupon(t, func(c *domain.Coupon) { c.Code = "DUP" })
		err := e.repo.Save(ctx, &domain.Coupon{ID: uuid.New(), Code: "DUP", DiscountType: domain.DiscountTypeFixed, DiscountValue: decimal.NewFromInt(1), IsActive: true})
		require.ErrorContains(t, err, "failed to save coupon")
		err = e.repo.Save(ctx, &domain.Coupon{ID: uuid.New(), Code: "ZERO", DiscountType: domain.DiscountTypeFixed, DiscountValue: decimal.Zero, IsActive: true})
		require.ErrorContains(t, err, "failed to save coupon")
	})

	t.Run("FindByCode is case and whitespace insensitive and wraps a miss with sql.ErrNoRows", func(t *testing.T) {
		c := e.coupon(t, func(c *domain.Coupon) { c.Code = "SUMMER10" })
		got, err := e.repo.FindByCode(ctx, "  summer10 ")
		require.NoError(t, err)
		assert.Equal(t, c.ID, got.ID)

		_, err = e.repo.FindByCode(ctx, "NOPE")
		require.ErrorIs(t, err, sql.ErrNoRows)
		assert.ErrorContains(t, err, "coupon not found")
		_, err = e.repo.FindByID(ctx, uuid.New())
		require.ErrorIs(t, err, sql.ErrNoRows)
	})

	t.Run("FindAll lists newest first", func(t *testing.T) {
		a := e.coupon(t, func(c *domain.Coupon) { c.Code = "ALL-A" })
		b := e.coupon(t, func(c *domain.Coupon) { c.Code = "ALL-B" })
		_, err := e.tdb.DB.ExecContext(ctx, `UPDATE coupons SET created_at = created_at - interval '1 hour' WHERE id = $1`, a.ID)
		require.NoError(t, err)
		all, err := e.repo.FindAll(ctx)
		require.NoError(t, err)
		var order []uuid.UUID
		for _, c := range all {
			if c.ID == a.ID || c.ID == b.ID {
				order = append(order, c.ID)
			}
		}
		assert.Equal(t, []uuid.UUID{b.ID, a.ID}, order)
	})

	t.Run("Update changes the editable fields but not the usage counter", func(t *testing.T) {
		c := e.coupon(t, func(c *domain.Coupon) { c.Code = "UPD" })
		require.NoError(t, e.repo.IncrementUsedCount(ctx, c.ID))

		c.Code, c.DiscountType, c.DiscountValue = "UPD2", domain.DiscountTypePercentage, decimal.NewFromInt(20)
		c.IsActive, c.MaxUses, c.MaxUsesPerUser = false, intp(9), intp(3)
		c.UsedCount = 99 // must be ignored
		require.NoError(t, e.repo.Update(ctx, c))

		got, err := e.repo.FindByID(ctx, c.ID)
		require.NoError(t, err)
		assert.Equal(t, "UPD2", got.Code)
		assert.Equal(t, domain.DiscountTypePercentage, got.DiscountType)
		assert.False(t, got.IsActive)
		assert.Equal(t, 9, *got.MaxUses)
		assert.Equal(t, 3, *got.MaxUsesPerUser)
		assert.Equal(t, 1, got.UsedCount)
	})

	t.Run("Update surfaces a constraint violation", func(t *testing.T) {
		a := e.coupon(t, func(c *domain.Coupon) { c.Code = "CLASH-A" })
		e.coupon(t, func(c *domain.Coupon) { c.Code = "CLASH-B" })
		a.Code = "CLASH-B"
		require.ErrorContains(t, e.repo.Update(ctx, a), "failed to update coupon")
	})

	t.Run("IncrementUsedCount adds one per call", func(t *testing.T) {
		c := e.coupon(t, nil)
		require.NoError(t, e.repo.IncrementUsedCount(ctx, c.ID))
		require.NoError(t, e.repo.IncrementUsedCount(ctx, c.ID))
		assert.Equal(t, 2, e.usedCount(t, c.ID))
	})
}

func TestRedeemAtomic(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()

	redeem := func(c *domain.Coupon, u uuid.UUID) bool {
		ok, err := e.repo.RedeemAtomic(ctx, c.ID, u)
		require.NoError(t, err)
		return ok
	}

	t.Run("an unlimited coupon is redeemed again and again, counting both counters", func(t *testing.T) {
		c := e.coupon(t, nil)
		u := e.user(t, "unl")
		for range 3 {
			assert.True(t, redeem(c, u))
		}
		assert.Equal(t, 3, e.usedCount(t, c.ID))
		n, err := e.repo.GetUserUsageCount(ctx, c.ID, u)
		require.NoError(t, err)
		assert.Equal(t, 3, n)
	})

	t.Run("the per-user cap stops a user but not others, and leaves no half-written state", func(t *testing.T) {
		c := e.coupon(t, func(c *domain.Coupon) { c.MaxUsesPerUser = intp(1) })
		alice, bob := e.user(t, "alice"), e.user(t, "bob")
		assert.True(t, redeem(c, alice))
		assert.False(t, redeem(c, alice), "second use by the same customer")
		assert.True(t, redeem(c, bob))
		assert.Equal(t, 2, e.usedCount(t, c.ID), "the refused attempt did not bump the global counter")
		n, _ := e.repo.GetUserUsageCount(ctx, c.ID, alice)
		assert.Equal(t, 1, n)
	})

	t.Run("the global cap is enforced", func(t *testing.T) {
		c := e.coupon(t, func(c *domain.Coupon) { c.MaxUses = intp(2) })
		assert.True(t, redeem(c, e.user(t, "g1")))
		assert.True(t, redeem(c, e.user(t, "g2")))
		third := e.user(t, "g3")
		assert.False(t, redeem(c, third))
		assert.Equal(t, 2, e.usedCount(t, c.ID))
		n, _ := e.repo.GetUserUsageCount(ctx, c.ID, third)
		assert.Zero(t, n, "no per-user row for a refused redemption")
	})

	t.Run("inactive, not-yet-valid, expired and unknown coupons are refused", func(t *testing.T) {
		u := e.user(t, "win")
		inactive := e.coupon(t, func(c *domain.Coupon) { c.IsActive = false })
		future := e.coupon(t, func(c *domain.Coupon) { c.ValidFrom = timep(time.Now().Add(time.Hour)) })
		past := e.coupon(t, func(c *domain.Coupon) { c.ValidUntil = timep(time.Now().Add(-time.Hour)) })
		for name, c := range map[string]*domain.Coupon{"inactive": inactive, "future": future, "expired": past} {
			assert.False(t, redeem(c, u), name)
			assert.Zero(t, e.usedCount(t, c.ID), name)
		}
		ok, err := e.repo.RedeemAtomic(ctx, uuid.New(), u)
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("inside its validity window it is redeemable", func(t *testing.T) {
		c := e.coupon(t, func(c *domain.Coupon) {
			c.ValidFrom, c.ValidUntil = timep(time.Now().Add(-time.Hour)), timep(time.Now().Add(time.Hour))
		})
		assert.True(t, redeem(c, e.user(t, "inwin")))
	})

	t.Run("concurrent redemptions never exceed the cap", func(t *testing.T) {
		const cap, attempts = 3, 12
		c := e.coupon(t, func(c *domain.Coupon) { c.MaxUses = intp(cap) })
		users := make([]uuid.UUID, attempts)
		for i := range users {
			users[i] = e.user(t, "race")
		}
		var wins atomic.Int32
		var wg sync.WaitGroup
		for _, u := range users {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ok, err := e.repo.RedeemAtomic(ctx, c.ID, u)
				assert.NoError(t, err)
				if ok {
					wins.Add(1)
				}
			}()
		}
		wg.Wait()
		assert.Equal(t, int32(cap), wins.Load())
		assert.Equal(t, cap, e.usedCount(t, c.ID))
	})

	t.Run("concurrent redemptions by one customer respect the per-user cap", func(t *testing.T) {
		c := e.coupon(t, func(c *domain.Coupon) { c.MaxUsesPerUser = intp(1) })
		u := e.user(t, "oneuser")
		var wins atomic.Int32
		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ok, err := e.repo.RedeemAtomic(ctx, c.ID, u)
				assert.NoError(t, err)
				if ok {
					wins.Add(1)
				}
			}()
		}
		wg.Wait()
		assert.Equal(t, int32(1), wins.Load())
		assert.Equal(t, 1, e.usedCount(t, c.ID))
	})

	t.Run("an unknown customer is a hard error and nothing is committed", func(t *testing.T) {
		c := e.coupon(t, nil)
		ok, err := e.repo.RedeemAtomic(ctx, c.ID, uuid.New())
		require.ErrorContains(t, err, "per-user redeem")
		assert.False(t, ok)
		assert.Zero(t, e.usedCount(t, c.ID))
	})

	t.Run("an unknown customer with a per-user cap is also a hard error", func(t *testing.T) {
		c := e.coupon(t, func(c *domain.Coupon) { c.MaxUsesPerUser = intp(2) })
		_, err := e.repo.RedeemAtomic(ctx, c.ID, uuid.New())
		require.ErrorContains(t, err, "per-user redeem")
	})
}

func TestDecrementUsageAtomic(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	c := e.coupon(t, nil)
	u := e.user(t, "dec")

	for range 2 {
		ok, err := e.repo.RedeemAtomic(ctx, c.ID, u)
		require.NoError(t, err)
		require.True(t, ok)
	}

	require.NoError(t, e.repo.DecrementUsageAtomic(ctx, c.ID, u))
	assert.Equal(t, 1, e.usedCount(t, c.ID))
	n, _ := e.repo.GetUserUsageCount(ctx, c.ID, u)
	assert.Equal(t, 1, n)

	require.NoError(t, e.repo.DecrementUsageAtomic(ctx, c.ID, u))
	require.NoError(t, e.repo.DecrementUsageAtomic(ctx, c.ID, u), "an extra rollback is a no-op")
	assert.Zero(t, e.usedCount(t, c.ID), "counters never go negative")
	n, _ = e.repo.GetUserUsageCount(ctx, c.ID, u)
	assert.Zero(t, n)

	other := e.user(t, "dec2")
	require.NoError(t, e.repo.DecrementUsageAtomic(ctx, c.ID, other), "a user who never redeemed")
	require.NoError(t, e.repo.DecrementUsageAtomic(ctx, uuid.New(), u), "an unknown coupon")
}

func TestFailedCouponAttempts(t *testing.T) {
	timezonetest.SkipNearMidnight(t, 30*time.Second) // the counter is per calendar day
	e := newEnv(t)
	ctx := t.Context()
	u, other := e.user(t, "att"), e.user(t, "att2")

	n, err := e.repo.CountFailedCouponAttemptsToday(ctx, u)
	require.NoError(t, err)
	assert.Zero(t, n)

	for range 3 {
		require.NoError(t, e.repo.RecordFailedCouponAttempt(ctx, u))
	}
	n, err = e.repo.CountFailedCouponAttemptsToday(ctx, u)
	require.NoError(t, err)
	assert.Equal(t, 3, n)
	n, err = e.repo.CountFailedCouponAttemptsToday(ctx, other)
	require.NoError(t, err)
	assert.Zero(t, n, "counters are per user")

	// Yesterday's attempts (Brussels calendar day) do not count today.
	_, err = e.tdb.DB.ExecContext(ctx, `UPDATE coupon_validation_attempts SET day = day - 1 WHERE user_id = $1`, u)
	require.NoError(t, err)
	n, err = e.repo.CountFailedCouponAttemptsToday(ctx, u)
	require.NoError(t, err)
	assert.Zero(t, n)
	require.NoError(t, e.repo.RecordFailedCouponAttempt(ctx, u))
	n, _ = e.repo.CountFailedCouponAttemptsToday(ctx, u)
	assert.Equal(t, 1, n, "a new day starts a new counter")

	require.ErrorContains(t, e.repo.RecordFailedCouponAttempt(ctx, uuid.New()), "failed to record coupon attempt")
}

func TestCouponRepositoryClosedConnection(t *testing.T) {
	conn := testhelpers.ClosedDB(t)
	repo := NewCouponRepository(&db.DBPool{Customer: conn, Admin: conn})
	ctx := t.Context()
	id, user := uuid.New(), uuid.New()

	_, err := repo.FindByCode(ctx, "X")
	assert.ErrorContains(t, err, "coupon not found")
	_, err = repo.FindByID(ctx, id)
	assert.ErrorContains(t, err, "coupon not found")
	_, err = repo.FindAll(ctx)
	assert.ErrorContains(t, err, "failed to fetch coupons")
	assert.ErrorContains(t, repo.Save(ctx, &domain.Coupon{ID: id}), "failed to save coupon")
	assert.ErrorContains(t, repo.Update(ctx, &domain.Coupon{ID: id}), "failed to update coupon")
	assert.ErrorContains(t, repo.IncrementUsedCount(ctx, id), "failed to increment coupon usage")
	ok, err := repo.RedeemAtomic(ctx, id, user)
	assert.ErrorContains(t, err, "begin redeem tx")
	assert.False(t, ok)
	assert.ErrorContains(t, repo.DecrementUsageAtomic(ctx, id, user), "begin decrement tx")
	_, err = repo.GetUserUsageCount(ctx, id, user)
	assert.ErrorContains(t, err, "failed to get user usage count")
	_, err = repo.CountFailedCouponAttemptsToday(ctx, user)
	assert.ErrorContains(t, err, "failed to count coupon attempts")
	assert.ErrorContains(t, repo.RecordFailedCouponAttempt(ctx, user), "failed to record coupon attempt")
}

// TestRedeemAndRollbackDatabaseFailures makes individual statements of the redeem / rollback
// transactions fail (triggers, a renamed column) and checks that nothing is half-committed.
func TestRedeemAndRollbackDatabaseFailures(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()

	failing := func(t *testing.T, table, when, event string, deferred bool) {
		t.Helper()
		testhelpers.FailTrigger(t, e.tdb.DB, table, when, event, deferred)
	}

	t.Run("a failure locking the coupon is reported", func(t *testing.T) {
		c := e.coupon(t, nil)
		u := e.user(t, "lockfail")
		_, err := e.tdb.DB.ExecContext(ctx, `ALTER TABLE coupons RENAME COLUMN max_uses_per_user TO max_uses_per_user_x`)
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = e.tdb.DB.Exec(`ALTER TABLE coupons RENAME COLUMN max_uses_per_user_x TO max_uses_per_user`)
		})
		ok, err := e.repo.RedeemAtomic(ctx, c.ID, u)
		require.ErrorContains(t, err, "lock coupon")
		assert.False(t, ok)
	})

	t.Run("a failing global increment rolls the per-user increment back", func(t *testing.T) {
		c := e.coupon(t, nil)
		u := e.user(t, "globalfail")
		failing(t, "coupons", "BEFORE", "UPDATE", false)
		ok, err := e.repo.RedeemAtomic(ctx, c.ID, u)
		require.ErrorContains(t, err, "global redeem")
		assert.False(t, ok)
		n, err := e.repo.GetUserUsageCount(ctx, c.ID, u)
		require.NoError(t, err)
		assert.Zero(t, n, "no dangling per-user usage")
	})

	t.Run("a failed commit leaves no usage behind", func(t *testing.T) {
		c := e.coupon(t, nil)
		u := e.user(t, "commitfail")
		failing(t, "coupon_users", "", "INSERT", true)
		ok, err := e.repo.RedeemAtomic(ctx, c.ID, u)
		require.ErrorContains(t, err, "commit redeem")
		assert.False(t, ok)
		assert.Zero(t, e.usedCount(t, c.ID))
	})

	t.Run("rollback: a failing per-user decrement changes nothing", func(t *testing.T) {
		c := e.coupon(t, nil)
		u := e.user(t, "decfail1")
		ok, err := e.repo.RedeemAtomic(ctx, c.ID, u)
		require.NoError(t, err)
		require.True(t, ok)
		failing(t, "coupon_users", "BEFORE", "UPDATE", false)
		require.ErrorContains(t, e.repo.DecrementUsageAtomic(ctx, c.ID, u), "decrement per-user usage")
		assert.Equal(t, 1, e.usedCount(t, c.ID))
	})

	t.Run("rollback: a failing global decrement undoes the per-user decrement", func(t *testing.T) {
		c := e.coupon(t, nil)
		u := e.user(t, "decfail2")
		ok, err := e.repo.RedeemAtomic(ctx, c.ID, u)
		require.NoError(t, err)
		require.True(t, ok)
		failing(t, "coupons", "BEFORE", "UPDATE", false)
		require.ErrorContains(t, e.repo.DecrementUsageAtomic(ctx, c.ID, u), "decrement global usage")
		n, err := e.repo.GetUserUsageCount(ctx, c.ID, u)
		require.NoError(t, err)
		assert.Equal(t, 1, n, "counters must not diverge")
	})

	t.Run("rollback: a failed commit changes nothing", func(t *testing.T) {
		c := e.coupon(t, nil)
		u := e.user(t, "decfail3")
		ok, err := e.repo.RedeemAtomic(ctx, c.ID, u)
		require.NoError(t, err)
		require.True(t, ok)
		failing(t, "coupons", "", "UPDATE", true)
		require.ErrorContains(t, e.repo.DecrementUsageAtomic(ctx, c.ID, u), "commit decrement")
		assert.Equal(t, 1, e.usedCount(t, c.ID))
	})

	t.Run("counting and recording attempts report database errors", func(t *testing.T) {
		_, err := e.tdb.DB.ExecContext(ctx, `ALTER TABLE coupon_validation_attempts RENAME COLUMN count TO count_x`)
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = e.tdb.DB.Exec(`ALTER TABLE coupon_validation_attempts RENAME COLUMN count_x TO count`) })
		_, err = e.repo.CountFailedCouponAttemptsToday(ctx, uuid.New())
		require.ErrorContains(t, err, "failed to count coupon attempts")
	})
}
