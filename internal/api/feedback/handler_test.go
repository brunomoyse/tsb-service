package feedback

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	es "tsb-service/pkg/email/scaleway"
	"tsb-service/pkg/utils"
)

// smtpSink is a minimal SMTP server that keeps the envelope recipients and the DATA of each message.
type smtpSink struct {
	mu       sync.Mutex
	messages []sinkMessage
}

type sinkMessage struct {
	rcpt []string
	data string
}

func (s *smtpSink) all() []sinkMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sinkMessage(nil), s.messages...)
}

func startSMTPSink(t *testing.T) *smtpSink {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := &smtpSink{}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn)
		}
	}()
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	t.Setenv("SMTP_HOST", host)
	t.Setenv("SMTP_PORT", port)
	t.Setenv("SCW_SENDER_EMAIL", "noreply@example.test")
	t.Setenv("SCW_SENDER_NAME", "Test")
	require.NoError(t, es.InitService())
	return s
}

func (s *smtpSink) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	rd := bufio.NewReader(conn)
	write := func(line string) { _, _ = conn.Write([]byte(line + "\r\n")) }
	write("220 sink ready")
	var msg sinkMessage
	var data strings.Builder
	inData := false
	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			return
		}
		if inData {
			if line == ".\r\n" {
				inData = false
				msg.data = data.String()
				s.mu.Lock()
				s.messages = append(s.messages, msg)
				s.mu.Unlock()
				msg, data = sinkMessage{}, strings.Builder{}
				write("250 queued")
				continue
			}
			data.WriteString(line)
			continue
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			write("250 sink")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			msg.rcpt = append(msg.rcpt, strings.Trim(strings.TrimSpace(line[len("RCPT TO:"):]), "<>"))
			write("250 ok")
		case strings.HasPrefix(cmd, "DATA"):
			inData = true
			write("354 go")
		case strings.HasPrefix(cmd, "QUIT"):
			write("221 bye")
			return
		default:
			write("250 ok")
		}
	}
}

// roundTripFunc replaces the default HTTP transport, the way Cloudflare is reached.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fakeTurnstile(t *testing.T, answer string, status int) (calls *[]url.Values) {
	t.Helper()
	var got []url.Values
	var mu sync.Mutex
	old := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		assert.Equal(t, "challenges.cloudflare.com", r.URL.Host)
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		mu.Lock()
		got = append(got, form)
		mu.Unlock()
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(answer)), Header: http.Header{}, Request: r}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = old })
	return &got
}

const validBody = `{"name":"  Ada Lovelace ","email":"ada@example.com","serviceType":"takeaway","feedbackType":"compliment","message":"  The salmon rolls were excellent!  ","turnstileToken":"tok-1"}`

func post(t *testing.T, body string, lang string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/feedback", func(c *gin.Context) {
		if lang != "" {
			c.Request = c.Request.WithContext(utils.SetLang(c.Request.Context(), lang))
		}
		HandleFeedback(c)
	})
	req := httptest.NewRequest(http.MethodPost, "/feedback", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "203.0.113.5:4000"
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestHandleFeedback_Validation(t *testing.T) {
	t.Setenv("TURNSTILE_SECRET_KEY", "")
	cases := map[string]string{
		"not json":              `{`,
		"missing everything":    `{}`,
		"short message":         `{"name":"A","email":"a@b.co","serviceType":"takeaway","feedbackType":"complaint","message":"too short"}`,
		"message over 2000":     `{"name":"A","email":"a@b.co","serviceType":"takeaway","feedbackType":"complaint","message":"` + strings.Repeat("x", 2001) + `"}`,
		"invalid email":         `{"name":"A","email":"nope","serviceType":"takeaway","feedbackType":"complaint","message":"long enough message"}`,
		"unknown service type":  `{"name":"A","email":"a@b.co","serviceType":"drive-through","feedbackType":"complaint","message":"long enough message"}`,
		"unknown feedback type": `{"name":"A","email":"a@b.co","serviceType":"takeaway","feedbackType":"praise","message":"long enough message"}`,
		"name over 100 chars":   `{"name":"` + strings.Repeat("n", 101) + `","email":"a@b.co","serviceType":"takeaway","feedbackType":"complaint","message":"long enough message"}`,
		"email over 255 chars":  `{"name":"A","email":"` + strings.Repeat("e", 251) + `@b.co","serviceType":"takeaway","feedbackType":"complaint","message":"long enough message"}`,
		"missing service type":  `{"name":"A","email":"a@b.co","feedbackType":"complaint","message":"long enough message"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			rec := post(t, body, "")
			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.JSONEq(t, `{"error":"invalid_input"}`, rec.Body.String())
		})
	}
}

func TestHandleFeedback_HoneypotPretendsToSucceedWithoutSending(t *testing.T) {
	t.Setenv("TURNSTILE_SECRET_KEY", "secret")
	calls := fakeTurnstile(t, `{"success":true}`, http.StatusOK)
	sink := startSMTPSink(t)
	t.Setenv("FEEDBACK_RECIPIENT_EMAIL", "owner@example.test")

	body := strings.Replace(validBody, `"turnstileToken"`, `"website":"http://spam.example","turnstileToken"`, 1)
	rec := post(t, body, "")

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"status":"ok"}`, rec.Body.String(), "the bot is not tipped off")
	assert.Empty(t, *calls, "no captcha call is spent on a bot")
	assert.Empty(t, sink.all(), "and nothing is sent")
}

func TestHandleFeedback_Turnstile(t *testing.T) {
	t.Setenv("FEEDBACK_RECIPIENT_EMAIL", "owner@example.test")

	t.Run("a refused captcha is 400 and sends nothing", func(t *testing.T) {
		t.Setenv("TURNSTILE_SECRET_KEY", "secret")
		calls := fakeTurnstile(t, `{"success":false}`, http.StatusOK)
		sink := startSMTPSink(t)
		rec := post(t, validBody, "")
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.JSONEq(t, `{"error":"captcha_failed"}`, rec.Body.String())
		require.Len(t, *calls, 1)
		assert.Equal(t, "secret", (*calls)[0].Get("secret"))
		assert.Equal(t, "tok-1", (*calls)[0].Get("response"))
		assert.Equal(t, "203.0.113.5", (*calls)[0].Get("remoteip"))
		assert.Empty(t, sink.all())
	})

	t.Run("an unreadable Cloudflare answer fails closed", func(t *testing.T) {
		t.Setenv("TURNSTILE_SECRET_KEY", "secret")
		fakeTurnstile(t, `<html>`, http.StatusOK)
		assert.Equal(t, http.StatusBadRequest, post(t, validBody, "").Code)
	})

	t.Run("an unreachable Cloudflare fails closed", func(t *testing.T) {
		t.Setenv("TURNSTILE_SECRET_KEY", "secret")
		old := http.DefaultTransport
		http.DefaultTransport = roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, io.ErrUnexpectedEOF })
		t.Cleanup(func() { http.DefaultTransport = old })
		assert.Equal(t, http.StatusBadRequest, post(t, validBody, "").Code)
	})

	t.Run("a missing token is sent as empty and refused by Cloudflare", func(t *testing.T) {
		t.Setenv("TURNSTILE_SECRET_KEY", "secret")
		calls := fakeTurnstile(t, `{"success":false}`, http.StatusOK)
		body := strings.Replace(validBody, `,"turnstileToken":"tok-1"`, "", 1)
		assert.Equal(t, http.StatusBadRequest, post(t, body, "").Code)
		require.Len(t, *calls, 1)
		assert.Equal(t, "", (*calls)[0].Get("response"))
	})

	t.Run("without a configured secret verification is skipped", func(t *testing.T) {
		t.Setenv("TURNSTILE_SECRET_KEY", "")
		old := http.DefaultTransport
		http.DefaultTransport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Error("Cloudflare must not be contacted when Turnstile is not configured")
			return nil, io.ErrUnexpectedEOF
		})
		t.Cleanup(func() { http.DefaultTransport = old })
		sink := startSMTPSink(t)
		rec := post(t, validBody, "")
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Len(t, sink.all(), 1)
	})
}

func TestHandleFeedback_SendsTheEmailToTheOwner(t *testing.T) {
	t.Setenv("TURNSTILE_SECRET_KEY", "secret")
	fakeTurnstile(t, `{"success":true}`, http.StatusOK)
	t.Setenv("FEEDBACK_RECIPIENT_EMAIL", "owner@example.test")
	sink := startSMTPSink(t)

	rec := post(t, validBody, "fr")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"status":"ok"}`, rec.Body.String())
	msgs := sink.all()
	require.Len(t, msgs, 1)
	assert.Equal(t, []string{"owner@example.test"}, msgs[0].rcpt, "the feedback goes to the owner, not to the customer")
	assert.Contains(t, msgs[0].data, "Customer feedback (compliment) - Ada Lovelace", "the name is trimmed")
}

func TestHandleFeedback_EmailFailure(t *testing.T) {
	t.Setenv("TURNSTILE_SECRET_KEY", "")
	// A mail server that is not listening: the send fails and the customer is told so.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	require.NoError(t, ln.Close())
	t.Setenv("SMTP_HOST", host)
	t.Setenv("SMTP_PORT", port)
	t.Setenv("SCW_SENDER_EMAIL", "noreply@example.test")
	t.Setenv("SCW_SENDER_NAME", "Test")
	require.NoError(t, es.InitService())
	t.Setenv("FEEDBACK_RECIPIENT_EMAIL", "owner@example.test")

	rec := post(t, validBody, "")

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.JSONEq(t, `{"error":"email_failed"}`, rec.Body.String())
}
