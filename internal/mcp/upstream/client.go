// Package upstream is the tsb-service GraphQL client used by the MCP server.
// It only ever talks HTTP to the same API the dashboard uses, so every
// backend side effect (pubsub broadcasts, staff audit log, file service) keeps
// happening.
package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// Client calls POST {baseURL}/graphql with a service account bearer token.
type Client struct {
	endpoint string
	http     *http.Client
	tokens   oauth2.TokenSource
	log      *slog.Logger
}

// New builds a client. baseURL is the API root (…/api/v1).
func New(baseURL string, tokens oauth2.TokenSource, log *slog.Logger) *Client {
	return &Client{
		endpoint: strings.TrimRight(baseURL, "/") + "/graphql",
		http:     &http.Client{Timeout: 60 * time.Second},
		tokens:   tokens,
		log:      log,
	}
}

// Error is an upstream failure. Error() is safe to show to the owner; the
// technical detail is only logged.
type Error struct {
	Kind    ErrorKind
	Message string
	Code    string
}

// ErrorKind classifies upstream failures.
type ErrorKind int

const (
	KindUnavailable ErrorKind = iota
	KindUnauthorized
	KindNotFound
	KindRejected
)

func (e *Error) Error() string { return e.Message }

// IsNotFound reports whether err is an upstream "not found".
func IsNotFound(err error) bool {
	var ue *Error
	return errors.As(err, &ue) && ue.Kind == KindNotFound
}

type gqlRequest struct {
	OperationName string         `json:"operationName"`
	Query         string         `json:"query"`
	Variables     map[string]any `json:"variables,omitempty"`
}

type gqlError struct {
	Message    string         `json:"message"`
	Path       []any          `json:"path"`
	Extensions map[string]any `json:"extensions"`
}

type gqlResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []gqlError      `json:"errors"`
}

// Do runs a named document from the Documents registry and decodes `data`
// into out.
func (c *Client) Do(ctx context.Context, op string, vars map[string]any, out any) error {
	doc, ok := Documents[op]
	if !ok {
		return fmt.Errorf("upstream: unknown operation %q", op)
	}
	body, err := json.Marshal(gqlRequest{OperationName: op, Query: doc, Variables: vars})
	if err != nil {
		return fmt.Errorf("upstream: encode %s: %w", op, err)
	}
	return c.send(ctx, op, "application/json", bytes.NewReader(body), out)
}

// Upload runs a named document as a GraphQL multipart request
// (graphql-multipart-request-spec) with one file mapped to filePath, e.g.
// "variables.input.image".
func (c *Client) Upload(ctx context.Context, op string, vars map[string]any, filePath, filename, contentType string, data []byte, out any) error {
	doc, ok := Documents[op]
	if !ok {
		return fmt.Errorf("upstream: unknown operation %q", op)
	}
	operations, err := json.Marshal(gqlRequest{OperationName: op, Query: doc, Variables: vars})
	if err != nil {
		return fmt.Errorf("upstream: encode %s: %w", op, err)
	}
	mapping, _ := json.Marshal(map[string][]string{"0": {filePath}})

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("operations", string(operations))
	_ = mw.WriteField("map", string(mapping))
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="0"; filename=%q`, filename))
	h.Set("Content-Type", contentType)
	part, err := mw.CreatePart(h)
	if err != nil {
		return fmt.Errorf("upstream: multipart: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return fmt.Errorf("upstream: multipart: %w", err)
	}
	if err := mw.Close(); err != nil {
		return fmt.Errorf("upstream: multipart: %w", err)
	}
	return c.send(ctx, op, mw.FormDataContentType(), &buf, out)
}

func (c *Client) send(ctx context.Context, op, contentType string, body io.Reader, out any) error {
	log := c.log.With("operation", op)

	tok, err := c.tokens.Token()
	if err != nil {
		log.Error("service account token request failed", "error", err)
		return &Error{Kind: KindUnauthorized, Message: "The assistant could not sign in to the restaurant system. Please try again later."}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, body)
	if err != nil {
		return fmt.Errorf("upstream: build request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "fr")
	tok.SetAuthHeader(req)

	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		log.Error("upstream request failed", "error", err)
		return &Error{Kind: KindUnavailable, Message: "The restaurant system is not reachable right now. Please try again in a moment."}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		log.Error("upstream read failed", "error", err)
		return &Error{Kind: KindUnavailable, Message: "The restaurant system is not reachable right now. Please try again in a moment."}
	}
	log.Debug("upstream call", "status", resp.StatusCode, "duration", time.Since(start))

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		log.Error("upstream refused the service account", "status", resp.StatusCode, "body", truncate(raw))
		return &Error{Kind: KindUnauthorized, Message: "The assistant is not allowed to do this in the restaurant system."}
	case resp.StatusCode >= 500:
		log.Error("upstream server error", "status", resp.StatusCode, "body", truncate(raw))
		return &Error{Kind: KindUnavailable, Message: "The restaurant system had a problem. Please try again in a moment."}
	}

	var gr gqlResponse
	if err := json.Unmarshal(raw, &gr); err != nil {
		log.Error("upstream returned invalid JSON", "status", resp.StatusCode, "body", truncate(raw))
		return &Error{Kind: KindUnavailable, Message: "The restaurant system returned an unexpected answer."}
	}
	if len(gr.Errors) > 0 {
		return c.mapErrors(log, gr.Errors)
	}
	if out != nil {
		if err := json.Unmarshal(gr.Data, out); err != nil {
			log.Error("upstream data does not match the expected shape", "error", err)
			return &Error{Kind: KindUnavailable, Message: "The restaurant system returned an unexpected answer."}
		}
	}
	return nil
}

func (c *Client) mapErrors(log *slog.Logger, errs []gqlError) error {
	first := errs[0]
	code, _ := first.Extensions["code"].(string)
	log.Warn("upstream graphql error", "code", code, "message", first.Message, "count", len(errs))

	msg := strings.ToLower(first.Message)
	switch {
	case code == "UNAUTHENTICATED" || code == "FORBIDDEN" || strings.Contains(msg, "unauthorized") || strings.Contains(msg, "forbidden") || strings.Contains(msg, "access denied"):
		return &Error{Kind: KindUnauthorized, Code: code, Message: "The assistant is not allowed to do this in the restaurant system."}
	case code == "NOT_FOUND" || strings.Contains(msg, "not found") || strings.Contains(msg, "no rows"):
		return &Error{Kind: KindNotFound, Code: code, Message: "This item no longer exists in the restaurant system."}
	case code != "" && code != "INTERNAL_SERVER_ERROR" && code != "GRAPHQL_VALIDATION_FAILED" && code != "GRAPHQL_PARSE_FAILED":
		return &Error{Kind: KindRejected, Code: code, Message: "The restaurant system refused the change (" + code + ")."}
	default:
		return &Error{Kind: KindRejected, Code: code, Message: "The restaurant system refused the request."}
	}
}

func truncate(b []byte) string {
	const max = 512
	if len(b) > max {
		return string(b[:max]) + "…"
	}
	return string(b)
}
