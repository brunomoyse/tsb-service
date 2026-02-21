package interfaces

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	addressApplication "tsb-service/internal/modules/address/application"
	addressDomain "tsb-service/internal/modules/address/domain"
	"tsb-service/internal/modules/user/application"
	"tsb-service/internal/modules/user/domain"
)

func init() {
	gin.SetMode(gin.TestMode)
}

const testJWTSecret = "test-jwt-secret-for-handler-tests"

// --- Mock UserService ---

type mockUserService struct {
	// Store return values for each method
	createUserFn             func(ctx context.Context, firstName, lastName, email string, phoneNumber, addressID, password, googleID *string) (*domain.User, error)
	loginFn                  func(ctx context.Context, email, password, jwtToken string) (*domain.User, *string, *string, error)
	getUserByIDFn            func(ctx context.Context, id string) (*domain.User, error)
	getUserByEmailFn         func(ctx context.Context, email string) (*domain.User, error)
	getUserByGoogleIDFn      func(ctx context.Context, googleID string) (*domain.User, error)
	generateTokensFn         func(ctx context.Context, user domain.User, jwtToken string) (string, string, error)
	refreshTokenFn           func(ctx context.Context, oldRefreshToken, jwtSecret string) (string, string, *domain.User, error)
	updateGoogleIDFn         func(ctx context.Context, userID, googleID string) (*domain.User, error)
	updateMeFn               func(ctx context.Context, userID string, firstName, lastName, email, phoneNumber, addressID *string) (*domain.User, error)
	updateUserPasswordFn     func(ctx context.Context, userID, password, salt string) (*domain.User, error)
	updateEmailVerifiedAtFn  func(ctx context.Context, userID string) (*domain.User, error)
	invalidateRefreshTokenFn func(ctx context.Context, refreshToken string) error
	verifyUserEmailFn        func(ctx context.Context, userID string) error
	requestPasswordResetFn   func(ctx context.Context, email string) error
	resetPasswordFn          func(ctx context.Context, token, newPassword string) error
}

func (m *mockUserService) CreateUser(ctx context.Context, firstName, lastName, email string, phoneNumber, addressID, password, googleID *string) (*domain.User, error) {
	if m.createUserFn != nil {
		return m.createUserFn(ctx, firstName, lastName, email, phoneNumber, addressID, password, googleID)
	}
	return nil, fmt.Errorf("not implemented")
}

func (m *mockUserService) Login(ctx context.Context, email, password, jwtToken string) (*domain.User, *string, *string, error) {
	if m.loginFn != nil {
		return m.loginFn(ctx, email, password, jwtToken)
	}
	return nil, nil, nil, fmt.Errorf("not implemented")
}

func (m *mockUserService) GetUserByID(ctx context.Context, id string) (*domain.User, error) {
	if m.getUserByIDFn != nil {
		return m.getUserByIDFn(ctx, id)
	}
	return nil, sql.ErrNoRows
}

func (m *mockUserService) GetUserByEmail(ctx context.Context, email string) (*domain.User, error) {
	if m.getUserByEmailFn != nil {
		return m.getUserByEmailFn(ctx, email)
	}
	return nil, sql.ErrNoRows
}

func (m *mockUserService) GetUserByGoogleID(ctx context.Context, googleID string) (*domain.User, error) {
	if m.getUserByGoogleIDFn != nil {
		return m.getUserByGoogleIDFn(ctx, googleID)
	}
	return nil, sql.ErrNoRows
}

func (m *mockUserService) GenerateTokens(ctx context.Context, user domain.User, jwtToken string) (string, string, error) {
	if m.generateTokensFn != nil {
		return m.generateTokensFn(ctx, user, jwtToken)
	}
	return "", "", fmt.Errorf("not implemented")
}

func (m *mockUserService) RefreshToken(ctx context.Context, oldRefreshToken, jwtSecret string) (string, string, *domain.User, error) {
	if m.refreshTokenFn != nil {
		return m.refreshTokenFn(ctx, oldRefreshToken, jwtSecret)
	}
	return "", "", nil, fmt.Errorf("not implemented")
}

func (m *mockUserService) UpdateGoogleID(ctx context.Context, userID, googleID string) (*domain.User, error) {
	if m.updateGoogleIDFn != nil {
		return m.updateGoogleIDFn(ctx, userID, googleID)
	}
	return nil, fmt.Errorf("not implemented")
}

func (m *mockUserService) UpdateMe(ctx context.Context, userID string, firstName, lastName, email, phoneNumber, addressID *string) (*domain.User, error) {
	if m.updateMeFn != nil {
		return m.updateMeFn(ctx, userID, firstName, lastName, email, phoneNumber, addressID)
	}
	return nil, fmt.Errorf("not implemented")
}

func (m *mockUserService) UpdateUserPassword(ctx context.Context, userID, password, salt string) (*domain.User, error) {
	if m.updateUserPasswordFn != nil {
		return m.updateUserPasswordFn(ctx, userID, password, salt)
	}
	return nil, fmt.Errorf("not implemented")
}

func (m *mockUserService) UpdateEmailVerifiedAt(ctx context.Context, userID string) (*domain.User, error) {
	if m.updateEmailVerifiedAtFn != nil {
		return m.updateEmailVerifiedAtFn(ctx, userID)
	}
	return nil, fmt.Errorf("not implemented")
}

func (m *mockUserService) InvalidateRefreshToken(ctx context.Context, refreshToken string) error {
	if m.invalidateRefreshTokenFn != nil {
		return m.invalidateRefreshTokenFn(ctx, refreshToken)
	}
	return nil
}

func (m *mockUserService) VerifyUserEmail(ctx context.Context, userID string) error {
	if m.verifyUserEmailFn != nil {
		return m.verifyUserEmailFn(ctx, userID)
	}
	return nil
}

func (m *mockUserService) RequestPasswordReset(ctx context.Context, email string) error {
	if m.requestPasswordResetFn != nil {
		return m.requestPasswordResetFn(ctx, email)
	}
	return nil
}

func (m *mockUserService) ResetPassword(ctx context.Context, token, newPassword string) error {
	if m.resetPasswordFn != nil {
		return m.resetPasswordFn(ctx, token, newPassword)
	}
	return nil
}

func (m *mockUserService) RequestDeletion(ctx context.Context, userID string) (*domain.User, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockUserService) CancelDeletionRequest(ctx context.Context, userID string) (*domain.User, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockUserService) BatchGetUsersByOrderIDs(ctx context.Context, orderIDs []string) (map[string][]*domain.User, error) {
	return nil, fmt.Errorf("not implemented")
}

// Compile-time check
var _ application.UserService = (*mockUserService)(nil)

// --- Mock AddressService ---

type mockAddressService struct{}

func (m *mockAddressService) SearchStreetNames(_ context.Context, _ string) ([]*addressDomain.Street, error) {
	return nil, nil
}
func (m *mockAddressService) GetDistinctHouseNumbers(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}
func (m *mockAddressService) GetBoxNumbers(_ context.Context, _ string, _ string) ([]*string, error) {
	return nil, nil
}
func (m *mockAddressService) GetFinalAddress(_ context.Context, _ string, _ string, _ *string) (*addressDomain.Address, error) {
	return nil, nil
}
func (m *mockAddressService) GetAddressByID(_ context.Context, _ string) (*addressDomain.Address, error) {
	return nil, nil
}
func (m *mockAddressService) BatchGetAddressesByOrderIDs(_ context.Context, _ []string) (map[string][]*addressDomain.Address, error) {
	return nil, nil
}
func (m *mockAddressService) BatchGetAddressesByUserIDs(_ context.Context, _ []string) (map[string][]*addressDomain.Address, error) {
	return nil, nil
}

var _ addressApplication.AddressService = (*mockAddressService)(nil)

// --- Helper ---

func newTestHandler(svc *mockUserService) *UserHandler {
	return NewUserHandler(svc, &mockAddressService{}, testJWTSecret)
}

func testUser() *domain.User {
	now := time.Now()
	return &domain.User{
		ID:              uuid.New(),
		FirstName:       "Jane",
		LastName:        "Doe",
		Email:           "jane@example.com",
		EmailVerifiedAt: &now,
		IsAdmin:         false,
	}
}

// --- Tests ---

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

		setCookieHeaders := w.Header().Values("Set-Cookie")
		assert.Len(t, setCookieHeaders, 2)
		for _, header := range setCookieHeaders {
			assert.Contains(t, header, "SameSite=None")
		}
	})
}

// --- Login Handler Tests ---

func TestLoginHandler(t *testing.T) {
	os.Setenv("SESSION_COOKIE_DOMAIN", ".example.com")
	os.Setenv("APP_ENV", "production")
	defer os.Unsetenv("SESSION_COOKIE_DOMAIN")
	defer os.Unsetenv("APP_ENV")

	t.Run("Successful login sets cookies and returns user", func(t *testing.T) {
		user := testUser()
		accessToken := "test-access-token"
		refreshToken := "test-refresh-token"

		svc := &mockUserService{
			loginFn: func(_ context.Context, email, password, _ string) (*domain.User, *string, *string, error) {
				if email == "jane@example.com" && password == "MyP@ssw0rd" {
					return user, &accessToken, &refreshToken, nil
				}
				return nil, nil, nil, fmt.Errorf("invalid credentials")
			},
		}
		handler := newTestHandler(svc)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		body := `{"email":"jane@example.com","password":"MyP@ssw0rd"}`
		c.Request = httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")

		handler.LoginHandler(c)

		assert.Equal(t, http.StatusOK, w.Code)

		// Verify cookies are set
		cookies := w.Result().Cookies()
		var hasAccess, hasRefresh bool
		for _, cookie := range cookies {
			if cookie.Name == "access_token" && cookie.Value == accessToken {
				hasAccess = true
			}
			if cookie.Name == "refresh_token" && cookie.Value == refreshToken {
				hasRefresh = true
			}
		}
		assert.True(t, hasAccess, "access_token cookie should be set")
		assert.True(t, hasRefresh, "refresh_token cookie should be set")

		// Verify response body contains user
		var resp LoginResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, user.ID, resp.User.ID)
		assert.Equal(t, "jane@example.com", resp.User.Email)
	})

	t.Run("Invalid credentials returns 401", func(t *testing.T) {
		svc := &mockUserService{
			loginFn: func(_ context.Context, _, _, _ string) (*domain.User, *string, *string, error) {
				return nil, nil, nil, fmt.Errorf("invalid credentials")
			},
		}
		handler := newTestHandler(svc)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		body := `{"email":"wrong@example.com","password":"badpass"}`
		c.Request = httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")

		handler.LoginHandler(c)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("Invalid JSON returns 400", func(t *testing.T) {
		svc := &mockUserService{}
		handler := newTestHandler(svc)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("{invalid"))
		c.Request.Header.Set("Content-Type", "application/json")

		handler.LoginHandler(c)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

// --- Register Handler Tests ---

func TestRegisterHandler(t *testing.T) {
	t.Run("Successful registration returns user", func(t *testing.T) {
		user := testUser()

		svc := &mockUserService{
			createUserFn: func(_ context.Context, firstName, lastName, email string, _, _, password, _ *string) (*domain.User, error) {
				assert.Equal(t, "Jane", firstName)
				assert.Equal(t, "Doe", lastName)
				assert.Equal(t, "jane@example.com", email)
				assert.NotNil(t, password)
				return user, nil
			},
		}
		handler := newTestHandler(svc)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		body := `{"firstName":"Jane","lastName":"Doe","email":"jane@example.com","password":"MyP@ssw0rd"}`
		c.Request = httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")

		handler.RegisterHandler(c)

		assert.Equal(t, http.StatusOK, w.Code)

		var resp UserResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, user.ID, resp.ID)
		assert.Equal(t, "jane@example.com", resp.Email)
	})

	t.Run("Missing required fields returns 400", func(t *testing.T) {
		svc := &mockUserService{}
		handler := newTestHandler(svc)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		body := `{"firstName":"Jane","lastName":"","email":"jane@example.com","password":"MyP@ssw0rd"}`
		c.Request = httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")

		handler.RegisterHandler(c)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("Service error returns 500", func(t *testing.T) {
		svc := &mockUserService{
			createUserFn: func(_ context.Context, _, _, _ string, _, _, _, _ *string) (*domain.User, error) {
				return nil, fmt.Errorf("duplicate email")
			},
		}
		handler := newTestHandler(svc)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		body := `{"firstName":"Jane","lastName":"Doe","email":"jane@example.com","password":"MyP@ssw0rd"}`
		c.Request = httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")

		handler.RegisterHandler(c)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})

	t.Run("Invalid JSON returns 400", func(t *testing.T) {
		svc := &mockUserService{}
		handler := newTestHandler(svc)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/register", strings.NewReader("{bad"))
		c.Request.Header.Set("Content-Type", "application/json")

		handler.RegisterHandler(c)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

// --- Logout Handler Tests ---

func TestLogoutHandler(t *testing.T) {
	os.Setenv("SESSION_COOKIE_DOMAIN", ".example.com")
	os.Setenv("APP_ENV", "production")
	defer os.Unsetenv("SESSION_COOKIE_DOMAIN")
	defer os.Unsetenv("APP_ENV")

	t.Run("Successful logout clears cookies", func(t *testing.T) {
		var invalidatedToken string
		svc := &mockUserService{
			invalidateRefreshTokenFn: func(_ context.Context, token string) error {
				invalidatedToken = token
				return nil
			},
		}
		handler := newTestHandler(svc)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/logout", nil)
		c.Request.AddCookie(&http.Cookie{Name: "refresh_token", Value: "old-refresh-token"})

		handler.LogoutHandler(c)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "old-refresh-token", invalidatedToken)

		// Verify cookies are cleared (MaxAge < 0)
		cookies := w.Result().Cookies()
		for _, cookie := range cookies {
			if cookie.Name == "access_token" || cookie.Name == "refresh_token" {
				assert.True(t, cookie.MaxAge < 0, "Cookie %s should be cleared", cookie.Name)
			}
		}

		// Verify cache-control headers
		assert.Equal(t, "no-store, no-cache, must-revalidate", w.Header().Get("Cache-Control"))
	})

	t.Run("Logout without refresh token still succeeds", func(t *testing.T) {
		svc := &mockUserService{}
		handler := newTestHandler(svc)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/logout", nil)

		handler.LogoutHandler(c)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("Invalidation failure returns 500", func(t *testing.T) {
		svc := &mockUserService{
			invalidateRefreshTokenFn: func(_ context.Context, _ string) error {
				return fmt.Errorf("db error")
			},
		}
		handler := newTestHandler(svc)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/logout", nil)
		c.Request.AddCookie(&http.Cookie{Name: "refresh_token", Value: "some-token"})

		handler.LogoutHandler(c)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})
}

// --- Refresh Token Handler Tests ---

func TestRefreshTokenHandler(t *testing.T) {
	os.Setenv("SESSION_COOKIE_DOMAIN", ".example.com")
	os.Setenv("APP_ENV", "production")
	defer os.Unsetenv("SESSION_COOKIE_DOMAIN")
	defer os.Unsetenv("APP_ENV")

	t.Run("Successful refresh returns new tokens", func(t *testing.T) {
		user := testUser()
		svc := &mockUserService{
			refreshTokenFn: func(_ context.Context, oldToken, _ string) (string, string, *domain.User, error) {
				if oldToken == "old-refresh" {
					return "new-access", "new-refresh", user, nil
				}
				return "", "", nil, fmt.Errorf("invalid token")
			},
		}
		handler := newTestHandler(svc)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/tokens/refresh", nil)
		c.Request.AddCookie(&http.Cookie{Name: "refresh_token", Value: "old-refresh"})

		handler.RefreshTokenHandler(c)

		assert.Equal(t, http.StatusOK, w.Code)

		// Verify new cookies
		cookies := w.Result().Cookies()
		var hasNewAccess, hasNewRefresh bool
		for _, cookie := range cookies {
			if cookie.Name == "access_token" && cookie.Value == "new-access" {
				hasNewAccess = true
			}
			if cookie.Name == "refresh_token" && cookie.Value == "new-refresh" {
				hasNewRefresh = true
			}
		}
		assert.True(t, hasNewAccess, "new access_token cookie should be set")
		assert.True(t, hasNewRefresh, "new refresh_token cookie should be set")

		// Verify response body
		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		userMap := resp["user"].(map[string]interface{})
		assert.Equal(t, user.Email, userMap["email"])
	})

	t.Run("Missing refresh token returns 401", func(t *testing.T) {
		svc := &mockUserService{}
		handler := newTestHandler(svc)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/tokens/refresh", nil)

		handler.RefreshTokenHandler(c)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("Invalid refresh token returns 401", func(t *testing.T) {
		svc := &mockUserService{
			refreshTokenFn: func(_ context.Context, _, _ string) (string, string, *domain.User, error) {
				return "", "", nil, fmt.Errorf("token revoked or expired")
			},
		}
		handler := newTestHandler(svc)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/tokens/refresh", nil)
		c.Request.AddCookie(&http.Cookie{Name: "refresh_token", Value: "expired-token"})

		handler.RefreshTokenHandler(c)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})
}

// --- GetUserProfile Handler Tests ---

func TestGetUserProfileHandler(t *testing.T) {
	t.Run("Returns user profile for authenticated user", func(t *testing.T) {
		user := testUser()
		svc := &mockUserService{
			getUserByIDFn: func(_ context.Context, id string) (*domain.User, error) {
				if id == user.ID.String() {
					return user, nil
				}
				return nil, sql.ErrNoRows
			},
		}
		handler := newTestHandler(svc)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/me", nil)
		c.Set("userID", user.ID.String())

		handler.GetUserProfileHandler(c)

		assert.Equal(t, http.StatusOK, w.Code)

		var resp UserResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, user.ID, resp.ID)
		assert.Equal(t, "Jane", resp.FirstName)
	})

	t.Run("Returns 401 without userID in context", func(t *testing.T) {
		svc := &mockUserService{}
		handler := newTestHandler(svc)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/me", nil)

		handler.GetUserProfileHandler(c)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("Returns 500 when service fails", func(t *testing.T) {
		svc := &mockUserService{
			getUserByIDFn: func(_ context.Context, _ string) (*domain.User, error) {
				return nil, fmt.Errorf("db connection error")
			},
		}
		handler := newTestHandler(svc)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/me", nil)
		c.Set("userID", uuid.NewString())

		handler.GetUserProfileHandler(c)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})
}

// --- UpdateMe Handler Tests ---

func TestUpdateMeHandler(t *testing.T) {
	t.Run("Updates user profile", func(t *testing.T) {
		user := testUser()
		updatedUser := *user
		updatedUser.FirstName = "Janet"

		svc := &mockUserService{
			updateMeFn: func(_ context.Context, userID string, firstName, _, _, _, _ *string) (*domain.User, error) {
				assert.Equal(t, user.ID.String(), userID)
				assert.Equal(t, "Janet", *firstName)
				return &updatedUser, nil
			},
		}
		handler := newTestHandler(svc)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		body := `{"firstName":"Janet"}`
		c.Request = httptest.NewRequest(http.MethodPut, "/me", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("userID", user.ID.String())

		handler.UpdateMeHandler(c)

		assert.Equal(t, http.StatusOK, w.Code)

		var resp UserResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "Janet", resp.FirstName)
	})

	t.Run("Returns 401 without userID", func(t *testing.T) {
		svc := &mockUserService{}
		handler := newTestHandler(svc)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		body := `{"firstName":"Janet"}`
		c.Request = httptest.NewRequest(http.MethodPut, "/me", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")

		handler.UpdateMeHandler(c)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("Invalid JSON returns 400", func(t *testing.T) {
		svc := &mockUserService{}
		handler := newTestHandler(svc)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPut, "/me", strings.NewReader("{bad"))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("userID", uuid.NewString())

		handler.UpdateMeHandler(c)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

// --- ForgotPassword Handler Tests ---

func TestForgotPasswordHandler(t *testing.T) {
	t.Run("Always returns 200 to prevent email enumeration", func(t *testing.T) {
		svc := &mockUserService{
			requestPasswordResetFn: func(_ context.Context, email string) error {
				assert.Equal(t, "jane@example.com", email)
				return nil
			},
		}
		handler := newTestHandler(svc)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		body := `{"email":"jane@example.com"}`
		c.Request = httptest.NewRequest(http.MethodPost, "/forgot-password", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")

		handler.ForgotPasswordHandler(c)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("Returns 200 even for non-existent email", func(t *testing.T) {
		svc := &mockUserService{
			requestPasswordResetFn: func(_ context.Context, _ string) error {
				return nil // silently succeeds
			},
		}
		handler := newTestHandler(svc)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		body := `{"email":"nobody@example.com"}`
		c.Request = httptest.NewRequest(http.MethodPost, "/forgot-password", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")

		handler.ForgotPasswordHandler(c)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("Missing email returns 400", func(t *testing.T) {
		svc := &mockUserService{}
		handler := newTestHandler(svc)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		body := `{"email":""}`
		c.Request = httptest.NewRequest(http.MethodPost, "/forgot-password", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")

		handler.ForgotPasswordHandler(c)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

// --- ResetPassword Handler Tests ---

func TestResetPasswordHandler(t *testing.T) {
	os.Setenv("SESSION_COOKIE_DOMAIN", ".example.com")
	os.Setenv("APP_ENV", "production")
	defer os.Unsetenv("SESSION_COOKIE_DOMAIN")
	defer os.Unsetenv("APP_ENV")

	t.Run("Successful password reset clears cookies", func(t *testing.T) {
		svc := &mockUserService{
			resetPasswordFn: func(_ context.Context, token, password string) error {
				assert.Equal(t, "valid-reset-token", token)
				assert.Equal(t, "NewP@ssw0rd", password)
				return nil
			},
		}
		handler := newTestHandler(svc)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		body := `{"token":"valid-reset-token","password":"NewP@ssw0rd"}`
		c.Request = httptest.NewRequest(http.MethodPost, "/reset-password", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")

		handler.ResetPasswordHandler(c)

		assert.Equal(t, http.StatusOK, w.Code)

		// Verify cookies are cleared
		cookies := w.Result().Cookies()
		for _, cookie := range cookies {
			if cookie.Name == "access_token" || cookie.Name == "refresh_token" {
				assert.True(t, cookie.MaxAge < 0, "Cookie %s should be cleared", cookie.Name)
			}
		}
	})

	t.Run("Invalid token returns 400", func(t *testing.T) {
		svc := &mockUserService{
			resetPasswordFn: func(_ context.Context, _, _ string) error {
				return fmt.Errorf("invalid or expired token")
			},
		}
		handler := newTestHandler(svc)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		body := `{"token":"bad-token","password":"NewP@ssw0rd"}`
		c.Request = httptest.NewRequest(http.MethodPost, "/reset-password", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")

		handler.ResetPasswordHandler(c)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("Missing fields returns 400", func(t *testing.T) {
		svc := &mockUserService{}
		handler := newTestHandler(svc)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		body := `{"token":"","password":""}`
		c.Request = httptest.NewRequest(http.MethodPost, "/reset-password", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")

		handler.ResetPasswordHandler(c)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

// --- VerifyEmail Handler Tests ---

func TestVerifyEmailHandler(t *testing.T) {
	os.Setenv("JWT_SECRET", testJWTSecret)
	os.Setenv("REDIRECT_EMAIL_VERIFY_SUCCESSFUL", "https://example.com/verified")
	defer os.Unsetenv("JWT_SECRET")
	defer os.Unsetenv("REDIRECT_EMAIL_VERIFY_SUCCESSFUL")

	t.Run("Missing token returns 400", func(t *testing.T) {
		svc := &mockUserService{}
		handler := newTestHandler(svc)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/verify", nil)

		handler.VerifyEmailHandler(c)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("Invalid token returns 400", func(t *testing.T) {
		svc := &mockUserService{}
		handler := newTestHandler(svc)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/verify?token=invalid-jwt-token", nil)

		handler.VerifyEmailHandler(c)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("Token with wrong purpose returns 400", func(t *testing.T) {
		svc := &mockUserService{}
		handler := newTestHandler(svc)

		// Generate a token with wrong purpose
		token := generateTestJWT(t, map[string]interface{}{
			"sub":     uuid.NewString(),
			"purpose": "password_reset", // wrong purpose
		})

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/verify?token="+token, nil)

		handler.VerifyEmailHandler(c)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("Valid token redirects on success", func(t *testing.T) {
		userID := uuid.NewString()
		svc := &mockUserService{
			verifyUserEmailFn: func(_ context.Context, id string) error {
				assert.Equal(t, userID, id)
				return nil
			},
		}
		handler := newTestHandler(svc)

		token := generateTestJWT(t, map[string]interface{}{
			"sub":     userID,
			"purpose": "email_verification",
		})

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/verify?token="+token, nil)

		handler.VerifyEmailHandler(c)

		assert.Equal(t, http.StatusFound, w.Code)
		assert.Equal(t, "https://example.com/verified", w.Header().Get("Location"))
	})
}

// --- Test JWT helper ---

func generateTestJWT(t *testing.T, claims map[string]interface{}) string {
	t.Helper()
	mapClaims := make(jwt.MapClaims)
	for k, v := range claims {
		mapClaims[k] = v
	}
	if _, ok := mapClaims["exp"]; !ok {
		mapClaims["exp"] = time.Now().Add(1 * time.Hour).Unix()
	}
	if _, ok := mapClaims["iat"]; !ok {
		mapClaims["iat"] = time.Now().Unix()
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, mapClaims)
	tokenStr, err := token.SignedString([]byte(testJWTSecret))
	require.NoError(t, err)
	return tokenStr
}
