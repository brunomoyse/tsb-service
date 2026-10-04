package scalewaytest

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	userDomain "tsb-service/internal/modules/user/domain"
	es "tsb-service/pkg/email/scaleway"
	"tsb-service/pkg/email/smtptest"
)

func TestUseRoutesMailToTheServerAndRestoresTheBackend(t *testing.T) {
	user := userDomain.User{FirstName: "Jeanne", LastName: "Dupont", Email: "jeanne@example.com"}
	before := es.IsInitialized()

	t.Run("inside the test", func(t *testing.T) {
		srv := smtptest.Start(t)
		Use(t, srv)
		require.True(t, es.IsInitialized())
		require.NoError(t, es.SendOrderCompletedEmail(user, "en"))
		assert.Len(t, srv.MailTo("jeanne@example.com"), 1)
		assert.Equal(t, "noreply@example.test", srv.Messages()[0].From)
	})

	assert.Equal(t, before, es.IsInitialized(), "the backend that was there before is back")
}

func TestUseDeadMakesEverySendFail(t *testing.T) {
	user := userDomain.User{FirstName: "Jeanne", Email: "jeanne@example.com"}
	UseDead(t)
	assert.Error(t, es.SendOrderCompletedEmail(user, "en"))
}

func TestUseInMainStaysOnTheServerUntilRestored(t *testing.T) {
	user := userDomain.User{FirstName: "Jeanne", Email: "jeanne@example.com"}
	srv, err := smtptest.New()
	require.NoError(t, err)
	defer srv.Close()
	t.Setenv("SMTP_HOST", "keep.example.test")
	t.Setenv("SCW_SENDER_NAME", "")
	require.NoError(t, os.Unsetenv("SCW_SENDER_NAME"))

	restore, err := UseInMain(srv)
	require.NoError(t, err)
	require.NoError(t, es.SendOrderCompletedEmail(user, "fr"))
	assert.Equal(t, 1, srv.Count())

	restore()
	assert.Equal(t, "keep.example.test", os.Getenv("SMTP_HOST"), "an environment variable that was set is put back")
	_, stillSet := os.LookupEnv("SCW_SENDER_NAME")
	assert.False(t, stillSet, "one that was not set is removed again")
}
