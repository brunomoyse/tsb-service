package deliveroo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

const (
	AuthURL    = "https://auth.developers.deliveroo.com/oauth2/token"
	MenuAPIURL = "https://api.developers.deliveroo.com/menu"
)

// Client handles Deliveroo API interactions
type Client struct {
	clientID     string
	clientSecret string
	httpClient   *http.Client

	// Token management
	token      string
	tokenMu    sync.RWMutex
	tokenExpiry time.Time
}

// TokenResponse represents the OAuth2 token response
type TokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

// NewClient creates a new Deliveroo API client
func NewClient(clientID, clientSecret string) *Client {
	return &Client{
		clientID:     clientID,
		clientSecret: clientSecret,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// getAccessToken retrieves a new OAuth2 access token
func (c *Client) getAccessToken(ctx context.Context) error {
	payload := map[string]string{
		"client_id":     c.clientID,
		"client_secret": c.clientSecret,
		"grant_type":    "client_credentials",
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal token request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", AuthURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create token request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to execute token request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("token request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return fmt.Errorf("failed to decode token response: %w", err)
	}

	c.tokenMu.Lock()
	c.token = tokenResp.AccessToken
	// Set expiry to 4 minutes (tokens last 5 minutes, refresh earlier to be safe)
	c.tokenExpiry = time.Now().Add(4 * time.Minute)
	c.tokenMu.Unlock()

	return nil
}

// ensureValidToken ensures we have a valid access token
func (c *Client) ensureValidToken(ctx context.Context) error {
	c.tokenMu.RLock()
	needsRefresh := c.token == "" || time.Now().After(c.tokenExpiry)
	c.tokenMu.RUnlock()

	if needsRefresh {
		return c.getAccessToken(ctx)
	}

	return nil
}

// getToken returns the current access token
func (c *Client) getToken() string {
	c.tokenMu.RLock()
	defer c.tokenMu.RUnlock()
	return c.token
}

// doRequest performs an authenticated API request
func (c *Client) doRequest(ctx context.Context, method, url string, body interface{}) (*http.Response, error) {
	if err := c.ensureValidToken(ctx); err != nil {
		return nil, fmt.Errorf("failed to get valid token: %w", err)
	}

	var bodyReader io.Reader
	if body != nil {
		jsonData, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewBuffer(jsonData)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.getToken()))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}

	return resp, nil
}

// UploadMenu uploads a complete menu to Deliveroo
func (c *Client) UploadMenu(ctx context.Context, brandID, menuID string, menu *Menu) error {
	url := fmt.Sprintf("%s/v1/brands/%s/menus/%s", MenuAPIURL, brandID, menuID)

	resp, err := c.doRequest(ctx, "PUT", url, menu)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upload menu failed with status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}