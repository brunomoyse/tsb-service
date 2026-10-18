package testhelpers

import (
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/google/uuid"
)

// TestJWTSecret is a known secret for testing
const TestJWTSecret = "test-secret-key-for-testing-only"

// TestAppClientID is the OIDC client of the mobile app in tests: a token carrying it marks the
// request as coming from the app, as the production auth middleware does for the native client.
const TestAppClientID = "test-native-client"

// testJwtClaims defines JWT claims used in test tokens.
type testJwtClaims struct {
	jwt.RegisteredClaims
	Type     string `json:"type"`
	ID       string `json:"jti"`
	IsAdmin  bool   `json:"is_admin"`
	ClientID string `json:"client_id,omitempty"`
}

// GenerateTestAccessToken creates a valid JWT access token for testing
func GenerateTestAccessToken(userID string, isAdmin bool) (string, error) {
	return generateTestToken(userID, isAdmin, "access", 1*time.Hour)
}

// GenerateTestAppAccessToken creates a customer access token issued to the mobile app client.
func GenerateTestAppAccessToken(userID string) (string, error) {
	claims := testJwtClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
		Type:     "access",
		ID:       uuid.NewString(),
		ClientID: TestAppClientID,
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(TestJWTSecret))
}

// GenerateTestRefreshToken creates a valid JWT refresh token for testing
func GenerateTestRefreshToken(userID string, isAdmin bool) (string, error) {
	return generateTestToken(userID, isAdmin, "refresh", 30*24*time.Hour)
}

func generateTestToken(userID string, isAdmin bool, tokenType string, duration time.Duration) (string, error) {
	claims := testJwtClaims{
		Subject:   userID,
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(duration)),
		Type:      tokenType,
		ID:        uuid.NewString(),
		IsAdmin:   isAdmin,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(TestJWTSecret))
}

// GenerateExpiredToken creates an expired JWT token for testing auth failure cases
func GenerateExpiredToken(userID string, isAdmin bool) (string, error) {
	claims := testJwtClaims{
		Subject:   userID,
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(-1 * time.Hour)),
		Type:      "access",
		ID:        uuid.NewString(),
		IsAdmin:   isAdmin,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(TestJWTSecret))
}

// ParseTestToken parses a JWT token and extracts userID and isAdmin status
func ParseTestToken(tokenString string, jwtSecret string) (string, bool, error) {
	claims, err := parseTestClaims(tokenString, jwtSecret)
	if err != nil {
		return "", false, err
	}
	return claims.Subject, claims.IsAdmin, nil
}

// testTokenIsApp reports whether a valid test token was issued to the mobile app client.
func testTokenIsApp(tokenString string, jwtSecret string) bool {
	claims, err := parseTestClaims(tokenString, jwtSecret)
	return err == nil && claims.ClientID == TestAppClientID
}

func parseTestClaims(tokenString string, jwtSecret string) (*testJwtClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &testJwtClaims{}, func(token *jwt.Token) (any, error) {
		return []byte(jwtSecret), nil
	})

	if err != nil {
		return nil, err
	}

	if claims, ok := token.Claims.(*testJwtClaims); ok && token.Valid {
		return claims, nil
	}

	return nil, jwt.ErrSignatureInvalid
}
