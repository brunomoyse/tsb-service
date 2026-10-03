package auth

import (
	"context"
	"errors"
	"net"
	"net/mail"
	"strings"
	"time"
)

// ErrInvalidEmail is the client error for an address that cannot receive the login code: bad syntax
// or a domain that does not exist ("name@hotmail.coma"). Returned as 422 {"error": "invalid_email"}.
const ErrInvalidEmail = "invalid_email"

// emailDomainLookupTimeout bounds the DNS check so a slow resolver never holds up a login.
const emailDomainLookupTimeout = 2 * time.Second

// mailResolver is the DNS resolver for the domain check, replaced in tests.
var mailResolver interface {
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
	LookupHost(ctx context.Context, host string) ([]string, error)
} = net.DefaultResolver

// isDeliverableEmail reports whether the address is worth sending a code to: valid syntax and a
// domain that exists in DNS (an MX record, or an A/AAAA record as the implicit MX of RFC 5321).
// It only says no on a definite answer: any other DNS failure lets the address through, so an
// outage of the resolver never blocks logins.
func isDeliverableEmail(ctx context.Context, email string) bool {
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return false
	}
	at := strings.LastIndexByte(email, '@')
	domain := email[at+1:]
	if !strings.Contains(domain, ".") {
		return false
	}

	ctx, cancel := context.WithTimeout(ctx, emailDomainLookupTimeout)
	defer cancel()
	mx, err := mailResolver.LookupMX(ctx, domain)
	if err == nil && len(mx) > 0 {
		return true
	}
	if err != nil && !isNotFound(err) {
		return true
	}
	if _, err := mailResolver.LookupHost(ctx, domain); err != nil && isNotFound(err) {
		return false
	}
	return true
}

func isNotFound(err error) bool {
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr) && dnsErr.IsNotFound
}
