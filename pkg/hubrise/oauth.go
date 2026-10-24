package hubrise

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultOAuthBaseURL is the HubRise authorization server.
	DefaultOAuthBaseURL = "https://manager.hubrise.com/oauth2/v1"

	// DefaultScope is what the integration asks for: orders (push and status), the customer list,
	// the catalog (read, and write for the dashboard-owned menu that is published to HubRise) and the
	// inventory (read and write), all at the location level.
	DefaultScope = "location[orders.write,customer_list.write,catalog.read,catalog.write,inventory.read,inventory.write]"
)

// TokenResponse is what the token endpoint answers: the access token and where it gives access to.
// Access tokens do not expire and have no refresh token; a 401 later means the connection was
// revoked, blocked or suspended.
type TokenResponse struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	AccountID        string `json:"account_id"`
	LocationID       string `json:"location_id"`
	CatalogID        string `json:"catalog_id"`
	CustomerListID   string `json:"customer_list_id"`
	AccountName      string `json:"account_name"`
	LocationName     string `json:"location_name"`
	CatalogName      string `json:"catalog_name"`
	CustomerListName string `json:"customer_list_name"`
}

// String implements fmt.Stringer without the access token.
func (t TokenResponse) String() string {
	return fmt.Sprintf("hubrise.TokenResponse{account=%s location=%s catalog=%s customer_list=%s}",
		t.AccountID, t.LocationID, t.CatalogID, t.CustomerListID)
}

// GoString implements fmt.GoStringer without the access token.
func (t TokenResponse) GoString() string { return t.String() }

// OAuth runs the authorization code flow against the HubRise authorization server. The code is
// exchanged server side, the client authenticating with HTTP Basic (client id and secret) and the
// code in a form body (docs/hubrise/02-authentication.md).
type OAuth struct {
	base         *url.URL
	clientID     string
	clientSecret secret
	http         *http.Client
	now          func() time.Time
}

// String implements fmt.Stringer without the client secret.
func (o OAuth) String() string { return "hubrise.OAuth{" + o.base.String() + " " + o.clientID + "}" }

// Format implements fmt.Formatter, so no verb can print the client secret.
func (o OAuth) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(o.String())) }

// OAuthOption configures an OAuth.
type OAuthOption func(*OAuth)

// WithOAuthHTTPClient sets the HTTP client (the default has a 20 s timeout). It is copied and its
// redirects pinned to the authorization server's host.
func WithOAuthHTTPClient(h *http.Client) OAuthOption { return func(o *OAuth) { o.http = h } }

// NewOAuth returns the OAuth flow for the authorization server at baseURL.
func NewOAuth(baseURL, clientID, clientSecret string, opts ...OAuthOption) (*OAuth, error) {
	base, err := parseBase(baseURL)
	if err != nil {
		return nil, err
	}
	if clientID == "" || clientSecret == "" {
		return nil, errors.New("hubrise: oauth needs a client id and a client secret")
	}
	o := &OAuth{base: base, clientID: clientID, clientSecret: secret(clientSecret), now: time.Now}
	for _, opt := range opts {
		opt(o)
	}
	if o.http == nil {
		o.http = &http.Client{Timeout: DefaultTimeout}
	}
	o.http = pinHost(o.http, base)
	return o, nil
}

// AuthorizeURL is where to send the user's browser. state is echoed back on the redirect and must
// be a one time nonce bound to the user.
func (o *OAuth) AuthorizeURL(redirectURI, scope, state string) string {
	q := url.Values{}
	q.Set("redirect_uri", redirectURI)
	q.Set("client_id", o.clientID)
	q.Set("scope", scope)
	if state != "" {
		q.Set("state", state)
	}
	return o.base.String() + "/authorize?" + q.Encode()
}

// ExchangeCode trades an authorization code (one use, valid 10 minutes) for an access token. A
// spent or expired code is a KindInvalid error (400), bad client credentials KindUnauthorized.
func (o *OAuth) ExchangeCode(ctx context.Context, code string) (*TokenResponse, error) {
	raw, err := o.post(ctx, "/token", url.Values{"code": {code}}, code)
	if err != nil {
		return nil, err
	}
	var tok TokenResponse
	if err := json.Unmarshal(raw, &tok); err != nil {
		return nil, &Error{Kind: KindProtocol, Op: "POST /token", Message: "undecodable token response", Err: errors.New("invalid JSON")}
	}
	if tok.AccessToken == "" {
		return nil, &Error{Kind: KindProtocol, Op: "POST /token", Message: "token response without access_token"}
	}
	return &tok, nil
}

// Revoke makes an access token unusable (POST /revoke). The caller still clears its own copy.
func (o *OAuth) Revoke(ctx context.Context, token string) error {
	_, err := o.post(ctx, "/revoke", url.Values{"token": {token}}, token)
	return err
}

// post sends a form with the client's Basic credentials. extra are secrets to scrub from errors.
func (o *OAuth) post(ctx context.Context, path string, form url.Values, extra ...string) ([]byte, error) {
	op := http.MethodPost + " " + path
	secrets := append([]string{string(o.clientSecret)}, extra...)

	u := o.base.String() + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, &Error{Kind: KindProtocol, Op: op, Message: "build request", Err: errors.New("invalid URL")}
	}
	req.SetBasicAuth(o.clientID, string(o.clientSecret))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := o.http.Do(req)
	if err != nil {
		return nil, newTransportError(ctx, op, err, secrets...)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, newTransportError(ctx, op, err, secrets...)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, newStatusError(op, resp.StatusCode, resp.Header, raw, o.now(), secrets...)
	}
	return raw, nil
}

// ScopePermissions returns the permissions a scope string grants, whatever its level:
// "location[orders.write,catalog.read]" gives ["orders.write", "catalog.read"]. A string without
// brackets is read as a comma separated list. Blanks are ignored.
func ScopePermissions(scope string) []string {
	inner := scope
	if open := strings.Index(scope, "["); open >= 0 {
		inner = scope[open+1:]
		if end := strings.LastIndex(inner, "]"); end >= 0 {
			inner = inner[:end]
		}
	}
	var out []string
	for _, p := range strings.FieldsFunc(inner, func(r rune) bool { return r == ',' || r == ' ' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ScopeHas reports whether scope grants permission ("catalog.write"): either exactly, or through
// the wildcard of its resource ("catalog.*").
func ScopeHas(scope, permission string) bool {
	resource, _, _ := strings.Cut(permission, ".")
	for _, p := range ScopePermissions(scope) {
		if p == permission || p == resource+".*" {
			return true
		}
	}
	return false
}
