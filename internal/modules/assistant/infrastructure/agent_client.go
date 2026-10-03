package infrastructure

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"tsb-service/internal/modules/assistant/domain"
)

// AgentClient calls the tsb-agent admin API.
type AgentClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// NewAgentClient returns a client for the agent at baseURL (e.g. http://tsb-agent:8082).
func NewAgentClient(baseURL, token string, httpClient *http.Client) domain.AgentClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &AgentClient{baseURL: strings.TrimRight(baseURL, "/"), token: token, httpClient: httpClient}
}

func (c *AgentClient) Connection(ctx context.Context) (domain.Connection, error) {
	var out domain.Connection
	return out, c.do(ctx, http.MethodGet, "/admin/connection", nil, &out)
}

func (c *AgentClient) StartLogin(ctx context.Context, replaceOwner bool) (domain.Login, error) {
	var out domain.Login
	return out, c.do(ctx, http.MethodPost, "/admin/login", map[string]bool{"replace_owner": replaceOwner}, &out)
}

func (c *AgentClient) LoginStatus(ctx context.Context, id string) (domain.Login, error) {
	var out domain.Login
	return out, c.do(ctx, http.MethodGet, "/admin/login/"+url.PathEscape(id), nil, &out)
}

func (c *AgentClient) Disconnect(ctx context.Context) (domain.Connection, error) {
	var out domain.Connection
	return out, c.do(ctx, http.MethodPost, "/admin/disconnect", nil, &out)
}

// do sends a request. A transport error or a 5xx answer is ErrUnavailable;
// a 404 is ErrUnknownLogin.
func (c *AgentClient) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", domain.ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("%w: %v", domain.ErrUnavailable, err)
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return domain.ErrUnknownLogin
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("%w: agent %s %s: HTTP %d", domain.ErrUnavailable, method, path, resp.StatusCode)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%w: decode agent response: %v", domain.ErrUnavailable, err)
	}
	return nil
}
