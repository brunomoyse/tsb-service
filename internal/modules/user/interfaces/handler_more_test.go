package interfaces

import (
	"context"
	"database/sql"
	"encoding/json"
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

// cachedAddresses answers GetByPlaceID from a map (cache-only lookups).
type cachedAddresses struct {
	mockAddressService
	byPlace map[string]*addressDomain.Address
	asked   []string
}

func (c *cachedAddresses) GetByPlaceID(_ context.Context, id string) (*addressDomain.Address, error) {
	c.asked = append(c.asked, id)
	if a, ok := c.byPlace[id]; ok {
		return a, nil
	}
	return nil, sql.ErrNoRows
}

func call(h gin.HandlerFunc, method, body, userID string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	if userID != "" {
		c.Set("userID", userID)
	}
	c.Request = httptest.NewRequest(method, "/me", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	h(c)
	return w
}

func TestProfileHandlersIncludeTheDefaultAddress(t *testing.T) {
	uid := uuid.New()
	place := "place-1"
	user := &domain.User{ID: uid, FirstName: "John", LastName: "Doe", Email: "j@x.test", DefaultPlaceID: &place}
	addr := &addressDomain.Address{PlaceID: place, StreetName: "Rue Neuve", HouseNumber: "12", Postcode: "4000", MunicipalityName: "Liège"}
	addresses := &cachedAddresses{byPlace: map[string]*addressDomain.Address{place: addr}}
	svc := &mockUserService{
		getUserByIDFn: func(context.Context, string) (*domain.User, error) { return user, nil },
		updateMeFn: func(context.Context, string, *string, *string, *string, *string, *string, *bool, *bool) (*domain.User, error) {
			return user, nil
		},
	}
	h := NewUserHandler(svc, addresses)

	for name, fn := range map[string]func() *httptest.ResponseRecorder{
		"profile": func() *httptest.ResponseRecorder {
			return call(h.GetUserProfileHandler, http.MethodGet, "", uid.String())
		},
		"update": func() *httptest.ResponseRecorder {
			return call(h.UpdateMeHandler, http.MethodPatch, `{}`, uid.String())
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := fn()
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			var resp UserResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			require.NotNil(t, resp.Address)
			assert.Equal(t, "Rue Neuve", resp.Address.StreetName)
			assert.Equal(t, uid, resp.ID)
		})
	}
	assert.Equal(t, []string{place, place}, addresses.asked)

	t.Run("an address missing from the cache leaves the profile without one", func(t *testing.T) {
		empty := NewUserHandler(svc, &cachedAddresses{})
		w := call(empty.GetUserProfileHandler, http.MethodGet, "", uid.String())
		require.Equal(t, http.StatusOK, w.Code)
		var resp UserResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Nil(t, resp.Address)
	})
}

func TestGetUserProfileHandlerFailure(t *testing.T) {
	svc := &mockUserService{getUserByIDFn: func(context.Context, string) (*domain.User, error) { return nil, sql.ErrConnDone }}
	w := call(NewUserHandler(svc, &mockAddressService{}).GetUserProfileHandler, http.MethodGet, "", uuid.NewString())
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.JSONEq(t, `{"error":"Failed to fetch user profile"}`, w.Body.String())
}

func TestUpdateMeHandlerErrors(t *testing.T) {
	uid := uuid.NewString()
	failWith := func(err error) *UserHandler {
		return NewUserHandler(&mockUserService{
			updateMeFn: func(context.Context, string, *string, *string, *string, *string, *string, *bool, *bool) (*domain.User, error) {
				return nil, err
			},
		}, &mockAddressService{})
	}

	t.Run("an invalid JSON body is 400 and never reaches the service", func(t *testing.T) {
		called := false
		h := NewUserHandler(&mockUserService{
			updateMeFn: func(context.Context, string, *string, *string, *string, *string, *string, *bool, *bool) (*domain.User, error) {
				called = true
				return nil, nil
			},
		}, &mockAddressService{})
		w := call(h.UpdateMeHandler, http.MethodPatch, `{"firstName": 12}`, uid)
		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.JSONEq(t, `{"error":"invalid request payload"}`, w.Body.String())
		assert.False(t, called)
	})

	t.Run("an invalid phone number is a 400 the client can act on", func(t *testing.T) {
		w := call(failWith(application.ErrInvalidPhoneNumber).UpdateMeHandler, http.MethodPatch, `{"phoneNumber":"123"}`, uid)
		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.JSONEq(t, `{"error":"invalid phone number"}`, w.Body.String())
	})

	t.Run("any other failure is a 500 that does not leak details", func(t *testing.T) {
		w := call(failWith(sql.ErrConnDone).UpdateMeHandler, http.MethodPatch, `{}`, uid)
		assert.Equal(t, http.StatusInternalServerError, w.Code)
		assert.JSONEq(t, `{"error":"failed to update user profile"}`, w.Body.String())
	})

	t.Run("the request fields reach the service and notification flags are never set from this endpoint", func(t *testing.T) {
		var got struct {
			first, last, email, phone, place *string
			marketing, updates               *bool
		}
		h := NewUserHandler(&mockUserService{
			updateMeFn: func(_ context.Context, _ string, f, l, e, p, a *string, m, u *bool) (*domain.User, error) {
				got.first, got.last, got.email, got.phone, got.place, got.marketing, got.updates = f, l, e, p, a, m, u
				return &domain.User{ID: uuid.New()}, nil
			},
		}, &mockAddressService{})
		w := call(h.UpdateMeHandler, http.MethodPatch, `{"firstName":"A","lastName":"B","email":"a@b.test","phoneNumber":"0470","addressPlaceId":"p"}`, uid)
		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "A", *got.first)
		assert.Equal(t, "B", *got.last)
		assert.Equal(t, "a@b.test", *got.email)
		assert.Equal(t, "0470", *got.phone)
		assert.Equal(t, "p", *got.place)
		assert.Nil(t, got.marketing)
		assert.Nil(t, got.updates)
	})
}
