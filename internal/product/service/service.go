package service

import (
	model "tsb-service/internal/product"
	"tsb-service/internal/product/repository"

	"github.com/google/uuid"
)

type ProductService interface {
	GetDashboardProducts(lang string) ([]model.DashboardProductListItem, error)
	GetDashboardProductByID(productID uuid.UUID) (model.DashboardProductDetails, error)
	GetDashboardCategories() ([]model.DashboardCategoryDetails, error)
	GetProductsGroupedByCategory(lang string) ([]model.CategoryWithProducts, error)
	GetCategories(lang string) ([]model.Category, error)
	GetProductsByCategory(lang string, categoryID uuid.UUID) ([]model.ProductInfo, error)
	UpdateProduct(productID uuid.UUID, form model.UpdateProductForm) (model.ProductFormResponse, error)
	CreateProduct(form model.CreateProductForm) (model.ProductFormResponse, error)
	CategoryExists(categoryID uuid.UUID) (bool, error)
}

type productService struct {
	repo repository.ProductRepository
}

func NewProductService(r repository.ProductRepository) ProductService {
	return &productService{
		repo: r,
	}
}

func (s *productService) GetDashboardProducts(lang string) ([]model.DashboardProductListItem, error) {
	return s.repo.GetDashboardProducts(lang)
}

func (s *productService) GetDashboardProductByID(productID uuid.UUID) (model.DashboardProductDetails, error) {
	return s.repo.GetDashboardProductByID(productID)
}

func (s *productService) GetDashboardCategories() ([]model.DashboardCategoryDetails, error) {
	return s.repo.GetDashboardCategories()
}

func (s *productService) GetProductsGroupedByCategory(lang string) ([]model.CategoryWithProducts, error) {
	return s.repo.GetProductsGroupedByCategory(lang)
}

func (s *productService) GetCategories(lang string) ([]model.Category, error) {
	return s.repo.GetCategories(lang)
}

func (s *productService) GetProductsByCategory(lang string, categoryID uuid.UUID) ([]model.ProductInfo, error) {
	return s.repo.GetProductsByCategory(lang, categoryID)
}

func (s *productService) UpdateProduct(productID uuid.UUID, form model.UpdateProductForm) (model.ProductFormResponse, error) {
	return s.repo.UpdateProduct(productID, form)
}

func (s *productService) CreateProduct(form model.CreateProductForm) (model.ProductFormResponse, error) {
	exists, err := s.repo.CategoryExists(*form.CategoryId)
	if err != nil {
		return model.ProductFormResponse{}, err
	}
	if !exists {
		return model.ProductFormResponse{}, nil
	}
	return s.repo.CreateProduct(form)
}

func (s *productService) CategoryExists(categoryID uuid.UUID) (bool, error) {
	return s.repo.CategoryExists(categoryID)
}
