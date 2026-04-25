package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// zitadelClient holds pre-resolved configuration for Zitadel API calls.
// Initialized once at startup via Init(), eliminating per-request os.Getenv calls.
type zitadelClient struct {
	httpClient    *http.Client
	baseURL       string // Internal Docker URL if set, otherwise external issuer URL
	externalHost  string // External hostname for Host header (empty if no internal URL)
	issuerURL     string // External issuer URL (for token endpoint)
	servicePAT    string
	adminPAT      string // Falls back to servicePAT if empty
	appBaseURL    string
	allowedClients map[string]bool // Whitelisted OIDC client IDs
	idpIDs         map[string]string // provider name → Zitadel IdP ID
}

// client is the package-level Zitadel client, initialized via Init().
var client *zitadelClient

// Config holds the environment variables needed to initialize the auth package.
type Config struct {
	ZitadelIssuer      string // ZITADEL_ISSUER (required)
	ZitadelInternalURL string // ZITADEL_INTERNAL_URL (optional, Docker)
	ZitadelClientID    string // ZITADEL_CLIENT_ID (required)
	NativeClientID     string // ZITADEL_NATIVE_CLIENT_ID (optional, Capacitor)
	ServicePAT         string // ZITADEL_SERVICE_PAT (required)
	AdminPAT           string // ZITADEL_ADMIN_PAT (optional, falls back to ServicePAT)
	AppBaseURL         string // APP_BASE_URL
	IdPGoogleID        string // ZITADEL_IDP_GOOGLE_ID
	IdPAppleID         string // ZITADEL_IDP_APPLE_ID
}

// Init initializes the auth package with the given configuration.
// Must be called once at startup before any handler is invoked.
func Init(cfg Config) {
	baseURL := cfg.ZitadelIssuer
	var externalHost string
	if cfg.ZitadelInternalURL != "" {
		baseURL = cfg.ZitadelInternalURL
		externalHost = cfg.ZitadelIssuer
		if h, ok := strings.CutPrefix(externalHost, "https://"); ok {
			externalHost = h
		} else if h, ok := strings.CutPrefix(externalHost, "http://"); ok {
			externalHost = h
		}
	}

	adminPAT := cfg.AdminPAT
	if adminPAT == "" {
		adminPAT = cfg.ServicePAT
	}

	allowed := map[string]bool{
		cfg.ZitadelClientID: true,
	}
	if cfg.NativeClientID != "" {
		allowed[cfg.NativeClientID] = true
	}

	idpIDs := make(map[string]string)
	if cfg.IdPGoogleID != "" {
		idpIDs["google"] = cfg.IdPGoogleID
	}
	if cfg.IdPAppleID != "" {
		idpIDs["apple"] = cfg.IdPAppleID
	}

	client = &zitadelClient{
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		baseURL:        baseURL,
		externalHost:   externalHost,
		issuerURL:      cfg.ZitadelIssuer,
		servicePAT:     cfg.ServicePAT,
		adminPAT:       adminPAT,
		appBaseURL:     cfg.AppBaseURL,
		allowedClients: allowed,
		idpIDs:         idpIDs,
	}
}

// zitadelRequest makes an authenticated request to the Zitadel API using the service PAT.
func zitadelRequest(method, path string, body any) ([]byte, int, error) {
	return zitadelRequestWithPAT(method, path, body, client.servicePAT)
}

// zitadelAdminRequest makes an authenticated request to the Zitadel API using the admin PAT.
func zitadelAdminRequest(method, path string, body any) ([]byte, int, error) {
	return zitadelRequestWithPAT(method, path, body, client.adminPAT)
}

func zitadelRequestWithPAT(method, path string, body any, pat string) ([]byte, int, error) {
	reqURL := client.baseURL + path

	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("marshal body: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, reqURL, reqBody)
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+pat)

	// When using internal Docker URL, set Host header to the external domain.
	// Zitadel resolves instances by Host header.
	if client.externalHost != "" {
		req.Host = client.externalHost
	}

	resp, err := client.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("read response: %w", err)
	}

	return respBody, resp.StatusCode, nil
}
