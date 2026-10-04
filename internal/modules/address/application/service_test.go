package application

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/address/domain"
)

type fakeCache struct {
	entries   map[string]*domain.AddressCache
	getErr    error
	upsertErr error
	upserted  []*domain.AddressCache
}

func (f *fakeCache) GetByPlaceID(_ context.Context, id string) (*domain.AddressCache, error) {
	return f.entries[id], f.getErr
}
func (f *fakeCache) Upsert(_ context.Context, e *domain.AddressCache) error {
	f.upserted = append(f.upserted, e)
	if f.upsertErr == nil {
		f.entries[e.PlaceID] = e
	}
	return f.upsertErr
}

type fakeGoogle struct {
	suggestions []domain.Suggestion
	autoErr     error
	autoArgs    [3]string
	details     *domain.AddressCache
	detailsErr  error
	detailsArgs [3]string
	detailsCall int
	dist, dur   int
	routeErr    error
	routeCalls  int
	routeDest   [2]float64
}

func (f *fakeGoogle) Autocomplete(_ context.Context, in, tok, lang string) ([]domain.Suggestion, error) {
	f.autoArgs = [3]string{in, tok, lang}
	return f.suggestions, f.autoErr
}
func (f *fakeGoogle) PlaceDetails(_ context.Context, id, tok, lang string) (*domain.AddressCache, error) {
	f.detailsCall++
	f.detailsArgs = [3]string{id, tok, lang}
	return f.details, f.detailsErr
}
func (f *fakeGoogle) ComputeRoute(_ context.Context, lat, lng float64) (int, int, error) {
	f.routeCalls++
	f.routeDest = [2]float64{lat, lng}
	return f.dist, f.dur, f.routeErr
}

func sp(s string) *string { return &s }

func newSvc(language string) (AddressService, *fakeCache, *fakeGoogle) {
	cache := &fakeCache{entries: map[string]*domain.AddressCache{}}
	g := &fakeGoogle{
		details: &domain.AddressCache{PlaceID: "p1", Lat: 50.6, Lng: 5.5, StreetName: sp("Rue Neuve"), HouseNumber: sp("12"), Postcode: sp("4000"), MunicipalityName: sp("Liège"), BoxNumber: sp("2A")},
		dist:    3200, dur: 480,
	}
	return NewAddressService(cache, g, language), cache, g
}

func TestAutocompleteUsesTheConfiguredLanguage(t *testing.T) {
	for in, want := range map[string]string{"": "fr", "nl": "nl"} {
		svc, _, g := newSvc(in)
		g.suggestions = []domain.Suggestion{{PlaceID: "p1"}}
		got, err := svc.Autocomplete(t.Context(), "rue", "tok")
		require.NoError(t, err)
		assert.Equal(t, g.suggestions, got)
		assert.Equal(t, [3]string{"rue", "tok", want}, g.autoArgs)
	}

	svc, _, g := newSvc("fr")
	g.autoErr = errors.New("quota")
	_, err := svc.Autocomplete(t.Context(), "x", "t")
	assert.ErrorIs(t, err, g.autoErr)
}

func TestResolve(t *testing.T) {
	t.Run("an empty place id is refused before any lookup", func(t *testing.T) {
		svc, cache, g := newSvc("fr")
		cache.getErr = errors.New("must not be reached")
		_, err := svc.Resolve(t.Context(), "", "tok")
		assert.EqualError(t, err, "placeID required")
		assert.Zero(t, g.detailsCall)
	})

	t.Run("a cache hit never calls Google", func(t *testing.T) {
		svc, cache, g := newSvc("fr")
		cache.entries["p1"] = &domain.AddressCache{PlaceID: "p1", StreetName: sp("Cached"), DistanceMeters: 1234, DurationSeconds: 99, Lat: 1, Lng: 2}
		a, err := svc.Resolve(t.Context(), "p1", "tok")
		require.NoError(t, err)
		assert.Equal(t, "Cached", a.StreetName)
		assert.InDelta(t, 1234, a.Distance, 0)
		assert.Zero(t, g.detailsCall)
		assert.Zero(t, g.routeCalls)
	})

	t.Run("a miss fetches details and the route, caches the entry and returns the address", func(t *testing.T) {
		svc, cache, g := newSvc("nl")
		a, err := svc.Resolve(t.Context(), "p1", "tok")
		require.NoError(t, err)

		assert.Equal(t, [3]string{"p1", "tok", "nl"}, g.detailsArgs)
		assert.Equal(t, [2]float64{50.6, 5.5}, g.routeDest, "the route goes to the place's coordinates")
		require.Len(t, cache.upserted, 1)
		assert.Equal(t, 3200, cache.upserted[0].DistanceMeters)
		assert.Equal(t, 480, cache.upserted[0].DurationSeconds)
		assert.False(t, cache.upserted[0].RefreshedAt.IsZero())

		assert.Equal(t, "p1", a.ID)
		assert.Equal(t, "p1", a.PlaceID)
		assert.Equal(t, "Rue Neuve", a.StreetName)
		assert.Equal(t, "12", a.HouseNumber)
		assert.Equal(t, "4000", a.Postcode)
		assert.Equal(t, "Liège", a.MunicipalityName)
		assert.Equal(t, "2A", *a.BoxNumber)
		assert.InDelta(t, 3200, a.Distance, 0)
		assert.InDelta(t, 50.6, *a.Lat, 1e-9)
		assert.Equal(t, 480, *a.Duration)

		// The second resolution is served from the cache.
		_, err = svc.Resolve(t.Context(), "p1", "tok")
		require.NoError(t, err)
		assert.Equal(t, 1, g.detailsCall)
	})

	t.Run("a place without structured parts yields empty strings, not nil dereferences", func(t *testing.T) {
		svc, _, g := newSvc("fr")
		g.details = &domain.AddressCache{PlaceID: "p2"}
		a, err := svc.Resolve(t.Context(), "p2", "tok")
		require.NoError(t, err)
		assert.Empty(t, a.StreetName)
		assert.Empty(t, a.HouseNumber)
		assert.Empty(t, a.Postcode)
		assert.Empty(t, a.MunicipalityName)
		assert.Nil(t, a.BoxNumber)
	})

	t.Run("each dependency failure is reported with its stage and nothing is cached", func(t *testing.T) {
		boom := errors.New("boom")

		svc, cache, _ := newSvc("fr")
		cache.getErr = boom
		_, err := svc.Resolve(t.Context(), "p1", "t")
		require.ErrorIs(t, err, boom)
		assert.ErrorContains(t, err, "cache lookup")

		svc, cache, g := newSvc("fr")
		g.detailsErr = boom
		_, err = svc.Resolve(t.Context(), "p1", "t")
		require.ErrorIs(t, err, boom)
		assert.ErrorContains(t, err, "place details")
		assert.Zero(t, g.routeCalls)
		assert.Empty(t, cache.upserted)

		svc, cache, g = newSvc("fr")
		g.routeErr = boom
		_, err = svc.Resolve(t.Context(), "p1", "t")
		require.ErrorIs(t, err, boom)
		assert.ErrorContains(t, err, "compute route")
		assert.Empty(t, cache.upserted, "an address without a distance must not be cached")

		svc, cache, _ = newSvc("fr")
		cache.upsertErr = boom
		_, err = svc.Resolve(t.Context(), "p1", "t")
		require.ErrorIs(t, err, boom)
		assert.ErrorContains(t, err, "cache upsert")
	})
}

func TestGetByPlaceIDIsCacheOnly(t *testing.T) {
	svc, cache, g := newSvc("fr")

	miss, err := svc.GetByPlaceID(t.Context(), "p1")
	require.NoError(t, err)
	assert.Nil(t, miss)
	assert.Zero(t, g.detailsCall, "a miss does not call Google")

	cache.entries["p1"] = &domain.AddressCache{PlaceID: "p1", StreetName: sp("Rue Neuve"), DistanceMeters: 800}
	hit, err := svc.GetByPlaceID(t.Context(), "p1")
	require.NoError(t, err)
	assert.Equal(t, "Rue Neuve", hit.StreetName)
	assert.InDelta(t, 800, hit.Distance, 0)

	cache.getErr = errors.New("db down")
	_, err = svc.GetByPlaceID(t.Context(), "p1")
	assert.ErrorIs(t, err, cache.getErr)
}
