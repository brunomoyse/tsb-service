package apperr_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/apperr"
)

func TestErrorKeepsEnglishMessageAndCode(t *testing.T) {
	err := apperr.New(apperr.CodeProductNotFound, "product Salmon not found")

	assert.Equal(t, "product Salmon not found", err.Error())
	assert.Equal(t, map[string]any{"code": "PRODUCT_NOT_FOUND"}, err.Extensions())
}

func TestNewfKeepsTheWrappedCause(t *testing.T) {
	err := apperr.Newf(apperr.CodePaymentFailed, "failed to create payment: %w", context.Canceled)

	assert.Equal(t, "failed to create payment: context canceled", err.Error())
	// The presenter relies on errors.Is to demote client disconnects.
	assert.ErrorIs(t, err, context.Canceled)
}

func TestWithAddsParamsWithoutMutatingTheOriginal(t *testing.T) {
	base := apperr.New(apperr.CodeDeliveryMinimumNotMet, "minimum order amount for delivery is 25")
	withMin := base.With("minimum", "25").With("field", "items")

	assert.Equal(t, map[string]any{"code": "DELIVERY_MINIMUM_NOT_MET"}, base.Extensions())
	assert.Equal(t, map[string]any{"code": "DELIVERY_MINIMUM_NOT_MET", "minimum": "25", "field": "items"}, withMin.Extensions())
}

func TestParamsCannotOverrideTheCode(t *testing.T) {
	err := apperr.New(apperr.CodeCouponInvalid, "x").With("code", "HACKED")

	assert.Equal(t, "COUPON_INVALID", err.Extensions()["code"])
}

func TestFromFindsTheErrorThroughWrapping(t *testing.T) {
	inner := apperr.New(apperr.CodeCouponAlreadyActive, "you already have an active order using a coupon")
	wrapped := fmt.Errorf("create order: %w", inner)

	got, ok := apperr.From(wrapped)
	require.True(t, ok)
	assert.Equal(t, apperr.CodeCouponAlreadyActive, got.Code)

	_, ok = apperr.From(errors.New("plain"))
	assert.False(t, ok)
}

func TestIsExpected(t *testing.T) {
	// The customer's doing: warn log, no Sentry event.
	for _, code := range []apperr.Code{
		apperr.CodeUnauthenticated, apperr.CodeForbidden, apperr.CodeNotFound, apperr.CodeUserError,
		apperr.CodeSlotTooSoon, apperr.CodeLunchSlotRequired, apperr.CodeProductNotFound,
		apperr.CodeSelectionInvalid, apperr.CodeDeliveryMinimumNotMet, apperr.CodeCouponAlreadyActive,
	} {
		assert.True(t, apperr.IsExpected(code), "%s should be expected", code)
	}
	// Ours: reported.
	for _, code := range []apperr.Code{
		apperr.CodePaymentFailed, apperr.CodeOrderCreateFailed, apperr.CodeCouponReserveFailed,
		apperr.CodeAddressUnresolvable, apperr.Code("SOMETHING_NEW"),
	} {
		assert.False(t, apperr.IsExpected(code), "%s should be reported", code)
	}
}
