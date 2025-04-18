package resolver

// This file will be automatically regenerated based on the schema, any resolver implementations
// will be copied through when generating and any unknown code will be moved to the end.
// Code generated by github.com/99designs/gqlgen version v0.17.70

import (
	"context"
	"fmt"
	graphql1 "tsb-service/internal/api/graphql"
	"tsb-service/internal/api/graphql/model"
	addressDomain "tsb-service/internal/modules/address/domain"
)

// Streets is the resolver for the streets field.
func (r *queryResolver) Streets(ctx context.Context, query string) ([]*model.Street, error) {
	s, err := r.AddressService.SearchStreetNames(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to search street names: %w", err)
	}

	// Map the street names to the GraphQL model
	streets := Map(s, func(street *addressDomain.Street) *model.Street {
		return ToGQLStreet(street)
	})

	// Return empty array if no streets were found.
	if len(streets) == 0 {
		return []*model.Street{}, nil
	}

	// Return the list
	return streets, nil
}

// HouseNumbers is the resolver for the houseNumbers field.
func (r *queryResolver) HouseNumbers(ctx context.Context, streetID string) ([]string, error) {
	houseNumbers, err := r.AddressService.GetDistinctHouseNumbers(ctx, streetID)
	if err != nil {
		return nil, fmt.Errorf("failed to get house numbers: %w", err)
	}

	// Return the list
	return houseNumbers, nil
}

// BoxNumbers is the resolver for the boxNumbers field.
func (r *queryResolver) BoxNumbers(ctx context.Context, streetID string, houseNumber string) ([]*string, error) {
	boxNumbers, err := r.AddressService.GetBoxNumbers(ctx, streetID, houseNumber)
	if err != nil {
		return nil, fmt.Errorf("failed to get box numbers: %w", err)
	}

	// Return the list
	return boxNumbers, nil
}

// Address is the resolver for the address field.
func (r *queryResolver) Address(ctx context.Context, id string) (*model.Address, error) {
	a, err := r.AddressService.GetAddressByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get address by id: %w", err)
	}

	return ToGQLAddress(a), nil
}

// AddressByLocation is the resolver for the addressByLocation field.
func (r *queryResolver) AddressByLocation(ctx context.Context, streetID string, houseNumber string, boxNumber *string) (*model.Address, error) {
	a, err := r.AddressService.GetFinalAddress(ctx, streetID, houseNumber, boxNumber)
	if err != nil {
		return nil, fmt.Errorf("failed to get final address: %w", err)
	}

	return ToGQLAddress(a), nil
}

// Query returns graphql1.QueryResolver implementation.
func (r *Resolver) Query() graphql1.QueryResolver { return &queryResolver{r} }

type queryResolver struct{ *Resolver }
