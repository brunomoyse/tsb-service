package auth

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- ChangePasswordHandler Tests ---

func TestChangePasswordHandler_Success(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v2/users/zitadel-sub-123/password", r.URL.Path)
		assert.Equal(t, "PUT", r.Method)
		assert.Equal(t, "Bearer test-admin-pat", r.Header.Get("Authorization"))

		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "OldP@ss1", body["currentPassword"])
		newPwd := body["newPassword"].(map[string]any)
		assert.Equal(t, "NewP@ss2", newPwd["password"])

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})

	w, c := ginContext("POST", "/auth/change-password", `{"currentPassword":"OldP@ss1","newPassword":"NewP@ss2"}`)
	setAuthContext(c, "zitadel-sub-123")
	ChangePasswordHandler(c)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["success"])
}

func TestChangePasswordHandler_WrongPassword(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":3,"message":"password invalid (COMMAND-3M0fs)"}`))
	})

	w, c := ginContext("POST", "/auth/change-password", `{"currentPassword":"wrong","newPassword":"NewP@ss2"}`)
	setAuthContext(c, "zitadel-sub-123")
	ChangePasswordHandler(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "wrong_password", resp["error"])
}

func TestChangePasswordHandler_WeakPassword(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":3,"message":"password complexity requirements not met (COMMAND-oz74F)"}`))
	})

	w, c := ginContext("POST", "/auth/change-password", `{"currentPassword":"OldP@ss1","newPassword":"weak"}`)
	setAuthContext(c, "zitadel-sub-123")
	ChangePasswordHandler(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "weak_password", resp["error"])
}

func TestChangePasswordHandler_NotAuthenticated(t *testing.T) {
	w, c := ginContext("POST", "/auth/change-password", `{"currentPassword":"OldP@ss1","newPassword":"NewP@ss2"}`)
	// No setAuthContext — simulates unauthenticated request
	ChangePasswordHandler(c)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestChangePasswordHandler_MissingFields(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"missing currentPassword", `{"newPassword":"NewP@ss2"}`},
		{"missing newPassword", `{"currentPassword":"OldP@ss1"}`},
		{"empty body", `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, c := ginContext("POST", "/auth/change-password", tt.body)
			setAuthContext(c, "zitadel-sub-123")
			ChangePasswordHandler(c)
			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	}
}

// --- HasPasswordHandler Tests ---

func TestHasPasswordHandler_HasPassword(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v2/users/zitadel-sub-456", r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"user":{"human":{"passwordChanged":"2026-01-15T10:30:00Z"}}}`))
	})

	w, c := ginContext("GET", "/auth/has-password", "")
	setAuthContext(c, "zitadel-sub-456")
	HasPasswordHandler(c)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["hasPassword"])
}

func TestHasPasswordHandler_NoPassword(t *testing.T) {
	setupMockZitadel(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"user":{"human":{"passwordChanged":"0001-01-01T00:00:00Z"}}}`))
	})

	w, c := ginContext("GET", "/auth/has-password", "")
	setAuthContext(c, "zitadel-sub-456")
	HasPasswordHandler(c)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, false, resp["hasPassword"])
}

func TestHasPasswordHandler_NotAuthenticated(t *testing.T) {
	w, c := ginContext("GET", "/auth/has-password", "")
	HasPasswordHandler(c)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
