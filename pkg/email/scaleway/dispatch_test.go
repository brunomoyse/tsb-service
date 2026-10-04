package scaleway

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"tsb-service/pkg/email/smtptest"

	temv1alpha1 "github.com/scaleway/scaleway-sdk-go/api/tem/v1alpha1"
	"github.com/stretchr/testify/require"
)

func TestInitService(t *testing.T) {
	clearEnv := func(t *testing.T) {
		t.Helper()
		isolateGlobals(t)
		for _, k := range []string{"SMTP_HOST", "SMTP_PORT", "SMTP_USER", "SMTP_PASSWORD", "SCW_ACCESS_KEY", "SCW_SECRET_KEY", "SCW_DEFAULT_ORGANIZATION_ID"} {
			t.Setenv(k, "")
		}
		baseReq = nil
		require.False(t, IsInitialized())
		t.Setenv("SCW_SENDER_EMAIL", "noreply@tsb.test")
		t.Setenv("SCW_SENDER_NAME", "Tokyo Sushi Bar")
		t.Setenv("SCW_REGION", "fr-par")
		t.Setenv("SCW_DEFAULT_PROJECT_ID", "11111111-2222-4333-8444-555555555555")
	}

	t.Run("SMTP backend with defaults", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("SMTP_HOST", "mailpit")

		require.NoError(t, InitService())

		require.True(t, IsInitialized())
		require.Equal(t, "mailpit", smtpHost)
		require.Equal(t, "1025", smtpPort, "default Mailpit port")
		require.Empty(t, smtpUser)
		require.Nil(t, temClient, "SMTP mode must not build a Scaleway client")
		require.Equal(t, "noreply@tsb.test", baseReq.From.Email)
		require.Equal(t, "Tokyo Sushi Bar", *baseReq.From.Name)
		require.EqualValues(t, "fr-par", baseReq.Region)
		require.Equal(t, "11111111-2222-4333-8444-555555555555", baseReq.ProjectID)
	})

	t.Run("SMTP backend with explicit port and credentials", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("SMTP_HOST", "smtp.example.com")
		t.Setenv("SMTP_PORT", "587")
		t.Setenv("SMTP_USER", "mailer")
		t.Setenv("SMTP_PASSWORD", "pw")

		require.NoError(t, InitService())
		require.Equal(t, "587", smtpPort)
		require.Equal(t, "mailer", smtpUser)
		require.Equal(t, "pw", smtpPassword)
	})

	t.Run("Scaleway TEM backend", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("SCW_ACCESS_KEY", "SCWXXXXXXXXXXXXXXXXX")
		t.Setenv("SCW_SECRET_KEY", "11111111-2222-4333-8444-555555555555")
		t.Setenv("SCW_DEFAULT_ORGANIZATION_ID", "11111111-2222-4333-8444-555555555555")

		require.NoError(t, InitService())

		require.True(t, IsInitialized())
		require.NotNil(t, temClient)
		require.Empty(t, smtpHost)
		require.Equal(t, "noreply@tsb.test", baseReq.From.Email)
		require.EqualValues(t, "fr-par", baseReq.Region)
	})

	t.Run("invalid Scaleway credentials are reported and leave the service uninitialized", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("SCW_ACCESS_KEY", "SCWXXXXXXXXXXXXXXXXX")
		t.Setenv("SCW_SECRET_KEY", "not-a-uuid")

		err := InitService()
		require.ErrorContains(t, err, "failed to create Scaleway client")
		require.False(t, IsInitialized())
		require.Nil(t, temClient)
	})
}

func TestDispatchViaTEM(t *testing.T) {
	t.Run("posts the email to the TEM API with auth and the full payload", func(t *testing.T) {
		f := startFakeTEM(t, temOK)
		require.NoError(t, SendOrderConfirmedEmail(sampleUser(), "fr", deliveryOrder(), sampleItems(), sampleAddress()))

		reqs := f.requests()
		require.Len(t, reqs, 1)
		r := reqs[0]
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/transactional-email/v1alpha1/regions/fr-par/emails", r.Path)
		require.NotEmpty(t, r.Token, "X-Auth-Token must carry the secret key")

		var body struct {
			From                struct{ Email, Name string }
			To                  []struct{ Email, Name string }
			Subject, HTML, Text string
			ProjectID           string                        `json:"project_id"`
			AdditionalHeaders   []struct{ Key, Value string } `json:"additional_headers"`
		}
		require.NoError(t, json.Unmarshal(r.Body, &body))
		require.Equal(t, "noreply@tsb.test", body.From.Email)
		require.Equal(t, "Tokyo Sushi Bar", body.From.Name)
		require.Len(t, body.To, 1)
		require.Equal(t, "jeanne@example.com", body.To[0].Email)
		require.Equal(t, "Jeanne Dupont", body.To[0].Name)
		require.Equal(t, "Commande confirmée", body.Subject)
		require.Equal(t, "11111111-2222-4333-8444-555555555555", body.ProjectID)
		require.Contains(t, body.Text, "27,70 €")
		require.Contains(t, body.HTML, "27,70")
		require.Len(t, body.AdditionalHeaders, 3)
	})

	t.Run("does not mutate the shared base request", func(t *testing.T) {
		startFakeTEM(t, temOK)
		require.NoError(t, SendWelcomeEmail(sampleUser(), "en", "https://shop.test/menu"))
		require.NoError(t, SendWelcomeEmail(sampleUser(), "en", "https://shop.test/menu"))
		require.Empty(t, baseReq.To, "each send builds its own recipient list")
		require.Empty(t, baseReq.Subject)
	})

	t.Run("invalid recipient from Scaleway is classified, not retried", func(t *testing.T) {
		f := startFakeTEM(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"type":"invalid_arguments","message":"invalid argument(s)","details":[{"argument_name":"rcpt","reason":"format","help_message":"Invalid email recipient address: \"\""}]}`))
		})
		err := SendVerificationEmail(sampleUser(), "fr", "https://shop.test/verify?t=1")
		require.ErrorIs(t, err, ErrInvalidRecipient)
		require.ErrorContains(t, err, "failed to send email")
		require.Len(t, f.requests(), 1)
	})

	t.Run("other API failures are surfaced but not classified as bad recipient", func(t *testing.T) {
		startFakeTEM(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"type":"denied_authentication","message":"insufficient permissions"}`))
		})
		err := SendVerificationEmail(sampleUser(), "fr", "https://shop.test/verify?t=1")
		require.ErrorContains(t, err, "failed to send email")
		require.False(t, errors.Is(err, ErrInvalidRecipient))
	})

	t.Run("suppressed recipient is skipped silently, nothing is sent", func(t *testing.T) {
		f := startFakeTEM(t, temOK)
		store := newRecordingStore()
		store.suppressed["jeanne@example.com"] = "hard_bounce"
		suppressionStore = store

		require.NoError(t, SendOrderReadyEmail(sampleUser(), "fr", pickupOrder()))
		require.Empty(t, f.requests())
		require.Equal(t, []string{"jeanne@example.com"}, store.lookupCalls)
	})
}

func TestDispatchViaSMTPSkipsSuppressedRecipient(t *testing.T) {
	srv := smtptest.Start(t)
	useSMTP(t, srv)
	store := newRecordingStore()
	store.suppressed["jeanne@example.com"] = "mailbox_not_found"
	suppressionStore = store

	require.NoError(t, SendWelcomeEmail(sampleUser(), "fr", "https://shop.test"))
	require.Empty(t, srv.Messages())

	// A different customer still gets their email.
	other := sampleUser()
	other.Email = "other@example.com"
	require.NoError(t, SendWelcomeEmail(other, "fr", "https://shop.test"))
	require.Len(t, srv.Messages(), 1)
	require.Equal(t, []string{"other@example.com"}, srv.Messages()[0].To)
}

func TestDispatchSMTPFailureIsWrapped(t *testing.T) {
	srv := smtptest.Start(t)
	srv.SetRejectAll(true)
	useSMTP(t, srv)
	err := SendWelcomeEmail(sampleUser(), "fr", "https://shop.test")
	require.ErrorContains(t, err, "failed to send email")
	require.ErrorContains(t, err, "550")
}

func TestOrderThreadHeaders(t *testing.T) {
	h := orderThreadHeaders("order-42")
	require.Len(t, h, 3)
	byKey := map[string]string{}
	for _, x := range h {
		byKey[x.Key] = x.Value
	}
	domain := brandDomain()
	root := "<order-thread-order-42@" + domain + ">"
	require.Equal(t, root, byKey["In-Reply-To"])
	require.Equal(t, root, byKey["References"])
	require.Regexp(t, `^<email-order-42-\d+@`+domain+`>$`, byKey["Message-ID"])
	require.NotEqual(t, root, byKey["Message-ID"], "each email has its own id; the root is never sent")
}

// A send builds its request on a copy of baseReq: nothing a Send*Email function does to the copy
// (appending a recipient, changing the sender) may reach the shared request or the next send.
func TestCopyBaseReqIsIndependentOfTheSharedRequest(t *testing.T) {
	isolateGlobals(t)
	name := "Tokyo Sushi Bar"
	spare := make([]*temv1alpha1.CreateEmailRequestAddress, 0, 4) // room to append in place: a shallow copy would write into it
	baseReq = &temv1alpha1.CreateEmailRequest{
		From: &temv1alpha1.CreateEmailRequestAddress{Email: "noreply@tsb.test", Name: &name},
		To:   spare,
	}

	first, err := copyBaseReq()
	require.NoError(t, err)
	first.To = append(first.To, &temv1alpha1.CreateEmailRequestAddress{Email: "a@example.test"})
	first.From.Email = "changed@example.test"
	second, err := copyBaseReq()
	require.NoError(t, err)

	require.Empty(t, second.To, "the recipient of the first send is not in the second")
	require.Empty(t, baseReq.To)
	require.Equal(t, "noreply@tsb.test", baseReq.From.Email, "the sender of the shared request is untouched")
	require.Equal(t, "noreply@tsb.test", second.From.Email)
	require.Nil(t, spare[:1][0], "the shared backing array was not written to")
}
