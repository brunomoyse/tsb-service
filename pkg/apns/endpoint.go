package apns

import (
	"net/http"

	"github.com/sideshow/apns2"
)

// NewWithEndpoint returns a Client that sends every push, whatever the environment it prefers, to
// the given host over httpClient. It exists so that code which fans pushes out (the GraphQL
// resolvers) can be tested against a fake APNs server; production uses NewClient.
func NewWithEndpoint(host string, httpClient *http.Client, bundleID string) *Client {
	endpoint := &apns2.Client{Host: host, HTTPClient: httpClient}
	return &Client{
		prod:              endpoint,
		dev:               endpoint,
		preferProd:        true,
		alertTopic:        bundleID,
		liveActivityTopic: bundleID + ".push-type.liveactivity",
	}
}
