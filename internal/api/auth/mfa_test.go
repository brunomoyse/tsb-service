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

const (
	dashboardClient = "dashboard-client"
	customerClient  = "customer-client"
)

func setupMockZitadelWithAdminClient(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	Init(Config{
		ZitadelIssuer:   srv.URL,
		ZitadelClientID: "test-client-id",
		ServicePAT:      "test-pat",
		AdminPAT:        "test-admin-pat",
		AdminClientIDs:  []string{dashboardClient},
	})
	resetIdempotencyGatesForTest()
}

// finalizeMock serves the Zitadel calls made by FinalizeOIDCHandler.
type finalizeMock struct {
	clientID     string
	totpEnrolled bool
	totpVerified bool
	finalized    bool
	sessionReads int
}

func (m *finalizeMock) handle(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/oidc/auth_requests/ar-1" && r.Method == "GET":
			_, _ = w.Write([]byte(`{"authRequest":{"id":"ar-1","clientId":"` + m.clientID + `"}}`))
		case r.URL.Path == "/v2/sessions/sess-1" && r.Method == "GET":
			m.sessionReads++
			totp := ""
			if m.totpVerified {
				totp = `,"totp":{"verifiedAt":"2026-10-03T10:00:00Z"}`
			}
			_, _ = w.Write([]byte(`{"session":{"factors":{"user":{"id":"staff-1"}` + totp + `}}}`))
		case r.URL.Path == "/v2/users/staff-1/authentication_methods" && r.Method == "GET":
			methods := `"AUTHENTICATION_METHOD_TYPE_OTP_EMAIL"`
			if m.totpEnrolled {
				methods += `,"AUTHENTICATION_METHOD_TYPE_TOTP"`
			}
			_, _ = w.Write([]byte(`{"authMethodTypes":[` + methods + `]}`))
		case r.URL.Path == "/v2/oidc/auth_requests/ar-1" && r.Method == "POST":
			m.finalized = true
			_, _ = w.Write([]byte(`{"callbackUrl":"https://dashboard.example/fr/auth/callback?code=abc"}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func finalize(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	w, c := ginContext("POST", "/auth/finalize", `{"authRequestId":"ar-1","sessionId":"sess-1","sessionToken":"tok-1"}`)
	FinalizeOIDCHandler(c)
	return w
}

func TestFinalize_StaffWithTOTPMustPassIt(t *testing.T) {
	m := &finalizeMock{clientID: dashboardClient, totpEnrolled: true}
	setupMockZitadelWithAdminClient(t, m.handle(t))

	w := finalize(t)

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), ErrMFARequired)
	assert.False(t, m.finalized, "auth request must not be finalized without the TOTP check")
}

func TestFinalize_StaffWithVerifiedTOTPSucceeds(t *testing.T) {
	m := &finalizeMock{clientID: dashboardClient, totpEnrolled: true, totpVerified: true}
	setupMockZitadelWithAdminClient(t, m.handle(t))

	w := finalize(t)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.True(t, m.finalized)
}

func TestFinalize_StaffWithoutTOTPSucceeds(t *testing.T) {
	m := &finalizeMock{clientID: dashboardClient}
	setupMockZitadelWithAdminClient(t, m.handle(t))

	w := finalize(t)

	assert.Equal(t, http.StatusOK, w.Code, "TOTP is optional: staff without it log in with email only")
	assert.True(t, m.finalized)
}

func TestFinalize_CustomerClientSkipsMFACheck(t *testing.T) {
	m := &finalizeMock{clientID: customerClient, totpEnrolled: true}
	setupMockZitadelWithAdminClient(t, m.handle(t))

	w := finalize(t)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.True(t, m.finalized)
	assert.Zero(t, m.sessionReads, "customer logins must not pay for the staff MFA lookups")
}

func TestFinalize_StaffCheckFailsClosed(t *testing.T) {
	var finalized bool
	setupMockZitadelWithAdminClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			finalized = true
		}
		w.WriteHeader(http.StatusInternalServerError)
	})

	w := finalize(t)

	assert.Equal(t, http.StatusBadGateway, w.Code)
	assert.False(t, finalized)
}

func TestVerifyOtpHandler_RequiresTotpWhenEnrolled(t *testing.T) {
	setupMockZitadelWithAdminClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/sessions/sess-1" && r.Method == "PATCH":
			_, _ = w.Write([]byte(`{"sessionToken":"tok-2"}`))
		case r.URL.Path == "/v2/sessions/sess-1" && r.Method == "GET":
			_, _ = w.Write([]byte(`{"session":{"factors":{"user":{"id":"staff-1"}}}}`))
		case r.URL.Path == "/v2/users/staff-1" && r.Method == "GET":
			_, _ = w.Write([]byte(`{"user":{"human":{"profile":{"givenName":"Ada","familyName":"Admin"}}}}`))
		case r.URL.Path == "/v2/users/staff-1/authentication_methods":
			_, _ = w.Write([]byte(`{"authMethodTypes":["AUTHENTICATION_METHOD_TYPE_TOTP"]}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	w, c := ginContext("POST", "/auth/session/otp/verify", `{"sessionId":"sess-1","sessionToken":"tok-1","code":"123456"}`)
	VerifyOtpHandler(c)

	require.Equal(t, http.StatusOK, w.Code)
	var resp verifyOtpResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.RequiresTotp)
	assert.False(t, resp.RequiresProfile)
}

func TestVerifyTotpHandler(t *testing.T) {
	t.Run("valid code returns the new session token", func(t *testing.T) {
		setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/v2/sessions/sess-1", r.URL.Path)
			require.Equal(t, "PATCH", r.Method)
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			assert.Equal(t, "tok-1", body["sessionToken"])
			totp := body["checks"].(map[string]any)["totp"].(map[string]any)
			assert.Equal(t, "654321", totp["code"])
			_, _ = w.Write([]byte(`{"sessionToken":"tok-2"}`))
		})
		w, c := ginContext("POST", "/auth/session/totp/verify", `{"sessionId":"sess-1","sessionToken":"tok-1","code":"654321"}`)
		VerifyTotpHandler(c)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.JSONEq(t, `{"sessionId":"sess-1","sessionToken":"tok-2"}`, w.Body.String())
	})

	t.Run("wrong code is a generic invalid_code", func(t *testing.T) {
		setupMockZitadel(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"message":"Errors.User.MFA.OTP.InvalidCode"}`))
		})
		w, c := ginContext("POST", "/auth/session/totp/verify", `{"sessionId":"sess-1","sessionToken":"tok-1","code":"000000"}`)
		VerifyTotpHandler(c)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
		assert.Contains(t, w.Body.String(), ErrInvalidCode)
	})
}

// staffRequest builds a request context as the OIDC middleware would for a
// dashboard admin (or, with admin=false, a customer).
func staffRequest(t *testing.T, method, path, body string, admin bool) (*httptest.ResponseRecorder, *gin.Context) {
	t.Helper()
	w, c := ginContext(method, path, body)
	ctx := utils.SetUserID(c.Request.Context(), "11111111-1111-1111-1111-111111111111")
	ctx = utils.SetZitadelSub(ctx, "staff-1")
	ctx = utils.SetIsAdmin(ctx, admin)
	c.Request = c.Request.WithContext(ctx)
	return w, c
}

func TestMFAManagement_RejectsNonAdmins(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("non-admin must not reach Zitadel: %s %s", r.Method, r.URL.Path)
	})

	w, c := staffRequest(t, "POST", "/auth/mfa/totp", `{}`, false)
	StartTOTPHandler(c)
	assert.Equal(t, http.StatusForbidden, w.Code)

	// POS devices have no Zitadel sub and are not admins.
	w, c = ginContext("GET", "/auth/mfa", "")
	c.Request = c.Request.WithContext(utils.SetIsPOS(c.Request.Context(), true))
	MFAStatusHandler(c)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestStartAndConfirmTOTP(t *testing.T) {
	var audited []string
	SetAuditFunc(func(_ *gin.Context, action string, success bool) {
		if success {
			audited = append(audited, action)
		}
	})
	t.Cleanup(func() { SetAuditFunc(nil) })

	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-admin-pat", r.Header.Get("Authorization"))
		switch {
		case r.URL.Path == "/v2/users/staff-1/totp" && r.Method == "POST":
			_, _ = w.Write([]byte(`{"uri":"otpauth://totp/Pili:staff?secret=ABC","secret":"ABC"}`))
		case r.URL.Path == "/v2/users/staff-1/totp/verify" && r.Method == "POST":
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			assert.Equal(t, "123456", body["code"])
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	w, c := staffRequest(t, "POST", "/auth/mfa/totp", `{}`, true)
	StartTOTPHandler(c)
	require.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"uri":"otpauth://totp/Pili:staff?secret=ABC","secret":"ABC"}`, w.Body.String())

	w, c = staffRequest(t, "POST", "/auth/mfa/totp/verify", `{"code":"123456"}`, true)
	ConfirmTOTPHandler(c)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, []string{"mfa.totp.enable"}, audited)
}

func TestRemoveTOTP(t *testing.T) {
	newMock := func(t *testing.T, codeValid bool, removed, sessionDeleted *bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/v2/users/staff-1/authentication_methods":
				_, _ = w.Write([]byte(`{"authMethodTypes":["AUTHENTICATION_METHOD_TYPE_TOTP"]}`))
			case r.URL.Path == "/v2/sessions" && r.Method == "POST":
				var body map[string]any
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				checks := body["checks"].(map[string]any)
				assert.Equal(t, "staff-1", checks["user"].(map[string]any)["userId"])
				if !codeValid {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"sessionId":"check-sess","sessionToken":"check-tok"}`))
			case r.URL.Path == "/v2/sessions/check-sess" && r.Method == "DELETE":
				*sessionDeleted = true
				_, _ = w.Write([]byte(`{}`))
			case r.URL.Path == "/v2/users/staff-1/totp" && r.Method == "DELETE":
				*removed = true
				_, _ = w.Write([]byte(`{}`))
			default:
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			}
		}
	}

	t.Run("wrong code keeps the factor", func(t *testing.T) {
		var removed, sessionDeleted bool
		setupMockZitadel(t, newMock(t, false, &removed, &sessionDeleted))
		w, c := staffRequest(t, "POST", "/auth/mfa/totp/remove", `{"code":"000000"}`, true)
		RemoveTOTPHandler(c)

		assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
		assert.False(t, removed)
	})

	t.Run("valid code removes the factor and cleans up the check session", func(t *testing.T) {
		var removed, sessionDeleted bool
		setupMockZitadel(t, newMock(t, true, &removed, &sessionDeleted))
		w, c := staffRequest(t, "POST", "/auth/mfa/totp/remove", `{"code":"123456"}`, true)
		RemoveTOTPHandler(c)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.True(t, removed)
		assert.True(t, sessionDeleted)
	})

	t.Run("missing code is rejected", func(t *testing.T) {
		setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		})
		w, c := staffRequest(t, "POST", "/auth/mfa/totp/remove", `{}`, true)
		RemoveTOTPHandler(c)
		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.True(t, strings.Contains(w.Body.String(), "code is required"))
	})
}
