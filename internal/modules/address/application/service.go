package application

import (
	"context"
	"tsb-service/internal/modules/address/domain"
)

type AddressService interface {
	SearchStreetNames(ctx context.Context, query string) ([]*domain.Street, error)
	GetDistinctHouseNumbers(ctx context.Context, streetID string) ([]string, error)
	GetBoxNumbers(ctx context.Context, streetID string, houseNumber string) ([]*string, error)
	GetFinalAddress(ctx context.Context, streetID string, houseNumber string, boxNumber *string) (*domain.Address, error)
	GetAddressByID(ctx context.Context, ID string) (*domain.Address, error)

	BatchGetAddressesByOrderIDs(ctx context.Context, orderIDs []string) (map[string][]*domain.Address, error)
}

type addressService struct {
	repo domain.AddressRepository
}

func NewAddressService(repo domain.AddressRepository) AddressService {
	return &addressService{
		repo: repo,
	}
}

func (s *addressService) SearchStreetNames(ctx context.Context, query string) ([]*domain.Street, error) {
	streetNames, err := s.repo.SearchStreetNames(ctx, query)
	if err != nil {
		return nil, err
	}
	return streetNames, nil
}

func (s *addressService) GetDistinctHouseNumbers(ctx context.Context, streetID string) ([]string, error) {
	houseNumbers, err := s.repo.GetDistinctHouseNumbers(ctx, streetID)
	if err != nil {
		return nil, err
	}
	return houseNumbers, nil
}

func (s *addressService) GetBoxNumbers(ctx context.Context, streetID string, houseNumber string) ([]*string, error) {
	boxNumbers, err := s.repo.GetBoxNumbers(ctx, streetID, houseNumber)
	if err != nil {
		return nil, err
	}
	return boxNumbers, nil
}

func (s *addressService) GetFinalAddress(ctx context.Context, streetID string, houseNumber string, boxNumber *string) (*domain.Address, error) {
	return s.repo.GetFinalAddress(ctx, streetID, houseNumber, boxNumber)
}

func (s *addressService) GetAddressByID(ctx context.Context, ID string) (*domain.Address, error) {
	return s.repo.GetAddressByID(ctx, ID)
}

func (s *addressService) BatchGetAddressesByOrderIDs(ctx context.Context, orderIDs []string) (map[string][]*domain.Address, error) {
	return s.repo.BatchGetAddressesByOrderIDs(ctx, orderIDs)
}
