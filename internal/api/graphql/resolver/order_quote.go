package resolver

// The quoteOrder query: createOrder's validation and pricing (order_pricing.go) without saving
// anything. Kept out of order.go so `gqlgen generate` does not shuffle it.

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"

	"tsb-service/internal/api/auth"
	"tsb-service/internal/api/graphql/apperr"
	"tsb-service/internal/api/graphql/model"
	orderDomain "tsb-service/internal/modules/order/domain"
	"tsb-service/pkg/utils"
)

// quoteOrder implements the quoteOrder query (the generated resolver in order.go delegates here).
//
// It is public (no @auth): the basket can be priced before the customer signs in. The coupon is the
// only authenticated part: validateCoupon is @auth and a validation records failed attempts per
// user, so an anonymous caller's coupon is reported as "not evaluated" (errorCode UNAUTHENTICATED)
// rather than checked.
func (r *Resolver) quoteOrder(ctx context.Context, input model.QuoteOrderInput) (*model.OrderQuote, error) {
	if err := r.allowPublicQuery(ctx, "quoteOrder"); err != nil {
		return nil, err
	}
	items, err := pricingItemsFromQuote(input.Items)
	if err != nil {
		return nil, err
	}

	userID := quoteCallerID(ctx)
	// Store-review accounts order outside opening hours (see CreateOrder): quote them the same way.
	skipGate := false
	if userID != nil {
		if user, err := r.UserService.GetUserByID(ctx, userID.String()); err != nil {
			zap.L().Debug("quoteOrder: user lookup failed, quoting as a regular customer", zap.Error(err))
		} else if user != nil {
			skipGate = auth.IsReviewUser(user.Email, user.FirstName, user.LastName)
		}
	}

	odType := orderDomain.OrderTypePickUp
	if input.OrderType == model.OrderTypeEnumDelivery {
		odType = orderDomain.OrderTypeDelivery
	}
	priced, err := r.orderPricer().price(ctx, pricingInput{
		UserID:             userID,
		OrderType:          odType,
		IsOnlinePayment:    input.IsOnlinePayment,
		AddressPlaceID:     input.AddressPlaceID,
		PreferredReadyTime: input.PreferredReadyTime,
		Items:              items,
		CouponCode:         input.CouponCode,
		SkipOrderingGate:   skipGate,
	})
	if err != nil {
		return nil, err
	}
	return toGQLOrderQuote(priced), nil
}

// quoteCallerID is the authenticated caller, or nil for an anonymous (or expired-token) request.
func quoteCallerID(ctx context.Context) *uuid.UUID {
	raw := utils.GetUserID(ctx)
	if raw == "" {
		return nil
	}
	if exp := utils.GetTokenExpiry(ctx); !exp.IsZero() && !time.Now().Before(exp) {
		return nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil
	}
	return &id
}

// pricingItemsFromOrder adapts the createOrder input.
func pricingItemsFromOrder(items []*model.CreateOrderItemInput) []pricingItem {
	out := make([]pricingItem, len(items))
	for i, item := range items {
		out[i] = pricingItem{
			ProductID:  item.ProductID,
			Quantity:   item.Quantity,
			ChoiceID:   item.ChoiceID,
			Selections: pricingSelections(item.Selections),
		}
	}
	return out
}

func pricingSelections(in []*model.CreateOrderItemSelectionInput) []pricingSelection {
	out := make([]pricingSelection, 0, len(in))
	for _, s := range in {
		out = append(out, pricingSelection{GroupID: s.GroupID, ChoiceID: s.ChoiceID, Quantity: s.Quantity})
	}
	return out
}

// pricingItemsFromQuote adapts the quote input; a malformed expectedLineTotal is the caller's bug.
func pricingItemsFromQuote(items []*model.QuoteOrderItemInput) ([]pricingItem, error) {
	out := make([]pricingItem, len(items))
	for i, item := range items {
		out[i] = pricingItem{
			ProductID:  item.ProductID,
			Quantity:   item.Quantity,
			ChoiceID:   item.ChoiceID,
			Selections: pricingSelections(item.Selections),
		}
		if item.ExpectedLineTotal != nil && *item.ExpectedLineTotal != "" {
			expected, err := decimal.NewFromString(*item.ExpectedLineTotal)
			if err != nil {
				return nil, apperr.Newf(apperr.CodeInvalidAmount, "invalid expectedLineTotal: %w", err)
			}
			out[i].ExpectedLineTotal = &expected
		}
	}
	return out, nil
}

// fixed2 renders an amount the way the quote exposes it: a decimal string with cents ("12.50").
func fixed2(d decimal.Decimal) string { return d.StringFixed(2) }

func moneyPtr(d *decimal.Decimal) *string {
	if d == nil {
		return nil
	}
	s := fixed2(*d)
	return &s
}

func toGQLOrderQuote(res *pricingResult) *model.OrderQuote {
	quote := &model.OrderQuote{
		Lines:          make([]*model.OrderQuoteLine, len(res.Lines)),
		Subtotal:       fixed2(res.Subtotal),
		DeliveryFee:    fixed2(res.DeliveryFee),
		PickupDiscount: fixed2(res.PickupDiscount),
		CouponDiscount: fixed2(res.CouponDiscount),
		OnlineFee:      fixed2(res.OnlineFee),
		Total:          fixed2(res.Total),
		Issues:         []*model.OrderIssue{},
	}
	for i, line := range res.Lines {
		selections := make([]*model.OrderQuoteSelection, len(line.Selections))
		for j, s := range line.Selections {
			selections[j] = &model.OrderQuoteSelection{
				GroupID:       s.GroupID,
				ChoiceID:      s.ChoiceID,
				Quantity:      s.Quantity,
				PriceModifier: fixed2(line.Modifiers[s.ChoiceID]),
			}
		}
		issues := []*model.OrderLineIssue{}
		for _, issue := range res.LineIssues(i) {
			issues = append(issues, &model.OrderLineIssue{Code: string(issue.Code()), CurrentPrice: moneyPtr(issue.CurrentPrice)})
		}
		quote.Lines[i] = &model.OrderQuoteLine{
			ProductID:    line.ProductID,
			Quantity:     line.Quantity,
			Selections:   selections,
			ProductPrice: moneyPtr(line.ProductPrice),
			UnitPrice:    fixed2(line.UnitPrice),
			LineTotal:    fixed2(line.LineTotal),
			Issues:       issues,
		}
	}
	for _, issue := range res.OrderIssues() {
		quote.Issues = append(quote.Issues, &model.OrderIssue{Code: string(issue.Code()), Minimum: issue.Minimum})
	}
	if res.Coupon != nil {
		quote.Coupon = &model.OrderQuoteCoupon{Code: res.Coupon.Code, Valid: res.Coupon.Valid, ErrorCode: res.Coupon.ErrorCode}
	}
	return quote
}
