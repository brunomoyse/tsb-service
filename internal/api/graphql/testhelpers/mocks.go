package testhelpers

import (
	"github.com/stretchr/testify/mock"
)

// MockMollieClient is a mock implementation of the Mollie API client
type MockMollieClient struct {
	mock.Mock
}

// Add Mollie client methods as needed during test implementation

// MockEmailService is a mock implementation of the email service
type MockEmailService struct {
	mock.Mock
}

// SendEmail mocks the email sending functionality
func (m *MockEmailService) SendEmail(to, subject, body string) error {
	args := m.Called(to, subject, body)
	return args.Error(0)
}

// MockDeliverooService is a mock implementation of the Deliveroo service
type MockDeliverooService struct {
	mock.Mock
}

// Add Deliveroo service methods as needed during test implementation

// NewMockServices creates all mock services for testing
func NewMockServices() (*MockMollieClient, *MockEmailService, *MockDeliverooService) {
	return &MockMollieClient{}, &MockEmailService{}, &MockDeliverooService{}
}
