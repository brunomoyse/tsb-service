package deliveroo

import (
	"context"
	"fmt"
	"log"
	"tsb-service/internal/modules/product/domain"
)

// Service handles menu synchronization with Deliveroo
type Service struct {
	adapter  *DeliverooAdapter
	brandID  string
	menuID   string
	currency string
	outletID string
}

// NewService creates a new Deliveroo sync service
func NewService(clientID, clientSecret, brandID, menuID, currency string) *Service {
	return NewServiceWithConfig(ServiceConfig{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		BrandID:      brandID,
		MenuID:       menuID,
		Currency:     currency,
		UseSandbox:   false,
	})
}

// ServiceConfig contains configuration for the Deliveroo service
type ServiceConfig struct {
	ClientID     string
	ClientSecret string
	BrandID      string
	MenuID       string
	OutletID     string
	Currency     string
	UseSandbox   bool
}

// NewServiceWithConfig creates a new Deliveroo service with full configuration
func NewServiceWithConfig(config ServiceConfig) *Service {
	adapter := NewAdapter(AdapterConfig{
		ClientID:     config.ClientID,
		ClientSecret: config.ClientSecret,
		UseSandbox:   config.UseSandbox,
	})

	return &Service{
		adapter:  adapter,
		brandID:  config.BrandID,
		menuID:   config.MenuID,
		currency: config.Currency,
		outletID: config.OutletID,
	}
}

// GetAdapter returns the underlying DeliverooAdapter for direct API access
func (s *Service) GetAdapter() *DeliverooAdapter {
	return s.adapter
}

// SyncMenu synchronizes the local menu with Deliveroo
func (s *Service) SyncMenu(ctx context.Context, categories []*domain.Category, products []*domain.Product) error {
	log.Printf("Starting menu sync to Deliveroo (brand: %s, menu: %s)", s.brandID, s.menuID)

	// Convert local menu to new Deliveroo format
	deliverooMenu, err := s.convertToDeliverooMenuUpload(categories, products)
	if err != nil {
		return fmt.Errorf("failed to convert menu: %w", err)
	}

	// Upload to Deliveroo using the adapter
	if err := s.adapter.PushMenu(ctx, s.brandID, s.menuID, deliverooMenu); err != nil {
		return fmt.Errorf("failed to upload menu to Deliveroo: %w", err)
	}

	log.Printf("Successfully synced %d categories and %d items to Deliveroo",
		len(deliverooMenu.Menu.Categories), len(deliverooMenu.Menu.Items))

	return nil
}

// convertToDeliverooMenuUpload converts local menu structure to new Deliveroo format
func (s *Service) convertToDeliverooMenuUpload(categories []*domain.Category, products []*domain.Product) (*MenuUploadRequest, error) {
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
			item, err := s.convertProductToNewItem(product)
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
		categoryDesc := map[string]string{}
		if catTranslation != nil {
			categoryName = catTranslation.Name
			if catTranslation.Description != nil {
				categoryDesc["fr"] = *catTranslation.Description
			}
		}

		deliverooCategories = append(deliverooCategories, Category{
			ID:          categoryID,
			Name:        map[string]string{"fr": categoryName},
			Description: categoryDesc,
			ItemIDs:     itemIDs,
		})
	}

	// Get site IDs (use outletID if configured, otherwise empty)
	siteIDs := []string{}
	if s.outletID != "" {
		siteIDs = append(siteIDs, s.outletID)
	}

	return &MenuUploadRequest{
		Name: "Restaurant Menu",
		Menu: MenuContent{
			Categories: deliverooCategories,
			Items:      deliverooItems,
		},
		SiteIDs: siteIDs,
	}, nil
}

// convertProductToNewItem converts a local product to the new Deliveroo item format
func (s *Service) convertProductToNewItem(product *domain.Product) (*Item, error) {
	// Get product translation (default to French)
	translation := product.GetTranslationFor("fr")
	if translation == nil {
		return nil, fmt.Errorf("no translation available for product %s", product.ID)
	}

	// Convert price from decimal to cents
	priceFloat, _ := product.Price.Float64()
	priceCents := int(priceFloat * 100)

	description := map[string]string{}
	if translation.Description != nil {
		description["fr"] = *translation.Description
	}

	// Build diets based on product attributes
	diets := []string{}
	if product.IsVegan {
		diets = append(diets, "vegan")
	}

	// Build allergies (empty for now, can be extended)
	allergies := []string{}

	return &Item{
		ID:                       product.ID.String(),
		Name:                     map[string]string{"fr": translation.Name},
		Description:              description,
		OperationalName:          translation.Name,
		PriceInfo:                PriceInfo{Price: priceCents},
		TaxRate:                  "0",
		ContainsAlcohol:          false,
		Allergies:                allergies,
		Diets:                    diets,
		Type:                     "ITEM",
		IsEligibleAsReplacement:  true,
		IsEligibleForSubstitution: true,
	}, nil
}