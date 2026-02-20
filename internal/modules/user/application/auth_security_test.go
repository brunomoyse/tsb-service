package application

import (
	"crypto/subtle"
	"testing"

	"github.com/golang-jwt/jwt/v4"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/user/domain"
)

const testJWTSecret = "test-secret-key-for-security-tests"

func TestValidatePasswordStrength(t *testing.T) {
	t.Run("Valid password passes", func(t *testing.T) {
		err := validatePasswordStrength("Str0ng!Pass")
		assert.NoError(t, err)
	})

	t.Run("Too short", func(t *testing.T) {
		err := validatePasswordStrength("Aa1!")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "at least 8 characters")
	})

	t.Run("Missing uppercase", func(t *testing.T) {
		err := validatePasswordStrength("lowercase1!")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "uppercase")
	})

	t.Run("Missing lowercase", func(t *testing.T) {
		err := validatePasswordStrength("UPPERCASE1!")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "lowercase")
	})

	t.Run("Missing digit", func(t *testing.T) {
		err := validatePasswordStrength("NoDigits!!")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "digit")
	})

	t.Run("Missing special character", func(t *testing.T) {
		err := validatePasswordStrength("NoSpecial1A")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "special character")
	})

	t.Run("All requirements met", func(t *testing.T) {
		err := validatePasswordStrength("MyP@ssw0rd")
		assert.NoError(t, err)
	})
}

func TestConstantTimePasswordComparison(t *testing.T) {
	salt := "dGVzdC1zYWx0LWJhc2U2NA==" // base64("test-salt-base64")

	t.Run("Correct password matches", func(t *testing.T) {
		hash, err := hashPassword("correctPassword", salt)
		require.NoError(t, err)
		require.NotEmpty(t, hash)

		rehash, err := hashPassword("correctPassword", salt)
		require.NoError(t, err)
		result := subtle.ConstantTimeCompare([]byte(hash), []byte(rehash))
		assert.Equal(t, 1, result, "Same password should produce matching hashes")
	})

	t.Run("Wrong password does not match", func(t *testing.T) {
		hash, err := hashPassword("correctPassword", salt)
		require.NoError(t, err)
		wrongHash, err := hashPassword("wrongPassword", salt)
		require.NoError(t, err)

		result := subtle.ConstantTimeCompare([]byte(hash), []byte(wrongHash))
		assert.Equal(t, 0, result, "Different passwords should not match")
	})

	t.Run("Empty password produces hash", func(t *testing.T) {
		hash, err := hashPassword("", salt)
		require.NoError(t, err)
		assert.NotEmpty(t, hash, "Empty password should still produce a hash")
	})

	t.Run("Invalid salt returns error", func(t *testing.T) {
		_, err := hashPassword("password", "not-valid-base64!!!")
		assert.Error(t, err, "Invalid base64 salt should return an error")
	})
}

func TestGenerateTokensIsAdminClaim(t *testing.T) {
	t.Run("Admin user token has IsAdmin=true", func(t *testing.T) {
		user := domain.User{
			ID:      uuid.New(),
			IsAdmin: true,
		}

		accessToken, refreshToken, err := generateTokens(user, testJWTSecret)
		require.NoError(t, err)
		require.NotEmpty(t, accessToken)
		require.NotEmpty(t, refreshToken)

		// Parse access token and verify IsAdmin
		claims := &domain.JwtClaims{}
		token, err := jwt.ParseWithClaims(accessToken, claims, func(t *jwt.Token) (interface{}, error) {
			return []byte(testJWTSecret), nil
		})
		require.NoError(t, err)
		require.True(t, token.Valid)
		assert.True(t, claims.IsAdmin, "Admin user token should have IsAdmin=true")
		assert.Equal(t, "access", claims.Type)
		assert.Equal(t, user.ID.String(), claims.Subject)

		// Parse refresh token and verify IsAdmin
		refreshClaims := &domain.JwtClaims{}
		rToken, err := jwt.ParseWithClaims(refreshToken, refreshClaims, func(t *jwt.Token) (interface{}, error) {
			return []byte(testJWTSecret), nil
		})
		require.NoError(t, err)
		require.True(t, rToken.Valid)
		assert.True(t, refreshClaims.IsAdmin, "Admin refresh token should have IsAdmin=true")
		assert.Equal(t, "refresh", refreshClaims.Type)
	})

	t.Run("Regular user token has IsAdmin=false", func(t *testing.T) {
		user := domain.User{
			ID:      uuid.New(),
			IsAdmin: false,
		}

		accessToken, _, err := generateTokens(user, testJWTSecret)
		require.NoError(t, err)

		claims := &domain.JwtClaims{}
		token, err := jwt.ParseWithClaims(accessToken, claims, func(t *jwt.Token) (interface{}, error) {
			return []byte(testJWTSecret), nil
		})
		require.NoError(t, err)
		require.True(t, token.Valid)
		assert.False(t, claims.IsAdmin, "Regular user token should have IsAdmin=false")
	})

	t.Run("Tokens do not use Audience for admin", func(t *testing.T) {
		user := domain.User{
			ID:      uuid.New(),
			IsAdmin: true,
		}

		accessToken, _, err := generateTokens(user, testJWTSecret)
		require.NoError(t, err)

		claims := &domain.JwtClaims{}
		_, err = jwt.ParseWithClaims(accessToken, claims, func(t *jwt.Token) (interface{}, error) {
			return []byte(testJWTSecret), nil
		})
		require.NoError(t, err)
		assert.Empty(t, claims.Audience, "Admin flag should not be in Audience field")
	})

	t.Run("Access and refresh tokens have unique JTIs", func(t *testing.T) {
		user := domain.User{
			ID:      uuid.New(),
			IsAdmin: false,
		}

		accessToken, refreshToken, err := generateTokens(user, testJWTSecret)
		require.NoError(t, err)

		accessClaims := &domain.JwtClaims{}
		_, err = jwt.ParseWithClaims(accessToken, accessClaims, func(t *jwt.Token) (interface{}, error) {
			return []byte(testJWTSecret), nil
		})
		require.NoError(t, err)

		refreshClaims := &domain.JwtClaims{}
		_, err = jwt.ParseWithClaims(refreshToken, refreshClaims, func(t *jwt.Token) (interface{}, error) {
			return []byte(testJWTSecret), nil
		})
		require.NoError(t, err)

		assert.NotEmpty(t, accessClaims.ID)
		assert.NotEmpty(t, refreshClaims.ID)
		assert.NotEqual(t, accessClaims.ID, refreshClaims.ID, "Access and refresh tokens should have different JTIs")
	})
}
