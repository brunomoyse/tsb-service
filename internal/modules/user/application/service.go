package application

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"tsb-service/internal/modules/user/domain"
	es "tsb-service/pkg/email/scaleway"
)

type UserService interface {
	GetUserByID(ctx context.Context, id string) (*domain.User, error)
	GetUserByEmail(ctx context.Context, email string) (*domain.User, error)
	UpdateMe(ctx context.Context, userID string, firstName *string, lastName *string, email *string, phoneNumber *string, addressPlaceID *string, notifyMarketing *bool, notifyOrderUpdates *bool) (*domain.User, error)
	RequestDeletion(ctx context.Context, userID string) (*domain.User, error)
	CancelDeletionRequest(ctx context.Context, userID string) (*domain.User, error)
	BatchGetUsersByOrderIDs(ctx context.Context, orderIDs []string) (map[string][]*domain.User, error)
	FindOrCreateByZitadelID(ctx context.Context, zitadelID, email, firstName, lastName string) (*domain.User, error)
	// ResolveZitadelID returns the app user UUID for a Zitadel sub (implements middleware.UserLookup)
	ResolveZitadelID(ctx context.Context, zitadelID, email, firstName, lastName string) (string, error)
}

type userService struct {
	repo domain.UserRepository
}

func NewUserService(repo domain.UserRepository) UserService {
	return &userService{
		repo: repo,
	}
}

func (s *userService) GetUserByID(ctx context.Context, id string) (*domain.User, error) {
	return s.repo.FindByID(ctx, id)
}

func (s *userService) GetUserByEmail(ctx context.Context, email string) (*domain.User, error) {
	return s.repo.FindByEmail(ctx, email)
}

func (s *userService) UpdateMe(ctx context.Context, userID string, firstName *string, lastName *string, email *string, phoneNumber *string, addressPlaceID *string, notifyMarketing *bool, notifyOrderUpdates *bool) (*domain.User, error) {
	user, err := s.repo.FindByID(ctx, userID)
	if err != nil {
		return nil, err
	}

	if firstName != nil {
		user.FirstName = *firstName
	}
	if lastName != nil {
		user.LastName = *lastName
	}
	if email != nil {
		user.Email = *email
	}
	if phoneNumber != nil {
		user.PhoneNumber = phoneNumber
	}
	if addressPlaceID != nil {
		// Empty string clears the saved default address; non-empty sets it.
		if *addressPlaceID == "" {
			user.DefaultPlaceID = nil
		} else {
			user.DefaultPlaceID = addressPlaceID
		}
	}
	if notifyMarketing != nil {
		user.NotifyMarketing = *notifyMarketing
	}
	if notifyOrderUpdates != nil {
		user.NotifyOrderUpdates = *notifyOrderUpdates
	}

	return s.repo.UpdateUser(ctx, user)
}

func (s *userService) RequestDeletion(ctx context.Context, userID string) (*domain.User, error) {
	user, err := s.repo.RequestDeletion(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to request deletion: %w", err)
	}

	go func() {
		err := es.SendDeletionRequestEmail(*user)
		if err != nil {
			zap.L().Error("failed to send deletion request email", zap.String("user_id", user.ID.String()), zap.Error(err))
		}
	}()

	return user, nil
}

func (s *userService) CancelDeletionRequest(ctx context.Context, userID string) (*domain.User, error) {
	user, err := s.repo.CancelDeletionRequest(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to cancel deletion request: %w", err)
	}
	return user, nil
}

func (s *userService) FindOrCreateByZitadelID(ctx context.Context, zitadelID, email, firstName, lastName string) (*domain.User, error) {
	// Try to find existing user by Zitadel ID
	user, err := s.repo.FindByZitadelID(ctx, zitadelID)
	if err == nil {
		return user, nil
	}

	// Not found by Zitadel ID — try by email (for migrated users not yet backfilled)
	user, err = s.repo.FindByEmail(ctx, email)
	if err == nil {
		// Link the Zitadel ID to this existing user
		user.ZitadelUserID = &zitadelID
		return s.repo.UpdateUser(ctx, user)
	}

	// Brand new user — create from OIDC claims
	newUser := domain.User{
		FirstName:     firstName,
		LastName:      lastName,
		Email:         email,
		ZitadelUserID: &zitadelID,
	}
	id, err := s.repo.Save(ctx, &newUser)
	if err != nil {
		return nil, fmt.Errorf("failed to create user from Zitadel: %w", err)
	}
	newUser.ID = id
	return &newUser, nil
}

func (s *userService) ResolveZitadelID(ctx context.Context, zitadelID, email, firstName, lastName string) (string, error) {
	user, err := s.FindOrCreateByZitadelID(ctx, zitadelID, email, firstName, lastName)
	if err != nil {
		return "", err
	}
	return user.ID.String(), nil
}

func (s *userService) BatchGetUsersByOrderIDs(ctx context.Context, orderIDs []string) (map[string][]*domain.User, error) {
	return s.repo.BatchGetUsersByOrderIDs(ctx, orderIDs)
}
