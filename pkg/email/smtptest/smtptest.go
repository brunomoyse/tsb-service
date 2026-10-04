// Package smtptest is an in-process SMTP server for tests of code that sends e-mail. It records
// every message it accepts and can be told to refuse recipients, so tests read what a customer
// would receive instead of replacing the sending code.
//
// It does not import the e-mail package, so the e-mail package's own tests can use it. To point
// the e-mail backend at a Server use scalewaytest.Use.
package smtptest

import (
	"bufio"
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Message is one accepted message: the envelope and the DATA with dot-stuffing undone.
type Message struct {
	From string
	To   []string
	Data []byte
}

// Server is the fake SMTP server. The zero value is not usable: create it with Start.
type Server struct {
	ln net.Listener

	mu            sync.Mutex
	msgs          []Message
	authLines     []string
	rejected      map[string]bool
	rejectAll     bool
	advertiseAuth bool
}

// New listens on a free loopback port and serves until Close. Use it where there is no *testing.T
// (a TestMain); tests call Start.
func New() (*Server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &Server{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.handle(c)
		}
	}()
	return s, nil
}

// Start is New for a test: the server stops when the test ends.
func Start(t *testing.T) *Server {
	t.Helper()
	s, err := New()
	require.NoError(t, err)
	t.Cleanup(s.Close)
	return s
}

// Host and Port are where the server listens.
func (s *Server) Host() string { h, _, _ := net.SplitHostPort(s.ln.Addr().String()); return h }
func (s *Server) Port() string { _, p, _ := net.SplitHostPort(s.ln.Addr().String()); return p }

// Close stops listening: later connections are refused. Use it to simulate a mail server that went
// away; it is also done when the test ends.
func (s *Server) Close() { _ = s.ln.Close() }

// SetRejectAll makes the server refuse every recipient (550), or accept them again.
func (s *Server) SetRejectAll(v bool) {
	s.mu.Lock()
	s.rejectAll = v
	s.mu.Unlock()
}

// Reject makes the server refuse every message to the address (a mailbox that bounces).
func (s *Server) Reject(addr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rejected == nil {
		s.rejected = map[string]bool{}
	}
	s.rejected[strings.ToLower(addr)] = true
}

// SetAdvertiseAuth makes EHLO announce AUTH PLAIN.
func (s *Server) SetAdvertiseAuth(v bool) {
	s.mu.Lock()
	s.advertiseAuth = v
	s.mu.Unlock()
}

// AuthLines are the credentials the clients sent with AUTH PLAIN (base64, as received).
func (s *Server) AuthLines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.authLines...)
}

// Messages lists everything accepted so far, oldest first.
func (s *Server) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Message(nil), s.msgs...)
}

// Count is the number of messages accepted so far.
func (s *Server) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.msgs)
}

// MailTo lists the messages whose envelope reaches the address (case-insensitive).
func (s *Server) MailTo(addr string) []Message {
	var out []Message
	for _, m := range s.Messages() {
		for _, to := range m.To {
			if strings.EqualFold(to, addr) {
				out = append(out, m)
				break
			}
		}
	}
	return out
}

func (s *Server) refuses(rcpt string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rejectAll || s.rejected[strings.ToLower(rcpt)]
}

func (s *Server) handle(c net.Conn) {
	defer func() { _ = c.Close() }()
	br := bufio.NewReader(c)
	write := func(l string) { _, _ = io.WriteString(c, l+"\r\n") }
	write("220 fake ESMTP")
	var cur Message
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		up := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(up, "EHLO"), strings.HasPrefix(up, "HELO"):
			write("250-fake")
			s.mu.Lock()
			auth := s.advertiseAuth
			s.mu.Unlock()
			if auth {
				write("250-AUTH PLAIN")
			}
			write("250 8BITMIME")
		case strings.HasPrefix(up, "AUTH PLAIN"):
			s.mu.Lock()
			s.authLines = append(s.authLines, strings.TrimSpace(line[len("AUTH PLAIN"):]))
			s.mu.Unlock()
			write("235 2.7.0 ok")
		case strings.HasPrefix(up, "MAIL FROM:"):
			cur = Message{From: angle(line)}
			write("250 ok")
		case strings.HasPrefix(up, "RCPT TO:"):
			rcpt := angle(line)
			if s.refuses(rcpt) {
				write("550 5.1.1 no such user")
				continue
			}
			cur.To = append(cur.To, rcpt)
			write("250 ok")
		case up == "DATA":
			write("354 go ahead")
			var data bytes.Buffer
			for {
				l, err := br.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				data.WriteString(strings.TrimPrefix(l, ".")) // undo dot-stuffing
			}
			cur.Data = data.Bytes()
			s.mu.Lock()
			s.msgs = append(s.msgs, cur)
			s.mu.Unlock()
			write("250 queued")
		case up == "QUIT":
			write("221 bye")
			return
		default:
			write("250 ok")
		}
	}
}

func angle(s string) string {
	i, j := strings.Index(s, "<"), strings.Index(s, ">")
	if i < 0 || j < i {
		return ""
	}
	return s[i+1 : j]
}

// ---- reading what was sent ----------------------------------------------------------------

// View is a parsed multipart/alternative message.
type View struct {
	Header  mail.Header
	Subject string
	From    string
	To      string
	Text    string
	HTML    string
}

// Parse reads a message the e-mail package produced: decoded subject, and its text and HTML parts.
func Parse(t *testing.T, raw []byte) View {
	t.Helper()
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	require.NoError(t, err)
	subject, err := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	require.NoError(t, err)

	mt, params, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
	require.NoError(t, err)
	require.Equal(t, "multipart/alternative", mt)

	v := View{Header: m.Header, Subject: subject, From: m.Header.Get("From"), To: m.Header.Get("To")}
	mr := multipart.NewReader(m.Body, params["boundary"])
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		body, err := io.ReadAll(p)
		require.NoError(t, err)
		s := strings.TrimSuffix(string(body), "\r\n")
		switch ct, _, _ := mime.ParseMediaType(p.Header.Get("Content-Type")); ct {
		case "text/plain":
			v.Text = s
		case "text/html":
			v.HTML = s
		default:
			t.Fatalf("unexpected part type %q", ct)
		}
	}
	return v
}

// Only returns the single message the server received.
func (s *Server) Only(t *testing.T) View {
	t.Helper()
	msgs := s.Messages()
	require.Len(t, msgs, 1, "expected exactly one SMTP message")
	return Parse(t, msgs[0].Data)
}

// SubjectOf is the decoded Subject header of a message.
func SubjectOf(t *testing.T, m Message) string {
	t.Helper()
	msg, err := mail.ReadMessage(bytes.NewReader(m.Data))
	require.NoError(t, err)
	subject, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	require.NoError(t, err)
	return subject
}

// SubjectsTo lists the subjects of the messages sent to the address so far.
func (s *Server) SubjectsTo(t *testing.T, addr string) []string {
	t.Helper()
	var out []string
	for _, m := range s.MailTo(addr) {
		out = append(out, SubjectOf(t, m))
	}
	return out
}

// WaitMailTo waits until n messages reached the address and returns them.
func (s *Server) WaitMailTo(t *testing.T, addr string, n int) []Message {
	t.Helper()
	require.Eventually(t, func() bool { return len(s.MailTo(addr)) >= n }, 20*time.Second, 20*time.Millisecond,
		"expected %d e-mails to %s, got %d", n, addr, len(s.MailTo(addr)))
	return s.MailTo(addr)
}

// WaitSubject waits until the address received a message with the subject and returns it.
func (s *Server) WaitSubject(t *testing.T, addr, subject string) Message {
	t.Helper()
	var found Message
	require.Eventually(t, func() bool {
		for _, m := range s.MailTo(addr) {
			if SubjectOf(t, m) == subject {
				found = m
				return true
			}
		}
		return false
	}, 20*time.Second, 20*time.Millisecond, "no %q e-mail reached %s; got %v", subject, addr, s.SubjectsTo(t, addr))
	return found
}

// CountSubject is the number of messages with the subject sent to the address so far.
func (s *Server) CountSubject(t *testing.T, addr, subject string) int {
	t.Helper()
	n := 0
	for _, got := range s.SubjectsTo(t, addr) {
		if got == subject {
			n++
		}
	}
	return n
}
