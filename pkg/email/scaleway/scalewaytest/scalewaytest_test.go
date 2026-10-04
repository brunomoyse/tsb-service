package scalewaytest

import (
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
