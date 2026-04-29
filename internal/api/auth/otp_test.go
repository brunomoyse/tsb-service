package auth

import (
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
		case r.URL.Path == "/v2/users/user-otp-1/otp_email" && r.Method == "POST":
			// Lazy OTP-email factor enrollment — first attempt succeeds.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"details":{}}`))
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

// TestRequestOtpHandler_UnknownEmail asserts that Pattern B provisions a
// placeholder Zitadel user on the fly and returns a real session, keeping
// the response shape identical to the existing-user case (anti-enumeration).
func TestRequestOtpHandler_UnknownEmail(t *testing.T) {
	var placeholderCreated, sessionCalled bool
	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/users" && r.Method == "POST":
			// Email lookup — no result, triggers placeholder creation.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":[]}`))
		case r.URL.Path == "/v2/users/human" && r.Method == "POST":
			placeholderCreated = true
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			profile := body["profile"].(map[string]any)
			assert.Equal(t, "-", profile["givenName"], "placeholder must use sentinel marker")
			assert.Equal(t, "-", profile["familyName"])
			email := body["email"].(map[string]any)
			assert.Equal(t, true, email["isVerified"], "placeholder email must be pre-verified — OTP completion proves control")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"userId":"placeholder-user"}`))
		case r.URL.Path == "/v2/users/placeholder-user/otp_email" && r.Method == "POST":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"details":{}}`))
		case r.URL.Path == "/v2/sessions" && r.Method == "POST":
			sessionCalled = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"sessionId":"sess-new","sessionToken":"tok-new","challenges":{"otpEmail":"123456"}}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	w, c := ginContext("POST", "/auth/session/otp/request", `{"loginName":"ghost@example.com"}`)
	RequestOtpHandler(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.True(t, placeholderCreated, "unknown email must provision a placeholder Zitadel user")
	assert.True(t, sessionCalled, "session creation must run for the placeholder user")

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	// Same fields and same shape as the existing-user case.
	assert.Equal(t, "sess-new", resp["sessionId"])
	assert.Equal(t, "tok-new", resp["sessionToken"])
}

func TestRequestOtpHandler_ZitadelSessionFails(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/users" && r.Method == "POST":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":[{"userId":"user-otp-2"}]}`))
		case r.URL.Path == "/v2/users/user-otp-2/otp_email" && r.Method == "POST":
			// Already-enrolled response (Zitadel returns 409) — must be a no-op.
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"code":6,"message":"AlreadyExists"}`))
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

// TestRequestOtpHandler_LazyOtpEnrollment asserts that the handler enrolls
// the user in the OTP Email factor before requesting the session challenge,
// so first-time OTP logins don't fail with "Multifactor OTP isn't ready".
func TestRequestOtpHandler_LazyOtpEnrollment(t *testing.T) {
	var enrollCalled, sessionCalled bool
	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/users" && r.Method == "POST":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":[{"userId":"user-lazy"}]}`))
		case r.URL.Path == "/v2/users/user-lazy/otp_email" && r.Method == "POST":
			enrollCalled = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"details":{}}`))
		case r.URL.Path == "/v2/sessions" && r.Method == "POST":
			sessionCalled = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"sessionId":"sess-lazy","sessionToken":"tok-lazy","challenges":{"otpEmail":"123456"}}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	w, c := ginContext("POST", "/auth/session/otp/request", `{"loginName":"new@example.com"}`)
	RequestOtpHandler(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.True(t, enrollCalled, "OTP Email factor must be enrolled before session create")
	assert.True(t, sessionCalled, "session creation must still happen after enrollment")
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
		switch {
		case r.URL.Path == "/v2/sessions/sess-otp-1" && r.Method == "PATCH":
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
		case r.URL.Path == "/v2/sessions/sess-otp-1" && r.Method == "GET":
			// Lookup of session userId for the requiresProfile check (Pattern B).
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"session":{"factors":{"user":{"id":"user-existing","loginName":"user@example.com"}}}}`))
		case r.URL.Path == "/v2/users/user-existing" && r.Method == "GET":
			// Existing user — real first name, no profile completion needed.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"user":{"human":{"profile":{"givenName":"Alice","familyName":"Wonderland"}}}}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
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
	// Existing user has a real name — no profile completion step needed.
	assert.Equal(t, false, resp["requiresProfile"])
}

// TestVerifyOtpHandler_PlaceholderUser asserts that Pattern B signals to the
// frontend when the user still has the placeholder name marker, so the UI
// can render the name-capture step before /auth/finalize.
func TestVerifyOtpHandler_PlaceholderUser(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/sessions/sess-new" && r.Method == "PATCH":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"sessionToken":"tok-verified"}`))
		case r.URL.Path == "/v2/sessions/sess-new" && r.Method == "GET":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"session":{"factors":{"user":{"id":"placeholder-user","loginName":"new@example.com"}}}}`))
		case r.URL.Path == "/v2/users/placeholder-user" && r.Method == "GET":
			// Placeholder marker still in place — frontend must capture name.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"user":{"human":{"profile":{"givenName":"-","familyName":"-"}}}}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	body := `{"sessionId":"sess-new","sessionToken":"tok-pending","code":"123456"}`
	w, c := ginContext("POST", "/auth/session/otp/verify", body)
	VerifyOtpHandler(c)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["requiresProfile"], "placeholder users must trigger the name-capture step")
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

// TestVerifyOtpHandler_DuplicateSubmitReturnsCachedResponse asserts that a
// second verify on the same (sessionId, code) returns the first response
// without re-hitting Zitadel. This is the user-visible bug from the test
// dashboard: a double-fired submit consumed the OTP code on the first call
// and returned "Code not found" on the second.
func TestVerifyOtpHandler_DuplicateSubmitReturnsCachedResponse(t *testing.T) {
	var patchHits, getSessionHits, getUserHits int32
	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/sessions/sess-dup" && r.Method == "PATCH":
			atomic.AddInt32(&patchHits, 1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"sessionToken":"tok-verified"}`))
		case r.URL.Path == "/v2/sessions/sess-dup" && r.Method == "GET":
			atomic.AddInt32(&getSessionHits, 1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"session":{"factors":{"user":{"id":"user-dup","loginName":"u@example.com"}}}}`))
		case r.URL.Path == "/v2/users/user-dup" && r.Method == "GET":
			atomic.AddInt32(&getUserHits, 1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"user":{"human":{"profile":{"givenName":"Alice","familyName":"Wonderland"}}}}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	body := `{"sessionId":"sess-dup","sessionToken":"tok","code":"111111"}`

	w1, c1 := ginContext("POST", "/auth/session/otp/verify", body)
	VerifyOtpHandler(c1)
	require.Equal(t, http.StatusOK, w1.Code)

	w2, c2 := ginContext("POST", "/auth/session/otp/verify", body)
	VerifyOtpHandler(c2)
	require.Equal(t, http.StatusOK, w2.Code)

	assert.Equal(t, int32(1), atomic.LoadInt32(&patchHits), "duplicate submit must hit Zitadel PATCH exactly once")
	assert.Equal(t, int32(1), atomic.LoadInt32(&getSessionHits), "session lookup must run once — cache replays the full response")
	assert.Equal(t, int32(1), atomic.LoadInt32(&getUserHits), "user lookup must run once for the same reason")
	assert.JSONEq(t, w1.Body.String(), w2.Body.String(), "cached response must equal first response")
}

// TestVerifyOtpHandler_ConcurrentSubmitsAreSerialized asserts that several
// in-flight verifies for the same sessionID end up calling Zitadel exactly
// once, the rest get the cached success.
func TestVerifyOtpHandler_ConcurrentSubmitsAreSerialized(t *testing.T) {
	var patchHits int32
	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/sessions/sess-conc" && r.Method == "PATCH":
			atomic.AddInt32(&patchHits, 1)
			// Hold the response briefly so the other goroutines pile up on
			// the per-session mutex rather than racing serially through.
			time.Sleep(20 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"sessionToken":"tok-verified"}`))
		case r.URL.Path == "/v2/sessions/sess-conc" && r.Method == "GET":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"session":{"factors":{"user":{"id":"user-conc"}}}}`))
		case r.URL.Path == "/v2/users/user-conc" && r.Method == "GET":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"user":{"human":{"profile":{"givenName":"Bob","familyName":"Builder"}}}}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	body := `{"sessionId":"sess-conc","sessionToken":"tok","code":"222222"}`
	const concurrency = 5

	var wg sync.WaitGroup
	results := make([]int, concurrency)
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			w, c := ginContext("POST", "/auth/session/otp/verify", body)
			VerifyOtpHandler(c)
			results[idx] = w.Code
		}(i)
	}
	wg.Wait()

	assert.Equal(t, int32(1), atomic.LoadInt32(&patchHits), "concurrent verifies must hit Zitadel exactly once")
	for i, code := range results {
		assert.Equal(t, http.StatusOK, code, "goroutine %d must have observed a successful verify", i)
	}
}

// TestVerifyOtpHandler_DifferentCodeBypassesCache asserts that after a
// failed verify, retrying with a different code still hits Zitadel — only
// successful (sessionID, code) pairs are cached.
func TestVerifyOtpHandler_DifferentCodeBypassesCache(t *testing.T) {
	var patchCalls int32
	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/sessions/sess-mix" && r.Method == "PATCH":
			n := atomic.AddInt32(&patchCalls, 1)
			if n == 1 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":3,"message":"otp invalid"}`))
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"sessionToken":"tok-v"}`))
		case r.URL.Path == "/v2/sessions/sess-mix" && r.Method == "GET":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"session":{"factors":{"user":{"id":"user-mix"}}}}`))
		case r.URL.Path == "/v2/users/user-mix" && r.Method == "GET":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"user":{"human":{"profile":{"givenName":"Carol","familyName":"Danvers"}}}}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	w1, c1 := ginContext("POST", "/auth/session/otp/verify", `{"sessionId":"sess-mix","sessionToken":"tok","code":"000000"}`)
	VerifyOtpHandler(c1)
	require.Equal(t, http.StatusUnauthorized, w1.Code)

	w2, c2 := ginContext("POST", "/auth/session/otp/verify", `{"sessionId":"sess-mix","sessionToken":"tok","code":"123456"}`)
	VerifyOtpHandler(c2)
	require.Equal(t, http.StatusOK, w2.Code)

	assert.Equal(t, int32(2), atomic.LoadInt32(&patchCalls), "wrong-then-right code must hit Zitadel twice — failures are not cached")
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
