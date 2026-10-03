package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// --- CompleteOtpProfileHandler: remaining branches ---

func TestCompleteOtpProfile_RejectsOverlongNames(t *testing.T) {
	setupMockZitadel(t, func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("must be rejected before Zitadel: %s %s", r.Method, r.URL.Path)
	})
	long := strings.Repeat("a", maxProfileNameLen+1)
	for name, body := range map[string]string{
		"first": `{"sessionId":"sess-1","sessionToken":"tok-1","firstName":"` + long + `","lastName":"B"}`,
		"last":  `{"sessionId":"sess-1","sessionToken":"tok-1","firstName":"A","lastName":"` + long + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			w, c := ginContext("POST", "/x", body)
			CompleteOtpProfileHandler(c)
			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	}
}

func TestCompleteOtpProfile_AcceptsMaxLengthNames(t *testing.T) {
	puts := 0
	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/v2/sessions/sess-1":
			_, _ = w.Write([]byte(`{"session":{"factors":` + otpVerifiedFactors + `}}`))
		case r.Method == "GET":
			_, _ = w.Write([]byte(`{"user":{"human":{"profile":{"givenName":"-","familyName":"-"}}}}`))
		case r.Method == "PUT":
			puts++
			_, _ = w.Write([]byte(`{}`))
		}
	})
	name := strings.Repeat("a", maxProfileNameLen)
	w, c := ginContext("POST", "/x", `{"sessionId":"sess-1","sessionToken":"tok-1","firstName":"`+name+`","lastName":"`+name+`"}`)
	CompleteOtpProfileHandler(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, 1, puts)
}

func TestCompleteOtpProfile_UpstreamFailures(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"user lookup fails": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v2/sessions/sess-1" {
				_, _ = w.Write([]byte(`{"session":{"factors":` + otpVerifiedFactors + `}}`))
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
		},
		"profile update fails": func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/v2/sessions/sess-1":
				_, _ = w.Write([]byte(`{"session":{"factors":` + otpVerifiedFactors + `}}`))
			case r.Method == "GET":
				_, _ = w.Write([]byte(`{"user":{"human":{"profile":{"givenName":"-","familyName":"-"}}}}`))
			default:
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"message":"boom"}`))
			}
		},
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			setupMockZitadel(t, h)
			w, c := ginContext("POST", "/x", completeProfileBody)
			CompleteOtpProfileHandler(c)
			assert.Equal(t, http.StatusBadGateway, w.Code)
		})
	}
}

func TestCompleteOtpProfile_SessionWithoutUserIsInvalid(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"session":{"factors":{"otpEmail":{"verifiedAt":"2026-10-03T10:00:00Z"}}}}`))
	})
	w, c := ginContext("POST", "/x", completeProfileBody)
	CompleteOtpProfileHandler(c)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestCompleteOtpProfile_WhitespaceOnlyNames(t *testing.T) {
	puts := 0
	setupMockZitadel(t, profileMock(t, otpVerifiedFactors, placeholderProfileMarker, placeholderProfileMarker, &puts))
	w, c := ginContext("POST", "/x", `{"sessionId":"sess-1","sessionToken":"tok-1","firstName":"   ","lastName":"  "}`)
	CompleteOtpProfileHandler(c)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Zero(t, puts)
}

// --- FinalizeOIDCHandler: remaining branches ---

func TestFinalize_UpstreamStatusIsPassedThrough(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":5,"message":"auth request expired"}`))
	})
	w, c := ginContext("POST", "/auth/finalize", `{"authRequestId":"ar","sessionId":"s","sessionToken":"t"}`)
	FinalizeOIDCHandler(c)

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), "expired")
}

func TestFinalize_Unreachable(t *testing.T) {
	setupDeadZitadel(t)
	w, c := ginContext("POST", "/auth/finalize", `{"authRequestId":"ar","sessionId":"s","sessionToken":"t"}`)
	FinalizeOIDCHandler(c)
	assert.Equal(t, http.StatusBadGateway, w.Code)
}

func TestFinalize_BadJSONFromZitadel(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`<html>`)) })
	w, c := ginContext("POST", "/auth/finalize", `{"authRequestId":"ar","sessionId":"s","sessionToken":"t"}`)
	FinalizeOIDCHandler(c)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestFinalize_BodyAndForwardedPayload(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v2/oidc/auth_requests/ar%2F1", r.URL.EscapedPath(), "authRequestId must be path-escaped")
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		sess := body["session"].(map[string]any)
		assert.Equal(t, "s-9", sess["sessionId"])
		assert.Equal(t, "t-9", sess["sessionToken"])
		_, _ = w.Write([]byte(`{"callbackUrl":"be.tokyosushibarliege.app:/cb?code=c&state=s"}`))
	})
	w, c := ginContext("POST", "/auth/finalize", `{"authRequestId":"ar/1","sessionId":"s-9","sessionToken":"t-9"}`)
	FinalizeOIDCHandler(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "be.tokyosushibarliege.app:/cb?code=c")
}

func TestFinalize_DuplicateSubmitHitsZitadelOnce(t *testing.T) {
	var hits atomic.Int32
	setupMockZitadel(t, func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) > 1 {
			w.WriteHeader(http.StatusBadRequest) // one-shot auth request already consumed
			return
		}
		_, _ = w.Write([]byte(`{"callbackUrl":"https://app/cb?code=1"}`))
	})
	body := `{"authRequestId":"ar-dup","sessionId":"s","sessionToken":"t"}`
	for range 2 {
		w, c := ginContext("POST", "/auth/finalize", body)
		FinalizeOIDCHandler(c)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), "code=1")
	}
	assert.Equal(t, int32(1), hits.Load())
}

func TestFinalize_FailureIsNotCached(t *testing.T) {
	var hits atomic.Int32
	setupMockZitadel(t, func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"callbackUrl":"https://app/cb?code=2"}`))
	})
	body := `{"authRequestId":"ar-retry","sessionId":"s","sessionToken":"t"}`
	w, c := ginContext("POST", "/auth/finalize", body)
	FinalizeOIDCHandler(c)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)

	w, c = ginContext("POST", "/auth/finalize", body)
	FinalizeOIDCHandler(c)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestFinalize_MalformedBody(t *testing.T) {
	w, c := ginContext("POST", "/auth/finalize", `{`)
	FinalizeOIDCHandler(c)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestFinalize_StaffAuthRequestLookupFailureFailsClosed(t *testing.T) {
	setupMockZitadelWithAdminClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/oidc/auth_requests/ar-1" && r.Method == "GET" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		t.Errorf("finalize must not be attempted: %s %s", r.Method, r.URL.Path)
	})
	w, c := ginContext("POST", "/auth/finalize", `{"authRequestId":"ar-1","sessionId":"sess-1","sessionToken":"t"}`)
	FinalizeOIDCHandler(c)
	assert.Equal(t, http.StatusBadGateway, w.Code)
}

func TestFinalize_StaffSessionLookupFailureFailsClosed(t *testing.T) {
	setupMockZitadelWithAdminClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/oidc/auth_requests/ar-1" && r.Method == "GET":
			_, _ = w.Write([]byte(`{"authRequest":{"id":"ar-1","clientId":"` + dashboardClient + `"}}`))
		case r.URL.Path == "/v2/sessions/sess-1":
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("finalize must not be attempted: %s %s", r.Method, r.URL.Path)
		}
	})
	w, c := ginContext("POST", "/auth/finalize", `{"authRequestId":"ar-1","sessionId":"sess-1","sessionToken":"t"}`)
	FinalizeOIDCHandler(c)
	assert.Equal(t, http.StatusBadGateway, w.Code)
}

// --- TokenExchangeHandler: remaining branches ---

func TestTokenExchange_ForwardsFormFields(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(b))
		assert.Equal(t, "authorization_code", form.Get("grant_type"))
		assert.Equal(t, "the-code", form.Get("code"))
		assert.Equal(t, "https://app/cb", form.Get("redirect_uri"))
		assert.Equal(t, "ver", form.Get("code_verifier"))
		assert.Equal(t, "test-client-id", form.Get("client_id"))
		_, _ = w.Write([]byte(`{"access_token":"at"}`))
	})
	w, c := ginContext("POST", "/x", `{"code":"the-code","redirectUri":"https://app/cb","clientId":"test-client-id","codeVerifier":"ver"}`)
	TokenExchangeHandler(c)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestTokenExchange_RefreshGrantFields(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(b))
		assert.Equal(t, "refresh_token", form.Get("grant_type"))
		assert.Equal(t, "rt", form.Get("refresh_token"))
		assert.Empty(t, form.Get("code"))
		_, _ = w.Write([]byte(`{"access_token":"at"}`))
	})
	w, c := ginContext("POST", "/x", `{"refreshToken":"rt","clientId":"test-client-id"}`)
	TokenExchangeHandler(c)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestTokenExchange_NativeClientAllowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"at"}`))
	}))
	t.Cleanup(srv.Close)
	Init(Config{ZitadelIssuer: srv.URL, ZitadelClientID: "web", NativeClientID: "native", ServicePAT: "p"})

	w, c := ginContext("POST", "/x", `{"code":"c","clientId":"native"}`)
	TokenExchangeHandler(c)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestTokenExchange_UpstreamErrorPassedThrough(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	})
	w, c := ginContext("POST", "/x", `{"code":"stale","clientId":"test-client-id"}`)
	TokenExchangeHandler(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.JSONEq(t, `{"error":"invalid_grant"}`, w.Body.String())
}

func TestTokenExchange_Unreachable(t *testing.T) {
	setupDeadZitadel(t)
	w, c := ginContext("POST", "/x", `{"code":"c","clientId":"test-client-id"}`)
	TokenExchangeHandler(c)
	assert.Equal(t, http.StatusBadGateway, w.Code)
}

func TestTokenExchange_MalformedBody(t *testing.T) {
	setupMockZitadel(t, nil)
	w, c := ginContext("POST", "/x", `{`)
	TokenExchangeHandler(c)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// --- AuthorizeProxyHandler: remaining branches ---

func TestAuthorizeProxy_Failures(t *testing.T) {
	t.Run("missing authorizeUrl", func(t *testing.T) {
		setupMockZitadel(t, nil)
		w, c := ginContext("POST", "/x", `{}`)
		AuthorizeProxyHandler(c)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("no redirect from zitadel", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
		t.Cleanup(srv.Close)
		Init(Config{ZitadelIssuer: srv.URL, ZitadelClientID: "c", ServicePAT: "p"})
		w := postAuthorizeProxy(t, srv.URL+authorizePath+"?client_id=c")
		assert.Equal(t, http.StatusBadGateway, w.Code)
	})

	t.Run("zitadel unreachable", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		base := srv.URL
		srv.Close()
		Init(Config{ZitadelIssuer: base, ZitadelClientID: "c", ServicePAT: "p"})
		w := postAuthorizeProxy(t, base+authorizePath+"?client_id=c")
		assert.Equal(t, http.StatusBadGateway, w.Code)
	})

	t.Run("legacy authRequest param and absolute location", func(t *testing.T) {
		var base string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Location", base+"/ui/login?authRequestID=V2_777")
			w.WriteHeader(http.StatusFound)
		}))
		t.Cleanup(srv.Close)
		base = srv.URL
		Init(Config{ZitadelIssuer: base, ZitadelClientID: "c", ServicePAT: "p"})
		w := postAuthorizeProxy(t, base+authorizePath+"?client_id=c")
		require.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), "V2_777")
	})
}

// --- IdP start ---

func TestStartIdP_RemainingBranches(t *testing.T) {
	body := `{"provider":"google","successUrl":"https://a/s","failureUrl":"https://a/f"}`

	t.Run("apple not configured", func(t *testing.T) {
		setupMockZitadelWithIdP(t, nil)
		w, c := ginContext("POST", "/x", `{"provider":"apple","successUrl":"https://a/s","failureUrl":"https://a/f"}`)
		StartIdPIntentHandler(c)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
	t.Run("apple configured", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			assert.Equal(t, "apple-idp", b["idpId"])
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"authUrl":"https://appleid.apple.com/auth"}`))
		}))
		t.Cleanup(srv.Close)
		Init(Config{ZitadelIssuer: srv.URL, ZitadelClientID: "c", ServicePAT: "p", IdPAppleID: "apple-idp"})
		w, c := ginContext("POST", "/x", `{"provider":"apple","successUrl":"https://a/s","failureUrl":"https://a/f"}`)
		StartIdPIntentHandler(c)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), "apple")
	})
	t.Run("unreachable", func(t *testing.T) {
		setupDeadZitadel(t)
		client.idpIDs["google"] = "g"
		w, c := ginContext("POST", "/x", body)
		StartIdPIntentHandler(c)
		assert.Equal(t, http.StatusBadGateway, w.Code)
	})
	t.Run("bad json", func(t *testing.T) {
		setupMockZitadelWithIdP(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`<`)) })
		w, c := ginContext("POST", "/x", body)
		StartIdPIntentHandler(c)
		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})
	t.Run("malformed body", func(t *testing.T) {
		setupMockZitadelWithIdP(t, nil)
		w, c := ginContext("POST", "/x", `{`)
		StartIdPIntentHandler(c)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

// --- IdP session: provisioning branches ---

const idpIntentExisting = `{
	"addHumanUser":{"profile":{"givenName":"Ann","familyName":"Lee"},"email":{"email":"ann@gmail.com"}},
	"idpInformation":{"idpId":"test-google-idp","userId":"g-1","userName":"Ann@Gmail.com"}
}`

type idpFake struct {
	intentStatus int
	intentBody   string
	userID       string // email search result
	linkStatus   int
	linkBody     string
	createStatus int
	createBody   string
	sessionCode  int
	sessionBody  string
	userGet      int

	linked   bool
	created  bool
	searched string
}

func (f *idpFake) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v2/idp_intents/") && r.Method == "POST":
			w.WriteHeader(orStatus(f.intentStatus, 200))
			if f.intentBody == "" {
				_, _ = w.Write([]byte(idpIntentExisting))
			} else {
				_, _ = w.Write([]byte(f.intentBody))
			}
		case r.URL.Path == "/v2/users" && r.Method == "POST":
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			q := b["queries"].([]any)[0].(map[string]any)["emailQuery"].(map[string]any)
			f.searched, _ = q["emailAddress"].(string)
			if f.userID == "" {
				_, _ = w.Write([]byte(`{"result":[]}`))
			} else {
				_, _ = w.Write([]byte(`{"result":[{"userId":"` + f.userID + `"}]}`))
			}
		case strings.HasSuffix(r.URL.Path, "/links") && r.Method == "POST":
			f.linked = true
			assert.Equal(t, "Bearer test-admin-pat", r.Header.Get("Authorization"), "linking needs the admin PAT")
			w.WriteHeader(orStatus(f.linkStatus, 200))
			_, _ = w.Write([]byte(f.linkBody))
		case r.URL.Path == "/v2/users/human" && r.Method == "POST":
			f.created = true
			w.WriteHeader(orStatus(f.createStatus, 201))
			if f.createBody == "" {
				_, _ = w.Write([]byte(`{"userId":"new-user"}`))
			} else {
				_, _ = w.Write([]byte(f.createBody))
			}
		case r.URL.Path == "/v2/sessions" && r.Method == "POST":
			w.WriteHeader(orStatus(f.sessionCode, 201))
			if f.sessionBody == "" {
				_, _ = w.Write([]byte(`{"sessionId":"sess-idp","sessionToken":"tok-idp"}`))
			} else {
				_, _ = w.Write([]byte(f.sessionBody))
			}
		case strings.HasPrefix(r.URL.Path, "/v2/users/") && r.Method == "GET":
			w.WriteHeader(orStatus(f.userGet, 200))
			_, _ = w.Write([]byte(`{"user":{"human":{"profile":{"givenName":"Ann","familyName":"Lee"}}}}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}
}

func idpSession(t *testing.T, f *idpFake, body string) (int, map[string]any) {
	t.Helper()
	fastProjectionPolling(t)
	setupMockZitadelWithIdP(t, f.handler(t))
	w, c := ginContext("POST", "/auth/idp/session", body)
	CreateIdPSessionHandler(c)
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return w.Code, resp
}

const idpBody = `{"idpIntentId":"i-1","idpIntentToken":"t-1"}`

func TestIdPSession_ExistingEmailAccountGetsLinked(t *testing.T) {
	for name, f := range map[string]*idpFake{
		"fresh link":                  {userID: "u-1"},
		"already linked 409":          {userID: "u-1", linkStatus: http.StatusConflict},
		"already linked other status": {userID: "u-1", linkStatus: http.StatusBadRequest, linkBody: `{"message":"IDP link already exists"}`},
	} {
		t.Run(name, func(t *testing.T) {
			code, resp := idpSession(t, f, idpBody)

			assert.Equal(t, http.StatusOK, code)
			assert.Equal(t, "sess-idp", resp["sessionId"])
			assert.True(t, f.linked)
			assert.False(t, f.created, "must not create a duplicate account")
			assert.Equal(t, "ann@gmail.com", f.searched, "IdP email is lowercased before the search")
		})
	}
}

func TestIdPSession_LinkFailureBlocksLogin(t *testing.T) {
	f := &idpFake{userID: "u-1", linkStatus: http.StatusInternalServerError, linkBody: `{"message":"boom"}`}
	code, resp := idpSession(t, f, idpBody)

	assert.Equal(t, http.StatusBadGateway, code)
	assert.NotEmpty(t, resp["error"])
}

func TestIdPSession_IntentLookupFailures(t *testing.T) {
	for name, f := range map[string]*idpFake{
		"status":   {intentStatus: http.StatusBadRequest, intentBody: `{"message":"bad token"}`},
		"bad json": {intentBody: `<`},
	} {
		t.Run(name, func(t *testing.T) {
			code, _ := idpSession(t, f, idpBody)
			assert.Equal(t, http.StatusBadGateway, code)
		})
	}
}

func TestIdPSession_CreateUserFailures(t *testing.T) {
	for name, f := range map[string]*idpFake{
		"zitadel rejects": {createStatus: http.StatusBadRequest, createBody: `{"message":"invalid"}`},
		"bad json":        {createBody: `<`},
	} {
		t.Run(name, func(t *testing.T) {
			code, _ := idpSession(t, f, idpBody)
			assert.Equal(t, http.StatusBadGateway, code)
		})
	}
}

func TestIdPSession_NewUserWithoutEmailIsCreated(t *testing.T) {
	f := &idpFake{intentBody: `{"addHumanUser":{"profile":{"givenName":"A","familyName":"B"}},"idpInformation":{"idpId":"x","userId":"1","userName":""}}`}
	code, resp := idpSession(t, f, idpBody)

	assert.Equal(t, http.StatusOK, code)
	assert.True(t, f.created)
	assert.Empty(t, f.searched, "no email search without an IdP email")
	assert.Equal(t, "sess-idp", resp["sessionId"])
}

func TestIdPSession_SessionFailures(t *testing.T) {
	t.Run("upstream status passes through", func(t *testing.T) {
		f := &idpFake{userID: "u-1", sessionCode: http.StatusNotFound, sessionBody: `{"message":"User could not be found"}`}
		code, _ := idpSession(t, f, idpBody)
		assert.Equal(t, http.StatusNotFound, code)
	})
	t.Run("bad json", func(t *testing.T) {
		f := &idpFake{userID: "u-1", sessionBody: `<`}
		code, _ := idpSession(t, f, idpBody)
		assert.Equal(t, http.StatusInternalServerError, code)
	})
	t.Run("unreachable", func(t *testing.T) {
		setupDeadZitadel(t)
		w, c := ginContext("POST", "/x", `{"idpIntentId":"i","idpIntentToken":"t","userId":"u-1"}`)
		CreateIdPSessionHandler(c)
		assert.Equal(t, http.StatusBadGateway, w.Code)
	})
}

func TestIdPSession_ProfileProbeFailureDoesNotBlockLogin(t *testing.T) {
	f := &idpFake{userGet: http.StatusInternalServerError}
	code, resp := idpSession(t, f, `{"idpIntentId":"i","idpIntentToken":"t","userId":"u-1"}`)

	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, false, resp["requiresProfile"])
}

func TestEnsureProfileName(t *testing.T) {
	log := zap.NewNop()
	profileOf := func(raw json.RawMessage) map[string]any {
		var m map[string]any
		require.NoError(t, json.Unmarshal(raw, &m))
		return m["profile"].(map[string]any)
	}

	t.Run("complete name untouched", func(t *testing.T) {
		in := json.RawMessage(`{"profile":{"givenName":"A","familyName":"B"}}`)
		assert.JSONEq(t, string(in), string(ensureProfileName(log, in)))
	})
	t.Run("missing profile backfilled", func(t *testing.T) {
		p := profileOf(ensureProfileName(log, json.RawMessage(`{"email":{"email":"a@b.c"}}`)))
		assert.Equal(t, "-", p["givenName"])
		assert.Equal(t, "-", p["familyName"])
	})
	t.Run("blank family backfilled, given kept", func(t *testing.T) {
		p := profileOf(ensureProfileName(log, json.RawMessage(`{"profile":{"givenName":"Ann","familyName":"  "}}`)))
		assert.Equal(t, "Ann", p["givenName"])
		assert.Equal(t, "-", p["familyName"])
	})
	t.Run("unparseable payload returned as-is", func(t *testing.T) {
		in := json.RawMessage(`not json`)
		assert.Equal(t, string(in), string(ensureProfileName(log, in)))
	})
}

// --- helpers.go ---

func TestFindZitadelUserByEmail(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		setupMockZitadel(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"result":[{"userId":"u9"}]}`)) })
		id, err := findZitadelUserByEmail("  A@B.com ")
		require.NoError(t, err)
		assert.Equal(t, "u9", id)
	})
	t.Run("not found", func(t *testing.T) {
		setupMockZitadel(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"result":[]}`)) })
		_, err := findZitadelUserByEmail("a@b.com")
		assert.Error(t, err)
	})
	t.Run("status", func(t *testing.T) {
		setupMockZitadel(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) })
		_, err := findZitadelUserByEmail("a@b.com")
		assert.Error(t, err)
	})
	t.Run("bad json", func(t *testing.T) {
		setupMockZitadel(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`<`)) })
		_, err := findZitadelUserByEmail("a@b.com")
		assert.Error(t, err)
	})
	t.Run("transport", func(t *testing.T) {
		setupDeadZitadel(t)
		_, err := findZitadelUserByEmail("a@b.com")
		assert.Error(t, err)
	})
}

func TestGetZitadelUserInfo(t *testing.T) {
	t.Run("human", func(t *testing.T) {
		setupMockZitadel(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"user":{"human":{"profile":{"givenName":"A","familyName":"B"},"email":{"email":"a@b.com"}}}}`))
		})
		email, g, f, err := GetZitadelUserInfo(context.Background(), "u1")
		require.NoError(t, err)
		assert.Equal(t, []string{"a@b.com", "A", "B"}, []string{email, g, f})
	})
	t.Run("status", func(t *testing.T) {
		setupMockZitadel(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) })
		_, _, _, err := GetZitadelUserInfo(context.Background(), "u1")
		assert.Error(t, err)
	})
	t.Run("transport", func(t *testing.T) {
		setupDeadZitadel(t)
		_, _, _, err := GetZitadelUserInfo(context.Background(), "u1")
		assert.Error(t, err)
	})
}

func TestDeleteZitadelUser(t *testing.T) {
	for status, wantErr := range map[int]bool{200: false, 404: false, 500: true, 403: true} {
		setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "Bearer test-admin-pat", r.Header.Get("Authorization"))
			w.WriteHeader(status)
		})
		err := DeleteZitadelUser(context.Background(), "u1")
		assert.Equal(t, wantErr, err != nil, "status %d", status)
	}
	setupDeadZitadel(t)
	assert.Error(t, DeleteZitadelUser(context.Background(), "u1"))
}

func TestCreatePlaceholderZitadelUser_NormalizesEmail(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-admin-pat", r.Header.Get("Authorization"))
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		assert.Equal(t, "a@b.com", b["userName"])
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"userId":"p1"}`))
	})
	id, err := createPlaceholderZitadelUser(" A@B.com ")
	require.NoError(t, err)
	assert.Equal(t, "p1", id)

	setupDeadZitadel(t)
	_, err = createPlaceholderZitadelUser("a@b.com")
	assert.Error(t, err)
}

func TestEnsureZitadelOtpEmail_Errors(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"message":"down"}`))
	})
	assert.Error(t, ensureZitadelOtpEmail("u1"))
	setupDeadZitadel(t)
	assert.Error(t, ensureZitadelOtpEmail("u1"))
}

func TestFetchUserProfileNamesAndUpdate_Errors(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`<`)) })
	_, _, err := fetchUserProfileNames("u1")
	assert.Error(t, err)
	_, err = userNeedsProfileCompletion("u1")
	assert.Error(t, err)

	setupDeadZitadel(t)
	_, _, err = fetchUserProfileNames("u1")
	assert.Error(t, err)
	assert.Error(t, updateZitadelUserProfile("u1", "A", "B"))
}

func TestContainsAny(t *testing.T) {
	assert.True(t, containsAny("it already exists", "nope", "already"))
	assert.False(t, containsAny("fine", "x", ""))
	assert.False(t, containsAny("fine"))
}

func TestFetchSessionFactors_Errors(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`<`)) })
	_, err := fetchSessionFactors("s")
	assert.Error(t, err)
	setupDeadZitadel(t)
	_, err = fetchSessionFactors("s")
	assert.Error(t, err)
}
