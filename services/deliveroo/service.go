package deliveroo

import (
	"context"
	"fmt"
	"log"
	"tsb-service/internal/modules/product/domain"
)

// Service handles menu synchronization with Deliveroo
type Service struct {
	client   *Client
	brandID  string
	menuID   string
	currency string
}

// NewService creates a new Deliveroo sync service
func NewService(clientID, clientSecret, brandID, menuID, currency string) *Service {
	return &Service{
		client:   NewClient(clientID, clientSecret),
		brandID:  brandID,
		menuID:   menuID,
		currency: currency,
	}
}

// SyncMenu synchronizes the local menu with Deliveroo
func (s *Service) SyncMenu(ctx context.Context, categories []*domain.Category, products []*domain.Product) error {
	log.Printf("Starting menu sync to Deliveroo (brand: %s, menu: %s)", s.brandID, s.menuID)

	// Convert local menu to Deliveroo format
	deliverooMenu, err := s.convertToDeliverooMenu(categories, products)
	if err != nil {
		return fmt.Errorf("failed to convert menu: %w", err)
	}

	// Upload to Deliveroo
	if err := s.client.UploadMenu(ctx, s.brandID, s.menuID, deliverooMenu); err != nil {
		return fmt.Errorf("failed to upload menu to Deliveroo: %w", err)
	}

	log.Printf("Successfully synced %d categories and %d items to Deliveroo",
		len(deliverooMenu.Categories), len(deliverooMenu.Items))

	return nil
}

// convertToDeliverooMenu converts local menu structure to Deliveroo format
func (s *Service) convertToDeliverooMenu(categories []*domain.Category, products []*domain.Product) (*Menu, error) {
	// Build a map of category ID to products
	categoryProducts := make(map[string][]*domain.Product)
	for _, product := range products {
		categoryID := product.CategoryID.String()
		categoryProducts[categoryID] = append(categoryProducts[categoryID], product)
	}

	// Convert categories
	deliverooCategories := make([]Category, 0, len(categories))
	deliverooItems := make([]Item, 0, len(products))

	for _, cat := range categories {
		categoryID := cat.ID.String()
		catProducts := categoryProducts[categoryID]

		// Collect item IDs for this category
		itemIDs := make([]string, 0, len(catProducts))
		for _, product := range catProducts {
			itemIDs = append(itemIDs, product.ID.String())

			// Convert product to Deliveroo item
			item, err := s.convertProductToItem(product)
			if err != nil {
				log.Printf("Warning: failed to convert product %s: %v", product.ID, err)
				continue
			}
			deliverooItems = append(deliverooItems, *item)
		}

		// Get category translation (default to French)
		catTranslation := cat.GetTranslationFor("fr")
		if catTranslation == nil && len(cat.Translations) > 0 {
			catTranslation = &cat.Translations[0]
		}

		categoryName := "Unnamed Category"
		categoryDescription := ""
		if catTranslation != nil {
			categoryName = catTranslation.Name
			if catTranslation.Description != nil {
				categoryDescription = *catTranslation.Description
			}
		}

		deliverooCategories = append(deliverooCategories, Category{
			ID:          categoryID,
			Name:        categoryName,
			Description: categoryDescription,
			ItemIDs:     itemIDs,
		})
	}

	return &Menu{
		Name:       "Restaurant Menu",
		Categories: deliverooCategories,
		Items:      deliverooItems,
	}, nil
}

// convertProductToItem converts a local product to a Deliveroo item
func (s *Service) convertProductToItem(product *domain.Product) (*Item, error) {
	// Get product translation (default to French)
	translation := product.GetTranslationFor("fr")
	if translation == nil {
		return nil, fmt.Errorf("no translation available for product %s", product.ID)
	}

	// Convert price from decimal to cents
	priceFloat, _ := product.Price.Float64()
	priceCents := int(priceFloat * 100)

	description := ""
	if translation.Description != nil {
		description = *translation.Description
	}

	// Build tags based on product attributes
	tags := make([]string, 0)
	if product.IsVegan {
		tags = append(tags, "vegan")
	}
	if product.IsHalal {
		tags = append(tags, "halal")
	}

	return &Item{
		ID:          product.ID.String(),
		Name:        translation.Name,
		Description: description,
		Price: Price{
			Amount:   priceCents,
			Currency: s.currency,
		},
		Available: product.IsAvailable,
		Visible:   product.IsVisible,
		Tags:      tags,
	}, nil
}