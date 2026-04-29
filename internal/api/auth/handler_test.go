package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupMockZitadel starts a mock Zitadel server and initializes the auth
// package client to use it instead of a real Zitadel instance.
func setupMockZitadel(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	Init(Config{
		ZitadelIssuer:   srv.URL,
		ZitadelClientID: "test-client-id",
		ServicePAT:      "test-pat",
		AdminPAT:        "test-admin-pat",
	})
	resetIdempotencyGatesForTest()
}

// setupMockZitadelWithIdP is like setupMockZitadel but also configures IdP IDs.
func setupMockZitadelWithIdP(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	Init(Config{
		ZitadelIssuer:   srv.URL,
		ZitadelClientID: "test-client-id",
		ServicePAT:      "test-pat",
		AdminPAT:        "test-admin-pat",
		IdPGoogleID:     "test-google-idp",
	})
	resetIdempotencyGatesForTest()
}

func ginContext(method, path, body string) (*httptest.ResponseRecorder, *gin.Context) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return w, c
}

// --- CompleteOtpProfileHandler Tests ---

func TestCompleteOtpProfileHandler_Success(t *testing.T) {
	var profileUpdated bool
	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/sessions/sess-1" && r.Method == "GET":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"session":{"factors":{"user":{"id":"placeholder-user"}}}}`))
		case r.URL.Path == "/v2/users/human/placeholder-user" && r.Method == "PUT":
			profileUpdated = true
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			profile := body["profile"].(map[string]any)
			assert.Equal(t, "Alice", profile["givenName"])
			assert.Equal(t, "Wonderland", profile["familyName"])
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	body := `{"sessionId":"sess-1","sessionToken":"tok-1","firstName":"Alice","lastName":"Wonderland"}`
	w, c := ginContext("POST", "/auth/session/otp/complete-profile", body)
	CompleteOtpProfileHandler(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.True(t, profileUpdated, "Zitadel user profile must be updated")
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["success"])
}

func TestCompleteOtpProfileHandler_InvalidSession(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":5,"message":"session not found"}`))
	})

	body := `{"sessionId":"missing","sessionToken":"tok","firstName":"Alice","lastName":"Wonderland"}`
	w, c := ginContext("POST", "/auth/session/otp/complete-profile", body)
	CompleteOtpProfileHandler(c)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "invalid_session", resp["error"])
}

func TestCompleteOtpProfileHandler_MissingFields(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"missing sessionId", `{"sessionToken":"tok","firstName":"A","lastName":"B"}`},
		{"missing sessionToken", `{"sessionId":"sess","firstName":"A","lastName":"B"}`},
		{"missing firstName", `{"sessionId":"sess","sessionToken":"tok","lastName":"B"}`},
		{"missing lastName", `{"sessionId":"sess","sessionToken":"tok","firstName":"A"}`},
		{"empty body", `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, c := ginContext("POST", "/auth/session/otp/complete-profile", tt.body)
			CompleteOtpProfileHandler(c)
			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	}
}

// --- FinalizeOIDCHandler Tests ---

func TestFinalizeOIDCHandler_Success(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/v2/oidc/auth_requests/")
		assert.Equal(t, "POST", r.Method)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"callbackUrl":"https://app.example.com/callback?code=abc&state=xyz"}`))
	})

	w, c := ginContext("POST", "/auth/finalize", `{"authRequestId":"req-123","sessionId":"sess-456","sessionToken":"tok-789"}`)
	FinalizeOIDCHandler(c)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp["callbackUrl"])
}

func TestFinalizeOIDCHandler_MissingFields(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"missing authRequestId", `{"sessionId":"sess-456","sessionToken":"tok-789"}`},
		{"missing sessionId", `{"authRequestId":"req-123","sessionToken":"tok-789"}`},
		{"empty body", `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, c := ginContext("POST", "/auth/finalize", tt.body)
			FinalizeOIDCHandler(c)
			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	}
}

func TestFinalizeOIDCHandler_ZitadelError(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":3,"message":"auth request not found"}`))
	})

	w, c := ginContext("POST", "/auth/finalize", `{"authRequestId":"bad-req","sessionId":"sess-456","sessionToken":"tok-789"}`)
	FinalizeOIDCHandler(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// --- TokenExchangeHandler Tests ---

func TestTokenExchangeHandler_CodeExchange(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/v2/token" {
			assert.Equal(t, "POST", r.Method)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"access_token":"at-123","refresh_token":"rt-456","expires_in":3600}`))
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	})

	w, c := ginContext("POST", "/auth/token-exchange", `{"code":"auth-code","redirectUri":"https://app.example.com/callback","clientId":"test-client-id","codeVerifier":"verifier-123"}`)
	TokenExchangeHandler(c)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "at-123", resp["access_token"])
}

func TestTokenExchangeHandler_RefreshToken(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/v2/token" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"access_token":"at-new","expires_in":3600}`))
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	})

	w, c := ginContext("POST", "/auth/token-exchange", `{"refreshToken":"rt-456","clientId":"test-client-id"}`)
	TokenExchangeHandler(c)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "at-new", resp["access_token"])
}

func TestTokenExchangeHandler_InvalidClientId(t *testing.T) {
	setupMockZitadel(t, nil) // No mock needed — rejected before network call

	w, c := ginContext("POST", "/auth/token-exchange", `{"code":"auth-code","clientId":"unknown-client"}`)
	TokenExchangeHandler(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "invalid client_id", resp["error"])
}

func TestTokenExchangeHandler_BothCodeAndRefreshToken(t *testing.T) {
	setupMockZitadel(t, nil)

	w, c := ginContext("POST", "/auth/token-exchange", `{"code":"auth-code","refreshToken":"rt-456","clientId":"test-client-id"}`)
	TokenExchangeHandler(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestTokenExchangeHandler_NeitherCodeNorRefreshToken(t *testing.T) {
	setupMockZitadel(t, nil)

	w, c := ginContext("POST", "/auth/token-exchange", `{"clientId":"test-client-id"}`)
	TokenExchangeHandler(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestTokenExchangeHandler_MissingClientId(t *testing.T) {
	w, c := ginContext("POST", "/auth/token-exchange", `{"code":"auth-code"}`)
	TokenExchangeHandler(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}
