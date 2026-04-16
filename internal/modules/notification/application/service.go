package application

import (
	"context"

	"github.com/google/uuid"

	"tsb-service/internal/modules/notification/domain"
)

// NotificationService defines the use cases for push notification token management.
type NotificationService interface {
	// Device push tokens
	RegisterDeviceToken(ctx context.Context, userID uuid.UUID, deviceToken, platform, role string) error
	GetDeviceTokens(ctx context.Context, userID uuid.UUID) ([]domain.DevicePushToken, error)
	GetAdminDeviceTokens(ctx context.Context) ([]domain.DevicePushToken, error)
	UnregisterDeviceToken(ctx context.Context, userID uuid.UUID, deviceToken string) error
}

type notificationService struct {
	repo domain.NotificationRepository
}

// NewNotificationService constructs a NotificationService with the given repository.
func NewNotificationService(repo domain.NotificationRepository) NotificationService {
	return &notificationService{repo: repo}
}

func (s *notificationService) RegisterDeviceToken(ctx context.Context, userID uuid.UUID, deviceToken, platform, role string) error {
	return s.repo.SaveDeviceToken(ctx, userID, deviceToken, platform, role)
}

func (s *notificationService) GetDeviceTokens(ctx context.Context, userID uuid.UUID) ([]domain.DevicePushToken, error) {
	return s.repo.FindDeviceTokensByUserID(ctx, userID)
}

func (s *notificationService) GetAdminDeviceTokens(ctx context.Context) ([]domain.DevicePushToken, error) {
	return s.repo.FindDeviceTokensByRole(ctx, "admin")
}

func (s *notificationService) UnregisterDeviceToken(ctx context.Context, userID uuid.UUID, deviceToken string) error {
	return s.repo.DeleteDeviceToken(ctx, userID, deviceToken)
}
