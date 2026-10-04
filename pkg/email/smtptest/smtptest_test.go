package smtptest

import (
	"net"
	"net/smtp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const mailBody = "From: a@x.test\r\nTo: b@x.test\r\nSubject: =?UTF-8?q?H=C3=A9llo?=\r\nContent-Type: multipart/alternative; boundary=BND\r\n\r\n" +
	"--BND\r\nContent-Type: text/plain\r\n\r\nplain text\r\n--BND\r\nContent-Type: text/html\r\n\r\n<p>html</p>\r\n--BND--\r\n"

func send(t *testing.T, s *Server, to string, auth smtp.Auth) error {
	t.Helper()
	return smtp.SendMail(net.JoinHostPort(s.Host(), s.Port()), auth, "a@x.test", []string{to}, []byte(mailBody))
}

func TestServerRecordsAndParsesMessages(t *testing.T) {
	s := Start(t)
	require.NoError(t, send(t, s, "Bob@X.test", nil))

	msgs := s.Messages()
	require.Len(t, msgs, 1)
	assert.Equal(t, "a@x.test", msgs[0].From)
	assert.Equal(t, []string{"Bob@X.test"}, msgs[0].To)
	assert.Equal(t, 1, s.Count())
	assert.Len(t, s.MailTo("bob@x.test"), 1, "recipient matching ignores case")
	assert.Empty(t, s.MailTo("someone@else.test"))

	v := s.Only(t)
	assert.Equal(t, "Héllo", v.Subject)
	assert.Equal(t, "plain text", v.Text)
	assert.Equal(t, "<p>html</p>", v.HTML)
	assert.Equal(t, []string{"Héllo"}, s.SubjectsTo(t, "bob@x.test"))
	assert.Equal(t, 1, s.CountSubject(t, "bob@x.test", "Héllo"))
	assert.Zero(t, s.CountSubject(t, "bob@x.test", "Other"))
	assert.Equal(t, msgs[0], s.WaitSubject(t, "bob@x.test", "Héllo"))
	assert.Len(t, s.WaitMailTo(t, "bob@x.test", 1), 1)
}

func TestServerUndoesDotStuffing(t *testing.T) {
	s := Start(t)
	c, err := smtp.Dial(net.JoinHostPort(s.Host(), s.Port()))
	require.NoError(t, err)
	require.NoError(t, c.Mail("a@x.test"))
	require.NoError(t, c.Rcpt("b@x.test"))
	w, err := c.Data()
	require.NoError(t, err)
	_, err = w.Write([]byte("Subject: x\r\n\r\n.starts with a dot\r\n"))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	require.NoError(t, c.Quit())

	assert.Contains(t, string(s.Messages()[0].Data), "\r\n.starts with a dot\r\n")
}

func TestServerRefusesRecipients(t *testing.T) {
	s := Start(t)
	s.Reject("Bounce@x.test")
	require.Error(t, send(t, s, "bounce@x.test", nil), "a rejected mailbox is refused, whatever its case")
	require.NoError(t, send(t, s, "fine@x.test", nil))

	s.SetRejectAll(true)
	require.Error(t, send(t, s, "fine@x.test", nil))
	s.SetRejectAll(false)
	require.NoError(t, send(t, s, "fine@x.test", nil))
	assert.Equal(t, 2, s.Count(), "refused messages are not recorded")
}

func TestServerAdvertisesAuthOnRequest(t *testing.T) {
	s := Start(t)
	auth := smtp.PlainAuth("", "user", "secret", "127.0.0.1")
	require.Error(t, send(t, s, "b@x.test", auth), "AUTH is not offered by default")

	s.SetAdvertiseAuth(true)
	require.NoError(t, send(t, s, "b@x.test", auth))
	require.Len(t, s.AuthLines(), 1)
	assert.NotEmpty(t, s.AuthLines()[0])
}

func TestServerCloseRefusesConnections(t *testing.T) {
	s := Start(t)
	require.NoError(t, send(t, s, "b@x.test", nil))
	s.Close()
	require.Error(t, send(t, s, "b@x.test", nil))
	assert.Equal(t, 1, s.Count())
}
