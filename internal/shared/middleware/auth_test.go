package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/pkg/types"
	"tsb-service/pkg/utils"
)

const testSecret = "test-middleware-secret"

func init() {
	gin.SetMode(gin.TestMode)
}

// generateToken creates a test JWT with IsAdmin claim
func generateToken(userID string, isAdmin bool, tokenType string, duration time.Duration) (string, error) {
	claims := types.JwtClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(duration)),
		},
		Type:    tokenType,
		ID:      uuid.NewString(),
		IsAdmin: isAdmin,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(testSecret))
}

func TestAuthMiddleware_IsAdmin(t *testing.T) {
	t.Run("Admin token sets isAdmin=true in context", func(t *testing.T) {
		userID := uuid.NewString()
		token, err := generateToken(userID, true, "access", 15*time.Minute)
		require.NoError(t, err)

		var capturedIsAdmin bool
		var capturedUserID string

		router := gin.New()
		router.Use(AuthMiddleware(testSecret))
		router.GET("/test", func(c *gin.Context) {
			capturedUserID = utils.GetUserID(c.Request.Context())
			capturedIsAdmin = utils.GetIsAdmin(c.Request.Context())
			c.Status(http.StatusOK)
		})

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, userID, capturedUserID)
		assert.True(t, capturedIsAdmin, "Admin token should set isAdmin=true")
	})

	t.Run("Non-admin token sets isAdmin=false in context", func(t *testing.T) {
		userID := uuid.NewString()
		token, err := generateToken(userID, false, "access", 15*time.Minute)
		require.NoError(t, err)

		var capturedIsAdmin bool

		router := gin.New()
		router.Use(AuthMiddleware(testSecret))
		router.GET("/test", func(c *gin.Context) {
			capturedIsAdmin = utils.GetIsAdmin(c.Request.Context())
			c.Status(http.StatusOK)
		})

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.False(t, capturedIsAdmin, "Non-admin token should set isAdmin=false")
	})

	t.Run("Missing token returns 401", func(t *testing.T) {
		router := gin.New()
		router.Use(AuthMiddleware(testSecret))
		router.GET("/test", func(c *gin.Context) {
			c.Status(http.StatusOK)
		})

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("Expired token returns 401", func(t *testing.T) {
		token, err := generateToken(uuid.NewString(), false, "access", -1*time.Hour)
		require.NoError(t, err)

		router := gin.New()
		router.Use(AuthMiddleware(testSecret))
		router.GET("/test", func(c *gin.Context) {
			c.Status(http.StatusOK)
		})

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("Token from cookie works", func(t *testing.T) {
		userID := uuid.NewString()
		token, err := generateToken(userID, true, "access", 15*time.Minute)
		require.NoError(t, err)

		var capturedUserID string
		var capturedIsAdmin bool

		router := gin.New()
		router.Use(AuthMiddleware(testSecret))
		router.GET("/test", func(c *gin.Context) {
			capturedUserID = utils.GetUserID(c.Request.Context())
			capturedIsAdmin = utils.GetIsAdmin(c.Request.Context())
			c.Status(http.StatusOK)
		})

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.AddCookie(&http.Cookie{Name: "access_token", Value: token})
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, userID, capturedUserID)
		assert.True(t, capturedIsAdmin)
	})

	t.Run("Invalid signature returns 401", func(t *testing.T) {
		token, err := generateToken(uuid.NewString(), false, "access", 15*time.Minute)
		require.NoError(t, err)

		router := gin.New()
		router.Use(AuthMiddleware("wrong-secret"))
		router.GET("/test", func(c *gin.Context) {
			c.Status(http.StatusOK)
		})

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})
}
