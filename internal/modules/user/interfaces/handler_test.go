package interfaces

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	addressDomain "tsb-service/internal/modules/address/domain"
	"tsb-service/internal/modules/user/application"
	"tsb-service/internal/modules/user/domain"
)

// --- Mock UserService ---

type mockUserService struct {
	getUserByIDFn   func(ctx context.Context, id string) (*domain.User, error)
	getUserByEmailFn func(ctx context.Context, email string) (*domain.User, error)
	updateMeFn      func(ctx context.Context, userID string, firstName, lastName, email, phoneNumber, addressID *string, notifyMarketing *bool) (*domain.User, error)
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

func (m *mockUserService) UpdateMe(ctx context.Context, userID string, firstName, lastName, email, phoneNumber, addressID *string, notifyMarketing *bool) (*domain.User, error) {
	if m.updateMeFn != nil {
		return m.updateMeFn(ctx, userID, firstName, lastName, email, phoneNumber, addressID, notifyMarketing)
	}
	return nil, fmt.Errorf("not implemented")
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

func (m *mockUserService) FindOrCreateByZitadelID(ctx context.Context, zitadelID, email, firstName, lastName string) (*domain.User, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockUserService) ResolveZitadelID(ctx context.Context, zitadelID, email, firstName, lastName string) (string, error) {
	return "", fmt.Errorf("not implemented")
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
func (m *mockAddressService) GetAddressByID(_ context.Context, _ string) (*addressDomain.Address, error) {
	return nil, nil
}
func (m *mockAddressService) GetAddressByStreetAndNumber(_ context.Context, _, _ string) (*addressDomain.Address, error) {
	return nil, nil
}
func (m *mockAddressService) BatchGetAddressesByUserIDs(_ context.Context, _ []string) (map[string][]*addressDomain.Address, error) {
	return nil, nil
}
func (m *mockAddressService) BatchGetAddressesByOrderIDs(_ context.Context, _ []string) (map[string][]*addressDomain.Address, error) {
	return nil, nil
}
func (m *mockAddressService) GetBoxNumbers(_ context.Context, _, _ string) ([]*string, error) {
	return nil, nil
}
func (m *mockAddressService) GetFinalAddress(_ context.Context, _, _ string, _ *string) (*addressDomain.Address, error) {
	return nil, nil
}
func (m *mockAddressService) GetStreetByID(_ context.Context, _ string) (*addressDomain.Street, error) {
	return nil, nil
}
func (m *mockAddressService) GetStreetAverageDistance(_ context.Context, _ string) (float64, error) {
	return 0, nil
}

// --- Tests ---

func TestGetUserProfileHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	testUserID := uuid.New()
	testUser := &domain.User{
		ID:        testUserID,
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
	}

	svc := &mockUserService{
		getUserByIDFn: func(_ context.Context, id string) (*domain.User, error) {
			if id == testUserID.String() {
				return testUser, nil
			}
			return nil, sql.ErrNoRows
		},
	}

	handler := NewUserHandler(svc, &mockAddressService{})

	t.Run("authenticated user gets profile", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set("userID", testUserID.String())
		c.Request = httptest.NewRequest(http.MethodGet, "/profile", nil)

		handler.GetUserProfileHandler(c)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp UserResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "John", resp.FirstName)
		assert.Equal(t, "john@example.com", resp.Email)
	})

	t.Run("unauthenticated user gets 401", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/profile", nil)

		handler.GetUserProfileHandler(c)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})
}

func TestUpdateMeHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	testUserID := uuid.New()

	svc := &mockUserService{
		updateMeFn: func(_ context.Context, userID string, firstName, lastName, email, phoneNumber, addressID *string, notifyMarketing *bool) (*domain.User, error) {
			return &domain.User{
				ID:        testUserID,
				FirstName: *firstName,
				LastName:  "Doe",
				Email:     "john@example.com",
			}, nil
		},
	}

	handler := NewUserHandler(svc, &mockAddressService{})

	t.Run("update first name", func(t *testing.T) {
		body := `{"firstName":"Johnny"}`
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set("userID", testUserID.String())
		c.Request = httptest.NewRequest(http.MethodPatch, "/me", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")

		handler.UpdateMeHandler(c)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp UserResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "Johnny", resp.FirstName)
	})

	t.Run("unauthenticated gets 401", func(t *testing.T) {
		body := `{"firstName":"Johnny"}`
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPatch, "/me", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")

		handler.UpdateMeHandler(c)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})
}
