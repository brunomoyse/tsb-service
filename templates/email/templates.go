package email

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"html/template"
	"os"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ses"
	"github.com/aws/aws-sdk-go-v2/service/ses/types"
)

//go:embed verify.html
var verifyTemplateFS embed.FS

// EmailService encapsulates all email functionality.
type EmailService struct {
	sesClient   *ses.Client
	senderEmail string
	senderName  string
	template    *template.Template
}

// NewEmailService initializes the EmailService by loading the AWS configuration,
// reading the SENDER_EMAIL environment variable, and parsing the embedded email template.
func NewEmailService(ctx context.Context) (*EmailService, error) {
	// Load AWS SES configuration.
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to load AWS config: %w", err)
	}
	client := ses.NewFromConfig(cfg)

	// Get the sender email address from environment.
	senderEmail := os.Getenv("SENDER_EMAIL")
	senderName := os.Getenv("SENDER_NAME")

	// Parse the embedded verification template.
	tmpl, err := template.ParseFS(verifyTemplateFS, "verify.html")
	if err != nil {
		return nil, fmt.Errorf("failed to parse verify template: %w", err)
	}

	return &EmailService{
		sesClient:   client,
		senderEmail: senderEmail,
		senderName:  senderName,
		template:    tmpl,
	}, nil
}

// RenderVerifyEmail renders the verification email using the provided user name and verification link.
func (es *EmailService) RenderVerifyEmail(userName, verifyLink string) (string, error) {
	data := struct {
		UserName   string
		VerifyLink string
	}{
		UserName:   userName,
		VerifyLink: verifyLink,
	}

	var buf bytes.Buffer
	if err := es.template.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute verify template: %w", err)
	}
	return buf.String(), nil
}

// SendVerificationEmail constructs and sends the verification email using AWS SES.
func (es *EmailService) SendVerificationEmail(ctx context.Context, toAddress, userName, verifyLink string) error {
	// Render the email content.
	htmlBody, err := es.RenderVerifyEmail(userName, verifyLink)
	if err != nil {
		return fmt.Errorf("failed to render verification email: %w", err)
	}

	from := fmt.Sprintf("%s <%s>", es.senderName, es.senderEmail)

	// Build the SES email input.
	input := &ses.SendEmailInput{
		Source: awsString(from),
		Destination: &types.Destination{
			ToAddresses: []string{toAddress},
		},
		Message: &types.Message{
			Subject: &types.Content{
				Data: awsString("Please verify your email"),
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
