package auth

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fastProjectionPolling shrinks the projection-wait cadence so tests don't sleep
// for the production 150ms/attempt. Restored automatically at test end.
func fastProjectionPolling(t *testing.T) {
	t.Helper()
	origDelay, origAttempts := userProjectionPollDelay, userProjectionPollAttempts
	userProjectionPollDelay = time.Millisecond
	t.Cleanup(func() {
		userProjectionPollDelay = origDelay
		userProjectionPollAttempts = origAttempts
	})
}

// TestWaitForZitadelUserProjection_ImmediateSuccess: when the user is already
// visible to the query side, the wait returns after a single probe.
func TestWaitForZitadelUserProjection_ImmediateSuccess(t *testing.T) {
	fastProjectionPolling(t)
	var calls atomic.Int32
	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v2/users/u1", r.URL.Path)
		assert.Equal(t, http.MethodGet, r.Method)
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"user":{}}`))
	})

	require.NoError(t, waitForZitadelUserProjection("u1"))
	assert.Equal(t, int32(1), calls.Load(), "should stop probing as soon as the user is found")
}

// TestWaitForZitadelUserProjection_SucceedsAfterLag is the core scenario: the
// query projection lags creation (404), then catches up (200). The wait must
// retry and succeed — this is exactly what was missing when the App Store
// reviewer's first-time Apple sign-in 404'd.
func TestWaitForZitadelUserProjection_SucceedsAfterLag(t *testing.T) {
	fastProjectionPolling(t)
	var calls atomic.Int32
	setupMockZitadel(t, func(w http.ResponseWriter, _ *http.Request) {
		// Project the user only on the 3rd probe — first two race the projection.
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":5,"message":"User could not be found"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"user":{}}`))
	})

	require.NoError(t, waitForZitadelUserProjection("u1"))
	assert.Equal(t, int32(3), calls.Load(), "should keep probing until the projection catches up")
}

// TestWaitForZitadelUserProjection_Timeout: if the user never becomes visible,
// the wait exhausts its attempts and returns an error (caller then proceeds
// best-effort).
func TestWaitForZitadelUserProjection_Timeout(t *testing.T) {
	fastProjectionPolling(t)
	userProjectionPollAttempts = 4
	var calls atomic.Int32
	setupMockZitadel(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":5,"message":"User could not be found"}`))
	})

	err := waitForZitadelUserProjection("u1")
	require.Error(t, err)
	assert.Equal(t, int32(4), calls.Load(), "should probe exactly the configured number of attempts")
}

// TestCreateIdPSessionHandler_NewUserProjectionRace is the end-to-end regression
// test for the reviewer bug: a brand-new IdP user is created, but Zitadel's user
// query projection lags, so the first GET /v2/users/{id} 404s. The handler must
// wait for the projection to catch up before creating the session, and return a
// successful session — NOT the spurious 404 the reviewer saw.
func TestCreateIdPSessionHandler_NewUserProjectionRace(t *testing.T) {
	fastProjectionPolling(t)
	var userGets atomic.Int32
	var sessionCreated atomic.Bool
	setupMockZitadelWithIdP(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/idp_intents/intent-race" && r.Method == "POST":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"addHumanUser":{"profile":{"givenName":"John","familyName":"Apple"},"email":{"email":"john@privaterelay.appleid.com"}},
				"idpInformation":{"idpId":"test-apple-idp","userId":"apple-sub-1","userName":"john@privaterelay.appleid.com"}
			}`))
		case r.URL.Path == "/v2/users" && r.Method == "POST":
			// Email search — no existing user.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":[]}`))
		case r.URL.Path == "/v2/users/human" && r.Method == "POST":
			// User created on the command side.
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"userId":"race-user"}`))
		case r.URL.Path == "/v2/users/race-user" && r.Method == "GET":
			// Projection lag: the first probe 404s, then the user materializes.
			if userGets.Add(1) == 1 {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"code":5,"message":"User could not be found"}`))
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"user":{"human":{"profile":{"givenName":"John","familyName":"Apple"}}}}`))
		case r.URL.Path == "/v2/sessions" && r.Method == "POST":
			// Must only run once the user is visible — assert the wait happened.
			assert.GreaterOrEqual(t, userGets.Load(), int32(2),
				"session must not be created until the projection caught up")
			sessionCreated.Store(true)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"sessionId":"sess-race","sessionToken":"tok-race"}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	w, c := ginContext("POST", "/auth/idp/session", `{"idpIntentId":"intent-race","idpIntentToken":"tok-race"}`)
	CreateIdPSessionHandler(c)

	// The reviewer got 404 here; with the fix it must be a clean 200 + session.
	assert.Equal(t, http.StatusOK, w.Code)
	assert.True(t, sessionCreated.Load(), "session should have been created after the wait")
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "sess-race", resp["sessionId"])
	assert.Equal(t, false, resp["requiresProfile"], "real name from Apple → no profile completion")
}
