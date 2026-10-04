package fcm

import (
	"context"
	"fmt"

	firebase "firebase.google.com/go/v4"
	"google.golang.org/api/option"
)

// NewWithEndpoint returns a Client that talks to an FCM-compatible server at endpoint, without
// credentials. It exists so that code which fans pushes out (the GraphQL resolvers) can be tested
// against a fake FCM server; production uses NewClient.
func NewWithEndpoint(ctx context.Context, projectID, endpoint string) (*Client, error) {
	app, err := firebase.NewApp(ctx, &firebase.Config{ProjectID: projectID},
		option.WithEndpoint(endpoint), option.WithoutAuthentication())
	if err != nil {
		return nil, fmt.Errorf("initialize Firebase app: %w", err)
	}
	msgClient, err := app.Messaging(ctx)
	if err != nil {
		return nil, fmt.Errorf("initialize FCM messaging client: %w", err)
	}
	return &Client{msgClient: msgClient}, nil
}
