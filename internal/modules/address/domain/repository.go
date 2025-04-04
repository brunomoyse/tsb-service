package domain

import (
	"context"
)

// AddressRepository defines the contract for persisting Address aggregates.
type AddressRepository interface {
	SearchStreetNames(ctx context.Context, query string) ([]Street, error)
	GetDistinctHouseNumbers(ctx context.Context, streetName string) ([]string, error)
	GetBoxNumbers(ctx context.Context, streetName, houseNumber string) ([]*string, error)
	GetFinalAddress(ctx context.Context, streetID string, houseNumber string, boxNumber *string) (*Address, error)
	GetAddressByID(ctx context.Context, ID string) (*Address, error)
}
