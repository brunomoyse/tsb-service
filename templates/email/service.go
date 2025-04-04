package email

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"html/template"
	"os"

	"tsb-service/pkg/utils"

	"github.com/sendgrid/sendgrid-go"
	"github.com/sendgrid/sendgrid-go/helpers/mail"
)

//go:embed */*.html
var emailFS embed.FS

// EmailService encapsulates all email functionality using SendGrid.
type EmailService struct {
	sendgridClient *sendgrid.Client
	senderEmail    string
	senderName     string
}

// NewEmailService initializes the EmailService by reading the SENDGRID_API_KEY,
// SENDER_EMAIL, and SENDER_NAME environment variables and preparing the SendGrid client.
func NewEmailService(ctx context.Context) (*EmailService, error) {
	apiKey := os.Getenv("SENDGRID_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("SENDGRID_API_KEY environment variable is required")
	}
	client := sendgrid.NewSendClient(apiKey)

	senderEmail := os.Getenv("SENDER_EMAIL")
	if senderEmail == "" {
		return nil, fmt.Errorf("SENDER_EMAIL environment variable is required")
	}

	senderName := os.Getenv("SENDER_NAME")
	if senderName == "" {
		senderName = "Tokyo Sushi Experience" // fallback or brand name
	}

	return &EmailService{
		sendgridClient: client,
		senderEmail:    senderEmail,
		senderName:     senderName,
	}, nil
}

// loadTemplate loads the specified template file from the embedded file system
// using a path in the form of {lang}/{templateName}.
func (es *EmailService) loadTemplate(lang, templateName string) (*template.Template, error) {
	path := fmt.Sprintf("%s/%s", lang, templateName)
	tmpl, err := template.ParseFS(emailFS, path)
	if err != nil {
		return nil, fmt.Errorf("failed to parse template %q: %w", path, err)
	}
	return tmpl, nil
}

// renderVerifyEmail loads the relevant language-specific "verify.html" file
// and injects the data into the template.
func (es *EmailService) renderVerifyEmail(lang, userName, verifyLink string) (string, error) {
	tmpl, err := es.loadTemplate(lang, "verify.html")
	if err != nil {
		return "", err
	}

	data := struct {
		UserName   string
		VerifyLink string
	}{
		UserName:   userName,
		VerifyLink: verifyLink,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute verify template: %w", err)
	}
	return buf.String(), nil
}

// SendVerificationEmail constructs and sends the verification email using SendGrid.
// lang: e.g. "en", "fr", "zh", etc.
func (es *EmailService) SendVerificationEmail(ctx context.Context, toAddress, userName, verifyLink string) error {
	lang := utils.GetLang(ctx)
	// Render the email content for the requested language.
	htmlContent, err := es.renderVerifyEmail(lang, userName, verifyLink)
	if err != nil {
		return fmt.Errorf("failed to render verification email: %w", err)
	}

	// Optionally, set a plain text version or strip HTML tags from htmlContent.
	plainTextContent := "Please verify your email by clicking the link."

	// Set localized subject if desired.
	subject := "Please verify your email"
	if lang == "fr" {
		subject = "Veuillez vérifier votre adresse e-mail"
	} else if lang == "zh" {
		subject = "验证您的邮箱"
	}

	from := mail.NewEmail(es.senderName, es.senderEmail)
	to := mail.NewEmail("", toAddress)
	message := mail.NewSingleEmail(from, subject, to, plainTextContent, htmlContent)

	response, err := es.sendgridClient.Send(message)
	if err != nil {
		return fmt.Errorf("failed to send verification email: %w", err)
	}
	if response.StatusCode >= 400 {
		return fmt.Errorf("failed to send verification email, status: %d, body: %s", response.StatusCode, response.Body)
	}
	return nil
}
