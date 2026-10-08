package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/order/domain"
)

// productsRepo answers the menu's product rows and remembers what it was asked.
type productsRepo struct {
	fakeOrderRepo
	err error

	orderedUser  uuid.UUID
	orderedLimit int
	ordered      []*domain.ProductOrderCount

	popularCalls int
	popularArgs  [3]any
	popular      []*domain.ProductOrderCount
}

func (r *productsRepo) FindOrderedProducts(_ context.Context, userID uuid.UUID, limit int) ([]*domain.ProductOrderCount, error) {
	r.orderedUser, r.orderedLimit = userID, limit
	return r.ordered, r.err
}

func (r *productsRepo) FindPopularProducts(_ context.Context, since time.Time, perCategory int, limit int) ([]*domain.ProductOrderCount, error) {
	r.popularCalls++
	r.popularArgs = [3]any{since, perCategory, limit}
	return r.popular, r.err
}

func newProductsService(repo *productsRepo, now *time.Time) *orderService {
	svc := NewOrderService(repo, nil).(*orderService)
	svc.now = func() time.Time { return *now }
	return svc
}

func TestGetOrderedProducts_PassesTheCustomerAndLimit(t *testing.T) {
	user := uuid.New()
	want := []*domain.ProductOrderCount{{ProductID: uuid.New(), OrderCount: 3}}
	repo := &productsRepo{ordered: want}
	now := time.Now()

	got, err := newProductsService(repo, &now).GetOrderedProducts(t.Context(), user, 12)
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Equal(t, user, repo.orderedUser)
	assert.Equal(t, 12, repo.orderedLimit)
}

func TestGetPopularProducts_LooksBackNinetyDaysTwoPerCategory(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	want := []*domain.ProductOrderCount{{ProductID: uuid.New(), OrderCount: 40}}
	repo := &productsRepo{popular: want}

	got, err := newProductsService(repo, &now).GetPopularProducts(t.Context(), 8)
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Equal(t, [3]any{now.Add(-90 * 24 * time.Hour), 2, 8}, repo.popularArgs)
}

func TestGetPopularProducts_CachedPerLimitForFifteenMinutes(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	repo := &productsRepo{popular: []*domain.ProductOrderCount{{ProductID: uuid.New(), OrderCount: 1}}}
	svc := newProductsService(repo, &now)

	_, err := svc.GetPopularProducts(t.Context(), 8)
	require.NoError(t, err)
	now = now.Add(14 * time.Minute)
	_, err = svc.GetPopularProducts(t.Context(), 8)
	require.NoError(t, err)
	assert.Equal(t, 1, repo.popularCalls, "a second visitor within 15 minutes gets the cached list")

	_, err = svc.GetPopularProducts(t.Context(), 4)
	require.NoError(t, err)
	assert.Equal(t, 2, repo.popularCalls, "another limit is its own entry")

	now = now.Add(time.Minute)
	_, err = svc.GetPopularProducts(t.Context(), 8)
	require.NoError(t, err)
	assert.Equal(t, 3, repo.popularCalls, "after 15 minutes the list is computed again")
}

func TestGetPopularProducts_AFailureIsNotCached(t *testing.T) {
	now := time.Now()
	repo := &productsRepo{err: errors.New("db down")}
	svc := newProductsService(repo, &now)

	_, err := svc.GetPopularProducts(t.Context(), 8)
	require.Error(t, err)

	repo.err = nil
	repo.popular = []*domain.ProductOrderCount{}
	got, err := svc.GetPopularProducts(t.Context(), 8)
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.Equal(t, 2, repo.popularCalls)
}
