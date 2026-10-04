package apnstest

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func push(t *testing.T, s *Server, token string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, s.URL+"/3/device/"+token, strings.NewReader(`{"aps":{"alert":"hi"}}`))
	require.NoError(t, err)
	req.Header.Set("apns-topic", "be.test.app")
	return s.Client().Do(req)
}

func TestFixedAnswerAndRecording(t *testing.T) {
	s := New(t, http.StatusBadRequest, "BadDeviceToken")
	resp, err := push(t, s, "tok-1")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	reqs := s.Requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, "/3/device/tok-1", reqs[0].Path)
	assert.Equal(t, "tok-1", reqs[0].Token)
	assert.Equal(t, "be.test.app", reqs[0].Header.Get("Apns-Topic"))
	assert.Equal(t, map[string]any{"alert": "hi"}, reqs[0].Payload["aps"])
	assert.Len(t, s.PushesTo("tok-1"), 1)
	assert.Empty(t, s.PushesTo("other"))
}

func TestByTokenAnswersPerToken(t *testing.T) {
	s := ByToken(t)
	for token, want := range map[string]int{"ok-1": 200, "dead-1": 410, "refused-1": 400} {
		resp, err := push(t, s, token)
		require.NoError(t, err, token)
		_ = resp.Body.Close()
		assert.Equal(t, want, resp.StatusCode, token)
	}
	_, err := push(t, s, "broken-1")
	require.Error(t, err, "a dropped connection is a transport error")
	assert.Len(t, s.PushesTo("broken-1"), 1, "the push was still recorded")
}
