package scaleway

import (
	"encoding/base64"
	"strings"
	"testing"

	"tsb-service/pkg/email/smtptest"

	temv1alpha1 "github.com/scaleway/scaleway-sdk-go/api/tem/v1alpha1"
	"github.com/stretchr/testify/require"
)

func addr(email string) *temv1alpha1.CreateEmailRequestAddress {
	return &temv1alpha1.CreateEmailRequestAddress{Email: email}
}

func baseSMTPReq() *temv1alpha1.CreateEmailRequest {
	r := testBaseReq()
	r.To = []*temv1alpha1.CreateEmailRequestAddress{addr("to@example.com")}
	r.Subject = "Hello"
	r.Text = "plain body"
	r.HTML = "<p>html body</p>"
	return r
}

func TestSendViaSMTPDelivers(t *testing.T) {
	srv := smtptest.Start(t)
	useSMTP(t, srv)

	req := baseSMTPReq()
	req.Subject = "Commande confirmée"
	req.AdditionalHeaders = []*temv1alpha1.CreateEmailRequestHeader{{Key: "References", Value: "<root@x>"}}
	require.NoError(t, sendViaSMTP(req))

	msgs := srv.Messages()
	require.Len(t, msgs, 1)
	require.Equal(t, "noreply@tsb.test", msgs[0].From)
	require.Equal(t, []string{"to@example.com"}, msgs[0].To)

	m := smtptest.Parse(t, msgs[0].Data)
	require.Equal(t, "Commande confirmée", m.Subject)
	require.Equal(t, `"Tokyo Sushi Bar" <noreply@tsb.test>`, m.From)
	require.Equal(t, "to@example.com", m.To)
	require.Equal(t, "<root@x>", m.Header.Get("References"))
	require.Equal(t, "plain body", m.Text)
	require.Equal(t, "<p>html body</p>", m.HTML)
}

func TestSendViaSMTPAuth(t *testing.T) {
	t.Run("PLAIN credentials are sent when configured", func(t *testing.T) {
		srv := smtptest.Start(t)
		srv.SetAdvertiseAuth(true)
		useSMTP(t, srv)
		smtpUser, smtpPassword = "mailer", "s3cret"

		require.NoError(t, sendViaSMTP(baseSMTPReq()))

		auth := srv.AuthLines()
		require.Len(t, auth, 1)
		raw, err := base64.StdEncoding.DecodeString(auth[0])
		require.NoError(t, err)
		require.Equal(t, "\x00mailer\x00s3cret", string(raw))
	})

	t.Run("anonymous when only one of user/password is set", func(t *testing.T) {
		srv := smtptest.Start(t)
		srv.SetAdvertiseAuth(true)
		useSMTP(t, srv)
		smtpUser, smtpPassword = "mailer", ""

		require.NoError(t, sendViaSMTP(baseSMTPReq()))
		require.Empty(t, srv.AuthLines())
		require.Len(t, srv.Messages(), 1)
	})

	t.Run("credentials against a server without AUTH fail", func(t *testing.T) {
		srv := smtptest.Start(t)
		useSMTP(t, srv)
		smtpUser, smtpPassword = "mailer", "s3cret"

		err := sendViaSMTP(baseSMTPReq())
		require.ErrorContains(t, err, "SMTP send:")
		require.Empty(t, srv.Messages())
	})
}

func TestSendViaSMTPErrors(t *testing.T) {
	t.Run("SMTP_HOST not configured", func(t *testing.T) {
		isolateGlobals(t)
		require.ErrorContains(t, sendViaSMTP(baseSMTPReq()), "without SMTP_HOST")
	})

	t.Run("request validation", func(t *testing.T) {
		srv := smtptest.Start(t)
		useSMTP(t, srv)

		noFrom := baseSMTPReq()
		noFrom.From = nil
		require.ErrorContains(t, sendViaSMTP(noFrom), "missing From")

		emptyFrom := baseSMTPReq()
		emptyFrom.From.Email = ""
		require.ErrorContains(t, sendViaSMTP(emptyFrom), "missing From")

		noTo := baseSMTPReq()
		noTo.To = nil
		require.ErrorContains(t, sendViaSMTP(noTo), "missing To")

		emptyTo := baseSMTPReq()
		emptyTo.To = []*temv1alpha1.CreateEmailRequestAddress{nil, addr("")}
		require.ErrorContains(t, sendViaSMTP(emptyTo), "every To entry was empty")

		require.Empty(t, srv.Messages(), "nothing may be sent for invalid requests")
	})

	t.Run("nil and empty recipients are skipped, valid ones kept", func(t *testing.T) {
		srv := smtptest.Start(t)
		useSMTP(t, srv)
		req := baseSMTPReq()
		req.To = []*temv1alpha1.CreateEmailRequestAddress{nil, addr(""), addr("a@example.com"), addr("b@example.com")}
		require.NoError(t, sendViaSMTP(req))
		require.Equal(t, []string{"a@example.com", "b@example.com"}, srv.Messages()[0].To)
	})

	t.Run("server rejecting the recipient", func(t *testing.T) {
		srv := smtptest.Start(t)
		srv.SetRejectAll(true)
		useSMTP(t, srv)
		err := sendViaSMTP(baseSMTPReq())
		require.ErrorContains(t, err, "SMTP send:")
		require.ErrorContains(t, err, "550")
	})

	t.Run("connection refused", func(t *testing.T) {
		srv := smtptest.Start(t)
		useSMTP(t, srv)
		srv.Close()
		require.ErrorContains(t, sendViaSMTP(baseSMTPReq()), "SMTP send:")
	})
}

func TestBuildMimeMessage(t *testing.T) {
	build := func(t *testing.T, req *temv1alpha1.CreateEmailRequest) (string, smtptest.View) {
		t.Helper()
		raw, err := buildMimeMessage(req, []string{"to@example.com"})
		require.NoError(t, err)
		return string(raw), smtptest.Parse(t, raw)
	}

	t.Run("From without display name is a bare address", func(t *testing.T) {
		req := baseSMTPReq()
		req.From = addr("noreply@tsb.test")
		raw, m := build(t, req)
		require.Equal(t, "noreply@tsb.test", m.From)
		require.Contains(t, raw, "From: noreply@tsb.test\r\n")

		empty := ""
		req.From.Name = &empty
		_, m = build(t, req)
		require.Equal(t, "noreply@tsb.test", m.From)
	})

	t.Run("multiple recipients are comma separated", func(t *testing.T) {
		raw, err := buildMimeMessage(baseSMTPReq(), []string{"a@x.test", "b@x.test"})
		require.NoError(t, err)
		require.Contains(t, string(raw), "To: a@x.test, b@x.test\r\n")
	})

	t.Run("custom headers pass through, nil and keyless are dropped", func(t *testing.T) {
		req := baseSMTPReq()
		req.AdditionalHeaders = []*temv1alpha1.CreateEmailRequestHeader{
			{Key: "Message-ID", Value: "<m@x>"}, nil, {Key: "", Value: "ignored"}, {Key: "In-Reply-To", Value: "<r@x>"},
		}
		raw, m := build(t, req)
		require.Equal(t, "<m@x>", m.Header.Get("Message-ID"))
		require.Equal(t, "<r@x>", m.Header.Get("In-Reply-To"))
		require.NotContains(t, raw, "ignored")
	})

	t.Run("text-only and html-only bodies", func(t *testing.T) {
		req := baseSMTPReq()
		req.HTML = ""
		_, m := build(t, req)
		require.Equal(t, "plain body", m.Text)
		require.Empty(t, m.HTML)

		req = baseSMTPReq()
		req.Text = ""
		_, m = build(t, req)
		require.Empty(t, m.Text)
		require.Equal(t, "<p>html body</p>", m.HTML)
	})

	t.Run("non-ASCII subject is RFC 2047 encoded, ASCII is verbatim", func(t *testing.T) {
		req := baseSMTPReq()
		req.Subject = "订单已确认"
		raw, m := build(t, req)
		require.Equal(t, "订单已确认", m.Subject)
		require.Contains(t, raw, "Subject: =?utf-8?b?")

		req.Subject = "Order confirmed"
		raw, _ = build(t, req)
		require.Contains(t, raw, "Subject: Order confirmed\r\n")
	})

	t.Run("each message gets a fresh boundary", func(t *testing.T) {
		a, _ := build(t, baseSMTPReq())
		b, _ := build(t, baseSMTPReq())
		boundary := func(s string) string {
			i := strings.Index(s, `boundary="`) + len(`boundary="`)
			return s[i : i+strings.Index(s[i:], `"`)]
		}
		require.True(t, strings.HasPrefix(boundary(a), "tsb_"))
		require.NotEqual(t, boundary(a), boundary(b))
	})
}

func TestEncodeMimeHeader(t *testing.T) {
	require.Equal(t, "plain ascii", encodeMimeHeader("plain ascii"))
	require.Equal(t, "", encodeMimeHeader(""))
	enc := encodeMimeHeader("Thé")
	require.True(t, strings.HasPrefix(enc, "=?utf-8?b?"), enc)
}
