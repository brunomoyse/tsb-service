package application

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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
	es "tsb-service/pkg/email/scaleway"
)

type PaymentService interface {
	CreatePayment(ctx context.Context, o orderDomain.Order, op []orderDomain.OrderProduct, u userDomain.User, a *addressDomain.Address, customRedirectURL *string) (*domain.MolliePayment, error)
	CreateFullRefund(ctx context.Context, externalPaymentID string) error
	// UpdatePaymentStatus fetches the latest status from Mollie, updates the local DB,
	// and returns the status update details + associated order ID.
	UpdatePaymentStatus(ctx context.Context, externalMolliePaymentID string) (*domain.PaymentStatusUpdate, *uuid.UUID, error)
	UpdatePaymentStatusByOrderID(ctx context.Context, orderID uuid.UUID, status string) (*domain.MolliePayment, error)
	GetPaymentByOrderID(ctx context.Context, orderID uuid.UUID) (*domain.MolliePayment, error)
	GetPaymentByExternalID(ctx context.Context, externalMolliePaymentID string) (*domain.MolliePayment, error)
	// HandlePaymentPaid processes a paid payment: verifies amount, enriches order, sends email.
	// Returns the domain order for the caller to publish to PubSub (avoids circular import with resolver).
	HandlePaymentPaid(ctx context.Context, orderID uuid.UUID) (*orderDomain.Order, error)
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

	for _, line := range op {
		lines = append(lines, mollie.PaymentLines{
			Type:         mollie.PhysicalProductLine,
			Description:  describe(line.Product),
			Quantity:     int(line.Quantity),
			QuantityUnit: "pcs",
			UnitPrice:    amt(line.UnitPrice),
			TotalAmount:  amt(line.TotalPrice),
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
		Description: "Tokyo Sushi Bar",
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

	refundRequest := mollie.CreatePaymentRefund{
		Amount: amt(payment.Amount),
	}

	res, refund, err := s.mollieClient.Refunds.CreatePaymentRefund(ctx, externalPaymentID, refundRequest, nil)
	if err != nil {
		return fmt.Errorf("failed to create refund: %w", err)
	}

	if res.StatusCode != 200 && res.StatusCode != 201 {
		return fmt.Errorf("failed to create refund: %s", res.Status)
	}

	refundedAmount, parseErr := decimal.NewFromString(refund.Amount.Value)
	if parseErr != nil {
		return fmt.Errorf("failed to parse refund amount: %w", parseErr)
	}

	if err := s.repo.MarkAsRefund(ctx, externalPaymentID, refundedAmount); err != nil {
		return fmt.Errorf("failed to mark payment as refunded: %w", err)
	}

	return nil
}

// UpdatePaymentStatus fetches the current payment from Mollie, updates the local DB
// with status + timestamps, and returns the update details + order ID.
func (s *paymentService) UpdatePaymentStatus(ctx context.Context, externalMolliePaymentID string) (*domain.PaymentStatusUpdate, *uuid.UUID, error) {
	_, externalPayment, err := s.mollieClient.Payments.Get(ctx, externalMolliePaymentID, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to fetch payment from Mollie: %w", err)
	}

	update := &domain.PaymentStatusUpdate{
		Status:       domain.PaymentStatus(externalPayment.Status),
		PaidAt:       externalPayment.PaidAt,
		AuthorizedAt: externalPayment.AuthorizedAt,
		CanceledAt:   externalPayment.CanceledAt,
		ExpiredAt:    externalPayment.ExpiredAt,
		FailedAt:     externalPayment.FailedAt,
	}

	orderID, err := s.repo.RefreshStatus(ctx, externalMolliePaymentID, update)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to update payment status: %w", err)
	}

	return update, orderID, nil
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
// verifies amount, fetches order/products/user, sends confirmation email.
// Returns the order so the caller can publish to PubSub (avoids circular import with resolver).
func (s *paymentService) HandlePaymentPaid(ctx context.Context, orderID uuid.UUID) (*orderDomain.Order, error) {
	order, orderProducts, err := s.orderService.GetOrderByID(ctx, orderID)
	if err != nil || order == nil {
		return nil, fmt.Errorf("failed to retrieve order: %w", err)
	}
	if orderProducts == nil {
		return nil, fmt.Errorf("no order products found for order %s", orderID)
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

	// Load product details
	productIDs := make([]string, len(*orderProducts))
	for i, op := range *orderProducts {
		productIDs[i] = op.ProductID.String()
	}

	products, err := s.productService.GetProductsByIDs(ctx, productIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve products: %w", err)
	}

	productMap := make(map[uuid.UUID]productDomain.ProductOrderDetails, len(products))
	for _, p := range products {
		productMap[p.ID] = *p
	}

	orderProductsResponse := make([]orderDomain.OrderProduct, len(*orderProducts))
	for i, op := range *orderProducts {
		prod, ok := productMap[op.ProductID]
		if !ok {
			return nil, fmt.Errorf("product %s not found", op.ProductID)
		}
		orderProductsResponse[i] = orderDomain.OrderProduct{
			Product: orderDomain.Product{
				ID:           prod.ID,
				Code:         prod.Code,
				CategoryName: prod.CategoryName,
				Name:         prod.Name,
			},
			Quantity:   op.Quantity,
			UnitPrice:  op.UnitPrice,
			TotalPrice: op.TotalPrice,
		}
	}

	u, err := s.userService.GetUserByID(ctx, order.UserID.String())
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve user: %w", err)
	}

	// Send confirmation email (respect user's order-updates preference)
	if u.NotifyOrderUpdates {
		if emailErr := es.SendOrderPendingEmail(*u, order.Language, *order, orderProductsResponse); emailErr != nil {
			zap.L().Error("failed to send order pending email", zap.String("order_id", orderID.String()), zap.Error(emailErr))
		}
	}

	return order, nil
}

// HandlePaymentFailed handles the business logic when a payment is cancelled/failed/expired:
// updates order status to CANCELLED and sends failure email.
func (s *paymentService) HandlePaymentFailed(ctx context.Context, orderID uuid.UUID) (*orderDomain.Order, error) {
	canceledStatus := orderDomain.OrderStatusCanceled
	if err := s.orderService.UpdateOrder(ctx, orderID, &canceledStatus, nil, nil); err != nil {
		return nil, fmt.Errorf("failed to update order status: %w", err)
	}

	// Fetch the updated order for PubSub notification and email
	order, _, orderErr := s.orderService.GetOrderByID(ctx, orderID)
	if orderErr != nil || order == nil {
		zap.L().Error("failed to retrieve order after payment failure", zap.String("order_id", orderID.String()), zap.Error(orderErr))
		return nil, nil
	}

	// Send failure email asynchronously
	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		u, userErr := s.userService.GetUserByID(bgCtx, order.UserID.String())
		if userErr != nil {
			zap.L().Error("failed to retrieve user for payment failed email", zap.String("order_id", orderID.String()), zap.Error(userErr))
			return
		}

		if u.NotifyOrderUpdates {
			if emailErr := es.SendPaymentFailedEmail(*u, order.Language); emailErr != nil {
				zap.L().Error("failed to send payment failed email", zap.String("order_id", orderID.String()), zap.Error(emailErr))
			}
		}
	}()

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

func amt(d decimal.Decimal) *mollie.Amount {
	return &mollie.Amount{
		Value:    d.StringFixed(2),
		Currency: "EUR",
	}
}

func describe(p orderDomain.Product) string {
	if p.Code != nil && *p.Code != "" {
		return fmt.Sprintf("%s ‒ %s %s", *p.Code, p.CategoryName, p.Name)
	}
	return fmt.Sprintf("%s %s", p.CategoryName, p.Name)
}
