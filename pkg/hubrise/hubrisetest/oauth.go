package hubrisetest

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"
	"strings"

	"tsb-service/pkg/hubrise"
)

// IssueAuthCode creates an authorization code the token endpoint will exchange once, as if the
// user had approved the connection in HubRise.
func (s *Server) IssueAuthCode() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	code := hex.EncodeToString(b)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.codes[code] = true
	return code
}

// RevokeToken makes an access token unusable, as if the user had revoked the connection in HubRise:
// every API call with it answers 401 until the connection is authorized again.
func (s *Server) RevokeToken(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revoked[token] = true
}

// TokenRevoked reports whether the token was revoked.
func (s *Server) TokenRevoked(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revoked[token]
}

// clientAuthorized checks the HTTP Basic client credentials of the OAuth endpoints.
func clientAuthorized(r *http.Request) bool {
	id, secret, ok := r.BasicAuth()
	return ok && id == ClientID && secret == ClientSecret
}

// oauthAuthorize plays the user approving the connection: it redirects to redirect_uri with a code
// and the state.
func (s *Server) oauthAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("client_id") != ClientID || q.Get("redirect_uri") == "" || q.Get("scope") == "" {
		writeAPIError(w, http.StatusBadRequest, "unknown client_id, or redirect_uri or scope missing")
		return
	}
	code := s.IssueAuthCode()
	s.mu.Lock()
	s.codeScopes[code] = q.Get("scope")
	s.mu.Unlock()
	if q.Get("redirect_uri") == "urn:ietf:wg:oauth:2.0:oob" {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(code))
		return
	}
	target, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || !isValidRedirect(target) {
		writeAPIError(w, http.StatusBadRequest, "invalid redirect_uri")
		return
	}
	rq := target.Query()
	rq.Set("code", code)
	if st := q.Get("state"); st != "" {
		rq.Set("state", st)
	}
	target.RawQuery = rq.Encode()
	http.Redirect(w, r, target.String(), http.StatusFound)
}

// isValidRedirect reports whether the mock may send the browser to u: an absolute http(s) URL on
// a loopback address or on a reserved test name (RFC 2606 .test, RFC 6761 localhost). Those never
// reach a real site, so the mock cannot be used as an open redirect, whatever redirect_uri it gets.
func isValidRedirect(u *url.URL) bool {
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".test") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) oauthToken(w http.ResponseWriter, r *http.Request) {
	if !clientAuthorized(r) {
		writeAPIError(w, http.StatusUnauthorized, "Invalid client id or secret")
		return
	}
	_ = r.ParseForm()
	code := r.PostForm.Get("code")
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.codes[code] {
		writeAPIError(w, http.StatusBadRequest, "The code is invalid, expired or already used")
		return
	}
	delete(s.codes, code) // one use
	// The token gets the scope the authorization was asked with (an authorization the control
	// methods issued without one grants everything).
	if scope := s.codeScopes[code]; scope != "" {
		s.clients[Token].scopes = hubrise.ScopePermissions(scope)
	} else {
		s.clients[Token].scopes = nil
	}
	delete(s.codeScopes, code)
	// Authorizing the same location again with the same client gives the same token.
	delete(s.revoked, Token)
	tok := hubrise.TokenResponse{
		AccessToken: Token, TokenType: "Bearer",
		AccountID: AccountID, LocationID: LocationID, CatalogID: CatalogID, CustomerListID: CustomerListID,
		AccountName: AccountName, LocationName: LocationName, CatalogName: CatalogName, CustomerListName: CustomerList,
	}
	if t := s.target; t != nil {
		tok.LocationID, tok.LocationName, tok.CatalogID, tok.CatalogName = t.LocationID, t.LocationName, t.CatalogID, t.CatalogName
	}
	writeJSON(w, http.StatusOK, tok)
}

func (s *Server) oauthRevoke(w http.ResponseWriter, r *http.Request) {
	if !clientAuthorized(r) {
		writeAPIError(w, http.StatusUnauthorized, "Invalid client id or secret")
		return
	}
	_ = r.ParseForm()
	token := r.PostForm.Get("token")
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.clients[token] == nil {
		writeAPIError(w, http.StatusBadRequest, "Unknown token")
		return
	}
	s.revoked[token] = true
	writeJSON(w, http.StatusOK, map[string]any{})
}

// OAuthTarget is the location and catalog the token endpoint reports.
type OAuthTarget struct {
	LocationID, LocationName string
	CatalogID, CatalogName   string
}

// SetOAuthTarget makes the next authorizations give access to another location and catalog, as if
// the admin had picked a different one in HubRise. nil restores the default.
func (s *Server) SetOAuthTarget(t *OAuthTarget) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.target = t
}
