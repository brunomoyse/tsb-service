package application

import (
	"context"
	"fmt"
	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"os"
	addressDomain "tsb-service/internal/modules/address/domain"
	orderDomain "tsb-service/internal/modules/order/domain"
	"tsb-service/internal/modules/payment/domain"
	userDomain "tsb-service/internal/modules/user/domain"
)

type PaymentService interface {
	CreatePayment(ctx context.Context, o orderDomain.Order, op []orderDomain.OrderProduct, u userDomain.User, a *addressDomain.Address) (*domain.MolliePayment, error)
	CreateFullRefund(ctx context.Context, externalPaymentID string) (*mollie.Refund, error)
	UpdatePaymentStatus(ctx context.Context, externalMolliePaymentID string) error
	UpdatePaymentStatusByOrderID(ctx context.Context, orderID uuid.UUID, status string) (*domain.MolliePayment, error)
	GetPaymentByOrderID(ctx context.Context, orderID uuid.UUID) (*domain.MolliePayment, error)
	GetExternalPaymentByID(ctx context.Context, externalMolliePaymentID string) (*mollie.Response, *mollie.Payment, error)
	GetPaymentByExternalID(ctx context.Context, externalMolliePaymentID string) (*domain.MolliePayment, error)

	BatchGetPaymentsByOrderIDs(ctx context.Context, orderIDs []string) (map[string][]*domain.MolliePayment, error)
}

type paymentService struct {
	repo         domain.PaymentRepository
	mollieClient mollie.Client
}

func NewPaymentService(repo domain.PaymentRepository, mollieClient mollie.Client) PaymentService {
	return &paymentService{
		repo:         repo,
		mollieClient: mollieClient,
	}
}

func (s *paymentService) CreatePayment(ctx context.Context, o orderDomain.Order, op []orderDomain.OrderProduct, u userDomain.User, a *addressDomain.Address) (*domain.MolliePayment, error) {
	var lines []mollie.PaymentLines

	// line items
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

	// shipping fee
	if o.DeliveryFee != nil && !o.DeliveryFee.IsZero() {
		lines = append(lines, mollie.PaymentLines{
			Type:        mollie.ShippingFeeLine,
			Description: "Frais de livraison",
			Quantity:    1,
			UnitPrice:   amt(*o.DeliveryFee),
			TotalAmount: amt(*o.DeliveryFee),
		})
	}

	// discount
	if o.DiscountAmount.Cmp(decimal.Zero) > 0 {
		neg := o.DiscountAmount.Neg() // make it negative

		lines = append(lines, mollie.PaymentLines{
			Type:        mollie.DiscountProductLine,
			Description: "Remise à emporter",
			Quantity:    1,
			UnitPrice:   amt(neg),
			TotalAmount: amt(neg),
		})
	}

	// online payment surcharge (if any)
	//if o.IsOnlinePayment {
	//	// adjust Type/Description as needed
	//	lines = append(lines, mollie.PaymentLines{
	//		Type:        "surcharge",
	//		Description: "Online payment fee",
	//		UnitPrice:   amt(decimal.Zero), // or actual surcharge amount
	//		TotalAmount: amt(decimal.Zero),
	//	})
	//}

	// Retrieve base URLs from environment variables.
	appBaseUrl := os.Getenv("APP_BASE_URL")
	if appBaseUrl == "" {
		return nil, fmt.Errorf("APP_BASE_URL is required")
	}

	webhookUrl := os.Getenv("MOLLIE_WEBHOOK_URL")
	if webhookUrl == "" {
		return nil, fmt.Errorf("MOLLIE_WEBHOOK_URL is required")
	}

	redirectUrl := appBaseUrl + "/order-completed/" + o.ID.String()
	cancelUrl := appBaseUrl + "/checkout"

	// Determine locale based on user language.
	locale := mollie.Locale("fr_FR")

	// Construct the payment request.
	paymentRequest := mollie.CreatePayment{
		Amount: &mollie.Amount{
			Value:    o.TotalPrice.StringFixed(2),
			Currency: "EUR",
		},
		Description: "Tokyo Sushi Bar",
		CancelURL:   cancelUrl,
		RedirectURL: redirectUrl,
		WebhookURL:  webhookUrl,
		Locale:      locale,
		Lines:       lines,
	}

	// If delivery push addresses
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
	} else {
		paymentRequest.ShippingAddress = nil
		paymentRequest.BillingAddress = nil
	}

	// Create the payment via the Mollie client.
	_, externalMolliePayment, err := s.mollieClient.Payments.Create(ctx, paymentRequest, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create Mollie payment: %w", err)
	}

	// Save the payment to the database
	payment, err := s.repo.Save(ctx, *externalMolliePayment, o.ID)
	if err != nil {
		return nil, err
	}
	return payment, nil
}

func (s *paymentService) CreateFullRefund(ctx context.Context, externalPaymentID string) (*mollie.Refund, error) {
	// Check if the payment is paid
	payment, err := s.GetPaymentByExternalID(ctx, externalPaymentID)
	if err != nil {
		return nil, fmt.Errorf("failed to find payment: %w", err)
	}

	if payment.Status != mollie.Paid {
		return nil, fmt.Errorf("payment is not paid: %s", payment.Status)
	}

	// Create a refund request
	refundRequest := mollie.CreatePaymentRefund{
		Amount: amt(payment.Amount),
	}

	// Create the refund via the Mollie client
	res, refund, err := s.mollieClient.Refunds.CreatePaymentRefund(ctx, externalPaymentID, refundRequest, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create refund: %w", err)
	}

	// If 200/201 OK, the refund was created successfully
	if res.StatusCode != 200 && res.StatusCode != 201 {
		return nil, fmt.Errorf("failed to create refund: %s", res.Status)
	}

	// Save the refund to the database
	err = s.repo.MarkAsRefund(ctx, externalPaymentID, refund.Amount)
	if err != nil {
		return nil, fmt.Errorf("failed to mark payment as refunded: %w", err)
	}

	return refund, nil
}

func (s *paymentService) UpdatePaymentStatus(ctx context.Context, externalMolliePaymentID string) error {
	// Fetch the payment from Mollie
	_, externalPayment, err := s.GetExternalPaymentByID(ctx, externalMolliePaymentID)

	if err != nil {
		return fmt.Errorf("failed to fetch payment from Mollie: %w", err)
	}

	_, err = s.repo.RefreshStatus(ctx, *externalPayment)
	if err != nil {
		return fmt.Errorf("failed to update payment status: %w", err)
	}

	return nil
}

func (s *paymentService) UpdatePaymentStatusByOrderID(ctx context.Context, orderID uuid.UUID, status string) (*domain.MolliePayment, error) {
	// Convert string status to mollie.OrderStatus
	mollieStatus := mollie.OrderStatus(status)

	// Update the payment status in the database
	payment, err := s.repo.UpdateStatusByOrderID(ctx, orderID, mollieStatus)
	if err != nil {
		return nil, fmt.Errorf("failed to update payment status: %w", err)
	}

	return payment, nil
}

func (s *paymentService) GetExternalPaymentByID(ctx context.Context, externalMolliePaymentID string) (*mollie.Response, *mollie.Payment, error) {
	return s.mollieClient.Payments.Get(ctx, externalMolliePaymentID, nil)
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

// helper to build a *mollie.Amount from a decimal.Decimal
func amt(d decimal.Decimal) *mollie.Amount {
	return &mollie.Amount{
		Value:    d.StringFixed(2),
		Currency: "EUR",
	}
}

// build description for a line item
func describe(p orderDomain.Product) string {
	if p.Code != nil && *p.Code != "" {
		return fmt.Sprintf("%s ‒ %s %s", *p.Code, p.CategoryName, p.Name)
	}
	return fmt.Sprintf("%s %s", p.CategoryName, p.Name)
}
