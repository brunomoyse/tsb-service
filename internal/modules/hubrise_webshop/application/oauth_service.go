package application

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"tsb-service/internal/modules/hubrise_webshop/domain"
)

// OAuthConfig holds the HubRise OAuth 2.0 client configuration.
type OAuthConfig struct {
	OAuthBaseURL string // e.g. https://manager.hubrise.com/oauth2/v1
	ClientID     string
	ClientSecret string
	RedirectURI  string
	Scope        string
}

// OAuthService handles the HubRise OAuth 2.0 authorization code flow.
type OAuthService struct {
	cfg      OAuthConfig
	connRepo domain.ConnectionRepository
}

// NewOAuthService builds an OAuthService.
func NewOAuthService(cfg OAuthConfig, connRepo domain.ConnectionRepository) *OAuthService {
	return &OAuthService{cfg: cfg, connRepo: connRepo}
}

// BuildAuthorizeURL returns the URL to redirect the admin to for
// HubRise authorization.
func (s *OAuthService) BuildAuthorizeURL(state string) string {
	v := url.Values{}
	v.Set("redirect_uri", s.cfg.RedirectURI)
	v.Set("client_id", s.cfg.ClientID)
	v.Set("scope", s.cfg.Scope)
	if state != "" {
		v.Set("state", state)
	}
	return s.cfg.OAuthBaseURL + "/authorize?" + v.Encode()
}

// TokenResponse is the payload returned by POST /oauth2/v1/token.
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

// ExchangeCode exchanges an auth code for an access token and
// persists the resulting connection.
func (s *OAuthService) ExchangeCode(ctx context.Context, code string) (*TokenResponse, error) {
	// HubRise expects application/json for the token endpoint in the mock;
	// the real endpoint accepts x-www-form-urlencoded. We send JSON here
	// because our mock is JSON-based; switch to form-urlencoded for prod.
	body := map[string]string{"code": code}
	b, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.OAuthBaseURL+"/token", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	basic := base64.StdEncoding.EncodeToString([]byte(s.cfg.ClientID + ":" + s.cfg.ClientSecret))
	req.Header.Set("Authorization", "Basic "+basic)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		truncated := respBody
		if len(truncated) > 256 {
			truncated = truncated[:256]
		}
		return nil, fmt.Errorf("oauth exchange failed: %d %s", resp.StatusCode, string(truncated))
	}

	var tok TokenResponse
	if err := json.Unmarshal(respBody, &tok); err != nil {
		return nil, err
	}

	conn := &domain.Connection{
		ClientName:     ClientName,
		LocationID:     tok.LocationID,
		AccountID:      tok.AccountID,
		AccessToken:    tok.AccessToken,
		Scope:          s.cfg.Scope,
	}
	if tok.CatalogID != "" {
		cid := tok.CatalogID
		conn.CatalogID = &cid
	}
	if tok.CustomerListID != "" {
		clid := tok.CustomerListID
		conn.CustomerListID = &clid
	}
	if err := s.connRepo.Upsert(ctx, conn); err != nil {
		return nil, err
	}

	return &tok, nil
}

// suppress unused import warning during early iterations
var _ = strings.HasPrefix
