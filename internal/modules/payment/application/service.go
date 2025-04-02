package application

import (
	"context"
	"fmt"
	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/google/uuid"
	"os"
	"time"
	orderDomain "tsb-service/internal/modules/order/domain"
	"tsb-service/internal/modules/payment/domain"
	"tsb-service/pkg/sse"
)

type PaymentService interface {
	CreatePayment(ctx context.Context, o orderDomain.Order, op []orderDomain.OrderProduct) (*domain.MolliePayment, error)
	UpdatePaymentStatus(ctx context.Context, externalMolliePaymentID string) error
	GetPaymentByOrderID(ctx context.Context, orderID uuid.UUID) (*domain.MolliePayment, error)
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

func (s *paymentService) CreatePayment(ctx context.Context, o orderDomain.Order, op []orderDomain.OrderProduct) (*domain.MolliePayment, error) {
	// Preallocate slice with exact length.
	paymentLines := make([]mollie.PaymentLines, len(op))

	for i, line := range op {
		var description string
		if line.Product.Code != nil && *line.Product.Code != "" {
			description = fmt.Sprintf("%s - %s %s", *line.Product.Code, line.Product.CategoryName, line.Product.Name)
		} else {
			description = fmt.Sprintf("%s %s", line.Product.CategoryName, line.Product.Name)
		}
		paymentLines[i] = mollie.PaymentLines{
			Description:  description,
			Quantity:     int(line.Quantity),
			QuantityUnit: "pcs",
			UnitPrice:    &mollie.Amount{Value: line.UnitPrice.StringFixed(2), Currency: "EUR"},
			TotalAmount:  &mollie.Amount{Value: line.TotalPrice.StringFixed(2), Currency: "EUR"},
		}
	}

	// Retrieve base URLs from environment variables.
	appBaseUrl := os.Getenv("APP_BASE_URL")
	if appBaseUrl == "" {
		return nil, fmt.Errorf("APP_BASE_URL is required")
	}

	webhookUrl := os.Getenv("MOLLIE_WEBHOOK_URL")
	if webhookUrl == "" {
		return nil, fmt.Errorf("MOLLIE_WEBHOOK_URL is required")
	}

	redirectEndpoint := appBaseUrl + "/order-completed/" + o.ID.String()

	// Determine locale based on user language.
	locale := mollie.Locale("fr_FR")

	fmt.Println(o.TotalPrice.StringFixed(2))

	// Construct the payment request.
	paymentRequest := mollie.CreatePayment{
		Amount: &mollie.Amount{
			Value:    o.TotalPrice.StringFixed(2),
			Currency: "EUR",
		},
		Description: "Tokyo Sushi Bar",
		RedirectURL: redirectEndpoint,
		WebhookURL:  webhookUrl,
		Locale:      locale,
		Lines:       paymentLines,
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

func (s *paymentService) UpdatePaymentStatus(ctx context.Context, externalMolliePaymentID string) error {
	// Fetch the payment from Mollie
	_, externalPayment, err := s.mollieClient.Payments.Get(ctx, externalMolliePaymentID, nil)

	if err != nil {
		return fmt.Errorf("failed to fetch payment from Mollie: %w", err)
	}

	orderID, err := s.repo.RefreshStatus(ctx, *externalPayment)
	if err != nil {
		return fmt.Errorf("failed to update payment status: %w", err)
	}

	eventPayload := fmt.Sprintf(
		`{"event": "orderUpdated", "orderID": "%s", "timestamp": "%s"}`,
		orderID,
		time.Now().Format(time.RFC3339),
	)

	// Broadcast the event to all connected SSE clients.
	sse.Hub.Broadcast(eventPayload)

	return nil
}

func (s *paymentService) GetPaymentByOrderID(ctx context.Context, orderID uuid.UUID) (*domain.MolliePayment, error) {
	payment, err := s.repo.FindByOrderID(ctx, orderID)
	if err != nil {
		return nil, fmt.Errorf("failed to find payment: %w", err)
	}

	return payment, nil
}
