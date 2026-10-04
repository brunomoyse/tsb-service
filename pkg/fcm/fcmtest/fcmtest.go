// Package fcmtest is a fake FCM v1 send endpoint for tests of code that sends pushes. It records
// every message and answers with a configured status and body, or, with ByToken, according to the
// registration token. It does not import pkg/fcm, so that package's own tests can use it; build a
// client for it with fcm.NewWithEndpoint(ctx, projectID, srv.URL).
package fcmtest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Request is one send received by the fake.
type Request struct {
	Path    string
	Token   string         // message.token
	Payload map[string]any // the whole "message" object
}

// Response is what the fake answers.
type Response struct {
	Status int
	Body   string // JSON
}

// OK is the success body of a send.
const OK = `{"name":"projects/test/messages/1"}`

// Error is an FCM error body with the given gRPC status and FCM error code.
func Error(status, code string) string {
	return `{"error":{"code":0,"status":"` + status + `","message":"x","details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"` + code + `"}]}}`
}

// Server is an httptest server standing in for the FCM send endpoint.
type Server struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []Request
}

// New answers every send with status and body.
func New(t *testing.T, status int, body string) *Server {
	t.Helper()
	return NewFunc(t, func(Request) Response { return Response{Status: status, Body: body} })
}

// ByToken answers by registration token: "dead-" is UNREGISTERED (404), "refused-" a permission
// failure (403), anything else a success.
func ByToken(t *testing.T) *Server {
	t.Helper()
	return NewFunc(t, func(r Request) Response {
		switch {
		case strings.HasPrefix(r.Token, "dead-"):
			return Response{Status: http.StatusNotFound, Body: Error("NOT_FOUND", "UNREGISTERED")}
		case strings.HasPrefix(r.Token, "refused-"):
			return Response{Status: http.StatusForbidden, Body: `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"x"}}`}
		}
		return Response{Status: http.StatusOK, Body: OK}
	})
}

// NewFunc answers every send with respond(request).
func NewFunc(t *testing.T, respond func(Request) Response) *Server {
	t.Helper()
	s := &Server{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Message map[string]any `json:"message"`
		}
		_ = json.Unmarshal(raw, &body)
		token, _ := body.Message["token"].(string)
		req := Request{Path: r.URL.Path, Token: token, Payload: body.Message}
		s.mu.Lock()
		s.reqs = append(s.reqs, req)
		s.mu.Unlock()

		resp := respond(req)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.Status)
		_, _ = io.WriteString(w, resp.Body)
	}))
	t.Cleanup(s.Close)
	return s
}

// Requests lists the sends received so far, oldest first.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.reqs...)
}

// PushesTo lists the sends to a registration token.
func (s *Server) PushesTo(token string) []Request {
	var out []Request
	for _, r := range s.Requests() {
		if r.Token == token {
			out = append(out, r)
		}
	}
	return out
}

// Paths lists the request paths received so far.
func (s *Server) Paths() []string {
	var out []string
	for _, r := range s.Requests() {
		out = append(out, r.Path)
	}
	return out
}
