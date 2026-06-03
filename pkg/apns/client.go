package apns

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/sideshow/apns2"
	"github.com/sideshow/apns2/token"
	"go.uber.org/zap"
)

// Client wraps the APNs HTTP/2 client for sending standard alert notifications
// and ActivityKit Live Activity updates.
//
// It holds BOTH the production and sandbox (development) HTTP/2 clients built
// from the same p8 auth key. APNs device tokens are environment-specific — a
// token minted by a development build (Xcode debug / Expo dev / EAS
// development) is only valid against the sandbox endpoint, while a token from a
// TestFlight or App Store build is only valid against production. Since the
// token table doesn't record which environment a token came from, every send
// tries the preferred endpoint first and, on a BadDeviceToken reply (APNs's
// "wrong environment" signal), retries once against the other endpoint. This
// lets a single backend serve both dev and release builds, and — critically —
// stops a sandbox token from being wrongly deleted as "invalid" when the
// production endpoint rejects it.
type Client struct {
	prod              *apns2.Client
	dev               *apns2.Client
	preferProd        bool
	alertTopic        string
	liveActivityTopic string
}

// NewClient creates an APNs client using JWT (p8 key) authentication.
// isProduction selects which endpoint is tried first; the other is used as an
// automatic fallback on a BadDeviceToken environment mismatch.
func NewClient(authKeyPath, keyID, teamID, bundleID string, isProduction bool) (*Client, error) {
	authKey, err := token.AuthKeyFromFile(authKeyPath)
	if err != nil {
		return nil, fmt.Errorf("load APNs auth key: %w", err)
	}

	tkn := &token.Token{
		AuthKey: authKey,
		KeyID:   keyID,
		TeamID:  teamID,
	}

	return &Client{
		prod:       apns2.NewTokenClient(tkn).Production(),
		dev:        apns2.NewTokenClient(tkn).Development(),
		preferProd: isProduction,
		alertTopic: bundleID,
		// ActivityKit requires a dedicated topic suffix for Live Activity pushes.
		liveActivityTopic: bundleID + ".push-type.liveactivity",
	}, nil
}

// ErrTokenInvalid indicates the device token is no longer valid and should be removed.
var ErrTokenInvalid = fmt.Errorf("device token is invalid")

// push sends the notification to the preferred APNs environment and, if APNs
// rejects the token as belonging to the other environment (BadDeviceToken),
// retries once against the other endpoint. Returns the response that should be
// acted on (the retry's response when a fallback happened).
func (c *Client) push(n *apns2.Notification) (*apns2.Response, error) {
	first, second := c.dev, c.prod
	if c.preferProd {
		first, second = c.prod, c.dev
	}

	res, err := first.Push(n)
	if err != nil {
		return nil, err
	}
	// BadDeviceToken == the token belongs to the other environment. Unregistered
	// / ExpiredToken mean the token is genuinely dead, so don't bother retrying.
	if !res.Sent() && res.Reason == apns2.ReasonBadDeviceToken {
		retry, retryErr := second.Push(n)
		if retryErr != nil {
			return nil, retryErr
		}
		return retry, nil
	}
	return res, nil
}

// SendAlert sends a standard alert push notification (visible in Notification Center).
// Returns ErrTokenInvalid if APNs reports the token as bad/unregistered.
func (c *Client) SendAlert(deviceToken, title, body string, data map[string]string) error {
	alert := map[string]string{
		"title": title,
		"body":  body,
	}

	aps := map[string]any{
		"alert": alert,
		"sound": "default",
	}

	payload := map[string]any{
		"aps": aps,
	}
	for k, v := range data {
		payload[k] = v
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal alert payload: %w", err)
	}

	notification := &apns2.Notification{
		DeviceToken: deviceToken,
		Topic:       c.alertTopic,
		PushType:    apns2.PushTypeAlert,
		Payload:     payloadBytes,
	}

	res, err := c.push(notification)
	if err != nil {
		return fmt.Errorf("push alert notification: %w", err)
	}
	if !res.Sent() {
		zap.L().Warn("APNs alert push not sent",
			zap.Int("status", res.StatusCode),
			zap.String("reason", res.Reason),
		)
		if res.Reason == apns2.ReasonBadDeviceToken || res.Reason == apns2.ReasonUnregistered || res.Reason == apns2.ReasonExpiredToken {
			return ErrTokenInvalid
		}
	}
	return nil
}

// SendLiveActivity sends an ActivityKit Live Activity push to a per-activity
// push token. event is "update" (state change) or "end" (terminal). contentState
// must match the app's LiveActivityAttributes.ContentState (title, subtitle,
// progress, ...). Returns ErrTokenInvalid if APNs reports the token as bad.
func (c *Client) SendLiveActivity(pushToken string, contentState map[string]any, event string) error {
	aps := map[string]any{
		"timestamp":     time.Now().Unix(),
		"event":         event,
		"content-state": contentState,
	}

	payloadBytes, err := json.Marshal(map[string]any{"aps": aps})
	if err != nil {
		return fmt.Errorf("marshal live activity payload: %w", err)
	}

	notification := &apns2.Notification{
		DeviceToken: pushToken,
		Topic:       c.liveActivityTopic,
		PushType:    apns2.PushTypeLiveActivity,
		Priority:    apns2.PriorityHigh,
		Payload:     payloadBytes,
	}

	res, err := c.push(notification)
	if err != nil {
		return fmt.Errorf("push live activity notification: %w", err)
	}
	if !res.Sent() {
		zap.L().Warn("APNs live activity push not sent",
			zap.Int("status", res.StatusCode),
			zap.String("reason", res.Reason),
			zap.String("event", event),
		)
		if res.Reason == apns2.ReasonBadDeviceToken || res.Reason == apns2.ReasonUnregistered || res.Reason == apns2.ReasonExpiredToken {
			return ErrTokenInvalid
		}
	}
	return nil
}

