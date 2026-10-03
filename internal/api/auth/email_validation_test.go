package auth

import (
	"context"
	"errors"
	"net"
	"testing"
)

type fakeResolver struct {
	mx    map[string][]*net.MX
	hosts map[string][]string
	fail  error // returned for every lookup when set
}

func (f fakeResolver) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	if f.fail != nil {
		return nil, f.fail
	}
	if mx, ok := f.mx[name]; ok {
		return mx, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (f fakeResolver) LookupHost(_ context.Context, host string) ([]string, error) {
	if f.fail != nil {
		return nil, f.fail
	}
	if addrs, ok := f.hosts[host]; ok {
		return addrs, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

func TestIsDeliverableEmail(t *testing.T) {
	orig := mailResolver
	t.Cleanup(func() { mailResolver = orig })
	mailResolver = fakeResolver{
		mx:    map[string][]*net.MX{"hotmail.com": {{Host: "mx.hotmail.com."}}},
		hosts: map[string][]string{"a-only.be": {"192.0.2.1"}},
	}

	cases := map[string]bool{
		"ann@hotmail.com":  true,
		"ann@a-only.be":    true,  // no MX, but an A record: implicit MX
		"ann@hotmail.coma": false, // the typo seen in production
		"ann@hotmail":      false,
		"not an email":     false,
		"Ann <ann@x.com>":  false,
		"ann@@hotmail.com": false,
		"":                 false,
	}
	for email, want := range cases {
		if got := isDeliverableEmail(context.Background(), email); got != want {
			t.Errorf("isDeliverableEmail(%q) = %v, want %v", email, got, want)
		}
	}
}

func TestIsDeliverableEmailFailsOpenOnDNSOutage(t *testing.T) {
	orig := mailResolver
	t.Cleanup(func() { mailResolver = orig })
	mailResolver = fakeResolver{fail: &net.DNSError{Err: "i/o timeout", IsTimeout: true}}
	if !isDeliverableEmail(context.Background(), "ann@hotmail.coma") {
		t.Fatal("a resolver outage must not block logins")
	}
	mailResolver = fakeResolver{fail: errors.New("boom")}
	if !isDeliverableEmail(context.Background(), "ann@example.com") {
		t.Fatal("an unknown DNS error must not block logins")
	}
}
