package email

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"html/template"
	"os"
	"tsb-service/pkg/utils"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ses"
	"github.com/aws/aws-sdk-go-v2/service/ses/types"
)

//go:embed */*.html
var emailFS embed.FS

// EmailService encapsulates all email functionality.
type EmailService struct {
	sesClient   *ses.Client
	senderEmail string
	senderName  string
}

// NewEmailService initializes the EmailService by loading the AWS configuration,
// reading the SENDER_EMAIL environment variable, and preparing the SES client.
func NewEmailService(ctx context.Context) (*EmailService, error) {
	// Load AWS SES configuration.
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to load AWS config: %w", err)
	}
	client := ses.NewFromConfig(cfg)

	senderEmail := os.Getenv("SENDER_EMAIL")
	if senderEmail == "" {
		return nil, fmt.Errorf("SENDER_EMAIL environment variable is required")
	}

	senderName := os.Getenv("SENDER_NAME")
	if senderName == "" {
		senderName = "Tokyo Sushi Experience" // fallback or brand name
	}

	return &EmailService{
		sesClient:   client,
		senderEmail: senderEmail,
		senderName:  senderName,
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

// SendVerificationEmail constructs and sends the verification email using AWS SES.
// lang: e.g. "en", "fr", "zh", etc.
func (es *EmailService) SendVerificationEmail(ctx context.Context, toAddress, userName, verifyLink string) error {
	lang := utils.GetLang(ctx)
	// Render the email content for the requested language.
	htmlBody, err := es.renderVerifyEmail(lang, userName, verifyLink)
	if err != nil {
		return fmt.Errorf("failed to render verification email: %w", err)
	}

	// Build a "From" header like "Tokyo Sushi Experience <sender@example.com>"
	from := fmt.Sprintf("%s <%s>", es.senderName, es.senderEmail)

	// Build the SES email input.
	// Optionally, you can also localize the subject line if needed.
	subject := "Please verify your email"
	if lang == "fr" {
		subject = "Veuillez vérifier votre adresse e-mail"
	} else if lang == "zh" {
		subject = "验证您的邮箱"
	}

	input := &ses.SendEmailInput{
		Source: awsString(from),
		Destination: &types.Destination{
			ToAddresses: []string{toAddress},
		},
		Message: &types.Message{
			Subject: &types.Content{
				Data: awsString(subject),
			},
			Body: &types.Body{
				Html: &types.Content{
					Data: awsString(htmlBody),
				},
			},
		},
	}

	// Send the email via SES.
	_, err = es.sesClient.SendEmail(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to send verification email: %w", err)
	}

	return nil
}

// awsString returns a pointer to the given string.
func awsString(s string) *string {
	return &s
}
