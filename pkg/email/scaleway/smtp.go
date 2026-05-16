package scaleway

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"mime"
	"net/smtp"
	"strings"

	temv1alpha1 "github.com/scaleway/scaleway-sdk-go/api/tem/v1alpha1"
)

/*
 * SMTP backend for the email package. Activated when SMTP_HOST env var is set,
 * which redirects every Send*Email call away from Scaleway TEM and toward a
 * plain SMTP server. Intended primarily for dev/e2e against a local Mailpit
 * container (axllent/mailpit on :1025), but also serviceable in any
 * environment that prefers SMTP over Scaleway's REST API.
 *
 * Auth is optional: SMTP_USER + SMTP_PASSWORD use PLAIN auth; if either is
 * empty we connect anonymously (Mailpit's default).
 */

// sendViaSMTP converts a Scaleway-shaped CreateEmailRequest into a
// multipart/alternative MIME message and delivers it via net/smtp.
func sendViaSMTP(req *temv1alpha1.CreateEmailRequest) error {
	if smtpHost == "" {
		return fmt.Errorf("sendViaSMTP called without SMTP_HOST configured")
	}
	if req.From == nil || req.From.Email == "" {
		return fmt.Errorf("SMTP send: missing From address")
	}
	if len(req.To) == 0 {
		return fmt.Errorf("SMTP send: missing To address")
	}

	recipients := make([]string, 0, len(req.To))
	for _, t := range req.To {
		if t != nil && t.Email != "" {
			recipients = append(recipients, t.Email)
		}
	}
	if len(recipients) == 0 {
		return fmt.Errorf("SMTP send: every To entry was empty")
	}

	body, err := buildMimeMessage(req, recipients)
	if err != nil {
		return fmt.Errorf("SMTP send: build MIME: %w", err)
	}

	addr := smtpHost + ":" + smtpPort
	var auth smtp.Auth
	if smtpUser != "" && smtpPassword != "" {
		auth = smtp.PlainAuth("", smtpUser, smtpPassword, smtpHost)
	}

	if err := smtp.SendMail(addr, auth, req.From.Email, recipients, body); err != nil {
		return fmt.Errorf("SMTP send: %w", err)
	}
	return nil
}

func buildMimeMessage(req *temv1alpha1.CreateEmailRequest, recipients []string) ([]byte, error) {
	boundary, err := mimeBoundary()
	if err != nil {
		return nil, err
	}

	var sb strings.Builder

	// From: "Display Name" <addr> when a name is set, otherwise just <addr>.
	from := req.From.Email
	if req.From.Name != nil && *req.From.Name != "" {
		from = fmt.Sprintf("%q <%s>", *req.From.Name, req.From.Email)
	}
	sb.WriteString("From: " + from + "\r\n")
	sb.WriteString("To: " + strings.Join(recipients, ", ") + "\r\n")
	sb.WriteString("Subject: " + encodeMimeHeader(req.Subject) + "\r\n")
	sb.WriteString("MIME-Version: 1.0\r\n")

	// Pass-through custom headers (e.g. orderThreadHeaders Message-ID / References).
	for _, h := range req.AdditionalHeaders {
		if h == nil || h.Key == "" {
			continue
		}
		sb.WriteString(h.Key + ": " + h.Value + "\r\n")
	}

	sb.WriteString("Content-Type: multipart/alternative; boundary=\"" + boundary + "\"\r\n")
	sb.WriteString("\r\n")

	if req.Text != "" {
		sb.WriteString("--" + boundary + "\r\n")
		sb.WriteString("Content-Type: text/plain; charset=\"utf-8\"\r\n")
		sb.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
		sb.WriteString(req.Text + "\r\n")
	}

	if req.HTML != "" {
		sb.WriteString("--" + boundary + "\r\n")
		sb.WriteString("Content-Type: text/html; charset=\"utf-8\"\r\n")
		sb.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
		sb.WriteString(req.HTML + "\r\n")
	}

	sb.WriteString("--" + boundary + "--\r\n")
	return []byte(sb.String()), nil
}

func mimeBoundary() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("mime boundary: %w", err)
	}
	return "tsb_" + hex.EncodeToString(b[:]), nil
}

// encodeMimeHeader returns an RFC 2047 encoded-word for any header value
// containing non-ASCII characters. Pure-ASCII subjects pass through untouched
// to keep test inspection readable.
func encodeMimeHeader(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] > 127 {
			return mime.BEncoding.Encode("utf-8", s)
		}
	}
	return s
}
