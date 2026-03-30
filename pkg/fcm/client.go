package fcm

import (
	"context"
	"fmt"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/messaging"
	"go.uber.org/zap"
)

// ErrTokenInvalid indicates the FCM registration token is no longer valid and should be removed.
var ErrTokenInvalid = fmt.Errorf("FCM registration token is invalid")

// Client wraps the Firebase Cloud Messaging client for sending push notifications to Android devices.
type Client struct {
	msgClient *messaging.Client
}

// NewClient creates an FCM client using Application Default Credentials.
// Set GOOGLE_APPLICATION_CREDENTIALS env var to the service account JSON path before calling.
func NewClient() (*Client, error) {
	app, err := firebase.NewApp(context.Background(), nil)
	if err != nil {
		return nil, fmt.Errorf("initialize Firebase app: %w", err)
	}

	msgClient, err := app.Messaging(context.Background())
	if err != nil {
		return nil, fmt.Errorf("initialize FCM messaging client: %w", err)
	}

	return &Client{msgClient: msgClient}, nil
}

// SendAlert sends a push notification to an Android device via FCM.
// Returns ErrTokenInvalid if FCM reports the registration token as invalid/unregistered.
func (c *Client) SendAlert(registrationToken, title, body string, data map[string]string) error {
	message := &messaging.Message{
		Token: registrationToken,
		Notification: &messaging.Notification{
			Title: title,
			Body:  body,
		},
		Data: data,
		Android: &messaging.AndroidConfig{
			Priority: "high",
			Notification: &messaging.AndroidNotification{
				Sound:       "default",
				ChannelID:   "order_updates",
				ClickAction: "FLUTTER_NOTIFICATION_CLICK",
			},
		},
	}

	_, err := c.msgClient.Send(context.Background(), message)
	if err != nil {
		if messaging.IsUnregistered(err) || messaging.IsInvalidArgument(err) {
			return ErrTokenInvalid
		}
		zap.L().Warn("FCM push not sent", zap.Error(err))
		return fmt.Errorf("send FCM notification: %w", err)
	}

	return nil
}
