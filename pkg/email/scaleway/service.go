package scaleway

import (
	"fmt"
	"github.com/scaleway/scaleway-sdk-go/logger"
	"os"
	addressDomain "tsb-service/internal/modules/address/domain"
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

// IsInitialized returns true if the Scaleway TEM client has been initialized.
func IsInitialized() bool {
	return baseReq != nil
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
	path := fmt.Sprintf("templates/%s/verify", lang)

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
		"nl": "Bevestig uw e-mailadres",
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
	path := fmt.Sprintf("templates/%s/welcome", lang)

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
		"nl": "Welkom bij Tokyo Sushi Bar",
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
	path := fmt.Sprintf("templates/%s/order-pending", lang)

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
		"nl": "Bestelling wacht op bevestiging",
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

func SendOrderConfirmedEmail(user userDomain.User, lang string, order orderDomain.Order, op []orderDomain.OrderProduct, address *addressDomain.Address) error {
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
	path := fmt.Sprintf("templates/%s/order-confirmed", lang)

	htmlContent, err := renderOrderConfirmedEmailHTML(path, user, op, order, address, lang)
	if err != nil {
		return fmt.Errorf("failed to render email template: %w", err)
	}

	plainTextContent, err := renderOrderConfirmedEmailText(path, user, op, order, address, lang)
	if err != nil {
		return fmt.Errorf("failed to render email template: %w", err)
	}

	subjects := map[string]string{
		"en": "Order confirmed",
		"fr": "Commande confirmée",
		"zh": "订单已确认",
		"nl": "Bestelling bevestigd",
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

func SendPasswordResetEmail(user userDomain.User, lang string, resetURL string) error {
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
	path := fmt.Sprintf("templates/%s/reset-password", lang)

	htmlContent, err := renderResetPasswordEmailHTML(path, user, resetURL)
	if err != nil {
		return fmt.Errorf("failed to render email template: %w", err)
	}

	plainTextContent, err := renderResetPasswordEmailText(path, user, resetURL)
	if err != nil {
		return fmt.Errorf("failed to render email template: %w", err)
	}

	subjects := map[string]string{
		"en": "Reset your password",
		"fr": "Réinitialiser votre mot de passe",
		"zh": "重置您的密码",
		"nl": "Wachtwoord opnieuw instellen",
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

// cancellationReasonLabels holds localized display labels for each enum value.
// OTHER is intentionally omitted — we fall back to the generic copy.
var cancellationReasonLabels = map[string]map[orderDomain.OrderCancellationReason]string{
	"fr": {
		orderDomain.OrderCancellationReasonOutOfStock:    "rupture de stock",
		orderDomain.OrderCancellationReasonKitchenClosed: "cuisine fermée",
		orderDomain.OrderCancellationReasonDeliveryArea:  "hors zone de livraison",
	},
	"en": {
		orderDomain.OrderCancellationReasonOutOfStock:    "out of stock",
		orderDomain.OrderCancellationReasonKitchenClosed: "kitchen closed",
		orderDomain.OrderCancellationReasonDeliveryArea:  "outside delivery area",
	},
	"nl": {
		orderDomain.OrderCancellationReasonOutOfStock:    "uitverkocht",
		orderDomain.OrderCancellationReasonKitchenClosed: "keuken gesloten",
		orderDomain.OrderCancellationReasonDeliveryArea:  "buiten bezorggebied",
	},
	"zh": {
		orderDomain.OrderCancellationReasonOutOfStock:    "缺货",
		orderDomain.OrderCancellationReasonKitchenClosed: "厨房已关闭",
		orderDomain.OrderCancellationReasonDeliveryArea:  "超出配送范围",
	},
}

// LocalizedCancellationReason returns the localized label for a reason, or an empty
// string when the reason is nil, OTHER, or the language is unknown.
func LocalizedCancellationReason(reason *orderDomain.OrderCancellationReason, lang string) string {
	if reason == nil || *reason == orderDomain.OrderCancellationReasonOther {
		return ""
	}
	labels, ok := cancellationReasonLabels[lang]
	if !ok {
		labels = cancellationReasonLabels["fr"]
	}
	return labels[*reason]
}

func SendOrderCanceledEmail(user userDomain.User, lang string, reason *orderDomain.OrderCancellationReason) error {
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
	path := fmt.Sprintf("templates/%s/order-canceled", lang)

	reasonLabel := LocalizedCancellationReason(reason, lang)

	htmlContent, err := renderOrderCanceledEmailHTML(path, user, reasonLabel)
	if err != nil {
		return fmt.Errorf("failed to render email template: %w", err)
	}

	plainTextContent, err := renderOrderCanceledEmailText(path, user, reasonLabel)
	if err != nil {
		return fmt.Errorf("failed to render email template: %w", err)
	}

	subjects := map[string]string{
		"en": "Order canceled",
		"fr": "Commande annulée",
		"zh": "订单已取消",
		"nl": "Bestelling geannuleerd",
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

func SendOrderReadyEmail(user userDomain.User, lang string, order orderDomain.Order) error {
	newReq := *baseReq

	userFullName := fmt.Sprintf("%s %s", user.FirstName, user.LastName)
	to := temv1alpha1.CreateEmailRequestAddress{
		Email: user.Email,
		Name:  &userFullName,
	}
	newReq.To = append(newReq.To, &to)

	path := fmt.Sprintf("templates/%s/order-ready", lang)

	htmlContent, err := renderOrderReadyEmailHTML(path, user, order)
	if err != nil {
		return fmt.Errorf("failed to render email template: %w", err)
	}

	plainTextContent, err := renderOrderReadyEmailText(path, user, order)
	if err != nil {
		return fmt.Errorf("failed to render email template: %w", err)
	}

	// Subject depends on order type
	var subjects map[string]string
	if order.OrderType == orderDomain.OrderTypeDelivery {
		subjects = map[string]string{
			"en": "Your order is on its way!",
			"fr": "Votre commande est en route !",
			"zh": "您的订单正在配送中！",
			"nl": "Uw bestelling is onderweg!",
		}
	} else {
		subjects = map[string]string{
			"en": "Your order is ready!",
			"fr": "Votre commande est prête !",
			"zh": "您的订单已准备好！",
			"nl": "Uw bestelling is klaar!",
		}
	}

	subject, ok := subjects[lang]
	if !ok {
		subject = subjects["fr"]
	}

	newReq.Subject = subject
	newReq.HTML = htmlContent
	newReq.Text = plainTextContent

	_, err = temClient.CreateEmail(&newReq)
	if err != nil {
		return fmt.Errorf("failed to send email: %w", err)
	}

	logger.Debugf("Email sent to %s with subject: %s", user.Email, subject)
	return nil
}

func SendOrderCompletedEmail(user userDomain.User, lang string) error {
	newReq := *baseReq

	userFullName := fmt.Sprintf("%s %s", user.FirstName, user.LastName)
	to := temv1alpha1.CreateEmailRequestAddress{
		Email: user.Email,
		Name:  &userFullName,
	}
	newReq.To = append(newReq.To, &to)

	path := fmt.Sprintf("templates/%s/order-completed", lang)

	htmlContent, err := renderOrderCompletedEmailHTML(path, user)
	if err != nil {
		return fmt.Errorf("failed to render email template: %w", err)
	}

	plainTextContent, err := renderOrderCompletedEmailText(path, user)
	if err != nil {
		return fmt.Errorf("failed to render email template: %w", err)
	}

	subjects := map[string]string{
		"en": "Thank you for your order!",
		"fr": "Merci pour votre commande !",
		"zh": "感谢您的订单！",
		"nl": "Bedankt voor uw bestelling!",
	}

	subject, ok := subjects[lang]
	if !ok {
		subject = subjects["fr"]
	}

	newReq.Subject = subject
	newReq.HTML = htmlContent
	newReq.Text = plainTextContent

	_, err = temClient.CreateEmail(&newReq)
	if err != nil {
		return fmt.Errorf("failed to send email: %w", err)
	}

	logger.Debugf("Email sent to %s with subject: %s", user.Email, subject)
	return nil
}

func SendPaymentFailedEmail(user userDomain.User, lang string) error {
	newReq := *baseReq

	userFullName := fmt.Sprintf("%s %s", user.FirstName, user.LastName)
	to := temv1alpha1.CreateEmailRequestAddress{
		Email: user.Email,
		Name:  &userFullName,
	}
	newReq.To = append(newReq.To, &to)

	path := fmt.Sprintf("templates/%s/payment-failed", lang)

	htmlContent, err := renderPaymentFailedEmailHTML(path, user)
	if err != nil {
		return fmt.Errorf("failed to render email template: %w", err)
	}

	plainTextContent, err := renderPaymentFailedEmailText(path, user)
	if err != nil {
		return fmt.Errorf("failed to render email template: %w", err)
	}

	subjects := map[string]string{
		"en": "Payment unsuccessful",
		"fr": "Paiement non abouti",
		"zh": "付款未成功",
		"nl": "Betaling mislukt",
	}

	subject, ok := subjects[lang]
	if !ok {
		subject = subjects["fr"]
	}

	newReq.Subject = subject
	newReq.HTML = htmlContent
	newReq.Text = plainTextContent

	_, err = temClient.CreateEmail(&newReq)
	if err != nil {
		return fmt.Errorf("failed to send email: %w", err)
	}

	logger.Debugf("Email sent to %s with subject: %s", user.Email, subject)
	return nil
}

func SendRefundIssuedEmail(user userDomain.User, lang string, refundAmount string) error {
	newReq := *baseReq

	userFullName := fmt.Sprintf("%s %s", user.FirstName, user.LastName)
	to := temv1alpha1.CreateEmailRequestAddress{
		Email: user.Email,
		Name:  &userFullName,
	}
	newReq.To = append(newReq.To, &to)

	path := fmt.Sprintf("templates/%s/refund-issued", lang)

	htmlContent, err := renderRefundIssuedEmailHTML(path, user, refundAmount)
	if err != nil {
		return fmt.Errorf("failed to render email template: %w", err)
	}

	plainTextContent, err := renderRefundIssuedEmailText(path, user, refundAmount)
	if err != nil {
		return fmt.Errorf("failed to render email template: %w", err)
	}

	subjects := map[string]string{
		"en": "Your refund has been issued",
		"fr": "Votre remboursement a été effectué",
		"zh": "您的退款已处理",
		"nl": "Uw terugbetaling is uitgevoerd",
	}

	subject, ok := subjects[lang]
	if !ok {
		subject = subjects["fr"]
	}

	newReq.Subject = subject
	newReq.HTML = htmlContent
	newReq.Text = plainTextContent

	_, err = temClient.CreateEmail(&newReq)
	if err != nil {
		return fmt.Errorf("failed to send email: %w", err)
	}

	logger.Debugf("Email sent to %s with subject: %s", user.Email, subject)
	return nil
}

func SendAccountLinkedEmail(user userDomain.User, lang string) error {
	newReq := *baseReq

	userFullName := fmt.Sprintf("%s %s", user.FirstName, user.LastName)
	to := temv1alpha1.CreateEmailRequestAddress{
		Email: user.Email,
		Name:  &userFullName,
	}
	newReq.To = append(newReq.To, &to)

	path := fmt.Sprintf("templates/%s/account-linked", lang)

	htmlContent, err := renderAccountLinkedEmailHTML(path, user)
	if err != nil {
		return fmt.Errorf("failed to render email template: %w", err)
	}

	plainTextContent, err := renderAccountLinkedEmailText(path, user)
	if err != nil {
		return fmt.Errorf("failed to render email template: %w", err)
	}

	subjects := map[string]string{
		"en": "Google account linked",
		"fr": "Compte Google associé",
		"zh": "Google 帐户已关联",
		"nl": "Google-account gekoppeld",
	}

	subject, ok := subjects[lang]
	if !ok {
		subject = subjects["fr"]
	}

	newReq.Subject = subject
	newReq.HTML = htmlContent
	newReq.Text = plainTextContent

	_, err = temClient.CreateEmail(&newReq)
	if err != nil {
		return fmt.Errorf("failed to send email: %w", err)
	}

	logger.Debugf("Email sent to %s with subject: %s", user.Email, subject)
	return nil
}

func SendReadyTimeUpdatedEmail(user userDomain.User, lang string, order orderDomain.Order) error {
	newReq := *baseReq

	userFullName := fmt.Sprintf("%s %s", user.FirstName, user.LastName)
	to := temv1alpha1.CreateEmailRequestAddress{
		Email: user.Email,
		Name:  &userFullName,
	}
	newReq.To = append(newReq.To, &to)

	path := fmt.Sprintf("templates/%s/ready-time-updated", lang)

	htmlContent, err := renderReadyTimeUpdatedEmailHTML(path, user, order, lang)
	if err != nil {
		return fmt.Errorf("failed to render email template: %w", err)
	}

	plainTextContent, err := renderReadyTimeUpdatedEmailText(path, user, order, lang)
	if err != nil {
		return fmt.Errorf("failed to render email template: %w", err)
	}

	subjects := map[string]string{
		"en": "Updated estimated time",
		"fr": "Horaire estimé modifié",
		"zh": "预计时间已更新",
		"nl": "Geschatte tijd bijgewerkt",
	}

	subject, ok := subjects[lang]
	if !ok {
		subject = subjects["fr"]
	}

	newReq.Subject = subject
	newReq.HTML = htmlContent
	newReq.Text = plainTextContent

	_, err = temClient.CreateEmail(&newReq)
	if err != nil {
		return fmt.Errorf("failed to send email: %w", err)
	}

	logger.Debugf("Email sent to %s with subject: %s", user.Email, subject)
	return nil
}

func SendDeletionRequestEmail(user userDomain.User) error {
	newReq := *baseReq

	// Send to admin (the sender email)
	adminEmail := os.Getenv("SCW_SENDER_EMAIL")
	adminName := "Tokyo Sushi Bar Admin"
	newReq.To = []*temv1alpha1.CreateEmailRequestAddress{
		{
			Email: adminEmail,
			Name:  &adminName,
		},
	}

	path := "templates/en/deletion-request"

	htmlContent, err := renderDeletionRequestEmailHTML(path, user)
	if err != nil {
		return fmt.Errorf("failed to render email template: %w", err)
	}

	plainTextContent, err := renderDeletionRequestEmailText(path, user)
	if err != nil {
		return fmt.Errorf("failed to render email template: %w", err)
	}

	newReq.Subject = fmt.Sprintf("Account deletion request - %s %s", user.FirstName, user.LastName)
	newReq.HTML = htmlContent
	newReq.Text = plainTextContent

	_, err = temClient.CreateEmail(&newReq)
	if err != nil {
		return fmt.Errorf("failed to send email: %w", err)
	}

	logger.Debugf("Deletion request email sent to admin for user %s", user.Email)
	return nil
}

func SendReengagementEmail(user userDomain.User, lang string) error {
	newReq := *baseReq

	userFullName := fmt.Sprintf("%s %s", user.FirstName, user.LastName)
	to := temv1alpha1.CreateEmailRequestAddress{
		Email: user.Email,
		Name:  &userFullName,
	}
	newReq.To = append(newReq.To, &to)

	path := fmt.Sprintf("templates/%s/reengagement", lang)

	htmlContent, err := renderReengagementEmailHTML(path, user)
	if err != nil {
		return fmt.Errorf("failed to render email template: %w", err)
	}

	plainTextContent, err := renderReengagementEmailText(path, user)
	if err != nil {
		return fmt.Errorf("failed to render email template: %w", err)
	}

	subjects := map[string]string{
		"en": "We miss you at Tokyo Sushi Bar!",
		"fr": "Vous nous manquez chez Tokyo Sushi Bar !",
		"zh": "Tokyo Sushi Bar 想念您！",
		"nl": "Wij missen u bij Tokyo Sushi Bar!",
	}

	subject, ok := subjects[lang]
	if !ok {
		subject = subjects["fr"]
	}

	newReq.Subject = subject
	newReq.HTML = htmlContent
	newReq.Text = plainTextContent

	_, err = temClient.CreateEmail(&newReq)
	if err != nil {
		return fmt.Errorf("failed to send email: %w", err)
	}

	logger.Debugf("Email sent to %s with subject: %s", user.Email, subject)
	return nil
}

func SendFeedbackEmail(name, email, serviceType, feedbackType, message, lang string) error {
	newReq := *baseReq

	// Send to admin (same pattern as SendDeletionRequestEmail)
	adminEmail := os.Getenv("SCW_SENDER_EMAIL")
	adminName := "Tokyo Sushi Bar Admin"
	newReq.To = []*temv1alpha1.CreateEmailRequestAddress{
		{
			Email: adminEmail,
			Name:  &adminName,
		},
	}

	path := "templates/en/feedback"

	htmlContent, err := renderFeedbackEmailHTML(path, name, email, serviceType, feedbackType, message, lang)
	if err != nil {
		return fmt.Errorf("failed to render email template: %w", err)
	}

	plainTextContent, err := renderFeedbackEmailText(path, name, email, serviceType, feedbackType, message, lang)
	if err != nil {
		return fmt.Errorf("failed to render email template: %w", err)
	}

	newReq.Subject = fmt.Sprintf("Customer feedback (%s) - %s", feedbackType, name)
	newReq.HTML = htmlContent
	newReq.Text = plainTextContent

	_, err = temClient.CreateEmail(&newReq)
	if err != nil {
		return fmt.Errorf("failed to send email: %w", err)
	}

	logger.Debugf("Feedback email sent to admin from %s", email)
	return nil
}
