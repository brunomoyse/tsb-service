package application

import (
	"context"
	"fmt"
	"time"

	"tsb-service/internal/modules/address/domain"
)

type AddressService interface {
	Autocomplete(ctx context.Context, input, sessionToken string) ([]domain.Suggestion, error)
	Resolve(ctx context.Context, placeID, sessionToken string) (*domain.Address, error)
	GetByPlaceID(ctx context.Context, placeID string) (*domain.Address, error) // cache-only; no Google call
}

type addressService struct {
	cache    domain.AddressCacheRepository
	google   domain.GoogleClient
	language string
}

func NewAddressService(cache domain.AddressCacheRepository, google domain.GoogleClient, language string) AddressService {
	if language == "" {
		language = "fr"
	}
	return &addressService{cache: cache, google: google, language: language}
}

func (s *addressService) Autocomplete(ctx context.Context, input, sessionToken string) ([]domain.Suggestion, error) {
	return s.google.Autocomplete(ctx, input, sessionToken, s.language)
}

func (s *addressService) Resolve(ctx context.Context, placeID, sessionToken string) (*domain.Address, error) {
	if placeID == "" {
		return nil, fmt.Errorf("placeID required")
	}

	// 1) Cache hit
	cached, err := s.cache.GetByPlaceID(ctx, placeID)
	if err != nil {
		return nil, fmt.Errorf("cache lookup: %w", err)
	}
	if cached != nil {
		return cacheToAddress(cached), nil
	}

	// 2) Cache miss — ask Google
	details, err := s.google.PlaceDetails(ctx, placeID, sessionToken, s.language)
	if err != nil {
		return nil, fmt.Errorf("place details: %w", err)
	}

	distance, duration, err := s.google.ComputeRoute(ctx, details.Lat, details.Lng)
	if err != nil {
		return nil, fmt.Errorf("compute route: %w", err)
	}
	details.DistanceMeters = distance
	details.DurationSeconds = duration
	details.RefreshedAt = time.Now()

	if err := s.cache.Upsert(ctx, details); err != nil {
		return nil, fmt.Errorf("cache upsert: %w", err)
	}
	return cacheToAddress(details), nil
}

func (s *addressService) GetByPlaceID(ctx context.Context, placeID string) (*domain.Address, error) {
	cached, err := s.cache.GetByPlaceID(ctx, placeID)
	if err != nil {
		return nil, err
	}
	if cached == nil {
		return nil, nil
	}
	return cacheToAddress(cached), nil
}

func cacheToAddress(c *domain.AddressCache) *domain.Address {
	streetName := ""
	if c.StreetName != nil {
		streetName = *c.StreetName
	}
	houseNumber := ""
	if c.HouseNumber != nil {
		houseNumber = *c.HouseNumber
	}
	postcode := ""
	if c.Postcode != nil {
		postcode = *c.Postcode
	}
	municipalityName := ""
	if c.MunicipalityName != nil {
		municipalityName = *c.MunicipalityName
	}
	lat := c.Lat
	lng := c.Lng
	dur := c.DurationSeconds
	return &domain.Address{
		ID:               c.PlaceID,
		PlaceID:          c.PlaceID,
		StreetName:       streetName,
		HouseNumber:      houseNumber,
		BoxNumber:        c.BoxNumber,
		Postcode:         postcode,
		MunicipalityName: municipalityName,
		Distance:         float64(c.DistanceMeters),
		Lat:              &lat,
		Lng:              &lng,
		Duration:         &dur,
	}
}
