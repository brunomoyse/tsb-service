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
	SiteAPIBaseURL    = "https://api.developers.deliveroo.com/site"

	// Sandbox URLs (can be toggled via config)
	AuthSandboxURL    = "https://auth-sandbox.developers.deliveroo.com"
	OrderSandboxURL   = "https://api-sandbox.developers.deliveroo.com/order"
	MenuSandboxURL    = "https://api-sandbox.developers.deliveroo.com/menu"
	SiteSandboxURL    = "https://api-sandbox.developers.deliveroo.com/site"

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
		case "site":
			return SiteSandboxURL
		}
	}

	switch apiType {
	case "auth":
		return AuthBaseURL
	case "order":
		return OrderAPIBaseURL
	case "menu":
		return MenuAPIBaseURL
	case "site":
		return SiteAPIBaseURL
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
	// OAuth2 token endpoint expects application/x-www-form-urlencoded
	data := "grant_type=client_credentials"

	url := fmt.Sprintf("%s/oauth2/token", a.getBaseURL("auth"))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBufferString(data))
	if err != nil {
		return fmt.Errorf("failed to create token request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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
func (a *DeliverooAdapter) ListOrders(ctx context.Context, status OrderStatus, since *time.Time, siteID string) ([]Order, error) {
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
	if siteID != "" {
		if queryParams == "" {
			queryParams += "?"
		} else {
			queryParams += "&"
		}
		queryParams += fmt.Sprintf("location_id=%s", siteID)
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

// ============================================================================
// Additional Order Management Endpoints
// ============================================================================

// CreateSyncStatus tells Deliveroo if an order was successfully sent to the POS system
func (a *DeliverooAdapter) CreateSyncStatus(ctx context.Context, orderID string, req CreateSyncStatusRequest) error {
	url := fmt.Sprintf("%s/v1/orders/%s/sync_status", a.getBaseURL("order"), orderID)

	resp, err := a.doRequest(ctx, http.MethodPost, url, req, nil)
	if err != nil {
		return fmt.Errorf("failed to create sync status: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("create sync status failed with status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// CreatePrepStage updates the preparation stage of an order
func (a *DeliverooAdapter) CreatePrepStage(ctx context.Context, orderID string, req CreatePrepStageRequest) error {
	url := fmt.Sprintf("%s/v1/orders/%s/prep_stage", a.getBaseURL("order"), orderID)

	resp, err := a.doRequest(ctx, http.MethodPost, url, req, nil)
	if err != nil {
		return fmt.Errorf("failed to create prep stage: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("create prep stage failed with status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// UpdateOrder accepts, rejects, or confirms an order (V1 PATCH endpoint)
func (a *DeliverooAdapter) UpdateOrder(ctx context.Context, orderID string, req UpdateOrderRequest) error {
	url := fmt.Sprintf("%s/v1/orders/%s", a.getBaseURL("order"), orderID)

	resp, err := a.doRequest(ctx, http.MethodPatch, url, req, nil)
	if err != nil {
		return fmt.Errorf("failed to update order: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("update order failed with status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// GetOrderV2 retrieves a single order by ID using V2 API
func (a *DeliverooAdapter) GetOrderV2(ctx context.Context, orderID string) (*Order, error) {
	url := fmt.Sprintf("%s/v2/orders/%s", a.getBaseURL("order"), orderID)

	resp, err := a.doRequest(ctx, http.MethodGet, url, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get order: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get order failed with status %d: %s", resp.StatusCode, string(body))
	}

	var order Order
	if err := json.NewDecoder(resp.Body).Decode(&order); err != nil {
		return nil, fmt.Errorf("failed to decode order response: %w", err)
	}

	return &order, nil
}

// GetOrdersV2 retrieves orders for a restaurant with pagination support
func (a *DeliverooAdapter) GetOrdersV2(ctx context.Context, brandID, restaurantID string, req GetOrdersV2Request) (*GetOrdersV2Response, error) {
	url := fmt.Sprintf("%s/v2/brand/%s/restaurant/%s/orders", a.getBaseURL("order"), brandID, restaurantID)

	// Build query parameters
	queryParams := ""
	if req.StartDate != nil {
		queryParams += fmt.Sprintf("?start_date=%s", req.StartDate.Format(time.RFC3339))
	}
	if req.EndDate != nil {
		if queryParams == "" {
			queryParams += "?"
		} else {
			queryParams += "&"
		}
		queryParams += fmt.Sprintf("end_date=%s", req.EndDate.Format(time.RFC3339))
	}
	if req.Cursor != nil {
		if queryParams == "" {
			queryParams += "?"
		} else {
			queryParams += "&"
		}
		queryParams += fmt.Sprintf("cursor=%s", *req.Cursor)
	}
	if req.LiveOrders {
		if queryParams == "" {
			queryParams += "?"
		} else {
			queryParams += "&"
		}
		queryParams += "live_orders=true"
	}

	url += queryParams

	resp, err := a.doRequest(ctx, http.MethodGet, url, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get orders: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get orders failed with status %d: %s", resp.StatusCode, string(body))
	}

	var ordersResp GetOrdersV2Response
	if err := json.NewDecoder(resp.Body).Decode(&ordersResp); err != nil {
		return nil, fmt.Errorf("failed to decode orders response: %w", err)
	}

	return &ordersResp, nil
}

// ============================================================================
// Webhook Configuration Endpoints
// ============================================================================

// GetOrderEventsWebhook retrieves the order events webhook URL
func (a *DeliverooAdapter) GetOrderEventsWebhook(ctx context.Context) (*WebhookConfig, error) {
	url := fmt.Sprintf("%s/v1/integrator/webhooks/order-events", a.getBaseURL("order"))

	resp, err := a.doRequest(ctx, http.MethodGet, url, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get order events webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get order events webhook failed with status %d: %s", resp.StatusCode, string(body))
	}

	var config WebhookConfig
	if err := json.NewDecoder(resp.Body).Decode(&config); err != nil {
		return nil, fmt.Errorf("failed to decode webhook config response: %w", err)
	}

	return &config, nil
}

// SetOrderEventsWebhook sets the order events webhook URL
func (a *DeliverooAdapter) SetOrderEventsWebhook(ctx context.Context, webhookURL string) error {
	url := fmt.Sprintf("%s/v1/integrator/webhooks/order-events", a.getBaseURL("order"))

	req := WebhookConfig{WebhookURL: webhookURL}

	resp, err := a.doRequest(ctx, http.MethodPut, url, req, nil)
	if err != nil {
		return fmt.Errorf("failed to set order events webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("set order events webhook failed with status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// GetRiderEventsWebhook retrieves the rider events webhook URL
func (a *DeliverooAdapter) GetRiderEventsWebhook(ctx context.Context) (*WebhookConfig, error) {
	url := fmt.Sprintf("%s/v1/integrator/webhooks/rider-events", a.getBaseURL("order"))

	resp, err := a.doRequest(ctx, http.MethodGet, url, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get rider events webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get rider events webhook failed with status %d: %s", resp.StatusCode, string(body))
	}

	var config WebhookConfig
	if err := json.NewDecoder(resp.Body).Decode(&config); err != nil {
		return nil, fmt.Errorf("failed to decode webhook config response: %w", err)
	}

	return &config, nil
}

// SetRiderEventsWebhook sets the rider events webhook URL
func (a *DeliverooAdapter) SetRiderEventsWebhook(ctx context.Context, webhookURL string) error {
	url := fmt.Sprintf("%s/v1/integrator/webhooks/rider-events", a.getBaseURL("order"))

	req := WebhookConfig{WebhookURL: webhookURL}

	resp, err := a.doRequest(ctx, http.MethodPut, url, req, nil)
	if err != nil {
		return fmt.Errorf("failed to set rider events webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("set rider events webhook failed with status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// GetSitesConfig retrieves the webhook configuration for sites under a brand
func (a *DeliverooAdapter) GetSitesConfig(ctx context.Context, brandID string) (*SitesConfig, error) {
	url := fmt.Sprintf("%s/v1/integrator/brands/%s/sites-config", a.getBaseURL("order"), brandID)

	resp, err := a.doRequest(ctx, http.MethodGet, url, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get sites config: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get sites config failed with status %d: %s", resp.StatusCode, string(body))
	}

	var config SitesConfig
	if err := json.NewDecoder(resp.Body).Decode(&config); err != nil {
		return nil, fmt.Errorf("failed to decode sites config response: %w", err)
	}

	return &config, nil
}

// SetSitesConfig sets the webhook configuration for sites under a brand
func (a *DeliverooAdapter) SetSitesConfig(ctx context.Context, brandID string, config SitesConfig) error {
	url := fmt.Sprintf("%s/v1/integrator/brands/%s/sites-config", a.getBaseURL("order"), brandID)

	resp, err := a.doRequest(ctx, http.MethodPut, url, config, nil)
	if err != nil {
		return fmt.Errorf("failed to set sites config: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("set sites config failed with status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// ============================================================================
// Menu API - V2 Item Unavailability Endpoints
// ============================================================================

// GetItemUnavailabilitiesV2 retrieves unavailable items for a site (V2)
func (a *DeliverooAdapter) GetItemUnavailabilitiesV2(ctx context.Context, brandID, siteID string) (*GetItemUnavailabilitiesResponse, error) {
	url := fmt.Sprintf("%s/v2/brands/%s/sites/%s/menu/item_unavailabilities", a.getBaseURL("menu"), brandID, siteID)

	resp, err := a.doRequest(ctx, http.MethodGet, url, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get item unavailabilities: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get item unavailabilities failed with status %d: %s", resp.StatusCode, string(body))
	}

	var unavailabilities GetItemUnavailabilitiesResponse
	if err := json.NewDecoder(resp.Body).Decode(&unavailabilities); err != nil {
		return nil, fmt.Errorf("failed to decode unavailabilities response: %w", err)
	}

	return &unavailabilities, nil
}

// ReplaceItemUnavailabilitiesV2 replaces ALL item unavailabilities for a site (V2)
func (a *DeliverooAdapter) ReplaceItemUnavailabilitiesV2(ctx context.Context, brandID, siteID string, req ReplaceAllUnavailabilitiesRequest) error {
	url := fmt.Sprintf("%s/v2/brands/%s/sites/%s/menu/item_unavailabilities", a.getBaseURL("menu"), brandID, siteID)

	resp, err := a.doRequest(ctx, http.MethodPut, url, req, nil)
	if err != nil {
		return fmt.Errorf("failed to replace item unavailabilities: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("replace item unavailabilities failed with status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// UpdateItemUnavailabilitiesV2 updates individual item unavailabilities for a site (V2)
func (a *DeliverooAdapter) UpdateItemUnavailabilitiesV2(ctx context.Context, brandID, siteID string, req UpdateItemUnavailabilitiesRequest) error {
	url := fmt.Sprintf("%s/v2/brands/%s/sites/%s/menu/item_unavailabilities", a.getBaseURL("menu"), brandID, siteID)

	resp, err := a.doRequest(ctx, http.MethodPost, url, req, nil)
	if err != nil {
		return fmt.Errorf("failed to update item unavailabilities: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("update item unavailabilities failed with status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// ============================================================================
// Menu API - PLU Management
// ============================================================================

// UpdatePLUs updates PLU (Price Look-Up) mappings for menu items
func (a *DeliverooAdapter) UpdatePLUs(ctx context.Context, brandID, menuID string, mappings UpdatePLUsRequest) error {
	url := fmt.Sprintf("%s/v1/brands/%s/menus/%s/plus", a.getBaseURL("menu"), brandID, menuID)

	resp, err := a.doRequest(ctx, http.MethodPost, url, mappings, nil)
	if err != nil {
		return fmt.Errorf("failed to update PLUs: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("update PLUs failed with status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// ============================================================================
// Menu API - V2 Menu Retrieval
// ============================================================================

// GetMenuV2 retrieves menu for a specific site (V2 - works with Menu Manager)
func (a *DeliverooAdapter) GetMenuV2(ctx context.Context, brandID, siteID string) (*MenuUploadRequest, error) {
	url := fmt.Sprintf("%s/v2/brands/%s/sites/%s/menu", a.getBaseURL("menu"), brandID, siteID)

	resp, err := a.doRequest(ctx, http.MethodGet, url, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get menu: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get menu failed with status %d: %s", resp.StatusCode, string(body))
	}

	var menu MenuUploadRequest
	if err := json.NewDecoder(resp.Body).Decode(&menu); err != nil {
		return nil, fmt.Errorf("failed to decode menu response: %w", err)
	}

	return &menu, nil
}

// ============================================================================
// Menu API - V3 Async Upload
// ============================================================================

// GetMenuUploadURLV3 gets a presigned S3 URL for uploading large menus
func (a *DeliverooAdapter) GetMenuUploadURLV3(ctx context.Context, brandID, menuID string) (*MenuUploadURLResponse, error) {
	url := fmt.Sprintf("%s/v3/brands/%s/menus/%s", a.getBaseURL("menu"), brandID, menuID)

	resp, err := a.doRequest(ctx, http.MethodPut, url, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get menu upload URL: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get menu upload URL failed with status %d: %s", resp.StatusCode, string(body))
	}

	var uploadResp MenuUploadURLResponse
	if err := json.NewDecoder(resp.Body).Decode(&uploadResp); err != nil {
		return nil, fmt.Errorf("failed to decode upload URL response: %w", err)
	}

	return &uploadResp, nil
}

// GetMenuV3 fetches menu metadata from V3 API
func (a *DeliverooAdapter) GetMenuV3(ctx context.Context, brandID, menuID string) (*MenuV3Response, error) {
	url := fmt.Sprintf("%s/v3/brands/%s/menus/%s", a.getBaseURL("menu"), brandID, menuID)

	resp, err := a.doRequest(ctx, http.MethodGet, url, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get menu V3: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get menu V3 failed with status %d: %s", resp.StatusCode, string(body))
	}

	var menu MenuV3Response
	if err := json.NewDecoder(resp.Body).Decode(&menu); err != nil {
		return nil, fmt.Errorf("failed to decode menu V3 response: %w", err)
	}

	return &menu, nil
}

// PublishMenuJob creates a job to publish menu to live
func (a *DeliverooAdapter) PublishMenuJob(ctx context.Context, brandID string, req PublishMenuJobRequest) (*JobResponse, error) {
	url := fmt.Sprintf("%s/v3/brands/%s/jobs", a.getBaseURL("menu"), brandID)

	resp, err := a.doRequest(ctx, http.MethodPost, url, req, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create publish job: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("create publish job failed with status %d: %s", resp.StatusCode, string(body))
	}

	var jobResp JobResponse
	if err := json.NewDecoder(resp.Body).Decode(&jobResp); err != nil {
		return nil, fmt.Errorf("failed to decode job response: %w", err)
	}

	return &jobResp, nil
}

// GetJobStatus checks the status of an async job
func (a *DeliverooAdapter) GetJobStatus(ctx context.Context, brandID, jobID string) (*JobResponse, error) {
	url := fmt.Sprintf("%s/v3/brands/%s/jobs/%s", a.getBaseURL("menu"), brandID, jobID)

	resp, err := a.doRequest(ctx, http.MethodGet, url, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get job status: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get job status failed with status %d: %s", resp.StatusCode, string(body))
	}

	var jobResp JobResponse
	if err := json.NewDecoder(resp.Body).Decode(&jobResp); err != nil {
		return nil, fmt.Errorf("failed to decode job status response: %w", err)
	}

	return &jobResp, nil
}

// UploadMenuToS3 uploads menu data directly to S3 presigned URL
func (a *DeliverooAdapter) UploadMenuToS3(ctx context.Context, uploadURL string, menu *MenuUploadRequest) error {
	jsonData, err := json.Marshal(menu)
	if err != nil {
		return fmt.Errorf("failed to marshal menu: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create S3 upload request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to upload to S3: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("S3 upload failed with status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// ============================================================================
// Menu API - Webhook Configuration
// ============================================================================

// GetMenuEventsWebhook retrieves the menu events webhook URL
func (a *DeliverooAdapter) GetMenuEventsWebhook(ctx context.Context) (*WebhookConfig, error) {
	url := fmt.Sprintf("%s/v1/integrator/webhooks/menu-events", a.getBaseURL("menu"))

	resp, err := a.doRequest(ctx, http.MethodGet, url, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get menu events webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get menu events webhook failed with status %d: %s", resp.StatusCode, string(body))
	}

	var config WebhookConfig
	if err := json.NewDecoder(resp.Body).Decode(&config); err != nil {
		return nil, fmt.Errorf("failed to decode webhook config response: %w", err)
	}

	return &config, nil
}

// SetMenuEventsWebhook sets the menu events webhook URL
func (a *DeliverooAdapter) SetMenuEventsWebhook(ctx context.Context, webhookURL string) error {
	url := fmt.Sprintf("%s/v1/integrator/webhooks/menu-events", a.getBaseURL("menu"))

	req := WebhookConfig{WebhookURL: webhookURL}

	resp, err := a.doRequest(ctx, http.MethodPut, url, req, nil)
	if err != nil {
		return fmt.Errorf("failed to set menu events webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("set menu events webhook failed with status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}