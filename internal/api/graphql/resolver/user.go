package resolver

// This file will be automatically regenerated based on the schema, any resolver implementations
// will be copied through when generating and any unknown code will be moved to the end.
// Code generated by github.com/99designs/gqlgen version v0.17.70

import (
	"context"
	"fmt"
	graphql1 "tsb-service/internal/api/graphql"
	"tsb-service/internal/api/graphql/model"
	addressApplication "tsb-service/internal/modules/address/application"
	addressDomain "tsb-service/internal/modules/address/domain"
	orderApplication "tsb-service/internal/modules/order/application"
	orderDomain "tsb-service/internal/modules/order/domain"
	"tsb-service/pkg/utils"
)

// UpdateMe is the resolver for the updateMe field.
func (r *mutationResolver) UpdateMe(ctx context.Context, input model.UpdateUserInput) (*model.User, error) {
	userID := utils.GetUserID(ctx)

	u, err := r.UserService.UpdateMe(
		ctx,
		userID,
		input.FirstName,
		input.LastName,
		input.Email,
		input.PhoneNumber,
		input.AddressID,
	)

	if err != nil {
		return nil, err
	}

	user := ToGQLUser(u)

	return user, nil
}

// Me is the resolver for the me field.
func (r *queryResolver) Me(ctx context.Context) (*model.User, error) {
	// Load the userID from the context
	userID := utils.GetUserID(ctx)

	u, err := r.UserService.GetUserByID(ctx, userID)

	if err != nil {
		return nil, fmt.Errorf("failed to load user: %w", err)
	}

	user := ToGQLUser(u)

	return user, nil
}

// Address is the resolver for the address field.
func (r *userResolver) Address(ctx context.Context, obj *model.User) (*model.Address, error) {
	loader := addressApplication.GetUserAddressLoader(ctx)

	if loader == nil {
		return nil, fmt.Errorf("no user address loader found")
	}

	// Check for error while loading the address.
	a, err := loader.Loader.Load(ctx, obj.ID.String())
	if err != nil {
		return nil, fmt.Errorf("failed to load user address: %w", err)
	}

	// Map the address to the GraphQL model
	address := Map(a, func(address *addressDomain.Address) *model.Address {
		return ToGQLAddress(address)
	})

	return address[0], nil
}

// Orders is the resolver for the orders field.
func (r *userResolver) Orders(ctx context.Context, obj *model.User) ([]*model.Order, error) {
	loader := orderApplication.GetUserOrderLoader(ctx)

	if loader == nil {
		return nil, fmt.Errorf("no user order loader found")
	}

	// Check for error while loading the orders.
	o, err := loader.Loader.Load(ctx, obj.ID.String())
	if err != nil {
		return nil, fmt.Errorf("failed to load user orders: %w", err)
	}

	// Map the orders to the GraphQL model
	orders := Map(o, func(order *orderDomain.Order) *model.Order {
		return ToGQLOrder(order)
	})

	return orders, nil
}

// User returns graphql1.UserResolver implementation.
func (r *Resolver) User() graphql1.UserResolver { return &userResolver{r} }

type userResolver struct{ *Resolver }
