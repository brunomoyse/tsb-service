package domain_test

import (
	"testing"

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
