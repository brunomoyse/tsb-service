package domain

import "context"

type AddressCacheRepository interface {
	GetByPlaceID(ctx context.Context, placeID string) (*AddressCache, error) // returns (nil, nil) on miss
	Upsert(ctx context.Context, entry *AddressCache) error
}

type GoogleClient interface {
	Autocomplete(ctx context.Context, input, sessionToken, language string) ([]Suggestion, error)
	PlaceDetails(ctx context.Context, placeID, sessionToken, language string) (*AddressCache, error) // returns cache-ready entry WITHOUT distance_meters/duration_seconds
	ComputeRoute(ctx context.Context, destLat, destLng float64) (distanceMeters int, durationSeconds int, err error)
}
