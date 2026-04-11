package infrastructure

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// HTTPClient wraps net/http to call the HubRise API with an access
// token and basic timeout/retry semantics.
type HTTPClient struct {
	BaseURL     string
	AccessToken string
	Client      *http.Client
}

// NewHTTPClient returns an HTTPClient configured with a 15-second
// timeout — appropriate for HubRise sync calls.
func NewHTTPClient(baseURL, accessToken string) *HTTPClient {
	return &HTTPClient{
		BaseURL:     baseURL,
		AccessToken: accessToken,
		Client:      &http.Client{Timeout: 15 * time.Second},
	}
}

// Do sends a JSON request with the X-Access-Token header set and
// returns the raw response body bytes. Non-2xx responses return an
// error that includes the status + truncated body.
func (c *HTTPClient) Do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}

	url := c.BaseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Access-Token", c.AccessToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		truncated := respBody
		if len(truncated) > 512 {
			truncated = truncated[:512]
		}
		return respBody, fmt.Errorf("hubrise %s %s: %d %s", method, path, resp.StatusCode, string(truncated))
	}

	return respBody, nil
}

// PutJSON is a convenience wrapper for PUT requests.
func (c *HTTPClient) PutJSON(ctx context.Context, path string, body any) ([]byte, error) {
	return c.Do(ctx, http.MethodPut, path, body)
}

// PostJSON is a convenience wrapper for POST requests.
func (c *HTTPClient) PostJSON(ctx context.Context, path string, body any) ([]byte, error) {
	return c.Do(ctx, http.MethodPost, path, body)
}
