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

	"tsb-service/pkg/utils"
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

// setAuthContext sets the Zitadel sub in the request context for authenticated handler tests.
func setAuthContext(c *gin.Context, zitadelSub string) {
	ctx := utils.SetZitadelSub(c.Request.Context(), zitadelSub)
	c.Request = c.Request.WithContext(ctx)
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

		// Verify returnCode is used (not sendCode)
		email := body["email"].(map[string]any)
		assert.NotNil(t, email["returnCode"])
		assert.Nil(t, email["sendCode"])

		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"userId":"zitadel-123","email":{"verificationCode":"abc123"}}`))
	})

	reqBody := `{"firstName":"John","lastName":"Doe","email":"test@example.com","password":"P@ssw0rd!"}`
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
		_, _ = w.Write([]byte(`{"userId":"zitadel-123","email":{"verificationCode":"abc123"}}`))
	})

	reqBody := `{"firstName":"John","lastName":"Doe","email":"test@example.com","password":"P@ssw0rd!","phone":"+32123456789"}`
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

	reqBody := `{"firstName":"John","lastName":"Doe","email":"existing@example.com","password":"P@ssw0rd!"}`
	w, c := ginContext("POST", "/auth/register", reqBody)

	RegisterHandler(c)

	assert.Equal(t, http.StatusConflict, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "email_already_exists", resp["error"])
}

func TestRegisterHandler_GoogleFirstUserLinking(t *testing.T) {
	callCount := 0
	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		switch {
		case r.URL.Path == "/v2/users/human" && r.Method == "POST":
			// User creation returns 409 (already exists via Google)
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"code":6,"message":"user already exists"}`))
		case r.URL.Path == "/v2/users" && r.Method == "POST":
			// Email search finds the existing Google user
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":[{"userId":"google-user-123"}]}`))
		case r.URL.Path == "/v2/users/google-user-123" && r.Method == "GET":
			// User has no password (Google-first)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"user":{"human":{"passwordChanged":"0001-01-01T00:00:00Z"}}}`))
		case r.URL.Path == "/v2/users/google-user-123/password" && r.Method == "POST":
			// Password set succeeds
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	reqBody := `{"firstName":"John","lastName":"Doe","email":"google@example.com","password":"P@ssw0rd!"}`
	w, c := ginContext("POST", "/auth/register", reqBody)

	RegisterHandler(c)

	assert.Equal(t, http.StatusCreated, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["success"])
}

func TestRegisterHandler_ExistingUserWithPassword(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/users/human" && r.Method == "POST":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"code":6,"message":"user already exists"}`))
		case r.URL.Path == "/v2/users" && r.Method == "POST":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":[{"userId":"existing-user-456"}]}`))
		case r.URL.Path == "/v2/users/existing-user-456" && r.Method == "GET":
			// User already has a password
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"user":{"human":{"passwordChanged":"2026-01-15T10:30:00Z"}}}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	reqBody := `{"firstName":"John","lastName":"Doe","email":"existing@example.com","password":"P@ssw0rd!"}`
	w, c := ginContext("POST", "/auth/register", reqBody)

	RegisterHandler(c)

	assert.Equal(t, http.StatusConflict, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "email_already_exists", resp["error"])
}

func TestRegisterHandler_WeakPassword(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":3,"message":"password complexity requirements not met"}`))
	})

	reqBody := `{"firstName":"John","lastName":"Doe","email":"test@example.com","password":"weak"}`
	w, c := ginContext("POST", "/auth/register", reqBody)

	RegisterHandler(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "weak_password", resp["error"])
}

func TestRegisterHandler_MissingFields(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"missing email", `{"firstName":"John","lastName":"Doe","password":"P@ssw0rd!"}`},
		{"missing password", `{"firstName":"John","lastName":"Doe","email":"test@example.com"}`},
		{"missing firstName", `{"lastName":"Doe","email":"test@example.com","password":"P@ssw0rd!"}`},
		{"missing lastName", `{"firstName":"John","email":"test@example.com","password":"P@ssw0rd!"}`},
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

// --- RequestPasswordResetHandler Tests ---

func TestRequestPasswordResetHandler_UserFound(t *testing.T) {
	var resetCalled bool
	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/users" && r.Method == "POST":
			// User search
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":[{"userId":"zitadel-456"}]}`))
		case strings.HasSuffix(r.URL.Path, "/password_reset") && r.Method == "POST":
			// Password reset — verify returnCode is used
			resetCalled = true
			assert.Equal(t, "/v2/users/zitadel-456/password_reset", r.URL.Path)
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			assert.NotNil(t, body["returnCode"])
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"verificationCode":"reset-code-xyz"}`))
		case r.URL.Path == "/v2/users/zitadel-456" && r.Method == "GET":
			// User details for email template
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"user":{"human":{"profile":{"givenName":"Test","familyName":"User"}}}}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	w, c := ginContext("POST", "/auth/password/request-reset", `{"email":"test@example.com"}`)

	RequestPasswordResetHandler(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.True(t, resetCalled, "password_reset endpoint should have been called")
}

func TestRequestPasswordResetHandler_UserNotFound(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		// User search returns empty results
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result":[]}`))
	})

	w, c := ginContext("POST", "/auth/password/request-reset", `{"email":"unknown@example.com"}`)

	RequestPasswordResetHandler(c)

	// Should still return 200 (email enumeration prevention)
	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["success"])
}

func TestRequestPasswordResetHandler_MissingEmail(t *testing.T) {
	w, c := ginContext("POST", "/auth/password/request-reset", `{}`)

	RequestPasswordResetHandler(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestRequestPasswordResetHandler_ZitadelResetFails(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/users" && r.Method == "POST":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":[{"userId":"zitadel-456"}]}`))
		case strings.HasSuffix(r.URL.Path, "/password_reset"):
			// Reset endpoint fails
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"internal error"}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	})

	w, c := ginContext("POST", "/auth/password/request-reset", `{"email":"test@example.com"}`)

	RequestPasswordResetHandler(c)

	// Should still return 200 (never expose failures)
	assert.Equal(t, http.StatusOK, w.Code)
}

// --- SetNewPasswordHandler Tests ---

func TestSetNewPasswordHandler_Success(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v2/users/user-123/password", r.URL.Path)
		assert.Equal(t, "POST", r.Method)

		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "reset-code-abc", body["verificationCode"])

		newPwd := body["newPassword"].(map[string]any)
		assert.Equal(t, "NewP@ssw0rd!", newPwd["password"])
		assert.Equal(t, false, newPwd["changeRequired"])

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})

	reqBody := `{"userId":"user-123","code":"reset-code-abc","password":"NewP@ssw0rd!"}`
	w, c := ginContext("POST", "/auth/password/reset", reqBody)

	SetNewPasswordHandler(c)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["success"])
}

func TestSetNewPasswordHandler_InvalidCode(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":3,"message":"verification code is invalid (COMMAND-3M0fs)"}`))
	})

	reqBody := `{"userId":"user-123","code":"bad-code","password":"NewP@ssw0rd!"}`
	w, c := ginContext("POST", "/auth/password/reset", reqBody)

	SetNewPasswordHandler(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "invalid_code", resp["error"])
}

func TestSetNewPasswordHandler_ExpiredCode(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":3,"message":"verification code expired"}`))
	})

	reqBody := `{"userId":"user-123","code":"expired-code","password":"NewP@ssw0rd!"}`
	w, c := ginContext("POST", "/auth/password/reset", reqBody)

	SetNewPasswordHandler(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "invalid_code", resp["error"])
}

func TestSetNewPasswordHandler_WeakPassword(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":3,"message":"password complexity requirements not met (COMMAND-oz74F)"}`))
	})

	reqBody := `{"userId":"user-123","code":"valid-code","password":"weak"}`
	w, c := ginContext("POST", "/auth/password/reset", reqBody)

	SetNewPasswordHandler(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "weak_password", resp["error"])
}

func TestSetNewPasswordHandler_MissingFields(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"missing userId", `{"code":"abc","password":"P@ssw0rd!"}`},
		{"missing code", `{"userId":"user-123","password":"P@ssw0rd!"}`},
		{"missing password", `{"userId":"user-123","code":"abc"}`},
		{"empty body", `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, c := ginContext("POST", "/auth/password/reset", tt.body)
			SetNewPasswordHandler(c)
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

// --- CreateSessionHandler Tests ---

func TestCreateSessionHandler_Success(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/sessions" && r.Method == "POST":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			checks := body["checks"].(map[string]any)
			assert.NotNil(t, checks["user"])
			assert.NotNil(t, checks["password"])
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"sessionId":"sess-123","sessionToken":"tok-abc"}`))
		case r.URL.Path == "/v2/users" && r.Method == "POST":
			// Email search for verification check
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":[{"userId":"user-456"}]}`))
		case r.URL.Path == "/v2/users/user-456" && r.Method == "GET":
			// Email is verified
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"user":{"human":{"email":{"isVerified":true}}}}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	w, c := ginContext("POST", "/auth/session", `{"loginName":"user@example.com","password":"P@ssw0rd!"}`)
	CreateSessionHandler(c)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "sess-123", resp["sessionId"])
	assert.Equal(t, "tok-abc", resp["sessionToken"])
}

func TestCreateSessionHandler_BadCredentials(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":16,"message":"invalid credentials"}`))
	})

	w, c := ginContext("POST", "/auth/session", `{"loginName":"user@example.com","password":"wrong"}`)
	CreateSessionHandler(c)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, ErrWrongPassword, resp["error"])
	assert.Nil(t, resp["message"], "must not leak Zitadel error details")
	assert.Nil(t, resp["code"], "must not leak Zitadel error code")
}

// TestCreateSessionHandler_UnknownAccount asserts the response for an account
// that doesn't exist is byte-identical to a wrong-password response. This
// prevents the /auth/session endpoint from becoming an account-enumeration
// oracle.
func TestCreateSessionHandler_UnknownAccount(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":16,"message":"user not found"}`))
	})

	w, c := ginContext("POST", "/auth/session", `{"loginName":"ghost@example.com","password":"P@ssw0rd!"}`)
	CreateSessionHandler(c)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, ErrWrongPassword, resp["error"])
}

func TestCreateSessionHandler_SocialOnlyAccount(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":3,"message":"user has not set a password (COMMAND-3nJ4t)"}`))
	})

	w, c := ginContext("POST", "/auth/session", `{"loginName":"google@example.com","password":"anything"}`)
	CreateSessionHandler(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "no_password", resp["error"])
}

func TestCreateSessionHandler_EmailNotVerified(t *testing.T) {
	var sessionDeleted bool
	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/sessions" && r.Method == "POST":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"sessionId":"sess-123","sessionToken":"tok-abc"}`))
		case r.URL.Path == "/v2/users" && r.Method == "POST":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":[{"userId":"user-789"}]}`))
		case r.URL.Path == "/v2/users/user-789" && r.Method == "GET":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"user":{"human":{"email":{"isVerified":false}}}}`))
		case r.URL.Path == "/v2/sessions/sess-123" && r.Method == "DELETE":
			sessionDeleted = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	w, c := ginContext("POST", "/auth/session", `{"loginName":"unverified@example.com","password":"P@ssw0rd!"}`)
	CreateSessionHandler(c)

	assert.Equal(t, http.StatusForbidden, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "email_not_verified", resp["error"])
	assert.True(t, sessionDeleted, "unverified session must be revoked")
}

func TestCreateSessionHandler_MissingFields(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"missing loginName", `{"password":"P@ssw0rd!"}`},
		{"missing password", `{"loginName":"user@example.com"}`},
		{"empty body", `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, c := ginContext("POST", "/auth/session", tt.body)
			CreateSessionHandler(c)
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
