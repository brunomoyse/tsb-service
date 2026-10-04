package domain_test

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"

	"tsb-service/internal/modules/payment/domain"
)

func TestPaymentStatusIsValid(t *testing.T) {
	for _, s := range []domain.PaymentStatus{
		domain.PaymentStatusOpen, domain.PaymentStatusCanceled, domain.PaymentStatusPending, domain.PaymentStatusAuthorized,
		domain.PaymentStatusExpired, domain.PaymentStatusFailed, domain.PaymentStatusPaid,
	} {
		assert.True(t, s.IsValid(), "%q", s)
	}
	assert.Len(t, domain.PaymentStatuses, 7, "every constant is listed")

	for _, s := range []domain.PaymentStatus{"", "payed", "PAID", " paid", "refunded", "cancelled"} {
		assert.False(t, s.IsValid(), "%q", s)
	}
}

func TestCancelSettlementRefundNotice(t *testing.T) {
	d := decimal.RequireFromString
	for name, tc := range map[string]struct {
		settlement domain.CancelSettlement
		want       string
	}{
		"this settlement refunded part of the payment: tell that amount":     {domain.CancelSettlement{Refunded: d("15.00"), TotalRefunded: d("20.00")}, "15.00"},
		"it refunded nothing because an earlier attempt did: tell the total": {domain.CancelSettlement{TotalRefunded: d("20.00")}, "20.00"},
		"nothing was refunded at all: nothing to tell":                       {domain.CancelSettlement{}, "0"},
	} {
		t.Run(name, func(t *testing.T) {
			assert.True(t, tc.settlement.RefundNotice().Equal(d(tc.want)), "RefundNotice() = %s, want %s", tc.settlement.RefundNotice(), tc.want)
		})
	}
}
