package apns

import (
	"encoding/json"
	"fmt"

	"github.com/sideshow/apns2"
	"github.com/sideshow/apns2/token"
	"go.uber.org/zap"
)

// Client wraps the APNs HTTP/2 client for sending standard alert notifications.
type Client struct {
	apnsClient *apns2.Client
	alertTopic string
}

// NewClient creates an APNs client using JWT (p8 key) authentication.
// Set isProduction=true for the production APNs endpoint, false for sandbox.
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

	var client *apns2.Client
	if isProduction {
		client = apns2.NewTokenClient(tkn).Production()
	} else {
		client = apns2.NewTokenClient(tkn).Development()
	}

	return &Client{
		apnsClient: client,
		alertTopic: bundleID,
	}, nil
}

// ErrTokenInvalid indicates the device token is no longer valid and should be removed.
var ErrTokenInvalid = fmt.Errorf("device token is invalid")

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

	res, err := c.apnsClient.Push(notification)
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

