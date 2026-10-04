package graphql_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/apperr"
	"tsb-service/internal/api/graphql/model"
	"tsb-service/internal/api/graphql/resolver"
	"tsb-service/internal/api/graphql/testhelpers"
)

// The coupon resolvers: administration, validation by customers and the live updates.

func recvCoupon(t *testing.T, ch <-chan *model.Coupon) *model.Coupon {
	t.Helper()
	select {
	case c := <-ch:
		require.NotNil(t, c)
		return c
	case <-time.After(10 * time.Second):
		require.FailNow(t, "no coupon was published")
		return nil
	}
}

// recvCouponCode waits for the publication of the coupon with the code, skipping older ones that
// the one-slot subscription buffer still held.
func recvCouponCode(t *testing.T, ch <-chan *model.Coupon, code string) {
	t.Helper()
	for range 10 {
		if recvCoupon(t, ch).Code == code {
			return
		}
	}
	require.FailNow(t, "the coupon was never published", code)
}

func TestCouponAdministration(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	r := env.Resolver
	admin := env.ctxFor(env.Fixtures.AdminUser.ID.String(), true, "en")
	updates, err := r.Subscription().CouponUpdated(admin)
	require.NoError(t, err)

	t.Run("createCoupon refuses what cannot be a discount", func(t *testing.T) {
		_, err := r.Mutation().CreateCoupon(admin, model.CreateCouponInput{DiscountType: "FIXED", DiscountValue: "ten", IsActive: true})
		require.ErrorContains(t, err, "invalid discount value")
		for _, c := range []struct{ typ, value, msg string }{
			{"BOGUS", "5", "invalid discount type"},
			{"FIXED", "0", "discount value must be positive"},
			{"FIXED", "-3", "discount value must be positive"},
			{"PERCENTAGE", "100.01", "percentage discount cannot exceed 100"},
		} {
			_, err := r.Mutation().CreateCoupon(admin, model.CreateCouponInput{DiscountType: c.typ, DiscountValue: c.value, IsActive: true})
			userErr(t, err, c.msg)
		}
		_, err = r.Mutation().CreateCoupon(admin, model.CreateCouponInput{DiscountType: "FIXED", DiscountValue: "5", MinOrderAmount: new("lots"), IsActive: true})
		require.ErrorContains(t, err, "invalid min order amount")
	})

	t.Run("a coupon is stored with its limits and published", func(t *testing.T) {
		from, until := time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour)
		got, err := r.Mutation().CreateCoupon(admin, model.CreateCouponInput{
			Code: new(" spring-5 "), DiscountType: "fixed", DiscountValue: "5", MinOrderAmount: new("20"),
			MaxUses: new(10), MaxUsesPerUser: new(2), IsActive: true, ValidFrom: &from, ValidUntil: &until})
		require.NoError(t, err)
		assert.Equal(t, "SPRING-5", got.Code)
		assert.Equal(t, "FIXED", got.DiscountType)
		assert.Equal(t, "5", got.DiscountValue)
		require.NotNil(t, got.MinOrderAmount)
		assert.Equal(t, "20", *got.MinOrderAmount)
		assert.Equal(t, model.CouponStatus("ACTIVE"), got.Status)
		recvCouponCode(t, updates, "SPRING-5")

		_, err = r.Mutation().CreateCoupon(admin, model.CreateCouponInput{Code: new("SPRING-5"), DiscountType: "FIXED", DiscountValue: "1", IsActive: true})
		userErr(t, err, "coupon code already exists")
	})

	t.Run("a generated code is retried on collision, and gives up after five", func(t *testing.T) {
		store := &faultyCouponStore{CouponService: r.CouponService, createErrs: []error{uniqueViolation(), uniqueViolation()}}
		retrying := env.with(func(r *resolver.Resolver) { r.CouponService = store })
		got, err := retrying.Mutation().CreateCoupon(admin, model.CreateCouponInput{DiscountType: "PERCENTAGE", DiscountValue: "10", IsActive: true})
		require.NoError(t, err)
		assert.NotEmpty(t, got.Code)
		assert.Empty(t, store.createErrs, "two collisions, then it worked")
		recvCoupon(t, updates)

		store = &faultyCouponStore{CouponService: r.CouponService, createErrs: []error{
			uniqueViolation(), uniqueViolation(), uniqueViolation(), uniqueViolation(), uniqueViolation()}}
		giveUp := env.with(func(r *resolver.Resolver) { r.CouponService = store })
		_, err = giveUp.Mutation().CreateCoupon(admin, model.CreateCouponInput{DiscountType: "PERCENTAGE", DiscountValue: "10", IsActive: true})
		require.ErrorContains(t, err, "failed to generate a unique coupon code")

		store = &faultyCouponStore{CouponService: r.CouponService, createErrs: []error{errBoom}}
		failing := env.with(func(r *resolver.Resolver) { r.CouponService = store })
		_, err = failing.Mutation().CreateCoupon(admin, model.CreateCouponInput{DiscountType: "PERCENTAGE", DiscountValue: "10", IsActive: true})
		require.ErrorIs(t, err, errBoom)
		_, err = failing.Mutation().CreateCoupon(admin, model.CreateCouponInput{Code: new("BOOM"), DiscountType: "PERCENTAGE", DiscountValue: "10", IsActive: true})
		require.NoError(t, err, "the failure was consumed, the next call is real")

		store = &faultyCouponStore{CouponService: r.CouponService, createErrs: []error{errBoom}}
		supplied := env.with(func(r *resolver.Resolver) { r.CouponService = store })
		_, err = supplied.Mutation().CreateCoupon(admin, model.CreateCouponInput{Code: new("SUPPLIED"), DiscountType: "PERCENTAGE", DiscountValue: "10", IsActive: true})
		require.ErrorContains(t, err, "failed to create coupon")
	})

	created, err := r.Mutation().CreateCoupon(admin, model.CreateCouponInput{Code: new("TARGET"), DiscountType: "PERCENTAGE", DiscountValue: "10", IsActive: true})
	require.NoError(t, err)
	recvCoupon(t, updates)

	t.Run("updateCoupon changes only what is given and validates the result", func(t *testing.T) {
		from, until := time.Now().Add(-time.Hour), time.Now().Add(48*time.Hour)
		got, err := r.Mutation().UpdateCoupon(admin, created.ID, model.UpdateCouponInput{
			Code: new("renamed"), DiscountType: new("fixed"), DiscountValue: new("7.5"), MinOrderAmount: new("15"),
			MaxUses: new(5), MaxUsesPerUser: new(1), IsActive: new(false), ValidFrom: &from, ValidUntil: &until})
		require.NoError(t, err)
		assert.Equal(t, "RENAMED", got.Code)
		assert.Equal(t, "FIXED", got.DiscountType)
		assert.Equal(t, "7.5", got.DiscountValue)
		assert.Equal(t, "15", *got.MinOrderAmount)
		assert.Equal(t, 5, *got.MaxUses)
		assert.False(t, got.IsActive)
		assert.Equal(t, model.CouponStatus("INACTIVE"), got.Status)
		recvCouponCode(t, updates, "RENAMED")

		stored, err := r.Query().Coupon(admin, created.ID)
		require.NoError(t, err)
		assert.Equal(t, "RENAMED", stored.Code, "the change is stored")
	})

	t.Run("updateCoupon refuses what cannot be stored", func(t *testing.T) {
		_, err := r.Mutation().UpdateCoupon(admin, uuid.New(), model.UpdateCouponInput{})
		require.ErrorContains(t, err, "coupon not found")
		_, err = r.Mutation().UpdateCoupon(admin, created.ID, model.UpdateCouponInput{DiscountValue: new("many")})
		require.ErrorContains(t, err, "invalid discount value")
		_, err = r.Mutation().UpdateCoupon(admin, created.ID, model.UpdateCouponInput{MinOrderAmount: new("lots")})
		require.ErrorContains(t, err, "invalid min order amount")
		_, err = r.Mutation().UpdateCoupon(admin, created.ID, model.UpdateCouponInput{DiscountType: new("percentage"), DiscountValue: new("150")})
		userErr(t, err, "percentage discount cannot exceed 100")

		_, err = r.Mutation().UpdateCoupon(admin, created.ID, model.UpdateCouponInput{Code: new("SPRING-5")})
		userErr(t, err, "coupon code already exists")

		store := &faultyCouponStore{CouponService: r.CouponService, updateErr: errBoom}
		failing := env.with(func(r *resolver.Resolver) { r.CouponService = store })
		_, err = failing.Mutation().UpdateCoupon(admin, created.ID, model.UpdateCouponInput{IsActive: new(true)})
		require.ErrorContains(t, err, "failed to update coupon")
	})

	t.Run("the lists", func(t *testing.T) {
		all, err := r.Query().Coupons(admin)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(all), 4)
		_, err = r.Query().Coupon(admin, uuid.New())
		require.ErrorContains(t, err, "coupon not found")

		store := &faultyCouponStore{CouponService: r.CouponService, listErr: errBoom}
		failing := env.with(func(r *resolver.Resolver) { r.CouponService = store })
		_, err = failing.Query().Coupons(admin)
		require.ErrorContains(t, err, "failed to get coupons")
	})

	t.Run("a customer may not administer coupons", func(t *testing.T) {
		_, token := testhelpers.SeedCustomer(t, env.DB.DB, "shopper")
		for _, q := range []string{`{ coupons { id } }`, `{ coupon(id: "` + created.ID.String() + `") { id } }`,
			`mutation { updateCoupon(id: "` + created.ID.String() + `", input: {isActive: true}) { id } }`} {
			resp := gqlAs(t, env.TestContext, token, "en", q, nil)
			require.Len(t, resp.Errors, 1, q)
			assert.Equal(t, "FORBIDDEN", resp.Errors[0].Extensions["code"], q)
		}
	})
}

func TestValidateCouponResolver(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	r := env.Resolver
	admin := env.ctxFor(env.Fixtures.AdminUser.ID.String(), true, "en")
	_, err := r.Mutation().CreateCoupon(admin, model.CreateCouponInput{Code: new("TEN"), DiscountType: "PERCENTAGE", DiscountValue: "10", MinOrderAmount: new("30"), IsActive: true})
	require.NoError(t, err)
	user, token := testhelpers.SeedCustomer(t, env.DB.DB, "validator")

	const q = `query ($code: String!, $amount: String!) { validateCoupon(code: $code, orderAmount: $amount) { valid discountAmount errorMessage errorCode } }`
	type result struct {
		ValidateCoupon struct {
			Valid          bool
			DiscountAmount string
			ErrorMessage   *string
			ErrorCode      *string
		}
	}
	run := func(t *testing.T, tok, code, amount string) (result, *orderErr) {
		t.Helper()
		resp := gqlAs(t, env.TestContext, tok, "en", q, map[string]any{"code": code, "amount": amount})
		if len(resp.Errors) > 0 {
			return result{}, &orderErr{Message: resp.Errors[0].Message, Extensions: resp.Errors[0].Extensions}
		}
		var out result
		require.NoError(t, json.Unmarshal(resp.Data, &out))
		return out, nil
	}

	t.Run("a valid code gives its discount", func(t *testing.T) {
		got, oerr := run(t, token, "ten", "50.00")
		require.Nil(t, oerr)
		assert.True(t, got.ValidateCoupon.Valid)
		assert.Equal(t, "5", got.ValidateCoupon.DiscountAmount)
		assert.Nil(t, got.ValidateCoupon.ErrorCode)
	})

	t.Run("a basket below the minimum says so, with a code the app translates", func(t *testing.T) {
		got, oerr := run(t, token, "TEN", "10")
		require.Nil(t, oerr)
		assert.False(t, got.ValidateCoupon.Valid)
		assert.Equal(t, "0", got.ValidateCoupon.DiscountAmount)
		assert.Equal(t, "COUPON_MIN_ORDER_NOT_MET", *got.ValidateCoupon.ErrorCode)
		assert.Contains(t, *got.ValidateCoupon.ErrorMessage, "30")
	})

	t.Run("an unknown code is refused without saying why", func(t *testing.T) {
		got, oerr := run(t, token, "NOPE", "50")
		require.Nil(t, oerr)
		assert.False(t, got.ValidateCoupon.Valid)
		assert.Equal(t, "COUPON_INVALID", *got.ValidateCoupon.ErrorCode)
		assert.Equal(t, "invalid or expired coupon", *got.ValidateCoupon.ErrorMessage)
	})

	t.Run("an amount that is not a number is a typed error", func(t *testing.T) {
		_, oerr := run(t, token, "TEN", "fifty")
		require.NotNil(t, oerr)
		assert.Equal(t, string(apperr.CodeInvalidAmount), oerr.Code())
	})

	t.Run("a caller id that is not a uuid is refused", func(t *testing.T) {
		_, err := r.Query().ValidateCoupon(env.ctxFor("nope", false, "en"), "TEN", "50")
		require.ErrorContains(t, err, "invalid user ID")
	})

	t.Run("a failing store is a server fault, not a refusal", func(t *testing.T) {
		broken := env.brokenResolver(t)
		broken.CouponValidateLimiter = nil
		_, err := broken.Query().ValidateCoupon(env.ctxFor(user.String(), false, "en"), "TEN", "50")
		appErr, ok := apperr.From(err)
		require.True(t, ok, "%v", err)
		assert.Equal(t, apperr.CodeCouponCheckFailed, appErr.Code)
	})

	t.Run("a customer is throttled per user to stop code guessing", func(t *testing.T) {
		_, tok2 := testhelpers.SeedCustomer(t, env.DB.DB, "guesser")
		var last result
		for range 4 {
			got, oerr := run(t, tok2, "TEN", "50")
			require.Nil(t, oerr)
			last = got
		}
		assert.False(t, last.ValidateCoupon.Valid, "the fourth attempt of the minute is refused")
		assert.Equal(t, "COUPON_RATE_LIMITED", *last.ValidateCoupon.ErrorCode)
		assert.Equal(t, "too many attempts, please try again in a minute", *last.ValidateCoupon.ErrorMessage)
	})

	t.Run("anonymous callers are refused", func(t *testing.T) {
		_, oerr := run(t, "", "TEN", "50")
		require.NotNil(t, oerr)
		assert.Equal(t, "UNAUTHENTICATED", oerr.Code())
	})
}
