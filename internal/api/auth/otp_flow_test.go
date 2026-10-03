package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/pkg/email/scaleway"
)

// emptySession is the enumeration-resistant answer for every swallowed failure.
func assertEmptySession(t *testing.T, code int, resp map[string]any) {
	t.Helper()
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "", resp["sessionId"])
	assert.Equal(t, "", resp["sessionToken"])
	_, hasError := resp["error"]
	assert.False(t, hasError)
}

// --- RequestOtpHandler: input validation ---

func TestRequestOtp_BadInput(t *testing.T) {
	for name, body := range map[string]string{
		"whitespace only": `{"loginName":"   "}`,
		"tabs and spaces": `{"loginName":" \t "}`,
		"malformed json":  `{"loginName":`,
		"not json":        `loginName=a@example.com`,
		"wrong type":      `{"loginName":42}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := &otpFake{userID: "u1"}
			code, _ := requestOtp(t, f, body)
			assert.Equal(t, http.StatusBadRequest, code)
			assert.Zero(t, f.count(), "no Zitadel call on invalid input")
		})
	}
}

func TestRequestOtp_NormalizesLoginName(t *testing.T) {
	f := &otpFake{userID: "u1"}
	code, resp := requestOtp(t, f, `{"loginName":"  Bob.Smith@Example.COM \n"}`)

	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "bob.smith@example.com", f.searchedEmail)
	assert.Equal(t, "bob.smith@example.com", f.sessionLogin)
	assert.Equal(t, "sess-1", resp["sessionId"])
}

func TestRequestOtp_UndeliverableEmail(t *testing.T) {
	for name, login := range map[string]string{
		"typo tld":        "ann@example.coma",
		"no dot":          "ann@localhost",
		"no at":           "not-an-email",
		"display name":    "Ann <ann@example.com>",
		"double at":       "ann@@example.com",
		"unknown domain":  "ann@nope.invalid.example",
		"leading at only": "@example.com",
	} {
		t.Run(name, func(t *testing.T) {
			f := &otpFake{}
			code, resp := requestOtp(t, f, fmt.Sprintf(`{"loginName":%q}`, login))

			assert.Equal(t, http.StatusUnprocessableEntity, code)
			assert.Equal(t, ErrInvalidEmail, resp["error"])
			assert.Zero(t, f.count(), "no lookup and no placeholder for an undeliverable address")
		})
	}
}

func TestRequestOtp_DNSOutageFailsOpen(t *testing.T) {
	orig := mailResolver
	t.Cleanup(func() { mailResolver = orig })
	mailResolver = fakeResolver{fail: &net.DNSError{Err: "i/o timeout", IsTimeout: true}}

	f := &otpFake{userID: "u1"}
	code, resp := requestOtp(t, f, `{"loginName":"ann@somewhere.be"}`)

	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "sess-1", resp["sessionId"])
}

func TestRequestOtp_E2EAndReviewLoginsSkipDeliverabilityCheck(t *testing.T) {
	resetLoginLists(t, "e2e@nowhere.invalid", "review@nowhere.invalid")
	for _, login := range []string{"e2e@nowhere.invalid", "Review@Nowhere.Invalid"} {
		t.Run(login, func(t *testing.T) {
			f := &otpFake{userID: "u1"}
			code, resp := requestOtp(t, f, fmt.Sprintf(`{"loginName":%q}`, login))

			assert.Equal(t, http.StatusOK, code)
			assert.Equal(t, "sess-1", resp["sessionId"])
		})
	}
}

// --- RequestOtpHandler: account resolution ---

func TestRequestOtp_ExistingUserCreatesNoPlaceholder(t *testing.T) {
	f := &otpFake{userID: "u1"}
	code, _ := requestOtp(t, f, `{"loginName":"known@example.com"}`)

	assert.Equal(t, http.StatusOK, code)
	assert.False(t, f.called("POST /v2/users/human"))
	assert.False(t, f.called("GET /v2/users/u1"), "projection wait is only for just-created placeholders")
	assert.True(t, f.called("POST /v2/users/u1/otp_email"))
}

func TestRequestOtp_UnknownEmailWaitsForProjection(t *testing.T) {
	f := &otpFake{given: "-", family: "-"}
	code, resp := requestOtp(t, f, `{"loginName":"new@example.com"}`)

	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "sess-1", resp["sessionId"])
	assert.True(t, f.called("POST /v2/users/human"))
	assert.True(t, f.called("GET /v2/users/placeholder-user"))
	assert.True(t, f.called("POST /v2/users/placeholder-user/otp_email"))
	assert.Equal(t, "new@example.com", f.sessionLogin)
}

func TestRequestOtp_ProjectionTimeoutStillCreatesSession(t *testing.T) {
	fastProjectionPolling(t)
	f := &otpFake{userGet: http.StatusNotFound}
	code, resp := requestOtp(t, f, `{"loginName":"new@example.com"}`)

	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "sess-1", resp["sessionId"])
}

func TestRequestOtp_SearchErrorFallsBackToPlaceholder(t *testing.T) {
	// A failing search is indistinguishable from "unknown": the handler tries
	// to provision, and still answers with a session shape.
	f := &otpFake{searchStatus: http.StatusInternalServerError}
	code, resp := requestOtp(t, f, `{"loginName":"new@example.com"}`)

	assert.Equal(t, http.StatusOK, code)
	assert.True(t, f.called("POST /v2/users/human"))
	assert.Equal(t, "sess-1", resp["sessionId"])
}

func TestRequestOtp_SearchErrorOnExistingUserStaysEnumerationResistant(t *testing.T) {
	// Search down AND the account already exists: Zitadel refuses the duplicate
	// placeholder, the caller must still get the generic empty answer.
	f := &otpFake{searchStatus: http.StatusBadGateway, createStatus: http.StatusConflict, createBody: `{"code":6,"message":"User already exists"}`}
	code, resp := requestOtp(t, f, `{"loginName":"known@example.com"}`)

	assertEmptySession(t, code, resp)
	assert.False(t, f.called("POST /v2/sessions"))
}

func TestRequestOtp_PlaceholderCreationFailures(t *testing.T) {
	for name, f := range map[string]*otpFake{
		"zitadel 500":     {createStatus: http.StatusInternalServerError, createBody: `{"message":"boom"}`},
		"zitadel 400":     {createStatus: http.StatusBadRequest, createBody: `{"message":"invalid email"}`},
		"invalid json":    {createBody: `not json`},
		"missing user id": {createBody: `{}`},
	} {
		t.Run(name, func(t *testing.T) {
			code, resp := requestOtp(t, f, `{"loginName":"new@example.com"}`)

			assertEmptySession(t, code, resp)
			assert.False(t, f.called("POST /v2/sessions"), "no session without a user")
		})
	}
}

func TestRequestOtp_EnrollmentFailureStillTriesSession(t *testing.T) {
	f := &otpFake{userID: "u1", enrollStatus: http.StatusInternalServerError, enrollBody: `{"message":"down"}`}
	code, resp := requestOtp(t, f, `{"loginName":"known@example.com"}`)

	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "sess-1", resp["sessionId"])
}

func TestRequestOtp_AlreadyEnrolledVariants(t *testing.T) {
	for name, f := range map[string]*otpFake{
		"409":                     {userID: "u1", enrollStatus: http.StatusConflict},
		"400 already exists text": {userID: "u1", enrollStatus: http.StatusBadRequest, enrollBody: `{"message":"OTP already exists"}`},
		"2xx":                     {userID: "u1", enrollStatus: http.StatusCreated},
	} {
		t.Run(name, func(t *testing.T) {
			code, resp := requestOtp(t, f, `{"loginName":"known@example.com"}`)
			assert.Equal(t, http.StatusOK, code)
			assert.Equal(t, "sess-1", resp["sessionId"])
		})
	}
}

// --- RequestOtpHandler: session creation ---

func TestRequestOtp_SessionRejectedIsGeneric(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusNotFound, http.StatusPreconditionFailed, http.StatusInternalServerError, http.StatusBadGateway} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			f := &otpFake{userID: "u1", sessionCode: status, sessionBody: `{"message":"secret internal detail"}`}
			code, resp := requestOtp(t, f, `{"loginName":"known@example.com"}`)

			assertEmptySession(t, code, resp)
		})
	}
}

func TestRequestOtp_SessionBadJSON(t *testing.T) {
	f := &otpFake{userID: "u1", sessionBody: `<html>`}
	code, resp := requestOtp(t, f, `{"loginName":"known@example.com"}`)

	assert.Equal(t, http.StatusBadGateway, code)
	assert.NotEmpty(t, resp["error"])
}

func TestRequestOtp_ZitadelUnreachable(t *testing.T) {
	// Provisioning fails first at transport level: generic answer.
	setupDeadZitadel(t)
	w, c := ginContext("POST", "/auth/session/otp/request", `{"loginName":"known@example.com"}`)
	RequestOtpHandler(c)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assertEmptySession(t, w.Code, resp)
}

func TestRequestOtp_SessionCreateConnectionDropped(t *testing.T) {
	f := &otpFake{userID: "u1", sessionDrop: true}
	code, resp := requestOtp(t, f, `{"loginName":"known@example.com"}`)

	assert.Equal(t, http.StatusBadGateway, code)
	assert.NotEmpty(t, resp["error"])
}

func TestRequestOtp_SessionOKStatus200(t *testing.T) {
	f := &otpFake{userID: "u1", sessionCode: http.StatusOK}
	code, resp := requestOtp(t, f, `{"loginName":"known@example.com"}`)

	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "sess-1", resp["sessionId"])
	assert.Equal(t, "tok-1", resp["sessionToken"])
}

func TestRequestOtp_CodeNeverReachesClient(t *testing.T) {
	fakeEmail(t, true, nil)
	f := &otpFake{userID: "u1"}
	setupMockZitadel(t, f.handler(t))
	w, c := ginContext("POST", "/auth/session/otp/request", `{"loginName":"known@example.com"}`)
	RequestOtpHandler(c)

	assert.NotContains(t, w.Body.String(), "123456")
}

// --- RequestOtpHandler: email delivery ---

func TestRequestOtp_SendsEmailWithProfileName(t *testing.T) {
	sent := fakeEmail(t, true, nil)
	f := &otpFake{userID: "u1", given: "Alice", family: "Wonderland", email: "alice@example.com"}
	code, _ := requestOtp(t, f, `{"loginName":"alice@example.com","lang":"en"}`)

	assert.Equal(t, http.StatusOK, code)
	require.Len(t, *sent, 1)
	m := (*sent)[0]
	assert.Equal(t, "Alice", m.user.FirstName)
	assert.Equal(t, "Wonderland", m.user.LastName)
	assert.Equal(t, "alice@example.com", m.user.Email)
	assert.Equal(t, "en", m.lang)
	assert.Equal(t, "123456", m.code)
}

func TestRequestOtp_DefaultsToFrench(t *testing.T) {
	sent := fakeEmail(t, true, nil)
	f := &otpFake{userID: "u1"}
	requestOtp(t, f, `{"loginName":"user@example.com"}`)

	require.Len(t, *sent, 1)
	assert.Equal(t, "fr", (*sent)[0].lang)
}

func TestRequestOtp_PlaceholderSalutationUsesEmail(t *testing.T) {
	sent := fakeEmail(t, true, nil)
	f := &otpFake{given: "-", family: "-", email: "new@example.com"}
	code, _ := requestOtp(t, f, `{"loginName":"new@example.com"}`)

	assert.Equal(t, http.StatusOK, code)
	require.Len(t, *sent, 1)
	m := (*sent)[0]
	assert.Equal(t, "new@example.com", m.user.FirstName, "placeholder marker must never be used as a name")
	assert.Empty(t, m.user.LastName)
}

func TestRequestOtp_ProfileLookupFailureFallsBackToLoginName(t *testing.T) {
	sent := fakeEmail(t, true, nil)
	f := &otpFake{userID: "u1", userGet: http.StatusInternalServerError}
	code, _ := requestOtp(t, f, `{"loginName":"user@example.com"}`)

	assert.Equal(t, http.StatusOK, code)
	require.Len(t, *sent, 1)
	assert.Equal(t, "user@example.com", (*sent)[0].user.FirstName)
	assert.Equal(t, "user@example.com", (*sent)[0].user.Email)
}

func TestRequestOtp_NoEmailWhenBackendNotReady(t *testing.T) {
	sent := fakeEmail(t, false, nil)
	f := &otpFake{userID: "u1"}
	code, resp := requestOtp(t, f, `{"loginName":"user@example.com"}`)

	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "sess-1", resp["sessionId"])
	assert.Empty(t, *sent)
}

func TestRequestOtp_NoEmailWhenZitadelReturnsNoCode(t *testing.T) {
	sent := fakeEmail(t, true, nil)
	f := &otpFake{userID: "u1", sessionBody: `{"sessionId":"sess-1","sessionToken":"tok-1"}`}
	code, _ := requestOtp(t, f, `{"loginName":"user@example.com"}`)

	assert.Equal(t, http.StatusOK, code)
	assert.Empty(t, *sent)
}

func TestRequestOtp_E2EAndReviewLoginsSkipSend(t *testing.T) {
	resetLoginLists(t, "e2e@example.com", "review@example.com")
	for _, login := range []string{"e2e@example.com", "E2E@example.com", "review@example.com"} {
		t.Run(login, func(t *testing.T) {
			sent := fakeEmail(t, true, nil)
			f := &otpFake{userID: "u1", email: strings.ToLower(login)}
			code, resp := requestOtp(t, f, fmt.Sprintf(`{"loginName":%q}`, login))

			assert.Equal(t, http.StatusOK, code)
			assert.Equal(t, "sess-1", resp["sessionId"])
			assert.Empty(t, *sent, "no real mail for e2e / store-review logins")
		})
	}
}

func TestRequestOtp_ReviewLoginStashesCode(t *testing.T) {
	resetLoginLists(t, "", "review@example.com")
	fakeEmail(t, true, nil)
	t.Cleanup(func() { reviewOtpStore = map[string]reviewOtpEntry{} })

	f := &otpFake{userID: "u1", email: "review@example.com"}
	requestOtp(t, f, `{"loginName":"review@example.com"}`)

	reviewOtpMu.Lock()
	entry, ok := reviewOtpStore["review@example.com"]
	reviewOtpMu.Unlock()
	require.True(t, ok)
	assert.Equal(t, "123456", entry.code)
}

func TestRequestOtp_OrdinaryLoginIsNotStashed(t *testing.T) {
	resetLoginLists(t, "", "review@example.com")
	fakeEmail(t, true, nil)
	t.Cleanup(func() { reviewOtpStore = map[string]reviewOtpEntry{} })

	f := &otpFake{userID: "u1"}
	requestOtp(t, f, `{"loginName":"someone@example.com"}`)

	reviewOtpMu.Lock()
	defer reviewOtpMu.Unlock()
	assert.Empty(t, reviewOtpStore)
}

func TestRequestOtp_SendFailureStillReturnsSession(t *testing.T) {
	sent := fakeEmail(t, true, errors.New("scaleway 503"))
	f := &otpFake{userID: "u1"}
	code, resp := requestOtp(t, f, `{"loginName":"user@example.com"}`)

	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "sess-1", resp["sessionId"])
	assert.Len(t, *sent, 1)
	assert.False(t, f.deleted)
}

func TestRequestOtp_InvalidRecipientDeletesFreshPlaceholder(t *testing.T) {
	fakeEmail(t, true, fmt.Errorf("send: %w", scaleway.ErrInvalidRecipient))
	f := &otpFake{given: "-", family: "-"}
	code, resp := requestOtp(t, f, `{"loginName":"typo@example.com"}`)

	assert.Equal(t, http.StatusUnprocessableEntity, code)
	assert.Equal(t, ErrInvalidEmail, resp["error"])
	assert.True(t, f.called("DELETE /v2/users/placeholder-user"), "the placeholder can never log in: delete it")
}

func TestRequestOtp_InvalidRecipientKeepsExistingAccount(t *testing.T) {
	fakeEmail(t, true, scaleway.ErrInvalidRecipient)
	f := &otpFake{userID: "u1"}
	code, resp := requestOtp(t, f, `{"loginName":"known@example.com"}`)

	assert.Equal(t, http.StatusUnprocessableEntity, code)
	assert.Equal(t, ErrInvalidEmail, resp["error"])
	assert.False(t, f.deleted, "a pre-existing account must never be deleted")
}

func TestRequestOtp_InvalidRecipientPlaceholderDeleteFailureStill422(t *testing.T) {
	fakeEmail(t, true, scaleway.ErrInvalidRecipient)
	f := &otpFake{given: "-", family: "-", deleteStatus: http.StatusInternalServerError}
	code, resp := requestOtp(t, f, `{"loginName":"typo@example.com"}`)

	assert.Equal(t, http.StatusUnprocessableEntity, code)
	assert.Equal(t, ErrInvalidEmail, resp["error"])
	assert.True(t, f.deleted)
}

// --- VerifyOtpHandler: extra branches ---

func TestVerifyOtp_ZitadelUnreachable(t *testing.T) {
	setupDeadZitadel(t)
	w, c := ginContext("POST", "/auth/session/otp/verify", `{"sessionId":"s","sessionToken":"t","code":"123456"}`)
	VerifyOtpHandler(c)

	assert.Equal(t, http.StatusBadGateway, w.Code)
}

func TestVerifyOtp_BadJSONFromZitadel(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`oops`)) })
	w, c := ginContext("POST", "/auth/session/otp/verify", `{"sessionId":"s","sessionToken":"t","code":"123456"}`)
	VerifyOtpHandler(c)

	assert.Equal(t, http.StatusBadGateway, w.Code)
}

func TestVerifyOtp_MalformedBody(t *testing.T) {
	w, c := ginContext("POST", "/auth/session/otp/verify", `{`)
	VerifyOtpHandler(c)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestVerifyOtp_ProfileLookupFailureDoesNotBlockLogin(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PATCH" {
			_, _ = w.Write([]byte(`{"sessionToken":"tok-verified"}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	})
	w, c := ginContext("POST", "/auth/session/otp/verify", `{"sessionId":"s","sessionToken":"t","code":"123456"}`)
	VerifyOtpHandler(c)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp verifyOtpResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "tok-verified", resp.SessionToken)
	assert.Equal(t, "s", resp.SessionID, "sessionId in the URL stays the one the client uses")
	assert.False(t, resp.RequiresProfile)
	assert.False(t, resp.RequiresTotp)
}

func TestVerifyOtp_WrongCodeIsNotCachedAndRightCodeSucceeds(t *testing.T) {
	var patches int
	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "PATCH":
			patches++
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			code := body["checks"].(map[string]any)["otpEmail"].(map[string]any)["code"]
			if code != "111111" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"message":"wrong"}`))
				return
			}
			_, _ = w.Write([]byte(`{"sessionToken":"tok-ok"}`))
		case r.URL.Path == "/v2/sessions/s":
			_, _ = w.Write([]byte(`{"session":{"factors":{"user":{"id":"u1"}}}}`))
		case r.URL.Path == "/v2/users/u1":
			_, _ = w.Write([]byte(`{"user":{"human":{"profile":{"givenName":"A","familyName":"B"}}}}`))
		case strings.HasSuffix(r.URL.Path, "/authentication_methods"):
			_, _ = w.Write([]byte(`{"authMethodTypes":[]}`))
		}
	})

	w, c := ginContext("POST", "/x", `{"sessionId":"s","sessionToken":"t","code":"000000"}`)
	VerifyOtpHandler(c)
	assert.Equal(t, http.StatusUnauthorized, w.Code)

	w, c = ginContext("POST", "/x", `{"sessionId":"s","sessionToken":"t","code":"111111"}`)
	VerifyOtpHandler(c)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, 2, patches)
}

// --- ResendOtpHandler: extra branches ---

type resendFake struct {
	patchStatus int
	patchBody   string
	getStatus   int
	getBody     string
}

func (f resendFake) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PATCH" {
			w.WriteHeader(orStatus(f.patchStatus, 200))
			if f.patchBody == "" {
				_, _ = w.Write([]byte(`{"sessionToken":"t","challenges":{"otpEmail":"654321"}}`))
			} else {
				_, _ = w.Write([]byte(f.patchBody))
			}
			return
		}
		w.WriteHeader(orStatus(f.getStatus, 200))
		if f.getBody == "" {
			_, _ = w.Write([]byte(`{"session":{"factors":{"user":{"loginName":"user@example.com","displayName":"Alice"}}}}`))
		} else {
			_, _ = w.Write([]byte(f.getBody))
		}
	}
}

func resend(t *testing.T, f resendFake, body string) (int, map[string]any) {
	t.Helper()
	setupMockZitadel(t, f.handler())
	w, c := ginContext("POST", "/auth/session/otp/resend", body)
	ResendOtpHandler(c)
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return w.Code, resp
}

const resendBody = `{"sessionId":"s","sessionToken":"t","lang":"en"}`

func TestResendOtp_SendsEmailToSessionUser(t *testing.T) {
	sent := fakeEmail(t, true, nil)
	code, resp := resend(t, resendFake{}, resendBody)

	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, true, resp["success"])
	require.Len(t, *sent, 1)
	m := (*sent)[0]
	assert.Equal(t, "user@example.com", m.user.Email)
	assert.Equal(t, "Alice", m.user.FirstName)
	assert.Equal(t, "en", m.lang)
	assert.Equal(t, "654321", m.code)
}

func TestResendOtp_DefaultsToFrench(t *testing.T) {
	sent := fakeEmail(t, true, nil)
	resend(t, resendFake{}, `{"sessionId":"s","sessionToken":"t"}`)

	require.Len(t, *sent, 1)
	assert.Equal(t, "fr", (*sent)[0].lang)
}

func TestResendOtp_SkipsSendWhenUnavailable(t *testing.T) {
	for name, tc := range map[string]struct {
		ready bool
		f     resendFake
	}{
		"backend not ready":  {false, resendFake{}},
		"no code returned":   {true, resendFake{patchBody: `{"sessionToken":"t"}`}},
		"session lookup 404": {true, resendFake{getStatus: http.StatusNotFound}},
		"session lookup bad": {true, resendFake{getBody: `nope`}},
		"session no login":   {true, resendFake{getBody: `{"session":{"factors":{"user":{}}}}`}},
	} {
		t.Run(name, func(t *testing.T) {
			sent := fakeEmail(t, tc.ready, nil)
			code, resp := resend(t, tc.f, resendBody)

			assert.Equal(t, http.StatusOK, code)
			assert.Equal(t, true, resp["success"])
			assert.Empty(t, *sent)
		})
	}
}

func TestResendOtp_ReviewAndE2ELoginsSkipSend(t *testing.T) {
	resetLoginLists(t, "user@example.com", "")
	sent := fakeEmail(t, true, nil)
	code, resp := resend(t, resendFake{}, resendBody)

	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, true, resp["success"])
	assert.Empty(t, *sent)
}

func TestResendOtp_ReviewLoginStashesResentCode(t *testing.T) {
	resetLoginLists(t, "", "user@example.com")
	t.Cleanup(func() { reviewOtpStore = map[string]reviewOtpEntry{} })
	sent := fakeEmail(t, true, nil)
	resend(t, resendFake{}, resendBody)

	assert.Empty(t, *sent)
	reviewOtpMu.Lock()
	defer reviewOtpMu.Unlock()
	assert.Equal(t, "654321", reviewOtpStore["user@example.com"].code)
}

func TestResendOtp_SendErrorsStaySilent(t *testing.T) {
	for name, err := range map[string]error{
		"invalid recipient": scaleway.ErrInvalidRecipient,
		"transient":         errors.New("scaleway 503"),
	} {
		t.Run(name, func(t *testing.T) {
			fakeEmail(t, true, err)
			code, resp := resend(t, resendFake{}, resendBody)

			assert.Equal(t, http.StatusOK, code)
			assert.Equal(t, true, resp["success"])
		})
	}
}

func TestResendOtp_BadJSONFromZitadelIsGenericSuccess(t *testing.T) {
	sent := fakeEmail(t, true, nil)
	code, resp := resend(t, resendFake{patchBody: `<html>`}, resendBody)

	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, true, resp["success"])
	assert.Empty(t, *sent)
}

func TestResendOtp_ZitadelUnreachable(t *testing.T) {
	setupDeadZitadel(t)
	w, c := ginContext("POST", "/auth/session/otp/resend", resendBody)
	ResendOtpHandler(c)

	assert.Equal(t, http.StatusBadGateway, w.Code)
}

func TestResendOtp_MalformedBody(t *testing.T) {
	w, c := ginContext("POST", "/auth/session/otp/resend", `{`)
	ResendOtpHandler(c)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// --- review last-otp endpoint ---

func reviewOtpRequest(query string) (int, map[string]any) {
	w, c := ginContext("GET", "/auth/review/last-otp?"+query, "")
	ReviewLastOtpHandler(c)
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return w.Code, resp
}

func TestReviewLastOtpHandler(t *testing.T) {
	resetLoginLists(t, "", "review@example.com")
	t.Cleanup(func() { reviewOtpStore = map[string]reviewOtpEntry{} })

	t.Run("disabled without a key looks like a missing route", func(t *testing.T) {
		t.Setenv("REVIEW_OTP_KEY", "")
		code, _ := reviewOtpRequest("login=review@example.com&key=")
		assert.Equal(t, http.StatusNotFound, code)
	})

	t.Run("wrong key", func(t *testing.T) {
		t.Setenv("REVIEW_OTP_KEY", "s3cret")
		code, _ := reviewOtpRequest("login=review@example.com&key=nope")
		assert.Equal(t, http.StatusUnauthorized, code)
	})

	t.Run("login not configured for review", func(t *testing.T) {
		t.Setenv("REVIEW_OTP_KEY", "s3cret")
		code, _ := reviewOtpRequest("login=customer@example.com&key=s3cret")
		assert.Equal(t, http.StatusForbidden, code)
	})

	t.Run("no code yet", func(t *testing.T) {
		t.Setenv("REVIEW_OTP_KEY", "s3cret")
		code, resp := reviewOtpRequest("login=review@example.com&key=s3cret")
		assert.Equal(t, http.StatusOK, code)
		assert.Equal(t, "", resp["code"])
	})

	t.Run("fresh code is returned", func(t *testing.T) {
		t.Setenv("REVIEW_OTP_KEY", "s3cret")
		stashReviewOtp("Review@Example.com", "424242")
		code, resp := reviewOtpRequest("login=REVIEW@example.com&key=s3cret")
		assert.Equal(t, http.StatusOK, code)
		assert.Equal(t, "424242", resp["code"])
	})

	t.Run("stale code is not returned", func(t *testing.T) {
		t.Setenv("REVIEW_OTP_KEY", "s3cret")
		reviewOtpMu.Lock()
		reviewOtpStore["review@example.com"] = reviewOtpEntry{code: "111111", storedAt: reviewOtpStore["review@example.com"].storedAt.Add(-2 * reviewOtpTTL)}
		reviewOtpMu.Unlock()
		_, resp := reviewOtpRequest("login=review@example.com&key=s3cret")
		assert.Equal(t, "", resp["code"])
	})
}

func TestStashReviewOtpIgnoresEmptyCode(t *testing.T) {
	resetLoginLists(t, "", "review@example.com")
	t.Cleanup(func() { reviewOtpStore = map[string]reviewOtpEntry{} })
	stashReviewOtp("review@example.com", "")

	reviewOtpMu.Lock()
	defer reviewOtpMu.Unlock()
	assert.Empty(t, reviewOtpStore)
}

func TestIsReviewLoginAndShouldSkipOtpEmail(t *testing.T) {
	resetLoginLists(t, " E2E@example.com , ,other@example.com", "review@example.com")

	assert.True(t, IsReviewLogin("  Review@Example.com "))
	assert.False(t, IsReviewLogin("customer@example.com"))
	assert.True(t, shouldSkipOtpEmail("e2e@example.com"))
	assert.True(t, shouldSkipOtpEmail("OTHER@example.com"))
	assert.False(t, shouldSkipOtpEmail("customer@example.com"))
	assert.False(t, shouldSkipOtpEmail(""))
}

func TestReviewListsDisabledWhenUnset(t *testing.T) {
	resetLoginLists(t, "", "")
	assert.False(t, IsReviewLogin("a@example.com"))
	assert.False(t, shouldSkipOtpEmail("a@example.com"))
}
