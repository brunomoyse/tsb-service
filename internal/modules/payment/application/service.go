package application

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"

	addressDomain "tsb-service/internal/modules/address/domain"
	orderApplication "tsb-service/internal/modules/order/application"
	orderDomain "tsb-service/internal/modules/order/domain"
	"tsb-service/internal/modules/payment/domain"
	productApplication "tsb-service/internal/modules/product/application"
	productDomain "tsb-service/internal/modules/product/domain"
	userApplication "tsb-service/internal/modules/user/application"
	userDomain "tsb-service/internal/modules/user/domain"
	"tsb-service/pkg/brand"
	es "tsb-service/pkg/email/scaleway"
	"tsb-service/pkg/utils"
)

type PaymentService interface {
	CreatePayment(ctx context.Context, o orderDomain.Order, op []orderDomain.OrderProduct, u userDomain.User, a *addressDomain.Address, customRedirectURL *string) (*domain.MolliePayment, error)
	CreateFullRefund(ctx context.Context, externalPaymentID string) error
	// SettleCancelledOrderPayment undoes the payment of an order that staff
	// just cancelled: a paid payment is refunded, an open one is cancelled at
	// Mollie so it can no longer be paid. It does not record the payment's new
	// status itself: the caller does so (PersistPaymentStatus) once the cancelled order is saved.
	SettleCancelledOrderPayment(ctx context.Context, payment *domain.MolliePayment) (domain.CancelSettlement, error)
	// FetchMollieStatus fetches the authoritative payment status from Mollie
	// without touching the local DB. The webhook handler persists it only after
	// the order business logic succeeds (see PersistPaymentStatus).
	FetchMollieStatus(ctx context.Context, externalMolliePaymentID string) (*domain.PaymentStatusUpdate, error)
	// PersistPaymentStatus writes the fetched status + timestamps to the local DB.
	PersistPaymentStatus(ctx context.Context, externalMolliePaymentID string, update *domain.PaymentStatusUpdate) error
	// WithPaymentLock serializes concurrent webhook deliveries for the same payment.
	WithPaymentLock(ctx context.Context, paymentID string, fn func(context.Context) error) error
	UpdatePaymentStatusByOrderID(ctx context.Context, orderID uuid.UUID, status string) (*domain.MolliePayment, error)
	GetPaymentByOrderID(ctx context.Context, orderID uuid.UUID) (*domain.MolliePayment, error)
	GetPaymentByExternalID(ctx context.Context, externalMolliePaymentID string) (*domain.MolliePayment, error)
	// HandlePaymentPaid processes a paid payment: refunds a cancelled order, logs an amount mismatch.
	// Returns the domain order for the caller to publish to PubSub (avoids circular import with resolver).
	HandlePaymentPaid(ctx context.Context, orderID uuid.UUID) (*orderDomain.Order, error)
	// SendPaidOrderConfirmation emails the customer after the paid status is persisted.
	SendPaidOrderConfirmation(ctx context.Context, orderID uuid.UUID) error
	HandlePaymentFailed(ctx context.Context, orderID uuid.UUID) (*orderDomain.Order, error)

	BatchGetPaymentsByOrderIDs(ctx context.Context, orderIDs []string) (map[string][]*domain.MolliePayment, error)
}

type paymentService struct {
	repo           domain.PaymentRepository
	mollieClient   mollie.Client
	orderService   orderApplication.OrderService
	userService    userApplication.UserService
	productService productApplication.ProductService
}

func NewPaymentService(
	repo domain.PaymentRepository,
	mollieClient mollie.Client,
	orderService orderApplication.OrderService,
	userService userApplication.UserService,
	productService productApplication.ProductService,
) PaymentService {
	return &paymentService{
		repo:           repo,
		mollieClient:   mollieClient,
		orderService:   orderService,
		userService:    userService,
		productService: productService,
	}
}

func (s *paymentService) CreatePayment(ctx context.Context, o orderDomain.Order, op []orderDomain.OrderProduct, u userDomain.User, a *addressDomain.Address, customRedirectURL *string) (*domain.MolliePayment, error) {
	var lines []mollie.PaymentLines
	serviceType := serviceTypeFromOrderType(o.OrderType)

	for _, line := range op {
		vatRate := line.VatRate
		if vatRate.IsZero() {
			vatRate = decimal.NewFromFloat(productDomain.VatCategory(line.Product.VatCategory).VatRatePercent(serviceType))
		}
		vatAmount := vatAmountFromGross(line.TotalPrice, vatRate)
		description, quantity, unitPrice := mollieLineAmounts(describe(line.Product), line)
		lines = append(lines, mollie.PaymentLines{
			Type:         mollie.PhysicalProductLine,
			Description:  description,
			Quantity:     quantity,
			QuantityUnit: "pcs",
			VATRate:      vatRate.StringFixed(2),
			UnitPrice:    amt(unitPrice),
			TotalAmount:  amt(line.TotalPrice),
			VATAmount:    amt(vatAmount),
		})
	}

	if o.DeliveryFee != nil && !o.DeliveryFee.IsZero() {
		lines = append(lines, mollie.PaymentLines{
			Type:        mollie.ShippingFeeLine,
			Description: "Frais de livraison",
			Quantity:    1,
			UnitPrice:   amt(*o.DeliveryFee),
			TotalAmount: amt(*o.DeliveryFee),
		})
	}

	if o.TakeawayDiscount.GreaterThan(decimal.Zero) {
		neg := o.TakeawayDiscount.Neg()
		lines = append(lines, mollie.PaymentLines{
			Type:        mollie.DiscountProductLine,
			Description: "Remise à emporter",
			Quantity:    1,
			UnitPrice:   amt(neg),
			TotalAmount: amt(neg),
		})
	}

	if o.CouponDiscount.GreaterThan(decimal.Zero) {
		neg := o.CouponDiscount.Neg()
		desc := "Réduction coupon"
		if o.CouponCode != nil {
			desc = fmt.Sprintf("Coupon %s", *o.CouponCode)
		}
		lines = append(lines, mollie.PaymentLines{
			Type:        mollie.DiscountProductLine,
			Description: desc,
			Quantity:    1,
			UnitPrice:   amt(neg),
			TotalAmount: amt(neg),
		})
	}

	if o.TransactionFee.GreaterThan(decimal.Zero) {
		lines = append(lines, mollie.PaymentLines{
			Type:        mollie.SurchargeLine,
			Description: "Frais de transaction",
			Quantity:    1,
			UnitPrice:   amt(o.TransactionFee),
			TotalAmount: amt(o.TransactionFee),
		})
	}

	// Mollie rejects a payment whose line totals don't sum exactly to the
	// amount. TotalPrice is snapped to 10 cents (clean customer-facing total)
	// while the lines are built from raw components, so a few cents of rounding
	// can diverge. Absorb any delta into a correction line.
	corr, err := roundingCorrectionLine(o.TotalPrice, lines)
	if err != nil {
		return nil, err
	}
	if corr != nil {
		lines = append(lines, *corr)
	}

	appBaseURL := os.Getenv("APP_BASE_URL")
	if appBaseURL == "" {
		return nil, fmt.Errorf("APP_BASE_URL is required")
	}

	webhookURL := os.Getenv("MOLLIE_WEBHOOK_URL")
	if webhookURL == "" {
		return nil, fmt.Errorf("MOLLIE_WEBHOOK_URL is required")
	}

	redirectURL := appBaseURL + "/order-completed/" + o.ID.String()
	if customRedirectURL != nil && *customRedirectURL != "" {
		redirectURL = *customRedirectURL + "/" + o.ID.String()
	}
	cancelURL := appBaseURL + "/checkout"

	localeMap := map[string]mollie.Locale{
		"fr": "fr_BE",
		"en": "en_US",
		"nl": "nl_BE",
		"zh": "zh_CN",
	}
	locale, ok := localeMap[o.Language]
	if !ok {
		locale = "fr_BE"
	}

	paymentRequest := mollie.CreatePayment{
		Amount: &mollie.Amount{
			Value:    o.TotalPrice.StringFixed(2),
			Currency: "EUR",
		},
		Description: brand.Current().Name,
		CancelURL:   cancelURL,
		RedirectURL: redirectURL,
		WebhookURL:  webhookURL,
		Locale:      locale,
		Lines:       lines,
	}

	if o.OrderType == orderDomain.OrderTypeDelivery {
		address := &mollie.Address{
			GivenName:       u.FirstName,
			FamilyName:      u.LastName,
			StreetAndNumber: a.StreetName + " " + a.HouseNumber,
			PostalCode:      a.Postcode,
			City:            a.MunicipalityName,
			Country:         "BE",
		}
		paymentRequest.ShippingAddress = address
		paymentRequest.BillingAddress = address
	}

	_, externalPayment, err := s.mollieClient.Payments.Create(ctx, paymentRequest, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create Mollie payment: %w", err)
	}

	// Map Mollie SDK response → domain struct
	domainPayment, err := mapExternalPayment(externalPayment, o.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to map Mollie payment: %w", err)
	}

	if err := s.repo.Save(ctx, domainPayment); err != nil {
		return nil, err
	}
	return domainPayment, nil
}

func (s *paymentService) CreateFullRefund(ctx context.Context, externalPaymentID string) error {
	payment, err := s.GetPaymentByExternalID(ctx, externalPaymentID)
	if err != nil {
		return fmt.Errorf("failed to find payment: %w", err)
	}

	if payment.Status != domain.PaymentStatusPaid {
		return fmt.Errorf("payment is not paid: %s", payment.Status)
	}

	_, err = s.refundRemaining(ctx, payment)
	return err
}

// refundRemaining refunds what is still refundable of a paid payment: its amount minus what Mollie
// says was already returned (by an earlier cancel, a webhook retry or staff in the Mollie dashboard). Repeated
// cancels or webhook retries therefore never refund twice, and a payment with nothing left is skipped
// (refunded == false). The caller is responsible for knowing the payment is paid at Mollie.
func (s *paymentService) refundRemaining(ctx context.Context, payment *domain.MolliePayment) (refunded bool, err error) {
	current, err := s.fetchMolliePayment(ctx, payment.MolliePaymentID)
	if err != nil {
		return false, err
	}
	return s.refundRemainingOf(ctx, payment, current)
}

// fetchMolliePayment reads the payment from Mollie, the authority on its status and on how much of
// it was already refunded (the local row is only as fresh as the last webhook or refund we made).
func (s *paymentService) fetchMolliePayment(ctx context.Context, molliePaymentID string) (*mollie.Payment, error) {
	_, current, err := s.mollieClient.Payments.Get(ctx, molliePaymentID, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch payment from Mollie: %w", err)
	}
	return current, nil
}

// refundRemainingOf is refundRemaining for a payment that was just read from Mollie.
func (s *paymentService) refundRemainingOf(ctx context.Context, payment *domain.MolliePayment, current *mollie.Payment) (bool, error) {
	alreadyRefunded, remaining, err := refundableAmount(payment, current)
	if err != nil {
		return false, err
	}

	if !remaining.IsPositive() {
		// Nothing left to return. If Mollie's total differs from our row (a refund whose bookkeeping
		// failed, one made in the Mollie dashboard, or one that failed later), record Mollie's.
		if !alreadyRefunded.Equal(payment.AmountRefunded) {
			if markErr := s.repo.MarkAsRefund(ctx, payment.MolliePaymentID, alreadyRefunded); markErr != nil {
				zap.L().Warn("failed to record a refund Mollie already made",
					zap.String("payment_id", payment.MolliePaymentID), zap.Error(markErr))
			}
		}
		return false, nil
	}

	refundRequest := mollie.CreatePaymentRefund{
		Amount: amt(remaining),
	}

	res, refund, err := s.mollieClient.Refunds.CreatePaymentRefund(ctx, payment.MolliePaymentID, refundRequest, nil)
	if err != nil {
		return false, fmt.Errorf("failed to create refund: %w", err)
	}

	if res.StatusCode != 200 && res.StatusCode != 201 {
		return false, fmt.Errorf("failed to create refund: %s", res.Status)
	}

	refundedAmount, parseErr := decimal.NewFromString(refund.Amount.Value)
	if parseErr != nil {
		return false, fmt.Errorf("failed to parse refund amount: %w", parseErr)
	}

	// amount_refunded is the running total of the payment, not the amount of this refund.
	if err := s.repo.MarkAsRefund(ctx, payment.MolliePaymentID, alreadyRefunded.Add(refundedAmount)); err != nil {
		return false, fmt.Errorf("failed to mark payment as refunded: %w", err)
	}

	return true, nil
}

// refundableAmount returns what was already refunded and what can still be refunded. Mollie's
// amountRefunded is the truth whenever it reports one: our row is only written after Mollie accepted
// a refund, so it can only be higher than Mollie's when a refund failed or was cancelled later, which
// gives that money back to "refundable". Our row is the fallback when Mollie omits the field. Mollie's
// amountRemaining (which also accounts for chargebacks) caps the remainder when it reports one.
func refundableAmount(payment *domain.MolliePayment, current *mollie.Payment) (alreadyRefunded, remaining decimal.Decimal, err error) {
	alreadyRefunded = payment.AmountRefunded
	if current.AmountRefunded != nil {
		fromMollie, parseErr := decimal.NewFromString(current.AmountRefunded.Value)
		if parseErr != nil {
			return decimal.Zero, decimal.Zero, fmt.Errorf("failed to parse amountRefunded: %w", parseErr)
		}
		alreadyRefunded = fromMollie
	}
	remaining = payment.Amount.Sub(alreadyRefunded)
	if current.AmountRemaining != nil {
		fromMollie, parseErr := decimal.NewFromString(current.AmountRemaining.Value)
		if parseErr != nil {
			return decimal.Zero, decimal.Zero, fmt.Errorf("failed to parse amountRemaining: %w", parseErr)
		}
		if fromMollie.LessThan(remaining) {
			remaining = fromMollie
		}
	}
	return alreadyRefunded, remaining, nil
}

func (s *paymentService) SettleCancelledOrderPayment(ctx context.Context, payment *domain.MolliePayment) (domain.CancelSettlement, error) {
	switch payment.Status {
	case domain.PaymentStatusPaid, domain.PaymentStatusOpen, domain.PaymentStatusPending, domain.PaymentStatusAuthorized:
	default:
		// canceled, expired, failed: nothing was charged and nothing can be paid any more.
		return domain.CancelSettlement{}, nil
	}

	// Decide on Mollie's state, not on our row: the customer may have paid since the last webhook, a
	// previous attempt may already have refunded or cancelled it before something later failed, and
	// the cancellation is retried until it works, so every step has to be safe to repeat.
	current, err := s.fetchMolliePayment(ctx, payment.MolliePaymentID)
	if err != nil {
		return domain.CancelSettlement{}, err
	}
	switch domain.PaymentStatus(current.Status) {
	case domain.PaymentStatusPaid:
		refunded, err := s.refundRemainingOf(ctx, payment, current)
		return domain.CancelSettlement{Refunded: refunded}, err
	case domain.PaymentStatusOpen, domain.PaymentStatusPending, domain.PaymentStatusAuthorized:
		// Not paid yet: cancel it at Mollie so the customer cannot pay for a
		// cancelled order. If Mollie no longer allows cancelling, a later
		// paid webhook refunds it (see HandlePaymentPaid).
		if !current.IsCancelable {
			zap.L().Warn("open payment of a cancelled order is not cancelable at Mollie",
				zap.String("payment_id", payment.MolliePaymentID))
			return domain.CancelSettlement{}, nil
		}
		_, cancelled, err := s.mollieClient.Payments.Cancel(ctx, payment.MolliePaymentID)
		if err != nil {
			return domain.CancelSettlement{}, fmt.Errorf("failed to cancel payment: %w", err)
		}
		// The caller records it after saving the order, so that a failed save leaves our row "open"
		// and Mollie's canceled webhook still cancels the order. Until then nobody can pay it.
		update := statusUpdateFrom(cancelled)
		update.Status = domain.PaymentStatusCanceled
		if update.CanceledAt == nil {
			now := time.Now()
			update.CanceledAt = &now
		}
		return domain.CancelSettlement{StatusUpdate: update}, nil
	default:
		// Already canceled / expired / failed at Mollie (an earlier attempt got that far, or the
		// customer let it expire): our row catches up once the order is saved.
		return domain.CancelSettlement{StatusUpdate: statusUpdateFrom(current)}, nil
	}
}

// FetchMollieStatus retrieves the authoritative payment status + timestamps from
// the Mollie API without writing to the local DB. The webhook handler persists
// this only after the order business logic succeeds, so a failed delivery is
// retried by Mollie and re-runs the business logic (the stored status is the
// commit marker).
func (s *paymentService) FetchMollieStatus(ctx context.Context, externalMolliePaymentID string) (*domain.PaymentStatusUpdate, error) {
	_, externalPayment, err := s.mollieClient.Payments.Get(ctx, externalMolliePaymentID, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch payment from Mollie: %w", err)
	}

	return statusUpdateFrom(externalPayment), nil
}

// statusUpdateFrom maps the status and timestamps of a Mollie payment.
func statusUpdateFrom(p *mollie.Payment) *domain.PaymentStatusUpdate {
	return &domain.PaymentStatusUpdate{
		Status:       domain.PaymentStatus(p.Status),
		PaidAt:       p.PaidAt,
		AuthorizedAt: p.AuthorizedAt,
		CanceledAt:   p.CanceledAt,
		ExpiredAt:    p.ExpiredAt,
		FailedAt:     p.FailedAt,
	}
}

// PersistPaymentStatus writes the status + timestamps to the local DB.
func (s *paymentService) PersistPaymentStatus(ctx context.Context, externalMolliePaymentID string, update *domain.PaymentStatusUpdate) error {
	if _, err := s.repo.RefreshStatus(ctx, externalMolliePaymentID, update); err != nil {
		return fmt.Errorf("failed to update payment status: %w", err)
	}
	return nil
}

// WithPaymentLock serializes concurrent webhook deliveries for the same payment.
func (s *paymentService) WithPaymentLock(ctx context.Context, paymentID string, fn func(context.Context) error) error {
	return s.repo.WithPaymentLock(ctx, paymentID, fn)
}

func (s *paymentService) UpdatePaymentStatusByOrderID(ctx context.Context, orderID uuid.UUID, status string) (*domain.MolliePayment, error) {
	payment, err := s.repo.UpdateStatusByOrderID(ctx, orderID, domain.PaymentStatus(status))
	if err != nil {
		return nil, fmt.Errorf("failed to update payment status: %w", err)
	}
	return payment, nil
}

func (s *paymentService) GetPaymentByExternalID(ctx context.Context, externalPaymentID string) (*domain.MolliePayment, error) {
	payment, err := s.repo.FindByExternalID(ctx, externalPaymentID)
	if err != nil {
		return nil, fmt.Errorf("failed to find payment: %w", err)
	}
	return payment, nil
}

func (s *paymentService) GetPaymentByOrderID(ctx context.Context, orderID uuid.UUID) (*domain.MolliePayment, error) {
	payment, err := s.repo.FindByOrderID(ctx, orderID)
	if err != nil {
		return nil, fmt.Errorf("failed to find payment: %w", err)
	}
	return payment, nil
}

func (s *paymentService) BatchGetPaymentsByOrderIDs(ctx context.Context, orderIDs []string) (map[string][]*domain.MolliePayment, error) {
	return s.repo.FindByOrderIDs(ctx, orderIDs)
}

// HandlePaymentPaid handles the business logic when a payment is confirmed as paid:
// refunds a cancelled order, logs an amount mismatch. It sends nothing: the confirmation
// email is SendPaidOrderConfirmation, called after the paid status is persisted.
// Returns the order so the caller can publish to PubSub (avoids circular import with resolver).
func (s *paymentService) HandlePaymentPaid(ctx context.Context, orderID uuid.UUID) (*orderDomain.Order, error) {
	order, orderProducts, err := s.orderService.GetOrderByID(ctx, orderID)
	if err != nil || order == nil {
		return nil, fmt.Errorf("failed to retrieve order: %w", err)
	}
	if orderProducts == nil {
		return nil, fmt.Errorf("no order products found for order %s", orderID)
	}

	// The order was cancelled (by staff, or a failed earlier attempt) while
	// its checkout was still open, and the customer paid it anyway. Refund
	// instead of announcing a new order. Runs before the paid status is
	// persisted, so a failed refund returns an error and Mollie retries;
	// refundRemaining skips an already refunded payment on that retry.
	if order.OrderStatus == orderDomain.OrderStatusCanceled || order.OrderStatus == orderDomain.OrderStatusFailed {
		return order, s.refundPaidCancelledOrder(ctx, order)
	}

	// Amount verification: log mismatch for manual review, don't block the order
	payment, paymentErr := s.repo.FindByOrderID(ctx, orderID)
	if paymentErr == nil && payment != nil && !payment.Amount.Equal(order.TotalPrice) {
		zap.L().Error("payment amount mismatch",
			zap.String("order_id", orderID.String()),
			zap.String("paid", payment.Amount.String()),
			zap.String("expected", order.TotalPrice.String()),
		)
	}

	return order, nil
}

// SendPaidOrderConfirmation emails the customer that their paid order is awaiting
// validation (respecting NotifyOrderUpdates). The webhook calls it only after the paid
// status is persisted, so a Mollie retry never sends it twice. The error is for logging.
func (s *paymentService) SendPaidOrderConfirmation(ctx context.Context, orderID uuid.UUID) error {
	order, orderProducts, err := s.orderService.GetOrderByID(ctx, orderID)
	if err != nil || order == nil {
		return fmt.Errorf("failed to retrieve order: %w", err)
	}
	if orderProducts == nil {
		return fmt.Errorf("no order products found for order %s", orderID)
	}

	u, err := s.userService.GetUserByID(ctx, order.UserID.String())
	if err != nil || u == nil {
		return fmt.Errorf("failed to retrieve user: %w", err)
	}
	if !u.NotifyOrderUpdates {
		return nil
	}

	productIDs := make([]string, len(*orderProducts))
	for i, op := range *orderProducts {
		productIDs[i] = op.ProductID.String()
	}

	// Names only, whatever the availability now: a product that sold out after the customer paid
	// must not prevent the confirmation.
	products, err := s.productService.GetProductNamesForInvoice(ctx, productIDs)
	if err != nil {
		return fmt.Errorf("failed to retrieve products: %w", err)
	}

	productMap := make(map[uuid.UUID]productDomain.ProductOrderDetails, len(products))
	for _, p := range products {
		productMap[p.ID] = *p
	}

	orderProductsResponse := make([]orderDomain.OrderProduct, len(*orderProducts))
	for i, op := range *orderProducts {
		prod, ok := productMap[op.ProductID]
		if !ok {
			return fmt.Errorf("product %s not found", op.ProductID)
		}
		orderProductsResponse[i] = orderDomain.OrderProduct{
			Product: orderDomain.Product{
				ID:           prod.ID,
				Code:         prod.Code,
				CategoryName: prod.CategoryName,
				Name:         prod.Name,
				VatCategory:  string(prod.VatCategory),
			},
			Quantity:   op.Quantity,
			UnitPrice:  op.UnitPrice,
			TotalPrice: op.TotalPrice,
			VatRate:    op.VatRateApplied,
		}
	}

	if err := es.SendOrderPendingEmail(*u, order.Language, *order, orderProductsResponse); err != nil {
		return fmt.Errorf("failed to send order pending email: %w", err)
	}
	return nil
}

// refundPaidCancelledOrder refunds a payment that Mollie reports as paid for
// an order that is already cancelled, and tells the customer.
func (s *paymentService) refundPaidCancelledOrder(ctx context.Context, order *orderDomain.Order) error {
	payment, err := s.repo.FindByOrderID(ctx, order.ID)
	if err != nil || payment == nil {
		return fmt.Errorf("failed to find payment for cancelled order %s: %w", order.ID, err)
	}
	refunded, err := s.refundRemaining(ctx, payment)
	if err != nil {
		return err
	}
	zap.L().Warn("payment completed for a cancelled order, refunded",
		zap.String("order_id", order.ID.String()), zap.Bool("refund_issued", refunded))
	if !refunded {
		return nil
	}
	u, err := s.userService.GetUserByID(ctx, order.UserID.String())
	if err != nil || u == nil || !u.NotifyOrderUpdates {
		return nil
	}
	if emailErr := es.SendRefundIssuedEmail(*u, order.Language, order.ID.String(), utils.FormatDecimal(payment.Amount)); emailErr != nil {
		zap.L().Error("failed to send refund issued email", zap.String("order_id", order.ID.String()), zap.Error(emailErr))
	}
	return nil
}

// HandlePaymentFailed handles the business logic when a payment is cancelled/failed/expired:
// updates order status to CANCELLED. Coupon usage rollback is handled centrally by
// OrderService.UpdateOrder on the transition into CANCELED (covering cash/admin/POS
// cancellations too), so it is not repeated here. No email is sent — users frequently
// retry the checkout in a fresh order, and a failure notification on the abandoned
// attempt would contradict the successful retry.
func (s *paymentService) HandlePaymentFailed(ctx context.Context, orderID uuid.UUID) (*orderDomain.Order, error) {
	canceledStatus := orderDomain.OrderStatusCanceled
	if err := s.orderService.UpdateOrder(ctx, orderID, &canceledStatus, nil, nil); err != nil {
		return nil, fmt.Errorf("failed to update order status: %w", err)
	}

	// Fetch the updated order for PubSub notification
	order, _, orderErr := s.orderService.GetOrderByID(ctx, orderID)
	if orderErr != nil || order == nil {
		zap.L().Error("failed to retrieve order after payment failure", zap.String("order_id", orderID.String()), zap.Error(orderErr))
		return nil, nil
	}

	return order, nil
}

// mapExternalPayment converts a Mollie SDK payment object to the domain struct.
func mapExternalPayment(external *mollie.Payment, orderID uuid.UUID) (*domain.MolliePayment, error) {
	amount, err := decimal.NewFromString(external.Amount.Value)
	if err != nil {
		return nil, fmt.Errorf("failed to convert amount: %w", err)
	}

	amountRefunded := decimal.Zero
	if external.AmountRefunded != nil {
		amountRefunded, err = decimal.NewFromString(external.AmountRefunded.Value)
		if err != nil {
			return nil, fmt.Errorf("failed to convert amountRefunded: %w", err)
		}
	}

	amountRemaining := decimal.Zero
	if external.AmountRemaining != nil {
		amountRemaining, err = decimal.NewFromString(external.AmountRemaining.Value)
		if err != nil {
			return nil, fmt.Errorf("failed to convert amountRemaining: %w", err)
		}
	}

	amountCaptured := decimal.Zero
	if external.AmountCaptured != nil {
		amountCaptured, err = decimal.NewFromString(external.AmountCaptured.Value)
		if err != nil {
			return nil, fmt.Errorf("failed to convert amountCaptured: %w", err)
		}
	}

	amountChargedBack := decimal.Zero
	if external.AmountChargedBack != nil {
		amountChargedBack, err = decimal.NewFromString(external.AmountChargedBack.Value)
		if err != nil {
			return nil, fmt.Errorf("failed to convert amountChargedBack: %w", err)
		}
	}

	settlementAmount := decimal.Zero
	if external.SettlementAmount != nil {
		settlementAmount, err = decimal.NewFromString(external.SettlementAmount.Value)
		if err != nil {
			return nil, fmt.Errorf("failed to convert settlementAmount: %w", err)
		}
	}

	var metadataJSON string
	if external.Metadata != nil {
		raw, marshalErr := json.Marshal(external.Metadata)
		if marshalErr != nil {
			return nil, fmt.Errorf("failed to marshal metadata: %w", marshalErr)
		}
		metadataJSON = string(raw)
	} else {
		metadataJSON = "null"
	}

	linksRaw, err := json.Marshal(external.Links)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal links: %w", err)
	}

	return &domain.MolliePayment{
		Resource:                        &external.Resource,
		MolliePaymentID:                 external.ID,
		Status:                          domain.PaymentStatus(external.Status),
		Description:                     &external.Description,
		CancelURL:                       &external.CancelURL,
		WebhookURL:                      &external.WebhookURL,
		CountryCode:                     &external.CountryCode,
		RestrictPaymentMethodsToCountry: &external.RestrictPaymentMethodsToCountry,
		ProfileID:                       &external.ProfileID,
		SettlementID:                    &external.SettlementID,
		OrderID:                         orderID,
		IsCancelable:                    external.IsCancelable,
		Metadata:                        []byte(metadataJSON),
		Links:                           []byte(linksRaw),
		CreatedAt:                       *external.CreatedAt,
		AuthorizedAt:                    external.AuthorizedAt,
		PaidAt:                          external.PaidAt,
		CanceledAt:                      external.CanceledAt,
		ExpiresAt:                       external.ExpiresAt,
		ExpiredAt:                       external.ExpiredAt,
		FailedAt:                        external.FailedAt,
		Amount:                          amount,
		AmountRefunded:                  amountRefunded,
		AmountRemaining:                 amountRemaining,
		AmountCaptured:                  amountCaptured,
		AmountChargedBack:               amountChargedBack,
		SettlementAmount:                settlementAmount,
	}, nil
}

// roundingCorrectionLine returns a Mollie line that absorbs any gap between the
// charged total and the sum of the existing line totals (as Mollie sees them,
// i.e. StringFixed(2)), or nil when they already match. Mollie rejects a
// payment whose line totals don't sum exactly to the amount.
func roundingCorrectionLine(total decimal.Decimal, lines []mollie.PaymentLines) (*mollie.PaymentLines, error) {
	var sum decimal.Decimal
	for _, l := range lines {
		v, err := decimal.NewFromString(l.TotalAmount.Value)
		if err != nil {
			return nil, fmt.Errorf("failed to parse line amount %q: %w", l.TotalAmount.Value, err)
		}
		sum = sum.Add(v)
	}
	diff := total.Sub(sum)
	if diff.IsZero() {
		return nil, nil
	}
	lineType := mollie.SurchargeLine
	if diff.IsNegative() {
		lineType = mollie.DiscountProductLine
	}
	return &mollie.PaymentLines{
		Type:        lineType,
		Description: "Ajustement",
		Quantity:    1,
		UnitPrice:   amt(diff),
		TotalAmount: amt(diff),
	}, nil
}

// mollieLineAmounts returns the description, quantity and unit price to send
// for an order line. Mollie requires unitPrice × quantity == totalAmount.
// Option surcharges are priced once per line (see orderDomain.PriceLine), so the
// stored unit_price (rounded to cents) does not always multiply back to
// total_price. When it doesn't, send the line as quantity 1 at its total, with
// the real quantity kept in the description, rather than a mismatching line.
func mollieLineAmounts(description string, line orderDomain.OrderProduct) (string, int, decimal.Decimal) {
	qty := decimal.NewFromInt(line.Quantity)
	if line.UnitPrice.Mul(qty).Equal(line.TotalPrice) {
		return description, int(line.Quantity), line.UnitPrice
	}
	return fmt.Sprintf("%d × %s", line.Quantity, description), 1, line.TotalPrice
}

func amt(d decimal.Decimal) *mollie.Amount {
	return &mollie.Amount{
		Value:    d.StringFixed(2),
		Currency: "EUR",
	}
}

func describe(p orderDomain.Product) string {
	label := strings.TrimSpace(p.CategoryName + " " + p.Name)
	if p.Code != nil && *p.Code != "" {
		if label == "" {
			return *p.Code
		}
		return fmt.Sprintf("%s ‒ %s", *p.Code, label)
	}
	if label == "" {
		// Mollie rejects a blank line description, which would fail the whole payment.
		return p.ID.String()
	}
	return label
}

func serviceTypeFromOrderType(orderType orderDomain.OrderType) productDomain.ServiceType {
	if orderType == orderDomain.OrderTypeDelivery {
		return productDomain.ServiceTypeDelivery
	}
	return productDomain.ServiceTypeTakeaway
}

func vatAmountFromGross(gross decimal.Decimal, rate decimal.Decimal) decimal.Decimal {
	if rate.IsZero() {
		return decimal.Zero
	}
	return gross.Mul(rate).Div(decimal.NewFromInt(100).Add(rate))
}
