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

// ListSites retrieves available sites for the configured brand
func (s *Service) ListSites(ctx context.Context) ([]SiteConfig, error) {
	sitesConfig, err := s.adapter.GetSitesConfig(ctx, s.brandID)
	if err != nil {
		return nil, fmt.Errorf("failed to get sites config: %w", err)
	}

	log.Printf("Found %d sites for brand %s", len(sitesConfig.Sites), s.brandID)
	for _, site := range sitesConfig.Sites {
		log.Printf("  - Site: %s (ID: %s, Webhook: %s)", site.Name, site.LocationID, site.OrdersAPIWebhookType)
	}

	return sitesConfig.Sites, nil
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
		ID:                        product.ID.String(),
		Name:                      map[string]string{"fr": translation.Name},
		Description:               description,
		OperationalName:           translation.Name,
		PriceInfo:                 PriceInfo{Price: priceCents},
		PLU:                       product.ID.String(), // Use product UUID as PLU for direct database lookup
		TaxRate:                   "0",
		ContainsAlcohol:           false,
		Allergies:                 allergies,
		Diets:                     diets,
		Type:                      "ITEM",
		IsEligibleAsReplacement:   true,
		IsEligibleForSubstitution: true,
	}, nil
}

// MenuSyncPreview contains the differences between local and Deliveroo menus
type MenuSyncPreview struct {
	ToCreate []*ProductToCreate
	ToUpdate []*ProductToUpdate
	ToDelete []*ProductToDelete
}

// ProductToCreate represents a new product that would be created on Deliveroo
type ProductToCreate struct {
	Name         string
	Price        float64
	Description  *string
	Category     string
	IsAvailable  bool
	IsVisible    bool
}

// ProductToUpdate represents a product that exists but has differences
type ProductToUpdate struct {
	ID                  string
	Name                string
	CurrentPrice        float64
	NewPrice            *float64
	CurrentDescription  *string
	NewDescription      *string
	CurrentAvailability bool
	NewAvailability     *bool
	CurrentVisibility   bool
	NewVisibility       *bool
}

// ProductToDelete represents a product that exists locally but not on Deliveroo
type ProductToDelete struct {
	ID     string
	Name   string
	Reason string
}

// PreviewMenuSync compares local menu with Deliveroo menu and returns differences
// If outletID (siteID) is configured, uses V2 API; otherwise uses V1 with menuID
func (s *Service) PreviewMenuSync(ctx context.Context, categories []*domain.Category, products []*domain.Product) (*MenuSyncPreview, error) {
	log.Printf("Previewing menu sync differences (brand: %s, menu/site: %s/%s)", s.brandID, s.menuID, s.outletID)

	// Fetch current Deliveroo menu - prefer V2 API with siteID if available
	var deliverooMenu *MenuUploadRequest
	var err error

	if s.outletID != "" {
		// Use V2 API with siteID (recommended)
		log.Printf("Using V2 API to fetch menu for site: %s", s.outletID)
		deliverooMenu, err = s.adapter.GetMenuV2(ctx, s.brandID, s.outletID)
	} else if s.menuID != "" {
		// Fallback to V1 API with menuID
		log.Printf("Using V1 API to fetch menu: %s", s.menuID)
		deliverooMenu, err = s.adapter.PullMenu(ctx, s.brandID, s.menuID)
	} else {
		return nil, fmt.Errorf("either menuID or outletID (siteID) must be configured")
	}

	if err != nil {
		return nil, fmt.Errorf("failed to fetch Deliveroo menu: %w", err)
	}

	// Convert local menu to Deliveroo format
	localMenu, err := s.convertToDeliverooMenuUpload(categories, products)
	if err != nil {
		return nil, fmt.Errorf("failed to convert local menu: %w", err)
	}

	// Build maps for comparison
	deliverooItems := make(map[string]Item)
	for _, item := range deliverooMenu.Menu.Items {
		deliverooItems[item.ID] = item
	}

	deliverooCategories := make(map[string]Category)
	for _, cat := range deliverooMenu.Menu.Categories {
		deliverooCategories[cat.ID] = cat
	}

	localItems := make(map[string]Item)
	for _, item := range localMenu.Menu.Items {
		localItems[item.ID] = item
	}

	localCategories := make(map[string]Category)
	for _, cat := range localMenu.Menu.Categories {
		localCategories[cat.ID] = cat
	}

	preview := &MenuSyncPreview{
		ToCreate: []*ProductToCreate{},
		ToUpdate: []*ProductToUpdate{},
		ToDelete: []*ProductToDelete{},
	}

	// Find items to create (exist locally but not on Deliveroo)
	for id, localItem := range localItems {
		if _, exists := deliverooItems[id]; !exists {
			// Find category name
			categoryName := "Unknown"
			for _, cat := range localCategories {
				for _, itemID := range cat.ItemIDs {
					if itemID == id {
						if name, ok := cat.Name["fr"]; ok {
							categoryName = name
						}
						break
					}
				}
			}

			var description *string
			if desc, ok := localItem.Description["fr"]; ok && desc != "" {
				description = &desc
			}

			name := "Unnamed"
			if n, ok := localItem.Name["fr"]; ok {
				name = n
			}

			preview.ToCreate = append(preview.ToCreate, &ProductToCreate{
				Name:        name,
				Price:       float64(localItem.PriceInfo.Price) / 100.0,
				Description: description,
				Category:    categoryName,
				IsAvailable: true,  // Default to true for new items
				IsVisible:   true,  // Default to true for new items
			})
		}
	}

	// Find items to update (exist in both but have differences)
	for id, localItem := range localItems {
		if deliverooItem, exists := deliverooItems[id]; exists {
			hasChanges := false
			update := &ProductToUpdate{
				ID:                  id,
				Name:                getItemName(localItem),
				CurrentPrice:        float64(deliverooItem.PriceInfo.Price) / 100.0,
				CurrentAvailability: true, // Assume available if it exists
				CurrentVisibility:   true, // Assume visible if it exists
			}

			// Check price difference
			if localItem.PriceInfo.Price != deliverooItem.PriceInfo.Price {
				newPrice := float64(localItem.PriceInfo.Price) / 100.0
				update.NewPrice = &newPrice
				hasChanges = true
			}

			// Check description difference
			localDesc := getItemDescription(localItem)
			deliverooDesc := getItemDescription(deliverooItem)
			update.CurrentDescription = deliverooDesc
			if !stringPtrEqual(localDesc, deliverooDesc) {
				update.NewDescription = localDesc
				hasChanges = true
			}

			if hasChanges {
				preview.ToUpdate = append(preview.ToUpdate, update)
			}
		}
	}

	// Find items to delete (exist on Deliveroo but not locally)
	for id, deliverooItem := range deliverooItems {
		if _, exists := localItems[id]; !exists {
			preview.ToDelete = append(preview.ToDelete, &ProductToDelete{
				ID:     id,
				Name:   getItemName(deliverooItem),
				Reason: "Item no longer exists in local menu",
			})
		}
	}

	log.Printf("Preview complete: %d to create, %d to update, %d to delete",
		len(preview.ToCreate), len(preview.ToUpdate), len(preview.ToDelete))

	return preview, nil
}

// Helper functions

func getItemName(item Item) string {
	if name, ok := item.Name["fr"]; ok {
		return name
	}
	if name, ok := item.Name["en"]; ok {
		return name
	}
	return "Unnamed"
}

func getItemDescription(item Item) *string {
	if desc, ok := item.Description["fr"]; ok && desc != "" {
		return &desc
	}
	if desc, ok := item.Description["en"]; ok && desc != "" {
		return &desc
	}
	return nil
}

func stringPtrEqual(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}