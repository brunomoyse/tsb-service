package auth

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- StartIdPIntentHandler Tests ---

func TestStartIdPIntentHandler_GoogleSuccess(t *testing.T) {
	setupMockZitadelWithIdP(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v2/idp_intents", r.URL.Path)
		assert.Equal(t, "POST", r.Method)

		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "test-google-idp", body["idpId"])

		urls := body["urls"].(map[string]any)
		assert.Equal(t, "https://app.example.com/success", urls["successUrl"])
		assert.Equal(t, "https://app.example.com/failure", urls["failureUrl"])

		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"authUrl":"https://accounts.google.com/o/oauth2/auth?client_id=..."}`))
	})

	w, c := ginContext("POST", "/auth/idp/start", `{"provider":"google","successUrl":"https://app.example.com/success","failureUrl":"https://app.example.com/failure"}`)
	StartIdPIntentHandler(c)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp["authUrl"], "google.com")
}

func TestStartIdPIntentHandler_UnsupportedProvider(t *testing.T) {
	setupMockZitadelWithIdP(t, nil)

	w, c := ginContext("POST", "/auth/idp/start", `{"provider":"twitter","successUrl":"https://app.example.com/success","failureUrl":"https://app.example.com/failure"}`)
	StartIdPIntentHandler(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "unsupported or unconfigured provider", resp["error"])
}

func TestStartIdPIntentHandler_MissingFields(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"missing provider", `{"successUrl":"https://a.com","failureUrl":"https://b.com"}`},
		{"missing successUrl", `{"provider":"google","failureUrl":"https://b.com"}`},
		{"missing failureUrl", `{"provider":"google","successUrl":"https://a.com"}`},
		{"empty body", `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, c := ginContext("POST", "/auth/idp/start", tt.body)
			StartIdPIntentHandler(c)
			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	}
}

func TestStartIdPIntentHandler_ZitadelError(t *testing.T) {
	setupMockZitadelWithIdP(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":13,"message":"internal error"}`))
	})

	w, c := ginContext("POST", "/auth/idp/start", `{"provider":"google","successUrl":"https://a.com","failureUrl":"https://b.com"}`)
	StartIdPIntentHandler(c)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// --- CreateIdPSessionHandler Tests ---

func TestCreateIdPSessionHandler_WithExistingUserId(t *testing.T) {
	setupMockZitadelWithIdP(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/sessions" && r.Method == "POST":
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			checks := body["checks"].(map[string]any)
			user := checks["user"].(map[string]any)
			assert.Equal(t, "existing-user-123", user["userId"])
			idpIntent := checks["idpIntent"].(map[string]any)
			assert.Equal(t, "intent-abc", idpIntent["idpIntentId"])

			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"sessionId":"sess-idp-1","sessionToken":"tok-idp-1"}`))
		case r.URL.Path == "/v2/users/existing-user-123" && r.Method == "GET":
			// Profile-completion probe — existing user has a real name.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"user":{"human":{"profile":{"givenName":"Existing","familyName":"User"}}}}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	w, c := ginContext("POST", "/auth/idp/session", `{"idpIntentId":"intent-abc","idpIntentToken":"tok-abc","userId":"existing-user-123"}`)
	CreateIdPSessionHandler(c)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "sess-idp-1", resp["sessionId"])
	assert.Equal(t, "tok-idp-1", resp["sessionToken"])
	// Real name → no profile completion needed.
	assert.Equal(t, false, resp["requiresProfile"])
}

func TestCreateIdPSessionHandler_NewUserFromIdP(t *testing.T) {
	setupMockZitadelWithIdP(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/idp_intents/intent-new" && r.Method == "POST":
			// Retrieve IdP intent info
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"addHumanUser":{"profile":{"givenName":"New","familyName":"User"},"email":{"email":"new@google.com"}},
				"idpInformation":{"idpId":"test-google-idp","userId":"google-sub-1","userName":"new@google.com"}
			}`))
		case r.URL.Path == "/v2/users" && r.Method == "POST":
			// Email search — user not found
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":[]}`))
		case r.URL.Path == "/v2/users/human" && r.Method == "POST":
			// Create new user — the IdP supplied a name, so it is preserved.
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			profile := body["profile"].(map[string]any)
			assert.Equal(t, "New", profile["givenName"])
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"userId":"new-user-789"}`))
		case r.URL.Path == "/v2/sessions" && r.Method == "POST":
			// Create session
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"sessionId":"sess-new","sessionToken":"tok-new"}`))
		case r.URL.Path == "/v2/users/new-user-789" && r.Method == "GET":
			// Profile-completion probe — real name was provided by the IdP.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"user":{"human":{"profile":{"givenName":"New","familyName":"User"}}}}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	w, c := ginContext("POST", "/auth/idp/session", `{"idpIntentId":"intent-new","idpIntentToken":"tok-new"}`)
	CreateIdPSessionHandler(c)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "sess-new", resp["sessionId"])
	assert.Equal(t, false, resp["requiresProfile"])
}

// TestCreateIdPSessionHandler_NewUserNoName covers the Apple fix: when the IdP
// returns no name (Apple omits it on repeat authorizations), the handler must
// backfill a placeholder name so Zitadel accepts the user creation, and signal
// requiresProfile so the frontend collects the real name before finalize.
func TestCreateIdPSessionHandler_NewUserNoName(t *testing.T) {
	setupMockZitadelWithIdP(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/idp_intents/intent-apple" && r.Method == "POST":
			// IdP intent with NO profile — mirrors Apple on a repeat sign-in.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"addHumanUser":{"email":{"email":"relay@privaterelay.appleid.com"}},
				"idpInformation":{"idpId":"test-apple-idp","userId":"apple-sub-1","userName":"relay@privaterelay.appleid.com"}
			}`))
		case r.URL.Path == "/v2/users" && r.Method == "POST":
			// Email search — user not found.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":[]}`))
		case r.URL.Path == "/v2/users/human" && r.Method == "POST":
			// The missing name must be backfilled with the placeholder marker.
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			profile := body["profile"].(map[string]any)
			assert.Equal(t, placeholderProfileMarker, profile["givenName"])
			assert.Equal(t, placeholderProfileMarker, profile["familyName"])
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"userId":"apple-user-1"}`))
		case r.URL.Path == "/v2/sessions" && r.Method == "POST":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"sessionId":"sess-apple","sessionToken":"tok-apple"}`))
		case r.URL.Path == "/v2/users/apple-user-1" && r.Method == "GET":
			// Profile-completion probe — still the placeholder marker.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"user":{"human":{"profile":{"givenName":"-","familyName":"-"}}}}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	w, c := ginContext("POST", "/auth/idp/session", `{"idpIntentId":"intent-apple","idpIntentToken":"tok-apple"}`)
	CreateIdPSessionHandler(c)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "sess-apple", resp["sessionId"])
	// No name from the IdP → frontend must collect first/last name.
	assert.Equal(t, true, resp["requiresProfile"])
}

// TestCreateIdPSessionHandler_AlreadyLinkedIdP covers a repeat IdP login where
// the external identity is already linked to a Zitadel user (e.g. an incomplete
// first sign-in left a placeholder account). Zitadel returns the linked userId
// at the top level of the intent, and the handler must use it directly — never
// re-searching, re-linking, or re-creating (those would fail on the duplicate).
func TestCreateIdPSessionHandler_AlreadyLinkedIdP(t *testing.T) {
	setupMockZitadelWithIdP(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/idp_intents/intent-linked" && r.Method == "POST":
			// Already-linked identity → Zitadel includes the top-level userId.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"userId":"linked-user-1",
				"addHumanUser":{"profile":{"givenName":"Ann","familyName":"Lee"},"email":{"email":"ann@google.com"}},
				"idpInformation":{"idpId":"test-google-idp","userId":"google-sub-9","userName":"ann@google.com"}
			}`))
		case r.URL.Path == "/v2/sessions" && r.Method == "POST":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"sessionId":"sess-linked","sessionToken":"tok-linked"}`))
		case r.URL.Path == "/v2/users/linked-user-1" && r.Method == "GET":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"user":{"human":{"profile":{"givenName":"Ann","familyName":"Lee"}}}}`))
		default:
			// A search (/v2/users) or create (/v2/users/human) here means the
			// short-circuit failed and we took the fragile path.
			t.Errorf("unexpected request (should have used linked userId): %s %s", r.Method, r.URL.Path)
		}
	})

	w, c := ginContext("POST", "/auth/idp/session", `{"idpIntentId":"intent-linked","idpIntentToken":"tok-linked"}`)
	CreateIdPSessionHandler(c)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "sess-linked", resp["sessionId"])
	assert.Equal(t, false, resp["requiresProfile"])
}

func TestCreateIdPSessionHandler_MissingFields(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"missing idpIntentId", `{"idpIntentToken":"tok-abc"}`},
		{"missing idpIntentToken", `{"idpIntentId":"intent-abc"}`},
		{"empty body", `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, c := ginContext("POST", "/auth/idp/session", tt.body)
			CreateIdPSessionHandler(c)
			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	}
}
