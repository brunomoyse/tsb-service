package middleware

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	testIssuerHost = "zitadel.test"
	testClientID   = "api-client"
	testProjectID  = "project-1"
	testKeyID      = "key-1"
)

// fakeZitadel serves OIDC discovery and the JWKS for one RSA key. The
// verifier reaches it through the internal-URL transport, the way it reaches
// zitadel-api in Docker.
func fakeZitadel(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	issuer := "https://" + testIssuerHost
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                issuer,
			"jwks_uri":                              issuer + "/oauth/v2/keys",
			"authorization_endpoint":                issuer + "/oauth/v2/authorize",
			"token_endpoint":                        issuer + "/oauth/v2/token",
			"introspection_endpoint":                issuer + "/oauth/v2/introspect",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/oauth/v2/keys", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "use": "sig", "alg": "RS256", "kid": testKeyID,
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return key, srv.URL
}

func signToken(t *testing.T, key *rsa.PrivateKey, claims jwt.MapClaims) string {
	t.Helper()
	now := time.Now()
	base := jwt.MapClaims{
		"iss": "https://" + testIssuerHost,
		"sub": "393485125419008005",
		"iat": now.Unix(),
		"nbf": now.Unix(),
		"exp": now.Add(time.Hour).Unix(),
	}
	for k, v := range claims {
		base[k] = v
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, base)
	tok.Header["kid"] = testKeyID
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestVerifyTokenAudiences(t *testing.T) {
	key, internalURL := fakeZitadel(t)
	v, err := NewOIDCVerifier(context.Background(), "https://"+testIssuerHost, internalURL, testClientID, testProjectID, nil)
	if err != nil {
		t.Fatalf("NewOIDCVerifier: %v", err)
	}
	v.SetAdminClientIDs([]string{"dashboard", "tsb-mcp"})
	roles := map[string]any{"admin": map[string]any{"org": "domain"}}

	tests := []struct {
		name      string
		claims    jwt.MapClaims
		wantErr   bool
		wantAdmin bool
	}{
		{
			name: "interactive login: aud holds every app client id",
			claims: jwt.MapClaims{"aud": []string{testProjectID, testClientID, "dashboard"}, "client_id": "dashboard",
				"urn:zitadel:iam:org:project:roles": roles},
			wantAdmin: true,
		},
		{
			name: "machine user: aud holds only the project id",
			claims: jwt.MapClaims{"aud": []string{testProjectID}, "client_id": "tsb-mcp",
				"urn:zitadel:iam:org:project:" + testProjectID + ":roles": roles},
			wantAdmin: true,
		},
		{
			name:    "other audience is refused",
			claims:  jwt.MapClaims{"aud": []string{"other-project"}, "client_id": "tsb-mcp"},
			wantErr: true,
		},
		{
			name: "project audience does not bypass the admin client allowlist",
			claims: jwt.MapClaims{"aud": []string{testProjectID}, "client_id": "customer-app",
				"urn:zitadel:iam:org:project:" + testProjectID + ":roles": roles},
			wantAdmin: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sub, isAdmin, isPOS, _, err := v.VerifyToken(context.Background(), signToken(t, key, tt.claims))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("VerifyToken accepted the token (sub %q)", sub)
				}
				return
			}
			if err != nil {
				t.Fatalf("VerifyToken: %v", err)
			}
			if sub != "393485125419008005" || isPOS {
				t.Errorf("sub = %q, isPOS = %v", sub, isPOS)
			}
			if isAdmin != tt.wantAdmin {
				t.Errorf("isAdmin = %v, want %v", isAdmin, tt.wantAdmin)
			}
		})
	}
}
