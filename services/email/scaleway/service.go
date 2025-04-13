package scaleway

import (
	"fmt"
	"github.com/scaleway/scaleway-sdk-go/logger"
	"os"
	orderDomain "tsb-service/internal/modules/order/domain"

	temv1alpha1 "github.com/scaleway/scaleway-sdk-go/api/tem/v1alpha1"
	"github.com/scaleway/scaleway-sdk-go/scw"
	userDomain "tsb-service/internal/modules/user/domain"
)

// temClient is our instance for interacting with Scaleway TEM.
var temClient *temv1alpha1.API

var baseReq *temv1alpha1.CreateEmailRequest

// InitService initializes the Scaleway TEM client using credentials from environment variables.
func InitService() error {
	accessKey := os.Getenv("SCW_ACCESS_KEY")
	secretKey := os.Getenv("SCW_SECRET_KEY")

	organizationID := os.Getenv("SCW_DEFAULT_ORGANIZATION_ID")
	projectID := os.Getenv("SCW_DEFAULT_PROJECT_ID")
	region := os.Getenv("SCW_REGION")

	// Create a Scaleway client with your credentials.
	scwClient, err := scw.NewClient(
		scw.WithAuth(accessKey, secretKey),
		scw.WithDefaultOrganizationID(organizationID),
		scw.WithDefaultProjectID(projectID),
		scw.WithDefaultRegion(scw.Region(region)),
	)
	if err != nil {
		return fmt.Errorf("failed to create Scaleway client: %w", err)
	}

	// Instantiate the TEM API using the Scaleway client.
	temClient = temv1alpha1.NewAPI(scwClient)

	senderEmail := os.Getenv("SCW_SENDER_EMAIL")
	senderName := os.Getenv("SCW_SENDER_NAME")

	// Load infos in the base CreateEmailRequest
	baseReq = &temv1alpha1.CreateEmailRequest{
		Region: scw.Region(region),
		From: &temv1alpha1.CreateEmailRequestAddress{
			Email: senderEmail,
			Name:  &senderName,
		},
		ProjectID: projectID,
	}

	return nil
}

func SendVerificationEmail(user userDomain.User, lang string, verificationURL string) error {
	// Copy baseReq to avoid modifying the original request.
	newReq := *baseReq

	userFullName := fmt.Sprintf("%s %s", user.FirstName, user.LastName)

	// Fill "To" field.
	to := temv1alpha1.CreateEmailRequestAddress{
		Email: user.Email,
		Name:  &userFullName,
	}

	// Push the address to the list of recipients.
	newReq.To = append(newReq.To, &to)

	// Determine the template path based on the user's language.
	path := fmt.Sprintf("templates/%s/verify.html", lang)

	htmlContent, err := renderVerifyEmailHTML(path, user, verificationURL)
	if err != nil {
		return fmt.Errorf("failed to render email template: %w", err)
	}

	plainTextContent, err := renderVerifyEmailText(path, user, verificationURL)
	if err != nil {
		return fmt.Errorf("failed to render email template: %w", err)
	}

	// Localized subject lines.
	subjects := map[string]string{
		"en": "Please verify your email",
		"fr": "Veuillez vérifier votre adresse e-mail",
		"zh": "验证您的邮箱",
	}

	subject, ok := subjects[lang]
	if !ok {
		subject = subjects["fr"]
	}

	newReq.Subject = subject
	newReq.HTML = htmlContent
	newReq.Text = plainTextContent
	// Send the email using the Scaleway TEM API.
	_, err = temClient.CreateEmail(&newReq)
	if err != nil {
		return fmt.Errorf("failed to send email: %w", err)
	}

	// Log the email sending process.
	logger.Debugf("Email sent to %s with subject: %s", user.Email, subject)

	return nil
}

func SendWelcomeEmail(user userDomain.User, lang, menuURL string) error {
	// Copy baseReq to avoid modifying the original request.
	newReq := *baseReq

	userFullName := fmt.Sprintf("%s %s", user.FirstName, user.LastName)

	// Fill "To" field.
	to := temv1alpha1.CreateEmailRequestAddress{
		Email: user.Email,
		Name:  &userFullName,
	}

	// Push the address to the list of recipients.
	newReq.To = append(newReq.To, &to)

	// Determine the template path based on the user's language.
	path := fmt.Sprintf("templates/%s/welcome.html", lang)

	htmlContent, err := renderWelcomeEmailHTML(path, user, menuURL)
	if err != nil {
		return fmt.Errorf("failed to render email template: %w", err)
	}

	plainTextContent, err := renderWelcomeEmailText(path, user, menuURL)
	if err != nil {
		return fmt.Errorf("failed to render email template: %w", err)
	}

	subjects := map[string]string{
		"en": "Welcome to Tokyo Sushi Bar",
		"fr": "Bienvenue chez Tokyo Sushi Bar",
		"zh": "欢迎光临 Tokyo Sushi Bar",
	}

	subject, ok := subjects[lang]
	if !ok {
		subject = subjects["fr"]
	}

	newReq.Subject = subject
	newReq.HTML = htmlContent
	newReq.Text = plainTextContent

	// Send the email using the Scaleway TEM API.
	_, err = temClient.CreateEmail(&newReq)
	if err != nil {
		return fmt.Errorf("failed to send email: %w", err)
	}

	logger.Debugf("Email sent to %s with subject: %s", user.Email, subject)

	return nil
}

func SendOrderPendingEmail(user userDomain.User, lang string, order orderDomain.Order, op []orderDomain.OrderProduct) error {
	// Copy baseReq to avoid modifying the original request.
	newReq := *baseReq

	userFullName := fmt.Sprintf("%s %s", user.FirstName, user.LastName)

	// Fill "To" field.
	to := temv1alpha1.CreateEmailRequestAddress{
		Email: user.Email,
		Name:  &userFullName,
	}

	// Push the address to the list of recipients.
	newReq.To = append(newReq.To, &to)

	// Determine the template path based on the user's language.
	path := fmt.Sprintf("templates/%s/order-pending.html", lang)

	htmlContent, err := renderOrderPendingEmailHTML(path, user, op, order)
	if err != nil {
		return fmt.Errorf("failed to render email template: %w", err)
	}

	plainTextContent, err := renderOrderPendingEmailText(path, user, op, order)
	if err != nil {
		return fmt.Errorf("failed to render email template: %w", err)
	}

	subjects := map[string]string{
		"en": "Order pending validation",
		"fr": "Commande en attente de validation",
		"zh": "订单待验证",
	}

	subject, ok := subjects[lang]
	if !ok {
		subject = subjects["fr"]
	}

	newReq.Subject = subject
	newReq.HTML = htmlContent
	newReq.Text = plainTextContent

	// Send the email using the Scaleway TEM API.
	_, err = temClient.CreateEmail(&newReq)
	if err != nil {
		return fmt.Errorf("failed to send email: %w", err)
	}

	logger.Debugf("Email sent to %s with subject: %s", user.Email, subject)

	return nil
}
