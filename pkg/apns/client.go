package apns

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/sideshow/apns2"
	"github.com/sideshow/apns2/token"
	"go.uber.org/zap"
)

// Client wraps the APNs HTTP/2 client for sending Live Activity updates
// and standard alert notifications.
type Client struct {
	apnsClient       *apns2.Client
	liveActivityTopic string
	alertTopic        string
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
		apnsClient:        client,
		liveActivityTopic: bundleID + ".push-type.liveactivity",
		alertTopic:        bundleID,
	}, nil
}

// UpdateLiveActivity sends a push notification to update or end a Live Activity.
// event must be "update" or "end".
func (c *Client) UpdateLiveActivity(pushToken string, contentState map[string]any, event string) error {
	payload := map[string]any{
		"aps": map[string]any{
			"timestamp":     time.Now().Unix(),
			"event":         event,
			"content-state": contentState,
		},
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal Live Activity payload: %w", err)
	}

	notification := &apns2.Notification{
		DeviceToken: pushToken,
		Topic:       c.liveActivityTopic,
		PushType:    apns2.PushTypeLiveActivity,
		Payload:     payloadBytes,
	}

	res, err := c.apnsClient.Push(notification)
	if err != nil {
		return fmt.Errorf("push Live Activity update: %w", err)
	}
	if !res.Sent() {
		zap.L().Warn("APNs Live Activity push not sent",
			zap.Int("status", res.StatusCode),
			zap.String("reason", res.Reason),
		)
	}
	return nil
}

// SendAlert sends a standard alert push notification (visible in Notification Center).
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
	}
	return nil
}

// BuildOrderContentState creates a ContentState map matching the Swift
// OrderTrackingAttributes.ContentState struct.
func BuildOrderContentState(status string, estimatedReadyTime *time.Time) map[string]any {
	state := map[string]any{
		"status":    status,
		"updatedAt": float64(time.Now().Unix()),
	}
	if estimatedReadyTime != nil {
		state["estimatedReadyTime"] = float64(estimatedReadyTime.Unix())
	}
	return state
}
