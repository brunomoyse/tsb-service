// Package apnstest is a fake APNs HTTP/2 endpoint for tests of code that sends pushes. It records
// every request and answers with a configured status and reason, or, with ByToken, according to the
// device token. It does not import pkg/apns, so that package's own tests can use it; build a client
// for it with apns.NewWithEndpoint(srv.URL, srv.Client(), bundleID).
package apnstest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Request is one push received by the fake.
type Request struct {
	Path    string
	Token   string // the device token (last path segment)
	Header  http.Header
	Payload map[string]any
}

// Response is what the fake answers.
type Response struct {
	Status int
	Reason string // sent as {"reason": ...} when not empty
	Drop   bool   // kill the connection instead of answering (a transport error)
}

// Server is an httptest TLS server standing in for one APNs environment.
type Server struct {
	*httptest.Server
	mu      sync.Mutex
	reqs    []Request
	respond func(Request) Response
}

// New answers every push with status and reason.
func New(t *testing.T, status int, reason string) *Server {
	t.Helper()
	return NewFunc(t, func(Request) Response { return Response{Status: status, Reason: reason} })
}

// ByToken answers by device token: "dead-" is Unregistered (410), "refused-" a refused payload
// (400 PayloadEmpty), "broken-" a dropped connection, anything else 200.
func ByToken(t *testing.T) *Server {
	t.Helper()
	return NewFunc(t, func(r Request) Response {
		switch {
		case strings.HasPrefix(r.Token, "dead-"):
			return Response{Status: http.StatusGone, Reason: "Unregistered"}
		case strings.HasPrefix(r.Token, "refused-"):
			return Response{Status: http.StatusBadRequest, Reason: "PayloadEmpty"}
		case strings.HasPrefix(r.Token, "broken-"):
			return Response{Drop: true}
		}
		return Response{Status: http.StatusOK}
	})
}

// NewFunc answers every push with respond(request).
func NewFunc(t *testing.T, respond func(Request) Response) *Server {
	t.Helper()
	s := &Server{respond: respond}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		req := Request{
			Path: r.URL.Path, Token: r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:],
			Header: r.Header.Clone(), Payload: payload,
		}
		s.mu.Lock()
		s.reqs = append(s.reqs, req)
		s.mu.Unlock()

		resp := respond(req)
		if resp.Drop {
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					_ = conn.Close()
					return
				}
			}
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(resp.Status)
		if resp.Reason != "" {
			_ = json.NewEncoder(w).Encode(map[string]string{"reason": resp.Reason})
		}
	}))
	t.Cleanup(s.Close)
	return s
}

// Requests lists the pushes received so far, oldest first.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.reqs...)
}

// PushesTo lists the pushes sent to a device token.
func (s *Server) PushesTo(token string) []Request {
	var out []Request
	for _, r := range s.Requests() {
		if r.Token == token {
			out = append(out, r)
		}
	}
	return out
}
