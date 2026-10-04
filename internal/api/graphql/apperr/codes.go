// Package apperr gives GraphQL errors a stable machine-readable identity.
//
// Resolvers return an *Error (directly, or wrapped with %w) and the GraphQL error presenter copies
// its Code, and any parameters attached with With, into `extensions` of the response error. Clients
// (tsb-core, tsb-mobile) map the code to translated text; the English Message is kept for logs and
// for older clients that still display it, but it is NOT a contract: never parse it.
//
// Codes are an API: add new ones freely, never rename or reuse one. tsb-core keeps the matching
// code -> i18n key table in layers/engine/utils/gqlErrors.ts, update both together.
package apperr

// Code is the value of `extensions.code`.
type Code string

// Pre-existing codes, emitted by the auth directives and a few resolvers.
const (
	// CodeUnauthenticated: missing / expired token.
	CodeUnauthenticated Code = "UNAUTHENTICATED"
	// CodeForbidden: authenticated but not allowed (admin only, not the owner).
	CodeForbidden Code = "FORBIDDEN"
	// CodeNotFound: the requested object does not exist (or is not visible to the caller).
	CodeNotFound Code = "NOT_FOUND"
	// CodeUserError: generic bad user input without a more specific code below.
	CodeUserError Code = "USER_ERROR"
)

// Ordering availability and the chosen ready-time slot (createOrder).
const (
	// CodeOrderingUnavailable: the shop switched online ordering off.
	CodeOrderingUnavailable Code = "ORDERING_UNAVAILABLE"
	// CodeOrderingClosedToday: no ordering hours today (closed day / schedule override).
	CodeOrderingClosedToday Code = "ORDERING_CLOSED_TODAY"
	// CodeSlotRequired: the shop is closed right now, so the order needs a fixed ready time.
	CodeSlotRequired Code = "SLOT_REQUIRED"
	// CodeSlotNotToday: the ready time is not on today's date.
	CodeSlotNotToday Code = "SLOT_NOT_TODAY"
	// CodeSlotTooSoon: the ready time is inside the minimum preparation window (or already past).
	CodeSlotTooSoon Code = "SLOT_TOO_SOON"
	// CodeSlotMisaligned: the ready time is not on a 15 minute boundary.
	CodeSlotMisaligned Code = "SLOT_MISALIGNED"
	// CodeSlotOutsideHours: the ready time is outside the ordering hours.
	CodeSlotOutsideHours Code = "SLOT_OUTSIDE_HOURS"
	// CodeLunchSlotRequired: the basket holds a lunch-only product but the slot is not a weekday lunch slot.
	CodeLunchSlotRequired Code = "LUNCH_SLOT_REQUIRED"
)

// Basket content (createOrder). `productId` is attached to the extensions where it is known.
const (
	CodeOrderEmpty        Code = "ORDER_EMPTY"
	CodeOrderTooManyItems Code = "ORDER_TOO_MANY_ITEMS"
	// CodeProductNotFound: a product of the basket no longer exists (deleted since the cart was saved).
	CodeProductNotFound Code = "PRODUCT_NOT_FOUND"
	// CodeProductUnavailable: the product exists but is sold out (is_available = false).
	CodeProductUnavailable Code = "PRODUCT_UNAVAILABLE"
	// CodeInvalidQuantity: a line quantity outside 1..99.
	CodeInvalidQuantity Code = "INVALID_QUANTITY"
	// CodeSelectionInvalid: a choice that does not exist / belongs to another product or group, a
	// selection quantity <= 0, or a number of selections outside the group's min/max.
	CodeSelectionInvalid Code = "SELECTION_INVALID"
	// CodeInvalidPrice: the line would have a negative price.
	CodeInvalidPrice Code = "INVALID_PRICE"
	// CodePriceChanged: quoteOrder only. The line total the client showed (`expectedLineTotal`) is no
	// longer today's price; the line carries `currentPrice`. createOrder never returns it.
	CodePriceChanged Code = "PRICE_CHANGED"
)

// Delivery (createOrder).
const (
	// CodeDeliveryMinimumNotMet: the basket is below the delivery minimum (`minimum` in the extensions, euros).
	CodeDeliveryMinimumNotMet Code = "DELIVERY_MINIMUM_NOT_MET"
	CodeAddressRequired       Code = "ADDRESS_REQUIRED"
	CodeAddressUnresolvable   Code = "ADDRESS_UNRESOLVABLE"
	// CodeDeliveryOutOfZone: the address is beyond the delivery radius.
	CodeDeliveryOutOfZone Code = "DELIVERY_OUT_OF_ZONE"
	// CodeDeliveryAreaExcluded: the address is in a postcode we never deliver to.
	CodeDeliveryAreaExcluded Code = "DELIVERY_AREA_EXCLUDED"
	// CodeDeliveryUnavailable: this instance is takeaway-only (RESTAURANT_DELIVERY_ENABLED=false).
	CodeDeliveryUnavailable Code = "DELIVERY_UNAVAILABLE"
)

// Coupons (createOrder, and `errorCode` of the validateCoupon result).
const (
	// CodeCouponInvalid: unknown, expired, inactive or used-up coupon (deliberately not more precise).
	CodeCouponInvalid Code = "COUPON_INVALID"
	// CodeCouponMinOrderNotMet: the basket is below the coupon's minimum (`minimum` in the extensions).
	CodeCouponMinOrderNotMet Code = "COUPON_MIN_ORDER_NOT_MET"
	// CodeCouponRateLimited: too many failed attempts (per minute or per day).
	CodeCouponRateLimited Code = "COUPON_RATE_LIMITED"
	// CodeCouponAlreadyActive: the customer already has another open order that holds a coupon.
	CodeCouponAlreadyActive Code = "COUPON_ALREADY_ACTIVE"
	// CodeCouponExhausted: the coupon was valid at validation but its usage limit was reached before the order.
	CodeCouponExhausted Code = "COUPON_EXHAUSTED"
	// CodeCouponReserveFailed: server fault while reserving the coupon.
	CodeCouponReserveFailed Code = "COUPON_RESERVE_FAILED"
	// CodeCouponCheckFailed: server fault while checking the coupon (database error, ...). The code
	// is NOT known to be invalid: clients should offer a retry, not "invalid coupon". Not expected,
	// so it reaches Sentry. Returned as a GraphQL error by validateCoupon, quoteOrder and createOrder.
	CodeCouponCheckFailed Code = "COUPON_CHECK_FAILED"
)

// Abuse protection on public queries.
const (
	// CodeRateLimited: too many requests from this IP to a public query (quoteOrder, resolveAddress).
	// Expected (the caller's doing, no Sentry). Clients should back off and retry; the limit is
	// generous (60 requests/minute per IP and query) so a debounced UI never reaches it.
	CodeRateLimited Code = "RATE_LIMITED"
)

// Payment and persistence (createOrder).
const (
	// CodeCashAmountInvalid: the "I will pay with" cash amount is not a non-negative number.
	CodeCashAmountInvalid Code = "CASH_AMOUNT_INVALID"
	// CodeOrderCreateFailed: server fault while saving the order.
	CodeOrderCreateFailed Code = "ORDER_CREATE_FAILED"
	// CodePaymentFailed: the payment provider refused / failed to create the payment; the order was removed.
	CodePaymentFailed Code = "PAYMENT_FAILED"
	// CodeInvalidAmount: a malformed order amount in validateCoupon.
	CodeInvalidAmount Code = "INVALID_AMOUNT"
	// CodePaymentSettlementFailed: staff cancelled an order but its payment could not be refunded /
	// cancelled at the payment provider (or looked up). The order is left UNCHANGED, so the same
	// updateOrder can simply be retried. Dashboard only (customers cannot cancel); the message is
	// shown to staff as is, so it is kept clear and free of provider details (those are in the logs).
	CodePaymentSettlementFailed Code = "PAYMENT_SETTLEMENT_FAILED"
	// CodePaymentNotRefundable: staff cancelled a paid order whose payment Mollie cannot refund
	// (a voucher / gift card payment, an expired refund window, a refund Mollie refuses). Retrying
	// cannot help, unlike PAYMENT_SETTLEMENT_FAILED: the order is left UNCHANGED and the customer has
	// to be refunded manually, outside Mollie. There is deliberately no "cancel anyway". Dashboard
	// only; the message is shown to staff as is.
	CodePaymentNotRefundable Code = "PAYMENT_NOT_REFUNDABLE"
)

// WeChat assistant (dashboard).
const (
	// CodeAssistantDisabled: no assistant is configured on this server.
	CodeAssistantDisabled Code = "ASSISTANT_DISABLED"
	// CodeAssistantUnavailable: the assistant service did not answer.
	CodeAssistantUnavailable Code = "ASSISTANT_UNAVAILABLE"
)

// expected lists the codes that are the customer's / the world's doing rather than ours: they are
// logged as warnings and never sent to Sentry. Everything else is a server fault.
var expected = map[Code]bool{
	CodeUnauthenticated:       true,
	CodeForbidden:             true,
	CodeNotFound:              true,
	CodeUserError:             true,
	CodeOrderingUnavailable:   true,
	CodeOrderingClosedToday:   true,
	CodeSlotRequired:          true,
	CodeSlotNotToday:          true,
	CodeSlotTooSoon:           true,
	CodeSlotMisaligned:        true,
	CodeSlotOutsideHours:      true,
	CodeLunchSlotRequired:     true,
	CodeOrderEmpty:            true,
	CodeOrderTooManyItems:     true,
	CodeProductNotFound:       true,
	CodeProductUnavailable:    true,
	CodeInvalidQuantity:       true,
	CodeSelectionInvalid:      true,
	CodeInvalidPrice:          true,
	CodePriceChanged:          true,
	CodeDeliveryMinimumNotMet: true,
	CodeAddressRequired:       true,
	CodeDeliveryOutOfZone:     true,
	CodeDeliveryAreaExcluded:  true,
	CodeDeliveryUnavailable:   true,
	CodeCouponInvalid:         true,
	CodeCouponMinOrderNotMet:  true,
	CodeCouponRateLimited:     true,
	CodeCouponAlreadyActive:   true,
	CodeCouponExhausted:       true,
	CodeCashAmountInvalid:     true,
	CodeInvalidAmount:         true,
	CodeRateLimited:           true,
	CodeAssistantDisabled:     true,
	// A provider outage is not a bug of ours and the message is for the staff member who must retry;
	// the resolver logs the cause at error level itself.
	CodePaymentSettlementFailed: true,
	// Staff must act (refund by hand); the resolver logs the cause at error level itself.
	CodePaymentNotRefundable: true,
}

// IsExpected reports whether the code is a user-side error (warn log, no Sentry event).
func IsExpected(code Code) bool { return expected[code] }
