package upstream

import (
	"context"
	"net/http"
	"net/url"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

// ServiceAccount describes the Zitadel machine user the MCP server acts as.
type ServiceAccount struct {
	Issuer       string // public issuer, e.g. https://auth.tokyosushibarliege.be
	InternalURL  string // optional in-cluster Zitadel URL; the public Host header is kept
	ClientID     string
	ClientSecret string
	ProjectID    string
}

// Scopes returns the scopes that make Zitadel put the project in `aud` and the
// project roles in the token, which tsb-service requires for @admin.
func (sa ServiceAccount) Scopes() []string {
	return []string{
		"openid",
		"urn:zitadel:iam:org:project:id:" + sa.ProjectID + ":aud",
		"urn:zitadel:iam:org:projects:roles",
	}
}

// TokenSource returns a cached, auto-refreshing client-credentials token
// source. The machine user must be configured with the JWT access token type:
// tsb-service validates tokens locally against JWKS and rejects opaque ones.
func (sa ServiceAccount) TokenSource(ctx context.Context) oauth2.TokenSource {
	base := sa.Issuer
	httpClient := &http.Client{Transport: http.DefaultTransport}
	if sa.InternalURL != "" {
		base = sa.InternalURL
		if u, err := url.Parse(sa.Issuer); err == nil {
			httpClient.Transport = hostRewrite{host: u.Host, next: http.DefaultTransport}
		}
	}
	cfg := clientcredentials.Config{
		ClientID:     sa.ClientID,
		ClientSecret: sa.ClientSecret,
		TokenURL:     base + "/oauth/v2/token",
		Scopes:       sa.Scopes(),
		AuthStyle:    oauth2.AuthStyleInHeader,
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, httpClient)
	return oauth2.ReuseTokenSource(nil, cfg.TokenSource(ctx))
}

// hostRewrite sends requests to the internal URL while presenting the public
// host, which Zitadel uses to resolve the instance.
type hostRewrite struct {
	host string
	next http.RoundTripper
}

func (h hostRewrite) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Host = h.host
	return h.next.RoundTrip(r)
}
