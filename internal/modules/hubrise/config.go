package hubrise

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/internal/modules/hubrise/infrastructure/tokencrypt"
)

// Environment variables of the integration.
const (
	EnvClientID     = "HUBRISE_CLIENT_ID"
	EnvClientSecret = "HUBRISE_CLIENT_SECRET"
	EnvTokenKey     = "HUBRISE_TOKEN_KEY"
	EnvAPIBaseURL   = "HUBRISE_API_BASE_URL"
	EnvOAuthBaseURL = "HUBRISE_OAUTH_BASE_URL"
	EnvReplicas     = "HUBRISE_REPLICAS"
	// EnvImageBaseURL is the public base URL of the product images (publish_images reads them there).
	// It defaults to S3_BUCKET_URL, then AWS_URL.
	EnvImageBaseURL = "HUBRISE_IMAGE_BASE_URL"
)

// DefaultReplicas is the number of API replicas assumed when HUBRISE_REPLICAS is not set: production
// runs two, and a single instance that thinks there are two only spends half of its rate budget.
const DefaultReplicas = 2

// Production HubRise endpoints, used when the env does not override them (tests and local
// development point both at the Go mock).
const (
	DefaultAPIBaseURL   = "https://api.hubrise.com/v1"
	DefaultOAuthBaseURL = "https://manager.hubrise.com/oauth2/v1"
)

// Config is the deployment-level configuration. HubRise is available on a deployment only when
// the client id, the client secret and the token key are all set (see Available).
type Config struct {
	ClientID     string
	ClientSecret domain.Redacted
	// TokenKey is the decoded 32 byte AES-256 key sealing the access token.
	TokenKey []byte

	APIBaseURL   string
	OAuthBaseURL string

	// ImageBaseURL is where the product images are served from (BaseURL/images/thumbnails/<id>.png);
	// empty means the images cannot be published.
	ImageBaseURL string

	// Replicas is how many API replicas share the HubRise rate budget of the connection. The client
	// limiter of each one is built from the default limits divided by it (HubRise counts the calls
	// of all the replicas together).
	Replicas int
}

// LoadConfig reads the configuration through getenv (os.Getenv in production). An installation
// that sets none of the three required variables gets a Config whose Available is false, with no
// error: that is standalone mode. A variable that is set but malformed (key not base64 of 32 bytes,
// a base URL that is not http(s)) is an error, so a typo is loud instead of silently disabling the
// integration.
func LoadConfig(getenv func(string) string) (Config, error) {
	cfg := Config{
		ClientID:     strings.TrimSpace(getenv(EnvClientID)),
		ClientSecret: domain.Redacted(strings.TrimSpace(getenv(EnvClientSecret))),
	}
	if raw := strings.TrimSpace(getenv(EnvTokenKey)); raw != "" {
		key, err := tokencrypt.ParseKey(raw)
		if err != nil {
			return Config{}, fmt.Errorf("hubrise: %s: %w", EnvTokenKey, err)
		}
		cfg.TokenKey = key
	}
	replicas, err := replicaCount(getenv)
	if err != nil {
		return Config{}, err
	}
	cfg.Replicas = replicas
	if cfg.APIBaseURL, err = baseURL(getenv, EnvAPIBaseURL, DefaultAPIBaseURL); err != nil {
		return Config{}, err
	}
	if cfg.OAuthBaseURL, err = baseURL(getenv, EnvOAuthBaseURL, DefaultOAuthBaseURL); err != nil {
		return Config{}, err
	}
	if cfg.ImageBaseURL, err = imageBaseURL(getenv); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// imageBaseURL reads HUBRISE_IMAGE_BASE_URL, which must be a valid URL when it is set, and otherwise
// the public URL of the product bucket the shop already knows (a value that is not a URL there is
// ignored: it is not this module's setting to refuse).
func imageBaseURL(getenv func(string) string) (string, error) {
	if strings.TrimSpace(getenv(EnvImageBaseURL)) != "" {
		return baseURL(getenv, EnvImageBaseURL, "")
	}
	for _, name := range []string{"S3_BUCKET_URL", "AWS_URL"} {
		if v, err := baseURL(getenv, name, ""); err == nil && v != "" {
			return v, nil
		}
	}
	return "", nil
}

// replicaCount reads HUBRISE_REPLICAS (a positive integer, DefaultReplicas when unset).
func replicaCount(getenv func(string) string) (int, error) {
	raw := strings.TrimSpace(getenv(EnvReplicas))
	if raw == "" {
		return DefaultReplicas, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 100 {
		return 0, fmt.Errorf("hubrise: %s must be a number of replicas between 1 and 100", EnvReplicas)
	}
	return n, nil
}

// baseURL returns the variable's value (or the default) without a trailing slash, and refuses
// anything that is not an absolute http(s) URL.
func baseURL(getenv func(string) string, name, def string) (string, error) {
	v := strings.TrimSpace(getenv(name))
	if v == "" {
		return def, nil
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("hubrise: %s must be an absolute http(s) URL", name)
	}
	return strings.TrimRight(v, "/"), nil
}

// Available reports whether HubRise is configured on this deployment.
func (c Config) Available() bool {
	return c.ClientID != "" && c.ClientSecret != "" && len(c.TokenKey) > 0
}

// Missing lists the required variables that are not set, for a startup log line when only some
// of them are (a half configured deployment stays in standalone mode).
func (c Config) Missing() []string {
	var missing []string
	if c.ClientID == "" {
		missing = append(missing, EnvClientID)
	}
	if c.ClientSecret == "" {
		missing = append(missing, EnvClientSecret)
	}
	if len(c.TokenKey) == 0 {
		missing = append(missing, EnvTokenKey)
	}
	return missing
}

// String shows the non-secret part only, so a Config can be logged safely.
func (c Config) String() string {
	return fmt.Sprintf("hubrise.Config{ClientID: %q, available: %t, API: %s, OAuth: %s}",
		c.ClientID, c.Available(), c.APIBaseURL, c.OAuthBaseURL)
}

// GoString keeps %#v from printing the key and secret.
func (c Config) GoString() string { return c.String() }
