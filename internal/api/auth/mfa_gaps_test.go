package auth

import (
	"encoding/json"
	"go.uber.org/zap"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dropConn closes the connection without answering, which the HTTP client reports as a transport error.
func dropConn(w http.ResponseWriter) {
	conn, _, _ := w.(http.Hijacker).Hijack()
	_ = conn.Close()
}

// shortBody promises more bytes than it sends, so reading the response fails.
func shortBody(w http.ResponseWriter) {
	w.Header().Set("Content-Length", "100")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"x":`))
}

func TestZitadelRequest_Edges(t *testing.T) {
	t.Run("a body that cannot be marshalled is reported before any request", func(t *testing.T) {
		setupDeadZitadel(t)
		_, _, err := zitadelRequest("POST", "/v2/x", func() {})
		require.ErrorContains(t, err, "marshal body")
	})

	t.Run("an invalid method is a request-construction error", func(t *testing.T) {
		setupDeadZitadel(t)
		_, _, err := zitadelRequest("BAD METHOD", "/v2/x", nil)
		require.ErrorContains(t, err, "create request")
	})

	t.Run("a transport failure is wrapped", func(t *testing.T) {
		setupDeadZitadel(t)
		_, status, err := zitadelRequest("GET", "/v2/x", nil)
		require.ErrorContains(t, err, "request failed")
		assert.Zero(t, status)
	})

	t.Run("an unreadable response body is an error", func(t *testing.T) {
		setupMockZitadel(t, func(w http.ResponseWriter, _ *http.Request) { shortBody(w) })
		_, _, err := zitadelRequest("GET", "/v2/x", nil)
		require.ErrorContains(t, err, "read response")
	})

	t.Run("redirects are never followed, so the PAT cannot leak to another host", func(t *testing.T) {
		var other int
		leak := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { other++ }))
		t.Cleanup(leak.Close)
		setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, leak.URL+"/steal", http.StatusFound)
		})
		_, status, err := zitadelRequest("GET", "/v2/x", nil)
		require.NoError(t, err)
		assert.Equal(t, http.StatusFound, status)
		assert.Zero(t, other, "the redirect target was never contacted")
	})

	t.Run("the service and admin PATs are sent as bearer tokens", func(t *testing.T) {
		var got []string
		setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
			got = append(got, r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{}`))
		})
		_, _, _ = zitadelRequest("GET", "/a", nil)
		_, _, _ = zitadelAdminRequest("GET", "/b", nil)
		assert.Equal(t, []string{"Bearer test-pat", "Bearer test-admin-pat"}, got)
	})

	t.Run("with an internal URL the external host is sent as the Host header", func(t *testing.T) {
		var gotHost string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotHost = r.Host
			_, _ = w.Write([]byte(`{}`))
		}))
		t.Cleanup(srv.Close)
		for issuer, want := range map[string]string{
			"https://auth.example.com": "auth.example.com",
			"http://auth.local:8080":   "auth.local:8080",
			"auth.bare.example":        "auth.bare.example",
		} {
			Init(Config{ZitadelIssuer: issuer, ZitadelInternalURL: srv.URL, ZitadelClientID: "c", ServicePAT: "p"})
			_, _, err := zitadelRequest("GET", "/v2/x", nil)
			require.NoError(t, err)
			assert.Equal(t, want, gotHost, issuer)
		}
	})

	t.Run("the admin PAT falls back to the service PAT", func(t *testing.T) {
		Init(Config{ZitadelIssuer: "https://z", ZitadelClientID: "c", ServicePAT: "only-pat"})
		assert.Equal(t, "only-pat", client.adminPAT)
		assert.True(t, client.allowedClients["c"])
	})

	t.Run("allowed clients, IdP ids and admin clients come from the config", func(t *testing.T) {
		Init(Config{
			ZitadelIssuer: "https://z", ZitadelClientID: "web", NativeClientID: "native", ServicePAT: "p",
			IdPGoogleID: "g-1", IdPAppleID: "a-1", AdminClientIDs: []string{" dash ", "", "  "},
		})
		assert.Equal(t, map[string]bool{"web": true, "native": true}, client.allowedClients)
		assert.Equal(t, map[string]string{"google": "g-1", "apple": "a-1"}, client.idpIDs)
		assert.Equal(t, map[string]bool{"dash": true}, client.adminClients, "blank entries are ignored")
	})
}

func TestMFAStatusHandler(t *testing.T) {
	status := func(t *testing.T, h http.HandlerFunc) (*httptest.ResponseRecorder, *gin.Context) {
		setupMockZitadel(t, h)
		return staffRequest(t, "GET", "/auth/mfa", "", true)
	}

	t.Run("reports an enrolled TOTP factor", func(t *testing.T) {
		w, c := status(t, func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/v2/users/staff-1/authentication_methods", r.URL.Path)
			_, _ = w.Write([]byte(`{"authMethodTypes":["AUTHENTICATION_METHOD_TYPE_OTP_EMAIL","AUTHENTICATION_METHOD_TYPE_TOTP"]}`))
		})
		MFAStatusHandler(c)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.JSONEq(t, `{"totp":true}`, w.Body.String())
	})

	t.Run("reports no factor", func(t *testing.T) {
		w, c := status(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"authMethodTypes":["AUTHENTICATION_METHOD_TYPE_OTP_EMAIL"]}`))
		})
		MFAStatusHandler(c)
		assert.JSONEq(t, `{"totp":false}`, w.Body.String())
	})

	for name, h := range map[string]http.HandlerFunc{
		"zitadel unreachable": func(w http.ResponseWriter, _ *http.Request) { dropConn(w) },
		"zitadel error":       func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
		"garbled answer":      func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`<html>`)) },
	} {
		t.Run("a lookup failure is a 502, never reported as 'no factor': "+name, func(t *testing.T) {
			w, c := status(t, h)
			MFAStatusHandler(c)
			assert.Equal(t, http.StatusBadGateway, w.Code)
			assert.JSONEq(t, `{"error":"authentication service unavailable"}`, w.Body.String())
		})
	}
}

func TestSessionLookupsFailClosedOnTheLoginPath(t *testing.T) {
	adminLogin := func(t *testing.T, h http.HandlerFunc) *httptest.ResponseRecorder {
		setupMockZitadelWithAdminClient(t, h)
		return finalize(t)
	}
	authRequest := `{"authRequest":{"id":"ar-1","clientId":"` + dashboardClient + `"}}`

	t.Run("auth request lookups", func(t *testing.T) {
		for name, h := range map[string]http.HandlerFunc{
			"unreachable":  func(w http.ResponseWriter, _ *http.Request) { dropConn(w) },
			"not found":    func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) },
			"garbled body": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`<html>`)) },
		} {
			w := adminLogin(t, h)
			assert.NotEqual(t, http.StatusOK, w.Code, name)
			assert.NotContains(t, w.Body.String(), "callbackUrl", name+": the login must not be finalized")
		}
	})

	t.Run("session lookups", func(t *testing.T) {
		for name, session := range map[string]http.HandlerFunc{
			"unreachable":  func(w http.ResponseWriter, _ *http.Request) { dropConn(w) },
			"not found":    func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) },
			"garbled body": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`<html>`)) },
			"no user":      func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"session":{"factors":{}}}`)) },
		} {
			finalized := false
			w := adminLogin(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/v2/oidc/auth_requests/ar-1" && r.Method == "GET":
					_, _ = w.Write([]byte(authRequest))
				case r.URL.Path == "/v2/sessions/sess-1":
					session(w, r)
				case r.URL.Path == "/v2/oidc/auth_requests/ar-1" && r.Method == "POST":
					finalized = true
				}
			})
			assert.False(t, finalized, name)
			assert.NotEqual(t, http.StatusOK, w.Code, name)
		}
	})

	t.Run("authentication method lookups", func(t *testing.T) {
		for name, methods := range map[string]http.HandlerFunc{
			"unreachable":  func(w http.ResponseWriter, _ *http.Request) { dropConn(w) },
			"server error": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
			"garbled body": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`<html>`)) },
		} {
			finalized := false
			w := adminLogin(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/v2/oidc/auth_requests/ar-1" && r.Method == "GET":
					_, _ = w.Write([]byte(authRequest))
				case r.URL.Path == "/v2/sessions/sess-1":
					_, _ = w.Write([]byte(`{"session":{"factors":{"user":{"id":"staff-1"}}}}`))
				case strings.HasSuffix(r.URL.Path, "/authentication_methods"):
					methods(w, r)
				case r.Method == "POST":
					finalized = true
				}
			})
			assert.False(t, finalized, name+": an admin login is not finalized when MFA cannot be checked")
			assert.NotEqual(t, http.StatusOK, w.Code, name)
		}
	})
}

func TestVerifyTotpHandler_Failures(t *testing.T) {
	post := func(t *testing.T, body string, h http.HandlerFunc) *httptest.ResponseRecorder {
		setupMockZitadel(t, h)
		w, c := ginContext("POST", "/auth/session/totp/verify", body)
		VerifyTotpHandler(c)
		return w
	}
	never := func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected upstream call %s %s", r.Method, r.URL.Path)
	}

	for name, body := range map[string]string{
		"not json":          `{`,
		"no session id":     `{"sessionToken":"t","code":"1"}`,
		"no session token":  `{"sessionId":"s","code":"1"}`,
		"no code":           `{"sessionId":"s","sessionToken":"t"}`,
		"everything absent": `{}`,
	} {
		w := post(t, body, never)
		assert.Equal(t, http.StatusBadRequest, w.Code, name)
		assert.Contains(t, w.Body.String(), "sessionId, sessionToken and code are required", name)
	}

	t.Run("an unreachable Zitadel is 502", func(t *testing.T) {
		w := post(t, `{"sessionId":"s","sessionToken":"t","code":"123456"}`, func(w http.ResponseWriter, _ *http.Request) { dropConn(w) })
		assert.Equal(t, http.StatusBadGateway, w.Code)
	})

	t.Run("an unreadable success answer is 502 and no token is returned", func(t *testing.T) {
		w := post(t, `{"sessionId":"s","sessionToken":"t","code":"123456"}`, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`<html>`)) })
		assert.Equal(t, http.StatusBadGateway, w.Code)
		assert.NotContains(t, w.Body.String(), "sessionToken")
	})

	t.Run("201 is accepted like 200", func(t *testing.T) {
		w := post(t, `{"sessionId":"s","sessionToken":"t","code":"123456"}`, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"sessionToken":"t2"}`))
		})
		assert.Equal(t, http.StatusOK, w.Code)
		assert.JSONEq(t, `{"sessionId":"s","sessionToken":"t2"}`, w.Body.String())
	})
}

type auditRecorder struct {
	mu      sync.Mutex
	actions []string
}

func (a *auditRecorder) fn(_ *gin.Context, action string, success bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	state := "failed"
	if success {
		state = "ok"
	}
	a.actions = append(a.actions, action+":"+state)
}

func recordAudit(t *testing.T) *auditRecorder {
	t.Helper()
	a := &auditRecorder{}
	SetAuditFunc(a.fn)
	t.Cleanup(func() { SetAuditFunc(nil) })
	return a
}

func TestStartTOTPHandler_Failures(t *testing.T) {
	start := func(t *testing.T, h http.HandlerFunc) *httptest.ResponseRecorder {
		setupMockZitadel(t, h)
		w, c := staffRequest(t, "POST", "/auth/mfa/totp", `{}`, true)
		StartTOTPHandler(c)
		return w
	}
	assert.Equal(t, http.StatusBadGateway, start(t, func(w http.ResponseWriter, _ *http.Request) { dropConn(w) }).Code)

	w := start(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPreconditionFailed)
		_, _ = w.Write([]byte(`{"message":"already enrolled"}`))
	})
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.JSONEq(t, `{"error":"totp_register_failed"}`, w.Body.String(), "Zitadel's message is not echoed to the client")

	for name, body := range map[string]string{"garbled": `<html>`, "no uri": `{"secret":"ABC"}`} {
		w := start(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) })
		assert.Equal(t, http.StatusBadGateway, w.Code, name)
		assert.NotContains(t, w.Body.String(), "secret", name)
	}

	w = start(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"uri":"otpauth://x","secret":"S"}`))
	})
	assert.Equal(t, http.StatusOK, w.Code, "201 is accepted like 200")
}

func TestConfirmTOTPHandler_Failures(t *testing.T) {
	confirm := func(t *testing.T, body string, h http.HandlerFunc) *httptest.ResponseRecorder {
		setupMockZitadel(t, h)
		w, c := staffRequest(t, "POST", "/auth/mfa/totp/verify", body, true)
		ConfirmTOTPHandler(c)
		return w
	}
	a := recordAudit(t)

	for name, body := range map[string]string{"not json": `{`, "no code": `{}`, "empty code": `{"code":""}`} {
		w := confirm(t, body, func(w http.ResponseWriter, r *http.Request) { t.Errorf("unexpected call %s", r.URL.Path) })
		assert.Equal(t, http.StatusBadRequest, w.Code, name)
		assert.Contains(t, w.Body.String(), "code is required", name)
	}
	assert.Empty(t, a.actions, "a malformed request is not an audited attempt")

	w := confirm(t, `{"code":"123456"}`, func(w http.ResponseWriter, _ *http.Request) { dropConn(w) })
	assert.Equal(t, http.StatusBadGateway, w.Code)

	w = confirm(t, `{"code":"000000"}`, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"Errors.User.MFA.OTP.InvalidCode"}`))
	})
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code, "422, not 401: the dashboard treats 401 as an expired session")
	assert.Contains(t, w.Body.String(), ErrInvalidCode)
	assert.Equal(t, []string{"mfa.totp.enable:failed"}, a.actions, "a refused enrollment is audited as a failure")
}

func TestRemoveTOTPHandler_Failures(t *testing.T) {
	const enrolled = `{"authMethodTypes":["AUTHENTICATION_METHOD_TYPE_TOTP"]}`
	remove := func(t *testing.T, body string, h http.HandlerFunc) *httptest.ResponseRecorder {
		setupMockZitadel(t, h)
		w, c := staffRequest(t, "POST", "/auth/mfa/totp/remove", body, true)
		RemoveTOTPHandler(c)
		return w
	}
	route := func(methods, session, del http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/authentication_methods"):
				methods(w, r)
			case r.URL.Path == "/v2/sessions" && r.Method == "POST":
				session(w, r)
			case r.Method == "DELETE" && strings.HasSuffix(r.URL.Path, "/totp"):
				del(w, r)
			default:
				_, _ = w.Write([]byte(`{}`))
			}
		}
	}
	ok := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }
	}
	validSession := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"sessionId":"check-sess","sessionToken":"check-tok"}`))
	}
	a := recordAudit(t)

	t.Run("a malformed request", func(t *testing.T) {
		for _, body := range []string{`{`, `{}`} {
			w := remove(t, body, func(w http.ResponseWriter, r *http.Request) { t.Errorf("unexpected call %s", r.URL.Path) })
			assert.Equal(t, http.StatusBadRequest, w.Code)
		}
	})

	t.Run("an unreadable factor lookup is 502 and nothing is removed", func(t *testing.T) {
		removed := false
		w := remove(t, `{"code":"123456"}`, route(func(w http.ResponseWriter, _ *http.Request) { dropConn(w) }, validSession,
			func(http.ResponseWriter, *http.Request) { removed = true }))
		assert.Equal(t, http.StatusBadGateway, w.Code)
		assert.False(t, removed)
	})

	t.Run("a user without a factor gets 409", func(t *testing.T) {
		w := remove(t, `{"code":"123456"}`, route(ok(`{"authMethodTypes":["AUTHENTICATION_METHOD_TYPE_OTP_EMAIL"]}`), validSession, nil))
		assert.Equal(t, http.StatusConflict, w.Code)
		assert.Contains(t, w.Body.String(), ErrMFANotEnrolled)
	})

	t.Run("an unreachable code check is 502 and nothing is removed", func(t *testing.T) {
		removed := false
		w := remove(t, `{"code":"123456"}`, route(ok(enrolled), func(w http.ResponseWriter, _ *http.Request) { dropConn(w) },
			func(http.ResponseWriter, *http.Request) { removed = true }))
		assert.Equal(t, http.StatusBadGateway, w.Code)
		assert.False(t, removed)
	})

	t.Run("a wrong code is audited as a failed disable and keeps the factor", func(t *testing.T) {
		removed := false
		w := remove(t, `{"code":"000000"}`, route(ok(enrolled), func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadRequest) },
			func(http.ResponseWriter, *http.Request) { removed = true }))
		assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
		assert.False(t, removed)
		assert.Contains(t, a.actions, "mfa.totp.disable:failed")
	})

	t.Run("an unreachable removal is 502 and is not audited as done", func(t *testing.T) {
		before := len(a.actions)
		w := remove(t, `{"code":"123456"}`, route(ok(enrolled), validSession, func(w http.ResponseWriter, _ *http.Request) { dropConn(w) }))
		assert.Equal(t, http.StatusBadGateway, w.Code)
		assert.Len(t, a.actions, before)
	})

	t.Run("a refused removal is 502 and is not audited as done", func(t *testing.T) {
		before := len(a.actions)
		w := remove(t, `{"code":"123456"}`, route(ok(enrolled), validSession, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"boom"}`))
		}))
		assert.Equal(t, http.StatusBadGateway, w.Code)
		assert.NotContains(t, w.Body.String(), "boom")
		assert.Len(t, a.actions, before)
	})

	t.Run("success removes the factor even when the check session cannot be deleted", func(t *testing.T) {
		removed := false
		w := remove(t, `{"code":"123456"}`, route(ok(enrolled), validSession, func(w http.ResponseWriter, _ *http.Request) {
			removed = true
			_, _ = w.Write([]byte(`{}`))
		}))
		assert.Equal(t, http.StatusOK, w.Code)
		assert.JSONEq(t, `{"totp":false}`, w.Body.String())
		assert.True(t, removed)
		assert.Contains(t, a.actions, "mfa.totp.disable:ok")
	})
}

func TestCheckTOTPCode(t *testing.T) {
	t.Run("a delete failure of the throwaway session does not invalidate a good code", func(t *testing.T) {
		deletes := 0
		setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "DELETE" {
				deletes++
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"sessionId":"s1","sessionToken":"t1"}`))
		})
		valid, err := checkTOTPCode("u1", "123456")
		require.NoError(t, err)
		assert.True(t, valid)
		assert.Equal(t, 1, deletes)
	})

	t.Run("a session answer without an id is still a valid code", func(t *testing.T) {
		setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "POST", r.Method, "nothing to delete")
			_, _ = w.Write([]byte(`{}`))
		})
		valid, err := checkTOTPCode("u1", "123456")
		require.NoError(t, err)
		assert.True(t, valid)
	})

	t.Run("the check carries the user and the code", func(t *testing.T) {
		var got map[string]any
		setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &got)
			w.WriteHeader(http.StatusBadRequest)
		})
		valid, err := checkTOTPCode("u1", "654321")
		require.NoError(t, err)
		assert.False(t, valid)
		checks := got["checks"].(map[string]any)
		assert.Equal(t, "u1", checks["user"].(map[string]any)["userId"])
		assert.Equal(t, "654321", checks["totp"].(map[string]any)["code"])
	})

	t.Run("an unreachable Zitadel is an error, not an invalid code", func(t *testing.T) {
		setupDeadZitadel(t)
		valid, err := checkTOTPCode("u1", "123456")
		require.Error(t, err)
		assert.False(t, valid)
	})
}

func TestAuthorizeProxyHandler_Edges(t *testing.T) {
	t.Run("an unparsable redirect target is a 502 (the HTTP client refuses it)", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Location", "http://[::1")
			w.WriteHeader(http.StatusFound)
		}))
		t.Cleanup(srv.Close)
		Init(Config{ZitadelIssuer: srv.URL, ZitadelClientID: "c", ServicePAT: "p"})
		w := postAuthorizeProxy(t, srv.URL+authorizePath+"?client_id=c")
		assert.Equal(t, http.StatusBadGateway, w.Code)
		assert.Contains(t, w.Body.String(), "failed to reach authorization server")
	})

	t.Run("an unparsable configured issuer rejects every URL", func(t *testing.T) {
		Init(Config{ZitadelIssuer: "http://[::1", ZitadelClientID: "c", ServicePAT: "p"})
		w := postAuthorizeProxy(t, "http://127.0.0.1:1"+authorizePath)
		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Contains(t, w.Body.String(), "invalid_authorize_url")
	})
}

func TestIdempotencyGate_DropsExpiredEntriesOnAcquire(t *testing.T) {
	g := newIdempotencyGate[string](time.Hour)
	stale := g.acquire("stale")
	g.cache(stale, "fp", "response")
	stale.expires = time.Now().Add(-time.Minute)
	g.release(stale)

	fresh := g.acquire("fresh")
	g.cache(fresh, "fp", "response")
	g.release(fresh)

	held := g.acquire("held") // stays locked: the sweep must leave it alone
	held.response, held.expires = new(string), time.Now().Add(-time.Minute)

	other := g.acquire("other")
	g.release(other)

	g.mu.Lock()
	_, hasStale := g.entries["stale"]
	_, hasFresh := g.entries["fresh"]
	_, hasHeld := g.entries["held"]
	g.mu.Unlock()
	assert.False(t, hasStale, "an expired cached response is forgotten")
	assert.True(t, hasFresh, "a live one is kept")
	assert.True(t, hasHeld, "an entry currently in use is never swept from under its holder")
	g.release(held)

	again := g.acquire("fresh")
	got, ok := again.hit("fp")
	g.release(again)
	assert.True(t, ok)
	assert.Equal(t, "response", got)
	_, ok = (&idempotencyEntry[string]{}).hit("fp")
	assert.False(t, ok, "an empty entry has no cached response")
}

func TestMFAManagementHandlersRejectNonAdmins(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("non-admin must not reach Zitadel: %s %s", r.Method, r.URL.Path)
	})
	a := recordAudit(t)

	w, c := staffRequest(t, "POST", "/auth/mfa/totp/verify", `{"code":"123456"}`, false)
	ConfirmTOTPHandler(c)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), ErrForbidden)

	w, c = staffRequest(t, "POST", "/auth/mfa/totp/remove", `{"code":"123456"}`, false)
	RemoveTOTPHandler(c)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Empty(t, a.actions)

	// An admin without a Zitadel sub (e.g. a token that never went through JIT) is refused too.
	w, c = ginContext("GET", "/auth/mfa", "")
	MFAStatusHandler(c)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestResolveOrCreateZitadelUser_UpstreamFailures(t *testing.T) {
	fastProjectionPolling(t)
	userProjectionPollAttempts = 2
	log := zap.NewNop()
	const intent = `{"idpInformation":{"idpId":"idp-1","userId":"g-1","userName":"New@Example.com"},"addHumanUser":{"profile":{"givenName":"A","familyName":"B"}}}`
	intentReply := func(w http.ResponseWriter) { _, _ = w.Write([]byte(intent)) }

	t.Run("an unreachable Zitadel while reading the intent", func(t *testing.T) {
		setupDeadZitadel(t)
		id, err := resolveOrCreateZitadelUser(log, "i1", "tok")
		require.ErrorContains(t, err, "retrieve idp intent")
		assert.Empty(t, id)
	})

	t.Run("a connection lost while linking an existing user", func(t *testing.T) {
		setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasPrefix(r.URL.Path, "/v2/idp_intents/"):
				intentReply(w)
			case r.URL.Path == "/v2/users" && r.Method == "POST":
				_, _ = w.Write([]byte(`{"result":[{"userId":"existing-1"}]}`))
			case strings.HasSuffix(r.URL.Path, "/links"):
				dropConn(w)
			}
		})
		id, err := resolveOrCreateZitadelUser(log, "i1", "tok")
		require.ErrorContains(t, err, "link idp to user")
		assert.Empty(t, id, "no user is returned when the link could not be made")
	})

	t.Run("a connection lost while creating the user", func(t *testing.T) {
		setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasPrefix(r.URL.Path, "/v2/idp_intents/"):
				intentReply(w)
			case r.URL.Path == "/v2/users" && r.Method == "POST":
				_, _ = w.Write([]byte(`{"result":[]}`))
			case r.URL.Path == "/v2/users/human":
				dropConn(w)
			}
		})
		_, err := resolveOrCreateZitadelUser(log, "i1", "tok")
		require.ErrorContains(t, err, "create zitadel user")
	})

	t.Run("a user that never becomes visible does not block the login", func(t *testing.T) {
		probes := 0
		setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasPrefix(r.URL.Path, "/v2/idp_intents/"):
				intentReply(w)
			case r.URL.Path == "/v2/users" && r.Method == "POST":
				_, _ = w.Write([]byte(`{"result":[]}`))
			case r.URL.Path == "/v2/users/human":
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"userId":"new-1"}`))
			case r.URL.Path == "/v2/users/new-1" && r.Method == "GET":
				probes++
				w.WriteHeader(http.StatusNotFound)
			}
		})
		id, err := resolveOrCreateZitadelUser(log, "i1", "tok")
		require.NoError(t, err)
		assert.Equal(t, "new-1", id)
		assert.Equal(t, 2, probes, "it polled the configured number of times, then went on")
	})
}
