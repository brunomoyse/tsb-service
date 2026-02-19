package interfaces

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func TestGetSameSiteMode(t *testing.T) {
	t.Run("Development mode returns SameSiteNoneMode", func(t *testing.T) {
		os.Setenv("APP_ENV", "development")
		defer os.Unsetenv("APP_ENV")

		mode := getSameSiteMode()
		assert.Equal(t, http.SameSiteNoneMode, mode)
	})

	t.Run("Production mode returns SameSiteLaxMode", func(t *testing.T) {
		os.Setenv("APP_ENV", "production")
		defer os.Unsetenv("APP_ENV")

		mode := getSameSiteMode()
		assert.Equal(t, http.SameSiteLaxMode, mode)
	})

	t.Run("Empty APP_ENV returns SameSiteLaxMode", func(t *testing.T) {
		os.Unsetenv("APP_ENV")

		mode := getSameSiteMode()
		assert.Equal(t, http.SameSiteLaxMode, mode)
	})
}

func TestSetAuthCookies(t *testing.T) {
	os.Setenv("SESSION_COOKIE_DOMAIN", ".example.com")
	os.Setenv("APP_ENV", "production")
	defer os.Unsetenv("SESSION_COOKIE_DOMAIN")
	defer os.Unsetenv("APP_ENV")

	t.Run("Sets access and refresh cookies", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/login", nil)

		setAuthCookies(c, "access-token-value", "refresh-token-value")

		cookies := w.Result().Cookies()
		assert.Len(t, cookies, 2)

		var accessCookie, refreshCookie *http.Cookie
		for _, cookie := range cookies {
			switch cookie.Name {
			case "access_token":
				accessCookie = cookie
			case "refresh_token":
				refreshCookie = cookie
			}
		}

		assert.NotNil(t, accessCookie, "access_token cookie should be set")
		assert.Equal(t, "access-token-value", accessCookie.Value)
		assert.Equal(t, "/", accessCookie.Path)
		assert.Contains(t, accessCookie.Domain, "example.com")
		assert.True(t, accessCookie.Secure)
		assert.True(t, accessCookie.HttpOnly)
		assert.Equal(t, 15*60, accessCookie.MaxAge)

		assert.NotNil(t, refreshCookie, "refresh_token cookie should be set")
		assert.Equal(t, "refresh-token-value", refreshCookie.Value)
		assert.Equal(t, 7*24*3600, refreshCookie.MaxAge)
	})
}

func TestClearAuthCookies(t *testing.T) {
	os.Setenv("SESSION_COOKIE_DOMAIN", ".example.com")
	os.Setenv("APP_ENV", "production")
	defer os.Unsetenv("SESSION_COOKIE_DOMAIN")
	defer os.Unsetenv("APP_ENV")

	t.Run("Clears access and refresh cookies", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/logout", nil)

		clearAuthCookies(c)

		cookies := w.Result().Cookies()
		assert.Len(t, cookies, 2)

		for _, cookie := range cookies {
			assert.Empty(t, cookie.Value, "Cookie %s should have empty value", cookie.Name)
			assert.True(t, cookie.MaxAge < 0, "Cookie %s should have negative MaxAge", cookie.Name)
			assert.Contains(t, cookie.Domain, "example.com")
			assert.True(t, cookie.Secure)
			assert.True(t, cookie.HttpOnly)
		}
	})
}

func TestSameSiteModeDevelopment(t *testing.T) {
	os.Setenv("SESSION_COOKIE_DOMAIN", "localhost")
	os.Setenv("APP_ENV", "development")
	defer os.Unsetenv("SESSION_COOKIE_DOMAIN")
	defer os.Unsetenv("APP_ENV")

	t.Run("Development mode sets SameSite=None on cookies", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/login", nil)

		setAuthCookies(c, "test-access", "test-refresh")

		// Check the raw Set-Cookie header for SameSite=None
		setCookieHeaders := w.Header().Values("Set-Cookie")
		assert.Len(t, setCookieHeaders, 2)
		for _, header := range setCookieHeaders {
			assert.Contains(t, header, "SameSite=None")
		}
	})
}
