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

// setupMockZitadel starts a mock Zitadel server and sets the env vars so the
// handlers call it instead of a real Zitadel instance.
func setupMockZitadel(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	t.Setenv("ZITADEL_ISSUER", srv.URL)
	t.Setenv("ZITADEL_SERVICE_PAT", "test-pat")
	t.Setenv("ZITADEL_ADMIN_PAT", "test-admin-pat")
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
		case r.URL.Path == "/v2/users/google-user-123/password" && r.Method == "PUT":
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
