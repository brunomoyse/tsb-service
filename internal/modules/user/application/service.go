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

// ZitadelUserFetcher retrieves a user's profile from Zitadel by sub.
// Used to enrich JIT provisioning because local JWT validation (zitadel-go
// SDK oauth.WithJWT) does not populate profile claims on the auth context —
// only `sub` is guaranteed. Without this fallback, social-IdP users (Apple,
// Google) are created with empty firstName/lastName/email.
type ZitadelUserFetcher interface {
	FetchUserInfo(ctx context.Context, userID string) (email, givenName, familyName string, err error)
}

type userService struct {
	repo           domain.UserRepository
	zitadelFetcher ZitadelUserFetcher
}

func NewUserService(repo domain.UserRepository, zitadelFetcher ZitadelUserFetcher) UserService {
	return &userService{
		repo:           repo,
		zitadelFetcher: zitadelFetcher,
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
	log := zap.L().With(zap.String("zitadel_sub", zitadelID))

	// Try to find existing user by Zitadel ID
	user, err := s.repo.FindByZitadelID(ctx, zitadelID)
	if err == nil {
		// Backfill empty profile fields — e.g. Apple/Google users who were
		// JIT-created before the Zitadel fallback existed have blank name/email.
		if user.Email == "" || user.FirstName == "" || user.LastName == "" {
			email, firstName, lastName = s.enrichFromZitadel(ctx, zitadelID, email, firstName, lastName)
			changed := false
			if user.Email == "" && email != "" {
				user.Email = email
				changed = true
			}
			if user.FirstName == "" && firstName != "" {
				user.FirstName = firstName
				changed = true
			}
			if user.LastName == "" && lastName != "" {
				user.LastName = lastName
				changed = true
			}
			if changed {
				log.Info("backfilling app user profile from Zitadel",
					zap.String("email", user.Email),
					zap.String("first_name", user.FirstName),
					zap.String("last_name", user.LastName),
				)
				return s.repo.UpdateUser(ctx, user)
			}
		}
		return user, nil
	}

	// New user path — ensure we have profile data from Zitadel before creating.
	email, firstName, lastName = s.enrichFromZitadel(ctx, zitadelID, email, firstName, lastName)

	// Try by email first (for migrated users not yet backfilled)
	if email != "" {
		user, err = s.repo.FindByEmail(ctx, email)
		if err == nil {
			user.ZitadelUserID = &zitadelID
			if user.FirstName == "" && firstName != "" {
				user.FirstName = firstName
			}
			if user.LastName == "" && lastName != "" {
				user.LastName = lastName
			}
			log.Info("linking existing app user to Zitadel", zap.String("email", email))
			return s.repo.UpdateUser(ctx, user)
		}
	}

	log.Info("creating app user from Zitadel JIT",
		zap.String("email", email),
		zap.String("first_name", firstName),
		zap.String("last_name", lastName),
	)
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

// enrichFromZitadel fills in any missing profile fields by fetching the Zitadel
// user record. JWT access tokens validated locally (oauth.WithJWT) don't
// populate these fields on the auth context, so the OIDC middleware forwards
// empty strings for social-IdP logins where ID-token claims aren't available.
func (s *userService) enrichFromZitadel(ctx context.Context, zitadelID, email, firstName, lastName string) (string, string, string) {
	if s.zitadelFetcher == nil || (email != "" && firstName != "" && lastName != "") {
		return email, firstName, lastName
	}
	zEmail, zFirst, zLast, err := s.zitadelFetcher.FetchUserInfo(ctx, zitadelID)
	if err != nil {
		zap.L().Warn("zitadel user fetch failed during JIT",
			zap.String("zitadel_sub", zitadelID), zap.Error(err))
		return email, firstName, lastName
	}
	if email == "" {
		email = zEmail
	}
	if firstName == "" {
		firstName = zFirst
	}
	if lastName == "" {
		lastName = zLast
	}
	return email, firstName, lastName
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
