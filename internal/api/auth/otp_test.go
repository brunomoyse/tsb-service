package auth

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- RequestOtpHandler Tests ---

func TestRequestOtpHandler_Success(t *testing.T) {
	var sessionCalled bool
	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/users" && r.Method == "POST":
			// Email lookup
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":[{"userId":"user-otp-1"}]}`))
		case r.URL.Path == "/v2/users/user-otp-1" && r.Method == "GET":
			// Email-verified gate
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"user":{"human":{"email":{"isVerified":true}}}}`))
		case r.URL.Path == "/v2/sessions" && r.Method == "POST":
			sessionCalled = true
			assert.Equal(t, "Bearer test-pat", r.Header.Get("Authorization"))

			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))

			// Identify the user but do NOT submit any auth-method check yet —
			// the otpEmail.code check is added later via VerifyOtpHandler.
			checks := body["checks"].(map[string]any)
			assert.NotNil(t, checks["user"])
			_, hasPassword := checks["password"]
			assert.False(t, hasPassword, "OTP request must not include a password check")

			// Request the otpEmail challenge with returnCode so the backend can
			// deliver the code via its own template.
			challenges := body["challenges"].(map[string]any)
			otpEmail := challenges["otpEmail"].(map[string]any)
			assert.NotNil(t, otpEmail["returnCode"])
			assert.Nil(t, otpEmail["sendCode"])

			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"sessionId":"sess-otp-1","sessionToken":"tok-otp-1","challenges":{"otpEmail":"123456"}}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	w, c := ginContext("POST", "/auth/session/otp/request", `{"loginName":"user@example.com","lang":"fr"}`)
	RequestOtpHandler(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.True(t, sessionCalled, "session creation must be called")

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "sess-otp-1", resp["sessionId"])
	assert.Equal(t, "tok-otp-1", resp["sessionToken"])

	// The OTP code itself must never leak to the client — only the sessionId
	// and sessionToken needed to verify it.
	assert.NotContains(t, w.Body.String(), "123456")
}

// TestRequestOtpHandler_UnknownEmail asserts the response shape is identical
// to the success case so the endpoint can't be turned into an account
// enumeration oracle.
func TestRequestOtpHandler_UnknownEmail(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		// User search returns no result
		assert.Equal(t, "/v2/users", r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result":[]}`))
	})

	w, c := ginContext("POST", "/auth/session/otp/request", `{"loginName":"ghost@example.com"}`)
	RequestOtpHandler(c)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	// Same fields as success — value differences must not be observable as
	// they would expose account state.
	_, hasSessionID := resp["sessionId"]
	_, hasSessionToken := resp["sessionToken"]
	assert.True(t, hasSessionID)
	assert.True(t, hasSessionToken)
}

func TestRequestOtpHandler_EmailNotVerified(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/users" && r.Method == "POST":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":[{"userId":"user-unverified"}]}`))
		case r.URL.Path == "/v2/users/user-unverified" && r.Method == "GET":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"user":{"human":{"email":{"isVerified":false}}}}`))
		case r.URL.Path == "/v2/sessions":
			t.Error("session creation must not be called for unverified email")
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	w, c := ginContext("POST", "/auth/session/otp/request", `{"loginName":"unverified@example.com"}`)
	RequestOtpHandler(c)

	assert.Equal(t, http.StatusForbidden, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "email_not_verified", resp["error"])
}

func TestRequestOtpHandler_ZitadelSessionFails(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/users" && r.Method == "POST":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":[{"userId":"user-otp-2"}]}`))
		case r.URL.Path == "/v2/users/user-otp-2" && r.Method == "GET":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"user":{"human":{"email":{"isVerified":true}}}}`))
		case r.URL.Path == "/v2/sessions":
			// Zitadel rejects (e.g. user temporarily locked)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":3,"message":"user locked"}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	w, c := ginContext("POST", "/auth/session/otp/request", `{"loginName":"locked@example.com"}`)
	RequestOtpHandler(c)

	// Generic 200 — we mustn't disclose Zitadel-side rejections through the
	// status code.
	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	// No error field leaked — only the sentinel session shape.
	_, hasError := resp["error"]
	assert.False(t, hasError)
}

func TestRequestOtpHandler_MissingFields(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"missing loginName", `{}`},
		{"empty loginName", `{"loginName":""}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, c := ginContext("POST", "/auth/session/otp/request", tt.body)
			RequestOtpHandler(c)
			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	}
}

// --- VerifyOtpHandler Tests ---

func TestVerifyOtpHandler_Success(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v2/sessions/sess-otp-1", r.URL.Path)
		assert.Equal(t, "PATCH", r.Method)

		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		// PATCH must include the original sessionToken to authorize the update.
		assert.Equal(t, "tok-otp-1", body["sessionToken"])

		// And the otpEmail.code check the user typed.
		checks := body["checks"].(map[string]any)
		otpEmail := checks["otpEmail"].(map[string]any)
		assert.Equal(t, "123456", otpEmail["code"])

		w.WriteHeader(http.StatusOK)
		// Zitadel responds with a fresh sessionToken whose otpEmail check is
		// fulfilled — that's the token used to finalize OIDC.
		_, _ = w.Write([]byte(`{"sessionToken":"tok-otp-2"}`))
	})

	body := `{"sessionId":"sess-otp-1","sessionToken":"tok-otp-1","code":"123456"}`
	w, c := ginContext("POST", "/auth/session/otp/verify", body)
	VerifyOtpHandler(c)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	// sessionId is preserved from the URL — the verify response must round-trip
	// it so the client uses the same session for finalize.
	assert.Equal(t, "sess-otp-1", resp["sessionId"])
	assert.Equal(t, "tok-otp-2", resp["sessionToken"])
}

func TestVerifyOtpHandler_InvalidCode(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":3,"message":"otp invalid (COMMAND-3M0fs)"}`))
	})

	body := `{"sessionId":"sess-otp-1","sessionToken":"tok-otp-1","code":"000000"}`
	w, c := ginContext("POST", "/auth/session/otp/verify", body)
	VerifyOtpHandler(c)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, ErrInvalidCode, resp["error"])
	// Don't expose Zitadel internals on a verify failure.
	assert.Nil(t, resp["message"], "must not leak Zitadel error details")
	assert.Nil(t, resp["code"])
}

// TestVerifyOtpHandler_ExpiredCode asserts that an expired code collapses to
// the same response as a wrong code — preventing an attacker from learning
// whether the code expired vs. was never valid.
func TestVerifyOtpHandler_ExpiredCode(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":3,"message":"otp expired"}`))
	})

	body := `{"sessionId":"sess-otp-1","sessionToken":"tok-otp-1","code":"123456"}`
	w, c := ginContext("POST", "/auth/session/otp/verify", body)
	VerifyOtpHandler(c)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, ErrInvalidCode, resp["error"])
}

// TestVerifyOtpHandler_UnknownSession covers the case where the sessionId is
// stale or never existed — must look the same as a wrong code.
func TestVerifyOtpHandler_UnknownSession(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":5,"message":"session not found"}`))
	})

	body := `{"sessionId":"missing","sessionToken":"tok","code":"123456"}`
	w, c := ginContext("POST", "/auth/session/otp/verify", body)
	VerifyOtpHandler(c)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, ErrInvalidCode, resp["error"])
}

func TestVerifyOtpHandler_MissingFields(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"missing sessionId", `{"sessionToken":"tok","code":"123456"}`},
		{"missing sessionToken", `{"sessionId":"sess","code":"123456"}`},
		{"missing code", `{"sessionId":"sess","sessionToken":"tok"}`},
		{"empty body", `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, c := ginContext("POST", "/auth/session/otp/verify", tt.body)
			VerifyOtpHandler(c)
			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	}
}

// --- ResendOtpHandler Tests ---

func TestResendOtpHandler_Success(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/sessions/sess-otp-1" && r.Method == "PATCH":
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			assert.Equal(t, "tok-otp-1", body["sessionToken"])

			// Resend re-issues the otpEmail challenge — no checks block.
			challenges := body["challenges"].(map[string]any)
			otpEmail := challenges["otpEmail"].(map[string]any)
			assert.NotNil(t, otpEmail["returnCode"])
			_, hasChecks := body["checks"]
			assert.False(t, hasChecks, "resend must not include a checks block")

			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"sessionToken":"tok-otp-1","challenges":{"otpEmail":"654321"}}`))
		case r.URL.Path == "/v2/sessions/sess-otp-1" && r.Method == "GET":
			// Lookup of session user for the resend email recipient (skipped
			// when scaleway isn't initialized — kept here as a benign mock).
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"session":{"factors":{"user":{"loginName":"user@example.com","displayName":"Test User"}}}}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	body := `{"sessionId":"sess-otp-1","sessionToken":"tok-otp-1","lang":"fr"}`
	w, c := ginContext("POST", "/auth/session/otp/resend", body)
	ResendOtpHandler(c)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["success"])
	// The fresh code must never be returned to the client.
	assert.NotContains(t, w.Body.String(), "654321")
}

// TestResendOtpHandler_ZitadelError asserts the endpoint always returns 200
// — never disclose the session state to the client through resend errors.
func TestResendOtpHandler_ZitadelError(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":5,"message":"session not found"}`))
	})

	body := `{"sessionId":"missing","sessionToken":"tok"}`
	w, c := ginContext("POST", "/auth/session/otp/resend", body)
	ResendOtpHandler(c)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["success"])
}

func TestResendOtpHandler_MissingFields(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"missing sessionId", `{"sessionToken":"tok"}`},
		{"missing sessionToken", `{"sessionId":"sess"}`},
		{"empty body", `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, c := ginContext("POST", "/auth/session/otp/resend", tt.body)
			ResendOtpHandler(c)
			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	}
}
