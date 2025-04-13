package scaleway

import (
	"bytes"
	"embed"
	"fmt"
	"github.com/scaleway/scaleway-sdk-go/logger"
	"github.com/shopspring/decimal"
	"html/template"
	"os"
	orderDomain "tsb-service/internal/modules/order/domain"
	"tsb-service/pkg/utils"

	temv1alpha1 "github.com/scaleway/scaleway-sdk-go/api/tem/v1alpha1"
	"github.com/scaleway/scaleway-sdk-go/scw"
	userDomain "tsb-service/internal/modules/user/domain"
)

//go:embed templates/*/*.html
var emailFS embed.FS

// UserTest is a placeholder struct for test email data.
type UserTest struct {
	FirstName string
	LastName  string
	Email     string
	Language  string
}

// temClient is our instance for interacting with Scaleway TEM.
var temClient *temv1alpha1.API

var req *temv1alpha1.CreateEmailRequest

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
	req = &temv1alpha1.CreateEmailRequest{
		Region: scw.Region(region),
		From: &temv1alpha1.CreateEmailRequestAddress{
			Email: senderEmail,
			Name:  &senderName,
		},
		ProjectID: projectID,
	}

	return nil
}

// loadTemplate loads the template file from the embedded file system using the provided path.
func loadTemplate(path string) (*template.Template, error) {
	tmpl, err := template.ParseFS(emailFS, path)
	if err != nil {
		return nil, fmt.Errorf("failed to parse template %q: %w", path, err)
	}
	return tmpl, nil
}

func renderVerifyEmail(path string, user userDomain.User, verifyLink string) (string, error) {
	tmpl, err := loadTemplate(path)

	if err != nil {
		return "", err
	}

	data := struct {
		UserName   string
		VerifyLink string
	}{
		UserName:   fmt.Sprintf("%s %s", user.FirstName, user.LastName),
		VerifyLink: verifyLink,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute verify template: %w", err)
	}
	return buf.String(), nil
}

func renderWelcomeEmail(path string, user userDomain.User, menuLink string) (string, error) {
	tmpl, err := loadTemplate(path)
	if err != nil {
		return "", err
	}

	data := struct {
		UserName string
		MenuLink string
	}{
		UserName: fmt.Sprintf("%s %s", user.FirstName, user.LastName),
		MenuLink: menuLink,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute welcome template: %w", err)
	}
	return buf.String(), nil
}

func renderOrderPendingEmail(path string, u userDomain.User, op []orderDomain.OrderProduct, o orderDomain.Order) (string, error) {
	tmpl, err := loadTemplate(path)
	if err != nil {
		return "", err
	}

	type OrderProductView struct {
		Name       string
		Quantity   int64
		TotalPrice string
	}

	var orderViews []OrderProductView
	for _, item := range op {
		orderViews = append(orderViews, OrderProductView{
			Name:       fmt.Sprintf("%s - %s", item.Product.CategoryName, item.Product.Name),
			Quantity:   item.Quantity,
			TotalPrice: utils.FormatDecimal(item.TotalPrice),
		})
	}

	subtotal := decimal.NewFromInt(0)

	for _, item := range op {
		subtotal = subtotal.Add(item.TotalPrice)
	}

	data := struct {
		UserName         string
		OrderItems       []OrderProductView
		OrderType        string
		SubtotalPrice    string
		TakeawayDiscount string
		DeliveryFee      string
		TotalPrice       string
	}{
		UserName:         fmt.Sprintf("%s %s", u.FirstName, u.LastName),
		OrderItems:       orderViews,
		OrderType:        string(o.OrderType),
		SubtotalPrice:    utils.FormatDecimal(subtotal),
		TakeawayDiscount: utils.FormatDecimal(decimal.NewFromFloat(0)), // @TODO
		DeliveryFee:      utils.FormatDecimal(*o.DeliveryFee),
		TotalPrice:       utils.FormatDecimal(o.TotalPrice),
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute order pending template: %w", err)
	}
	return buf.String(), nil
}

func SendVerificationEmail(user userDomain.User, lang string, verificationURL string) error {
	// Copy req to avoid modifying the original request.
	newReq := *req

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

	htmlContent, err := renderVerifyEmail(path, user, verificationURL)
	if err != nil {
		return fmt.Errorf("failed to render email template: %w", err)
	}

	plainTextContent := "This is a verification email."

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
	// Copy req to avoid modifying the original request.
	newReq := *req

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

	htmlContent, err := renderWelcomeEmail(path, user, menuURL)
	if err != nil {
		return fmt.Errorf("failed to render email template: %w", err)
	}

	plainTextContent := "This is a welcome email."

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
	// Copy req to avoid modifying the original request.
	newReq := *req

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

	htmlContent, err := renderOrderPendingEmail(path, user, op, order)
	if err != nil {
		return fmt.Errorf("failed to render email template: %w", err)
	}

	plainTextContent := "This is an order pending email."

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
