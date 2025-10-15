package deliveroo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	// API Base URLs
	AuthBaseURL       = "https://auth.developers.deliveroo.com"
	OrderAPIBaseURL   = "https://api.developers.deliveroo.com/order"
	MenuAPIBaseURL    = "https://api.developers.deliveroo.com/menu"

	// Sandbox URLs (can be toggled via config)
	AuthSandboxURL    = "https://auth-sandbox.developers.deliveroo.com"
	OrderSandboxURL   = "https://api-sandbox.developers.deliveroo.com/order"
	MenuSandboxURL    = "https://api-sandbox.developers.deliveroo.com/menu"

	// Retry configuration
	MaxRetries        = 3
	InitialBackoff    = 1 * time.Second
	MaxBackoff        = 30 * time.Second
	BackoffMultiplier = 2.0

	// Token expiry buffer (refresh 1 minute before expiry)
	TokenExpiryBuffer = 1 * time.Minute
)

// DeliverooAdapter provides a comprehensive interface to Deliveroo APIs
type DeliverooAdapter struct {
	clientID     string
	clientSecret string
	useSandbox   bool
	httpClient   *http.Client

	// Token management
	tokenMu      sync.RWMutex
	accessToken  string
	tokenExpiry  time.Time
}

// AdapterConfig contains configuration for the adapter
type AdapterConfig struct {
	ClientID     string
	ClientSecret string
	UseSandbox   bool
	HTTPTimeout  time.Duration
}

// NewAdapter creates a new DeliverooAdapter
func NewAdapter(config AdapterConfig) *DeliverooAdapter {
	timeout := config.HTTPTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	return &DeliverooAdapter{
		clientID:     config.ClientID,
		clientSecret: config.ClientSecret,
		useSandbox:   config.UseSandbox,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// getBaseURL returns the appropriate base URL for the given API type
func (a *DeliverooAdapter) getBaseURL(apiType string) string {
	if a.useSandbox {
		switch apiType {
		case "auth":
			return AuthSandboxURL
		case "order":
			return OrderSandboxURL
		case "menu":
			return MenuSandboxURL
		}
	}

	switch apiType {
	case "auth":
		return AuthBaseURL
	case "order":
		return OrderAPIBaseURL
	case "menu":
		return MenuAPIBaseURL
	}

	return ""
}

// tokenResponse represents OAuth2 token response
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

// getAccessToken retrieves a new OAuth2 access token
func (a *DeliverooAdapter) getAccessToken(ctx context.Context) error {
	payload := map[string]string{
		"grant_type": "client_credentials",
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal token request: %w", err)
	}

	url := fmt.Sprintf("%s/oauth2/token", a.getBaseURL("auth"))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create token request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(a.clientID, a.clientSecret)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to execute token request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("token request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return fmt.Errorf("failed to decode token response: %w", err)
	}

	a.tokenMu.Lock()
	a.accessToken = tokenResp.AccessToken
	// Set expiry with buffer
	expiryDuration := time.Duration(tokenResp.ExpiresIn) * time.Second
	a.tokenExpiry = time.Now().Add(expiryDuration - TokenExpiryBuffer)
	a.tokenMu.Unlock()

	return nil
}

// ensureValidToken ensures we have a valid access token
func (a *DeliverooAdapter) ensureValidToken(ctx context.Context) error {
	a.tokenMu.RLock()
	needsRefresh := a.accessToken == "" || time.Now().After(a.tokenExpiry)
	a.tokenMu.RUnlock()

	if needsRefresh {
		return a.getAccessToken(ctx)
	}

	return nil
}

// getToken returns the current access token (thread-safe)
func (a *DeliverooAdapter) getToken() string {
	a.tokenMu.RLock()
	defer a.tokenMu.RUnlock()
	return a.accessToken
}

// generateIdempotencyKey generates a unique idempotency key
func generateIdempotencyKey() string {
	return uuid.New().String()
}

// shouldRetry determines if a request should be retried based on status code
func shouldRetry(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests || statusCode >= 500
}

// calculateBackoff calculates the backoff duration for a given attempt
func calculateBackoff(attempt int) time.Duration {
	backoff := float64(InitialBackoff) * math.Pow(BackoffMultiplier, float64(attempt))
	if backoff > float64(MaxBackoff) {
		backoff = float64(MaxBackoff)
	}
	return time.Duration(backoff)
}

// doRequest performs an authenticated API request with retry logic
func (a *DeliverooAdapter) doRequest(ctx context.Context, method, url string, body interface{}, headers map[string]string) (*http.Response, error) {
	if err := a.ensureValidToken(ctx); err != nil {
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

	var lastErr error
	for attempt := 0; attempt <= MaxRetries; attempt++ {
		// Reset body reader for retries
		if body != nil {
			jsonData, _ := json.Marshal(body)
			bodyReader = bytes.NewBuffer(jsonData)
		}

		req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}

		// Set standard headers
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", a.getToken()))

		// Add custom headers (including idempotency key)
		for key, value := range headers {
			req.Header.Set(key, value)
		}

		resp, err := a.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("request failed: %w", err)
			if attempt < MaxRetries {
				backoff := calculateBackoff(attempt)
				select {
				case <-time.After(backoff):
					continue
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			continue
		}

		// Check if we should retry
		if shouldRetry(resp.StatusCode) && attempt < MaxRetries {
			resp.Body.Close()
			lastErr = fmt.Errorf("received status %d, retrying", resp.StatusCode)
			backoff := calculateBackoff(attempt)
			select {
			case <-time.After(backoff):
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		return resp, nil
	}

	return nil, fmt.Errorf("max retries exceeded: %w", lastErr)
}

// ListOrders retrieves orders with filtering options
func (a *DeliverooAdapter) ListOrders(ctx context.Context, status OrderStatus, since *time.Time, outletID string) ([]Order, error) {
	url := fmt.Sprintf("%s/v2/orders", a.getBaseURL("order"))

	// Build query parameters
	queryParams := ""
	if status != "" {
		queryParams += fmt.Sprintf("?status=%s", status)
	}
	if since != nil {
		if queryParams == "" {
			queryParams += "?"
		} else {
			queryParams += "&"
		}
		queryParams += fmt.Sprintf("since=%s", since.Format(time.RFC3339))
	}
	if outletID != "" {
		if queryParams == "" {
			queryParams += "?"
		} else {
			queryParams += "&"
		}
		queryParams += fmt.Sprintf("location_id=%s", outletID)
	}

	url += queryParams

	resp, err := a.doRequest(ctx, http.MethodGet, url, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list orders: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list orders failed with status %d: %s", resp.StatusCode, string(body))
	}

	var ordersResp OrdersListResponse
	if err := json.NewDecoder(resp.Body).Decode(&ordersResp); err != nil {
		return nil, fmt.Errorf("failed to decode orders response: %w", err)
	}

	return ordersResp.Orders, nil
}

// AcknowledgeOrder acknowledges receipt of an order
func (a *DeliverooAdapter) AcknowledgeOrder(ctx context.Context, orderID string) error {
	url := fmt.Sprintf("%s/v1/orders/%s/acknowledge", a.getBaseURL("order"), orderID)

	headers := map[string]string{
		"Idempotency-Key": generateIdempotencyKey(),
	}

	reqBody := AcknowledgeOrderRequest{
		AcknowledgedAt: time.Now(),
	}

	resp, err := a.doRequest(ctx, http.MethodPost, url, reqBody, headers)
	if err != nil {
		return fmt.Errorf("failed to acknowledge order: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("acknowledge order failed with status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// AcceptOrder accepts an order with preparation time
func (a *DeliverooAdapter) AcceptOrder(ctx context.Context, orderID string, prepMinutes int) error {
	url := fmt.Sprintf("%s/v1/orders/%s/accept", a.getBaseURL("order"), orderID)

	headers := map[string]string{
		"Idempotency-Key": generateIdempotencyKey(),
	}

	reqBody := AcceptOrderRequest{
		AcceptedAt:         time.Now(),
		PreparationMinutes: prepMinutes,
	}

	resp, err := a.doRequest(ctx, http.MethodPost, url, reqBody, headers)
	if err != nil {
		return fmt.Errorf("failed to accept order: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("accept order failed with status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// UpdateOrderStatus updates the status of an order
func (a *DeliverooAdapter) UpdateOrderStatus(ctx context.Context, orderID string, status OrderStatus, readyAt, pickupAt *time.Time) error {
	url := fmt.Sprintf("%s/v1/orders/%s/status", a.getBaseURL("order"), orderID)

	headers := map[string]string{
		"Idempotency-Key": generateIdempotencyKey(),
	}

	reqBody := UpdateOrderStatusRequest{
		Status:   status,
		ReadyAt:  readyAt,
		PickupAt: pickupAt,
	}

	resp, err := a.doRequest(ctx, http.MethodPut, url, reqBody, headers)
	if err != nil {
		return fmt.Errorf("failed to update order status: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("update order status failed with status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// PullMenu retrieves the current menu from Deliveroo
func (a *DeliverooAdapter) PullMenu(ctx context.Context, brandID, menuID string) (*MenuUploadRequest, error) {
	url := fmt.Sprintf("%s/v1/brands/%s/menus/%s", a.getBaseURL("menu"), brandID, menuID)

	resp, err := a.doRequest(ctx, http.MethodGet, url, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to pull menu: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("pull menu failed with status %d: %s", resp.StatusCode, string(body))
	}

	var menu MenuUploadRequest
	if err := json.NewDecoder(resp.Body).Decode(&menu); err != nil {
		return nil, fmt.Errorf("failed to decode menu response: %w", err)
	}

	return &menu, nil
}

// PushMenu uploads a menu to Deliveroo
func (a *DeliverooAdapter) PushMenu(ctx context.Context, brandID, menuID string, menu *MenuUploadRequest) error {
	url := fmt.Sprintf("%s/v1/brands/%s/menus/%s", a.getBaseURL("menu"), brandID, menuID)

	headers := map[string]string{
		"Idempotency-Key": generateIdempotencyKey(),
	}

	resp, err := a.doRequest(ctx, http.MethodPut, url, menu, headers)
	if err != nil {
		return fmt.Errorf("failed to push menu: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("push menu failed with status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}