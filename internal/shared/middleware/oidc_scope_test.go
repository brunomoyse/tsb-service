package middleware

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"tsb-service/pkg/utils"
)

func TestClientMayBeAdmin(t *testing.T) {
	const dashboard = "dashboard-client"

	t.Run("no allowlist keeps legacy behavior", func(t *testing.T) {
		v := &OIDCVerifier{}
		v.SetAdminClientIDs([]string{"", " "})
		if !v.clientMayBeAdmin(map[string]any{"client_id": "customer-client"}) {
			t.Fatal("expected any client to pass when no allowlist is configured")
		}
	})

	v := &OIDCVerifier{}
	v.SetAdminClientIDs([]string{" " + dashboard + " ", "other-staff-client"})

	cases := []struct {
		name   string
		claims map[string]any
		want   bool
	}{
		{"dashboard client_id", map[string]any{"client_id": dashboard}, true},
		{"dashboard azp fallback", map[string]any{"azp": dashboard}, true},
		{"customer client", map[string]any{"client_id": "customer-client"}, false},
		{"client_id wins over azp", map[string]any{"client_id": "customer-client", "azp": dashboard}, false},
		{"no client claim", map[string]any{}, false},
		{"nil claims", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := v.clientMayBeAdmin(tc.claims); got != tc.want {
				t.Fatalf("clientMayBeAdmin(%v) = %v, want %v", tc.claims, got, tc.want)
			}
		})
	}
}

type stubAppJWT struct{ deviceID uuid.UUID }

func (s stubAppJWT) VerifyAccessToken(context.Context, string) (uuid.UUID, error) {
	return s.deviceID, nil
}
func (s stubAppJWT) AccessTokenExpiry(string) time.Time { return time.Time{} }

func TestPOSTokenGetsStaffScopeNotAdmin(t *testing.T) {
	deviceID := uuid.New()
	v := &OIDCVerifier{appJWT: stubAppJWT{deviceID: deviceID}}

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/graphql", nil)

	if !v.tryVerifyAppJWT(c, "pos-token") {
		t.Fatal("expected POS token to verify")
	}
	ctx := c.Request.Context()
	if utils.GetIsAdmin(ctx) {
		t.Fatal("POS device must not get admin scope")
	}
	if !utils.GetIsPOS(ctx) || !utils.GetIsStaff(ctx) {
		t.Fatal("POS device must get staff scope")
	}
	if utils.GetUserID(ctx) != deviceID.String() {
		t.Fatalf("userID = %q, want device ID", utils.GetUserID(ctx))
	}
}
