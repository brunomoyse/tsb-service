package application

import (
	"context"

	"github.com/google/uuid"

	"tsb-service/internal/modules/notification/domain"
)

// NotificationService defines the use cases for push notification token management.
type NotificationService interface {
	// Live Activity tokens
	RegisterLiveActivityToken(ctx context.Context, orderID uuid.UUID, pushToken string) error
	GetLiveActivityTokens(ctx context.Context, orderID uuid.UUID) ([]string, error)
	CleanupLiveActivityTokens(ctx context.Context, orderID uuid.UUID) error

	// Device push tokens
	RegisterDeviceToken(ctx context.Context, userID uuid.UUID, deviceToken, platform string) error
	GetDeviceTokens(ctx context.Context, userID uuid.UUID) ([]domain.DevicePushToken, error)
	UnregisterDeviceToken(ctx context.Context, userID uuid.UUID, deviceToken string) error
}

type notificationService struct {
	repo domain.NotificationRepository
}

// NewNotificationService constructs a NotificationService with the given repository.
func NewNotificationService(repo domain.NotificationRepository) NotificationService {
	return &notificationService{repo: repo}
}

func (s *notificationService) RegisterLiveActivityToken(ctx context.Context, orderID uuid.UUID, pushToken string) error {
	return s.repo.SaveLiveActivityToken(ctx, orderID, pushToken)
}

func (s *notificationService) GetLiveActivityTokens(ctx context.Context, orderID uuid.UUID) ([]string, error) {
	return s.repo.FindLiveActivityTokensByOrderID(ctx, orderID)
}

func (s *notificationService) CleanupLiveActivityTokens(ctx context.Context, orderID uuid.UUID) error {
	return s.repo.DeleteLiveActivityTokensByOrderID(ctx, orderID)
}

func (s *notificationService) RegisterDeviceToken(ctx context.Context, userID uuid.UUID, deviceToken, platform string) error {
	return s.repo.SaveDeviceToken(ctx, userID, deviceToken, platform)
}

func (s *notificationService) GetDeviceTokens(ctx context.Context, userID uuid.UUID) ([]domain.DevicePushToken, error) {
	return s.repo.FindDeviceTokensByUserID(ctx, userID)
}

func (s *notificationService) UnregisterDeviceToken(ctx context.Context, userID uuid.UUID, deviceToken string) error {
	return s.repo.DeleteDeviceToken(ctx, userID, deviceToken)
}
