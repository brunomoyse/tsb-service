// Package brand centralizes restaurant identity (name, legal info, contact
// details) so the same binary can serve different restaurants. Values come
// from RESTAURANT_* env vars; defaults match the Tokyo Sushi Bar production
// deployment so existing instances need no new configuration.
package brand

import (
	"cmp"
	"os"
)

type Config struct {
	// Name is the customer-facing display name (Mollie payment description,
	// push notification titles, email subjects and bodies).
	Name string
	// LegalName is the registered company name shown on invoices.
	LegalName string
	// Address is the full postal address shown on invoices.
	Address string
	// Phone is the public contact phone number shown on invoices.
	Phone string
	// Email is the public contact email shown on invoices.
	Email string
	// VAT is the company/VAT registration number shown on invoices.
	VAT string
	// Domain is the email domain used to build RFC 5322 Message-ID headers
	// for order email threading.
	Domain string
	// LogoPath is the path (relative to APP_BASE_URL) of the logo embedded
	// in transactional emails.
	LogoPath string
	// InvoicePrefix is the short code prefixing human-readable invoice
	// references (e.g. "TSB" in "TSB-2026-1A2B3C4D").
	InvoicePrefix string
}

// current is initialized at package init so tests and callers always see a
// valid config; main calls Load() after godotenv has read .env so file-based
// env vars are honored too.
var current = NewFromEnv()

// Load refreshes the package-level config from the environment. Call once at
// startup, after loading .env, before serving traffic.
func Load() Config {
	current = NewFromEnv()
	return current
}

// Current returns the loaded brand config.
func Current() Config {
	return current
}

func NewFromEnv() Config {
	return Config{
		Name:          cmp.Or(os.Getenv("RESTAURANT_NAME"), "Tokyo Sushi Bar"),
		LegalName:     cmp.Or(os.Getenv("RESTAURANT_LEGAL_NAME"), "Tokyo Sushi Bar — SRL"),
		Address:       cmp.Or(os.Getenv("RESTAURANT_ADDRESS"), "Rue de la Cathédrale 59, 4000 Liège, Belgique"),
		Phone:         cmp.Or(os.Getenv("RESTAURANT_PHONE"), "+32 4 222 98 88"),
		Email:         cmp.Or(os.Getenv("RESTAURANT_EMAIL"), "tokyosushibar888@gmail.com"),
		VAT:           cmp.Or(os.Getenv("RESTAURANT_VAT"), "BE0772.499.585"),
		Domain:        cmp.Or(os.Getenv("RESTAURANT_DOMAIN"), "tokyosushibarliege.be"),
		LogoPath:      cmp.Or(os.Getenv("RESTAURANT_LOGO_PATH"), "/images/tsb-black-font-100.png"),
		InvoicePrefix: cmp.Or(os.Getenv("RESTAURANT_INVOICE_PREFIX"), "TSB"),
	}
}
