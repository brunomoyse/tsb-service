package auth

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	userDomain "tsb-service/internal/modules/user/domain"
)

// TestMain keeps every test off the real network: the deliverability check
// resolves through a fake resolver that knows the domains the tests use.
func TestMain(m *testing.M) {
	mailResolver = fakeResolver{mx: map[string][]*net.MX{
		"example.com": {{Host: "mx.example.com."}},
		"gmail.com":   {{Host: "mx.gmail.com."}},
	}}
	os.Exit(m.Run())
}

// setupDeadZitadel points the client at a server that is already closed, so
// every upstream call fails at the transport level.
func setupDeadZitadel(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	Init(Config{ZitadelIssuer: url, ZitadelClientID: "test-client-id", ServicePAT: "test-pat", AdminPAT: "test-admin-pat"})
	resetIdempotencyGatesForTest()
}

// resetLoginLists re-reads E2E_NO_SEND_LOGINS and REVIEW_OTP_LOGINS, which are
// parsed once per process, and restores the empty state afterwards.
func resetLoginLists(t *testing.T, e2e, review string) {
	t.Helper()
	reset := func() {
		noSendLogins, noSendLoginsOnce = nil, sync.Once{}
		reviewLogins, reviewLoginsOnce = nil, sync.Once{}
	}
	t.Setenv("E2E_NO_SEND_LOGINS", e2e)
	t.Setenv("REVIEW_OTP_LOGINS", review)
	reset()
	t.Cleanup(reset)
}

type sentMail struct {
	user userDomain.User
	lang string
	code string
}

// fakeEmail swaps the email backend for a recorder. A non-nil sendErr is
// returned from every send.
func fakeEmail(t *testing.T, ready bool, sendErr error) *[]sentMail {
	t.Helper()
	var (
		mu   sync.Mutex
		sent []sentMail
	)
	origReady, origSend := emailBackendReady, sendLoginOtpEmail
	t.Cleanup(func() { emailBackendReady, sendLoginOtpEmail = origReady, origSend })
	emailBackendReady = func() bool { return ready }
	sendLoginOtpEmail = func(u userDomain.User, lang, code string) error {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, sentMail{u, lang, code})
		return sendErr
	}
	return &sent
}

// otpFake is a configurable Zitadel for the OTP request flow. Zero values
// give the happy path for an existing user with a real name.
type otpFake struct {
	mu sync.Mutex

	userID       string // "" = the email search finds nobody
	searchStatus int
	createStatus int
	createBody   string
	enrollStatus int
	enrollBody   string
	userGet      int // status of GET /v2/users/{id}; 0 = 200
	given        string
	family       string
	email        string
	sessionCode  int
	sessionBody  string
	sessionDrop  bool // close the connection on session create
	deleteStatus int

	calls         []string
	searchedEmail string
	sessionLogin  string
	deleted       bool
}

func (f *otpFake) called(call string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == call {
			return true
		}
	}
	return false
}

func (f *otpFake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func orStatus(s, def int) int {
	if s == 0 {
		return def
	}
	return s
}

func (f *otpFake) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch {
		case r.URL.Path == "/v2/users" && r.Method == "POST":
			qs, _ := body["queries"].([]any)
			if len(qs) == 1 {
				q := qs[0].(map[string]any)["emailQuery"].(map[string]any)
				f.searchedEmail, _ = q["emailAddress"].(string)
			}
			w.WriteHeader(orStatus(f.searchStatus, 200))
			if f.userID == "" {
				_, _ = w.Write([]byte(`{"result":[]}`))
			} else {
				_, _ = w.Write([]byte(`{"result":[{"userId":"` + f.userID + `"}]}`))
			}
		case r.URL.Path == "/v2/users/human" && r.Method == "POST":
			w.WriteHeader(orStatus(f.createStatus, 201))
			if f.createBody != "" {
				_, _ = w.Write([]byte(f.createBody))
			} else {
				_, _ = w.Write([]byte(`{"userId":"placeholder-user"}`))
			}
		case strings.HasSuffix(r.URL.Path, "/otp_email") && r.Method == "POST":
			w.WriteHeader(orStatus(f.enrollStatus, 200))
			_, _ = w.Write([]byte(f.enrollBody))
		case r.URL.Path == "/v2/sessions" && r.Method == "POST":
			if u, ok := body["checks"].(map[string]any)["user"].(map[string]any); ok {
				f.sessionLogin, _ = u["loginName"].(string)
			}
			if f.sessionDrop {
				conn, _, _ := w.(http.Hijacker).Hijack()
				_ = conn.Close()
				return
			}
			w.WriteHeader(orStatus(f.sessionCode, 201))
			if f.sessionBody != "" {
				_, _ = w.Write([]byte(f.sessionBody))
			} else {
				_, _ = w.Write([]byte(`{"sessionId":"sess-1","sessionToken":"tok-1","challenges":{"otpEmail":"123456"}}`))
			}
		case strings.HasPrefix(r.URL.Path, "/v2/users/") && r.Method == "GET":
			w.WriteHeader(orStatus(f.userGet, 200))
			given, family := f.given, f.family
			if given == "" {
				given, family = "Alice", "Wonderland"
			}
			email := f.email
			if email == "" {
				email = "user@example.com"
			}
			_, _ = w.Write([]byte(`{"user":{"human":{"profile":{"givenName":"` + given + `","familyName":"` + family + `"},"email":{"email":"` + email + `"}}}}`))
		case strings.HasPrefix(r.URL.Path, "/v2/users/") && r.Method == "DELETE":
			f.deleted = true
			w.WriteHeader(orStatus(f.deleteStatus, 200))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}
}

func requestOtp(t *testing.T, f *otpFake, body string) (int, map[string]any) {
	t.Helper()
	setupMockZitadel(t, f.handler(t))
	w, c := ginContext("POST", "/auth/session/otp/request", body)
	RequestOtpHandler(c)
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return w.Code, resp
}
