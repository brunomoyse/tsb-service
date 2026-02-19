package testhelpers

import (
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/google/uuid"

	"tsb-service/internal/modules/user/domain"
)

// TestJWTSecret is a known secret for testing
const TestJWTSecret = "test-secret-key-for-testing-only"

// GenerateTestAccessToken creates a valid JWT access token for testing
func GenerateTestAccessToken(userID string, isAdmin bool) (string, error) {
	return generateTestToken(userID, isAdmin, "access", 15*time.Minute)
}

// GenerateTestRefreshToken creates a valid JWT refresh token for testing
func GenerateTestRefreshToken(userID string, isAdmin bool) (string, error) {
	return generateTestToken(userID, isAdmin, "refresh", 7*24*time.Hour)
}

// generateTestToken is an internal helper to generate JWT tokens for testing
func generateTestToken(userID string, isAdmin bool, tokenType string, duration time.Duration) (string, error) {
	// Create token claims with IsAdmin custom field
	claims := domain.JwtClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(duration)),
		},
		Type:    tokenType,
		ID:      uuid.NewString(),
		IsAdmin: isAdmin,
	}

	// Create and sign the token
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signedToken, err := token.SignedString([]byte(TestJWTSecret))
	if err != nil {
		return "", err
	}

	return signedToken, nil
}

// GenerateExpiredToken creates an expired JWT token for testing auth failure cases
func GenerateExpiredToken(userID string, isAdmin bool) (string, error) {
	claims := domain.JwtClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-1 * time.Hour)), // Expired 1 hour ago
		},
		Type:    "access",
		ID:      uuid.NewString(),
		IsAdmin: isAdmin,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signedToken, err := token.SignedString([]byte(TestJWTSecret))
	if err != nil {
		return "", err
	}

	return signedToken, nil
}

// ParseTestToken parses a JWT token and extracts userID and isAdmin status
func ParseTestToken(tokenString string, jwtSecret string) (string, bool, error) {
	token, err := jwt.ParseWithClaims(tokenString, &domain.JwtClaims{}, func(token *jwt.Token) (interface{}, error) {
		return []byte(jwtSecret), nil
	})

	if err != nil {
		return "", false, err
	}

	if claims, ok := token.Claims.(*domain.JwtClaims); ok && token.Valid {
		return claims.Subject, claims.IsAdmin, nil
	}

	return "", false, jwt.ErrSignatureInvalid
}
