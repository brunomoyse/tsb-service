package fcmtest

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func send(t *testing.T, s *Server, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, s.URL+"/projects/p/messages:send",
		strings.NewReader(`{"message":{"token":"`+token+`","data":{"k":"v"}}}`))
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestFixedAnswerAndRecording(t *testing.T) {
	s := New(t, http.StatusForbidden, Error("PERMISSION_DENIED", "SENDER_ID_MISMATCH"))
	assert.Equal(t, http.StatusForbidden, send(t, s, "tok-1").StatusCode)

	reqs := s.Requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, "/projects/p/messages:send", reqs[0].Path)
	assert.Equal(t, "tok-1", reqs[0].Token)
	assert.Equal(t, map[string]any{"k": "v"}, reqs[0].Payload["data"])
	assert.Equal(t, []string{"/projects/p/messages:send"}, s.Paths())
	assert.Len(t, s.PushesTo("tok-1"), 1)
	assert.Empty(t, s.PushesTo("other"))
}

func TestByTokenAnswersPerToken(t *testing.T) {
	s := ByToken(t)
	for token, want := range map[string]int{"ok-1": 200, "dead-1": 404, "refused-1": 403} {
		assert.Equal(t, want, send(t, s, token).StatusCode, token)
	}
}
