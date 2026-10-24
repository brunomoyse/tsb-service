package hubrise

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"strconv"
)

// GetCallback retrieves the callback registration (GET /callback).
func (c *Client) GetCallback(ctx context.Context) (*Callback, error) {
	var out Callback
	if _, err := c.do(ctx, call{method: http.MethodGet, path: "/callback", out: &out}); err != nil {
		return nil, err
	}
	return &out, nil
}

// SetCallback registers the callback (POST /callback), replacing the connection's current one. An
// empty URL registers a passive callback, read with ListCallbackEvents.
func (c *Client) SetCallback(ctx context.Context, cb Callback) (*Callback, error) {
	var out Callback
	if _, err := c.do(ctx, call{method: http.MethodPost, path: "/callback", in: cb, out: &out}); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteCallback removes the callback registration (DELETE /callback).
func (c *Client) DeleteCallback(ctx context.Context) error {
	_, err := c.do(ctx, call{method: http.MethodDelete, path: "/callback"})
	return err
}

// ListCallbackEvents fetches one page of the events HubRise holds for the connection (GET
// /callback/events): the ones it could not deliver, or all of them for a passive callback. The
// events carry no state fields; re-read the resource. Count 0 is the default page size.
func (c *Client) ListCallbackEvents(ctx context.Context, count int, cursor string) (Page[Event], error) {
	q := url.Values{}
	if count > 0 {
		q.Set("count", strconv.Itoa(count))
	}
	return listPage[Event](ctx, c, call{method: http.MethodGet, path: "/callback/events", query: q}, cursor)
}

// AckCallbackEvent acknowledges, and so deletes, an event (DELETE /callback/events/:id).
func (c *Client) AckCallbackEvent(ctx context.Context, eventID string) error {
	_, err := c.do(ctx, call{method: http.MethodDelete, path: "/callback/events/" + esc(eventID)})
	return err
}

// SignatureHeader is the header carrying the HMAC of an active callback request.
const SignatureHeader = "X-HubRise-Hmac-SHA256"

// Sign returns the signature HubRise puts in SignatureHeader: the hex HMAC-SHA256 of the raw
// request body keyed with the client secret.
func Sign(clientSecret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(clientSecret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifySignature checks the value of SignatureHeader against the raw body in constant time. An
// empty secret or header never verifies.
func VerifySignature(clientSecret string, body []byte, header string) bool {
	if clientSecret == "" || header == "" {
		return false
	}
	got, err := hex.DecodeString(header)
	if err != nil {
		return false
	}
	want, _ := hex.DecodeString(Sign(clientSecret, body))
	return hmac.Equal(got, want)
}
