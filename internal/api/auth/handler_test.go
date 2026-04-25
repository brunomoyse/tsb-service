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
}

func ginContext(method, path, body string) (*httptest.ResponseRecorder, *gin.Context) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return w, c
}

// --- RegisterHandler Tests ---

func TestRegisterHandler_Success(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v2/users/human", r.URL.Path)
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "Bearer test-admin-pat", r.Header.Get("Authorization"))

		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "test@example.com", body["userName"])

		profile := body["profile"].(map[string]any)
		assert.Equal(t, "John", profile["givenName"])
		assert.Equal(t, "Doe", profile["familyName"])

		// returnCode (we send the email ourselves) — never sendCode (Zitadel sends).
		email := body["email"].(map[string]any)
		assert.NotNil(t, email["returnCode"])
		assert.Nil(t, email["sendCode"])

		// Passwordless: registration must never include a password block.
		_, hasPassword := body["password"]
		assert.False(t, hasPassword, "passwordless registration must not include a password")

		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"userId":"zitadel-123","emailCode":"abc123"}`))
	})

	reqBody := `{"firstName":"John","lastName":"Doe","email":"test@example.com"}`
	w, c := ginContext("POST", "/auth/register", reqBody)

	RegisterHandler(c)

	assert.Equal(t, http.StatusCreated, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["success"])
}

func TestRegisterHandler_WithPhone(t *testing.T) {
	var receivedBody map[string]any
	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"userId":"zitadel-123"}`))
	})

	reqBody := `{"firstName":"John","lastName":"Doe","email":"test@example.com","phone":"+32123456789"}`
	w, c := ginContext("POST", "/auth/register", reqBody)

	RegisterHandler(c)

	assert.Equal(t, http.StatusCreated, w.Code)
	assert.NotNil(t, receivedBody["phone"])
	phone := receivedBody["phone"].(map[string]any)
	assert.Equal(t, "+32123456789", phone["phone"])
}

func TestRegisterHandler_DuplicateEmail(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"code":6,"message":"user already exists"}`))
	})

	reqBody := `{"firstName":"John","lastName":"Doe","email":"existing@example.com"}`
	w, c := ginContext("POST", "/auth/register", reqBody)

	RegisterHandler(c)

	assert.Equal(t, http.StatusConflict, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "email_already_exists", resp["error"])
}

func TestRegisterHandler_MissingFields(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"missing email", `{"firstName":"John","lastName":"Doe"}`},
		{"missing firstName", `{"lastName":"Doe","email":"test@example.com"}`},
		{"missing lastName", `{"firstName":"John","email":"test@example.com"}`},
		{"empty body", `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, c := ginContext("POST", "/auth/register", tt.body)
			RegisterHandler(c)
			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	}
}

// --- VerifyEmailHandler Tests ---

func TestVerifyEmailHandler_Success(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/users/user-abc/email/verify" && r.Method == "POST" {
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			assert.Equal(t, "verify-code-123", body["verificationCode"])
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		// User fetch for welcome email (async goroutine — may or may not be called)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"user":{"human":{"profile":{"givenName":"Test","familyName":"User"},"email":{"email":"test@example.com"}}}}`))
	})

	w, c := ginContext("POST", "/auth/verify-email", `{"userId":"user-abc","code":"verify-code-123"}`)

	VerifyEmailHandler(c)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["success"])
}

func TestVerifyEmailHandler_InvalidCode(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":3,"message":"verification code is invalid"}`))
	})

	w, c := ginContext("POST", "/auth/verify-email", `{"userId":"user-abc","code":"bad-code"}`)

	VerifyEmailHandler(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "invalid_code", resp["error"])
}

func TestVerifyEmailHandler_MissingFields(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"missing userId", `{"code":"abc"}`},
		{"missing code", `{"userId":"user-123"}`},
		{"empty body", `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, c := ginContext("POST", "/auth/verify-email", tt.body)
			VerifyEmailHandler(c)
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
