package resolver

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"tsb-service/internal/api/graphql/apperr"
	couponApplication "tsb-service/internal/modules/coupon/application"
	couponDomain "tsb-service/internal/modules/coupon/domain"
	"tsb-service/pkg/utils"
)

// errCouponService embeds the interface so only ValidateCoupon has to be faked.
type errCouponService struct {
	couponApplication.CouponService
	err error
}

func (s errCouponService) ValidateCoupon(context.Context, string, decimal.Decimal, uuid.UUID, couponDomain.Channel) (*couponDomain.Coupon, decimal.Decimal, error) {
	return nil, decimal.Zero, s.err
}

// channelCouponService records the channel the resolver validates for.
type channelCouponService struct {
	couponApplication.CouponService
	got *couponDomain.Channel
}

func (s channelCouponService) ValidateCoupon(_ context.Context, _ string, _ decimal.Decimal, _ uuid.UUID, channel couponDomain.Channel) (*couponDomain.Coupon, decimal.Decimal, error) {
	*s.got = channel
	return &couponDomain.Coupon{}, decimal.NewFromInt(5), nil
}

func validateAs(t *testing.T, err error) (*string, *string, error) {
	t.Helper()
	r := &Resolver{CouponService: errCouponService{err: err}}
	ctx := utils.SetUserID(t.Context(), uuid.NewString())
	res, gqlErr := r.Query().ValidateCoupon(ctx, "ANY", "10")
	if gqlErr != nil {
		return nil, nil, gqlErr
	}
	if res.Valid {
		t.Fatal("must not be valid")
	}
	return res.ErrorMessage, res.ErrorCode, nil
}

func TestValidateCoupon_InfrastructureFailureIsAGraphQLError(t *testing.T) {
	_, _, err := validateAs(t, &couponDomain.CheckFailedError{Err: errors.New("pq: password authentication failed for user tsb")})
	appErr, ok := apperr.From(err)
	if !ok || appErr.Code != apperr.CodeCouponCheckFailed {
		t.Fatalf("err = %v, want COUPON_CHECK_FAILED", err)
	}
}

func TestValidateCoupon_RefusalNeverCarriesRawErrorText(t *testing.T) {
	// A refusal that is not one of the two user-safe kinds gets the fixed generic text, whatever
	// the underlying error says.
	msg, code, err := validateAs(t, errors.New("pq: relation \"coupons\" does not exist"))
	if err != nil {
		t.Fatal(err)
	}
	if *code != string(apperr.CodeCouponInvalid) || *msg != "invalid or expired coupon" {
		t.Errorf("got %q / %q", *code, *msg)
	}

	msg, code, err = validateAs(t, &couponDomain.MinOrderNotMetError{Required: decimal.RequireFromString("30")})
	if err != nil {
		t.Fatal(err)
	}
	if *code != string(apperr.CodeCouponMinOrderNotMet) || *msg != "minimum order amount of 30 not met" {
		t.Errorf("got %q / %q", *code, *msg)
	}
}

func TestValidateCoupon_AppOnlyRefusalIsShownWithItsCode(t *testing.T) {
	msg, code, err := validateAs(t, &couponDomain.AppOnlyError{})
	if err != nil {
		t.Fatal(err)
	}
	if *code != string(apperr.CodeCouponAppOnly) || *msg != "this coupon can only be used in the app" {
		t.Errorf("got %q / %q", *code, *msg)
	}
}

func TestValidateCoupon_ChannelComesFromTheToken(t *testing.T) {
	for name, tc := range map[string]struct {
		isApp bool
		want  couponDomain.Channel
	}{
		"a token of the native client is the app": {true, couponDomain.ChannelApp},
		"any other token is the website":          {false, couponDomain.ChannelWeb},
	} {
		t.Run(name, func(t *testing.T) {
			var got couponDomain.Channel
			r := &Resolver{CouponService: channelCouponService{got: &got}}
			ctx := utils.SetIsAppClient(utils.SetUserID(t.Context(), uuid.NewString()), tc.isApp)
			res, err := r.Query().ValidateCoupon(ctx, "ANY", "10")
			if err != nil || !res.Valid {
				t.Fatalf("res = %+v, err = %v", res, err)
			}
			if got != tc.want {
				t.Errorf("channel = %q, want %q", got, tc.want)
			}
		})
	}
}
