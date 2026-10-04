package application

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/user/domain"
)

type batchRepo struct {
	jitRepo
	byOrder map[string][]*domain.User
	asked   []string
}

func (r *batchRepo) BatchGetUsersByOrderIDs(_ context.Context, ids []string) (map[string][]*domain.User, error) {
	r.asked = ids
	return r.byOrder, nil
}

func newUpdateEnv(u *domain.User) (UserService, *jitRepo) {
	repo := &jitRepo{users: []*domain.User{u}}
	return NewUserService(repo, nil), repo
}

func TestGetUserByEmailNormalisesTheLookup(t *testing.T) {
	repo := &jitRepo{users: []*domain.User{{ID: uuid.New(), Email: "ada@example.com"}}}
	svc := NewUserService(repo, nil)

	got, err := svc.GetUserByEmail(t.Context(), "  ADA@Example.com ")
	require.NoError(t, err)
	assert.Equal(t, "ada@example.com", got.Email)

	_, err = svc.GetUserByEmail(t.Context(), "nobody@example.com")
	assert.ErrorIs(t, err, sql.ErrNoRows)
}

func TestUpdateMe(t *testing.T) {
	newUser := func() *domain.User {
		return &domain.User{
			ID: uuid.New(), FirstName: "Ada", LastName: "Lovelace", Email: "ada@example.com",
			PhoneNumber: strp("+32470000000"), DefaultPlaceID: strp("old-place"),
			NotifyMarketing: true, NotifyOrderUpdates: true,
		}
	}

	t.Run("with no fields the user is saved unchanged", func(t *testing.T) {
		u := newUser()
		svc, _ := newUpdateEnv(u)
		got, err := svc.UpdateMe(t.Context(), u.ID.String(), nil, nil, nil, nil, nil, nil, nil)
		require.NoError(t, err)
		assert.Equal(t, *u, *got)
	})

	t.Run("every provided field is applied, email normalised and phone converted to E.164", func(t *testing.T) {
		u := newUser()
		svc, repo := newUpdateEnv(u)
		f, l, e, p, a := "Grace", "Hopper", "  GRACE@Example.COM", "0470 12 34 56", "new-place"
		off := false
		got, err := svc.UpdateMe(t.Context(), u.ID.String(), &f, &l, &e, &p, &a, &off, &off)
		require.NoError(t, err)
		assert.Equal(t, "Grace", got.FirstName)
		assert.Equal(t, "Hopper", got.LastName)
		assert.Equal(t, "grace@example.com", got.Email)
		assert.Equal(t, "+32470123456", *got.PhoneNumber)
		assert.Equal(t, "new-place", *got.DefaultPlaceID)
		assert.False(t, got.NotifyMarketing)
		assert.False(t, got.NotifyOrderUpdates)
		assert.Equal(t, 1, repo.updated)
	})

	t.Run("an empty phone number clears it, an empty place id clears the default address", func(t *testing.T) {
		u := newUser()
		svc, _ := newUpdateEnv(u)
		empty := ""
		got, err := svc.UpdateMe(t.Context(), u.ID.String(), nil, nil, nil, &empty, &empty, nil, nil)
		require.NoError(t, err)
		assert.Nil(t, got.PhoneNumber)
		assert.Nil(t, got.DefaultPlaceID)
	})

	t.Run("an invalid phone number is refused and nothing is saved", func(t *testing.T) {
		u := newUser()
		svc, repo := newUpdateEnv(u)
		bad := "12"
		_, err := svc.UpdateMe(t.Context(), u.ID.String(), nil, nil, nil, &bad, nil, nil, nil)
		assert.ErrorIs(t, err, ErrInvalidPhoneNumber)
		assert.Zero(t, repo.updated)
	})

	t.Run("an unknown user is an error", func(t *testing.T) {
		svc, _ := newUpdateEnv(newUser())
		_, err := svc.UpdateMe(t.Context(), uuid.NewString(), nil, nil, nil, nil, nil, nil, nil)
		assert.ErrorIs(t, err, sql.ErrNoRows)
	})
}

func TestBatchGetUsersByOrderIDsAndLoader(t *testing.T) {
	u := &domain.User{ID: uuid.New(), FirstName: "Ada"}
	orderID := uuid.NewString()
	repo := &batchRepo{byOrder: map[string][]*domain.User{orderID: {u}}}
	svc := NewUserService(repo, nil)

	got, err := svc.BatchGetUsersByOrderIDs(t.Context(), []string{orderID})
	require.NoError(t, err)
	assert.Equal(t, []*domain.User{u}, got[orderID])
	assert.Equal(t, []string{orderID}, repo.asked)

	t.Run("loader attached to the context resolves the customer of an order", func(t *testing.T) {
		ctx := AttachDataLoaders(t.Context(), svc)
		users, err := GetOrderUserLoader(ctx).Loader.Load(ctx, orderID)
		require.NoError(t, err)
		assert.Equal(t, []*domain.User{u}, users)
		none, err := GetOrderUserLoader(ctx).Loader.Load(ctx, uuid.NewString())
		require.NoError(t, err)
		assert.Empty(t, none)
	})

	t.Run("no loader on the context", func(t *testing.T) {
		assert.Nil(t, GetOrderUserLoader(t.Context()))
	})
}
