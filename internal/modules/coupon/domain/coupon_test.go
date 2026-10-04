package domain

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var d = decimal.RequireFromString

func intp(n int) *int { return &n }

func timep(t time.Time) *time.Time { return &t }

func decp(s string) *decimal.Decimal { v := d(s); return &v }

func TestNormalizeCode(t *testing.T) {
	assert.Equal(t, "SUMMER", NormalizeCode("  summer "))
	assert.Equal(t, "SUMMER-10", NormalizeCode("Summer-10"))
	assert.Equal(t, "", NormalizeCode("   "))
}

func TestGenerateCode(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		code, err := GenerateCode()
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(code, "TSB-"), code)
		suffix := strings.TrimPrefix(code, "TSB-")
		require.Len(t, suffix, generatedCodeLength)
		for _, r := range suffix {
			assert.Contains(t, codeAlphabet, string(r), "only unambiguous characters: %s", code)
		}
		assert.Equal(t, code, NormalizeCode(code), "a generated code is already in canonical form")
		seen[code] = true
	}
	assert.Greater(t, len(seen), 190, "codes are random, not constant")
	for _, ambiguous := range "01IO" {
		assert.NotContains(t, codeAlphabet, string(ambiguous))
	}
}

func TestErrorTypes(t *testing.T) {
	min := &MinOrderNotMetError{Required: d("25.00")}
	assert.Equal(t, "minimum order amount of 25 not met", min.Error())

	cause := errors.New("connection refused")
	check := &CheckFailedError{Err: cause}
	assert.Equal(t, "coupon check failed: connection refused", check.Error())
	assert.ErrorIs(t, check, cause)
	assert.Equal(t, cause, check.Unwrap())

	assert.Contains(t, (&DailyAttemptLimitError{}).Error(), "too many coupon attempts")
	assert.Equal(t, 5, MaxFailedCouponAttemptsPerDay)
}

func TestCouponStatus(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		c    Coupon
		want Status
	}{
		{"active without limits", Coupon{IsActive: true}, StatusActive},
		{"disabled wins over everything", Coupon{IsActive: false, ValidUntil: timep(now.Add(-time.Hour)), MaxUses: intp(1), UsedCount: 1}, StatusInactive},
		{"not started yet", Coupon{IsActive: true, ValidFrom: timep(now.Add(time.Hour))}, StatusScheduled},
		{"started", Coupon{IsActive: true, ValidFrom: timep(now.Add(-time.Hour))}, StatusActive},
		{"ended", Coupon{IsActive: true, ValidUntil: timep(now.Add(-time.Hour))}, StatusExpired},
		{"still running", Coupon{IsActive: true, ValidUntil: timep(now.Add(time.Hour))}, StatusActive},
		{"used up", Coupon{IsActive: true, MaxUses: intp(3), UsedCount: 3}, StatusExhausted},
		{"over-used is still exhausted", Coupon{IsActive: true, MaxUses: intp(3), UsedCount: 4}, StatusExhausted},
		{"uses left", Coupon{IsActive: true, MaxUses: intp(3), UsedCount: 2}, StatusActive},
		{"expired and exhausted reports expired first", Coupon{IsActive: true, ValidUntil: timep(now.Add(-time.Hour)), MaxUses: intp(1), UsedCount: 1}, StatusExpired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.c.Status())
		})
	}
}

func TestCouponValidate(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name      string
		c         Coupon
		amount    string
		userUsage int
		wantErr   string
	}{
		{"valid", Coupon{IsActive: true}, "10", 0, ""},
		{"inactive", Coupon{IsActive: false}, "10", 0, "coupon is not active"},
		{"before its window", Coupon{IsActive: true, ValidFrom: timep(now.Add(time.Minute))}, "10", 0, "coupon is not yet valid"},
		{"after its window", Coupon{IsActive: true, ValidUntil: timep(now.Add(-time.Minute))}, "10", 0, "coupon has expired"},
		{"global cap reached", Coupon{IsActive: true, MaxUses: intp(2), UsedCount: 2}, "10", 0, "coupon usage limit reached"},
		{"global cap not reached", Coupon{IsActive: true, MaxUses: intp(2), UsedCount: 1}, "10", 0, ""},
		{"per-user cap reached", Coupon{IsActive: true, MaxUsesPerUser: intp(1)}, "10", 1, "per-user usage limit reached"},
		{"per-user cap not reached", Coupon{IsActive: true, MaxUsesPerUser: intp(2)}, "10", 1, ""},
		{"exactly the minimum is enough", Coupon{IsActive: true, MinOrderAmount: decp("25.00")}, "25.00", 0, ""},
		{"below the minimum", Coupon{IsActive: true, MinOrderAmount: decp("25.00")}, "24.99", 0, "minimum order amount of 25 not met"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.c.Validate(d(tc.amount), tc.userUsage)
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}

	t.Run("the minimum failure is typed so callers can show it", func(t *testing.T) {
		err := (&Coupon{IsActive: true, MinOrderAmount: decp("30")}).Validate(d("10"), 0)
		minErr, ok := errors.AsType[*MinOrderNotMetError](err)
		require.True(t, ok)
		assert.True(t, minErr.Required.Equal(d("30")))
	})

	t.Run("other failures are not the minimum error", func(t *testing.T) {
		err := (&Coupon{IsActive: false}).Validate(d("10"), 0)
		_, ok := errors.AsType[*MinOrderNotMetError](err)
		assert.False(t, ok)
	})

	t.Run("checks run in order: inactive is reported before the minimum", func(t *testing.T) {
		err := (&Coupon{IsActive: false, MinOrderAmount: decp("30")}).Validate(d("10"), 0)
		assert.Contains(t, err.Error(), "not active")
	})
}

func TestCalculateDiscount(t *testing.T) {
	cases := []struct {
		name   string
		c      Coupon
		amount string
		want   string
	}{
		{"10% of 50", Coupon{DiscountType: DiscountTypePercentage, DiscountValue: d("10")}, "50.00", "5.00"},
		{"percentage rounds to cents", Coupon{DiscountType: DiscountTypePercentage, DiscountValue: d("15")}, "33.33", "5.00"},
		{"percentage rounds half up", Coupon{DiscountType: DiscountTypePercentage, DiscountValue: d("12.5")}, "10.04", "1.26"},
		{"100% is the whole order", Coupon{DiscountType: DiscountTypePercentage, DiscountValue: d("100")}, "42.00", "42.00"},
		{"a misconfigured 150% is clamped to the order", Coupon{DiscountType: DiscountTypePercentage, DiscountValue: d("150")}, "42.00", "42.00"},
		{"percentage of nothing", Coupon{DiscountType: DiscountTypePercentage, DiscountValue: d("10")}, "0", "0"},
		{"fixed amount smaller than the order", Coupon{DiscountType: DiscountTypeFixed, DiscountValue: d("5.00")}, "20.00", "5.00"},
		{"fixed amount equal to the order", Coupon{DiscountType: DiscountTypeFixed, DiscountValue: d("20.00")}, "20.00", "20.00"},
		{"fixed amount capped at the order", Coupon{DiscountType: DiscountTypeFixed, DiscountValue: d("50.00")}, "20.00", "20.00"},
		{"unknown type gives no discount", Coupon{DiscountType: "bogus", DiscountValue: d("5")}, "20.00", "0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.c.CalculateDiscount(d(tc.amount))
			assert.True(t, got.Equal(d(tc.want)), "got %s want %s", got, tc.want)
			assert.True(t, got.LessThanOrEqual(d(tc.amount)), "a discount never exceeds the order")
		})
	}
}
