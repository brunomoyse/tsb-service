package resolver

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"tsb-service/internal/api/graphql/model"
	posDomain "tsb-service/internal/modules/pos/domain"
)

func toGQLPosDevice(d *posDomain.Device) *model.PosDevice {
	return &model.PosDevice{
		ID:           d.ID,
		Label:        d.Label,
		SerialNumber: d.SerialNumber,
		RegisteredAt: d.RegisteredAt,
		LastSeenAt:   d.LastSeenAt,
		RevokedAt:    d.RevokedAt,
	}
}

// SetUserRrn is the resolver for the setUserRrn field.
func (r *mutationResolver) SetUserRrn(ctx context.Context, userID uuid.UUID, rrn string) (*model.User, error) {
	if err := r.PosService.SetUserRRN(ctx, userID, rrn); err != nil {
		return nil, err
	}
	return r.fetchUser(ctx, userID)
}

// SetUserPin is the resolver for the setUserPin field.
func (r *mutationResolver) SetUserPin(ctx context.Context, userID uuid.UUID, pin string) (*model.User, error) {
	if err := r.PosService.SetUserPin(ctx, userID, pin); err != nil {
		return nil, err
	}
	return r.fetchUser(ctx, userID)
}

// RevokePosDevice is the resolver for the revokePosDevice field.
func (r *mutationResolver) RevokePosDevice(ctx context.Context, id uuid.UUID) (*model.PosDevice, error) {
	if err := r.PosService.RevokeDevice(ctx, id); err != nil {
		return nil, err
	}
	devices, err := r.PosService.ListDevices(ctx)
	if err != nil {
		return nil, err
	}
	for i := range devices {
		if devices[i].ID == id {
			return toGQLPosDevice(&devices[i]), nil
		}
	}
	return nil, fmt.Errorf("device not found after revoke")
}

// PosDevices is the resolver for the posDevices field.
func (r *queryResolver) PosDevices(ctx context.Context) ([]*model.PosDevice, error) {
	list, err := r.PosService.ListDevices(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*model.PosDevice, 0, len(list))
	for i := range list {
		out = append(out, toGQLPosDevice(&list[i]))
	}
	return out, nil
}

func (r *Resolver) fetchUser(ctx context.Context, id uuid.UUID) (*model.User, error) {
	u, err := r.UserService.GetUserByID(ctx, id.String())
	if err != nil {
		return nil, fmt.Errorf("load user: %w", err)
	}
	return ToGQLUser(u), nil
}
