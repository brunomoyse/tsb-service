package application

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"tsb-service/internal/modules/hubrise_webshop/domain"
	"tsb-service/internal/modules/hubrise_webshop/infrastructure"
	productApp "tsb-service/internal/modules/product/application"
	"tsb-service/pkg/logging"
)

// ClientName identifies the HubRise client row (one per OAuth token).
const ClientName = "tsb-webshop"

// CatalogPusher orchestrates building the catalog snapshot and
// pushing it to HubRise via `PUT /v1/catalogs/:id`.
//
// It implements `productApp.CatalogPushTrigger` so it can be injected
// into the product module for automatic pushes after each mutation.
type CatalogPusher struct {
	baseURL     string
	productSvc  productApp.ProductService
	connRepo    domain.ConnectionRepository
	syncRepo    domain.CatalogSyncStateRepository

	debounceMu sync.Mutex
	debounceTimer *time.Timer
}

// NewCatalogPusher constructs a new pusher.
func NewCatalogPusher(
	baseURL string,
	productSvc productApp.ProductService,
	connRepo domain.ConnectionRepository,
	syncRepo domain.CatalogSyncStateRepository,
) *CatalogPusher {
	return &CatalogPusher{
		baseURL:    baseURL,
		productSvc: productSvc,
		connRepo:   connRepo,
		syncRepo:   syncRepo,
	}
}

// TriggerPush implements productApp.CatalogPushTrigger by scheduling
// a debounced Push() call 2 seconds after the most recent change.
func (p *CatalogPusher) TriggerPush(ctx context.Context) {
	p.debounceMu.Lock()
	defer p.debounceMu.Unlock()
	if p.debounceTimer != nil {
		p.debounceTimer.Stop()
	}
	// We intentionally detach the background push from the caller
	// ctx — the request context would be cancelled by the time the
	// timer fires.
	p.debounceTimer = time.AfterFunc(2*time.Second, func() {
		bgCtx := context.Background()
		if err := p.Push(bgCtx); err != nil {
			logging.FromContext(bgCtx).Error("hubrise catalog push failed", zap.Error(err))
		}
	})
}

// Push builds the full catalog snapshot and PUTs it to HubRise.
func (p *CatalogPusher) Push(ctx context.Context) error {
	conn, err := p.connRepo.GetByClient(ctx, ClientName)
	if err != nil {
		return fmt.Errorf("load hubrise connection: %w", err)
	}
	if conn == nil || conn.CatalogID == nil || *conn.CatalogID == "" {
		// Not connected yet — nothing to push.
		return nil
	}

	catalogID := *conn.CatalogID
	snapshot, err := p.buildSnapshot(ctx)
	if err != nil {
		p.markFailed(ctx, err)
		return err
	}

	client := infrastructure.NewHTTPClient(p.baseURL, conn.AccessToken)
	if _, err := client.PutJSON(ctx, "/catalogs/"+catalogID, snapshot); err != nil {
		p.markFailed(ctx, err)
		return err
	}

	p.markSuccess(ctx)
	return nil
}

// buildSnapshot fetches all categories + products + choices from the
// product module and converts them to HubRise JSON.
func (p *CatalogPusher) buildSnapshot(ctx context.Context) (*domain.HubriseCatalog, error) {
	categories, err := p.productSvc.GetCategories(ctx)
	if err != nil {
		return nil, fmt.Errorf("get categories: %w", err)
	}
	products, err := p.productSvc.GetProducts(ctx)
	if err != nil {
		return nil, fmt.Errorf("get products: %w", err)
	}

	hubCats := make([]domain.HubriseCategory, 0, len(categories))
	catRefByID := make(map[string]string, len(categories))
	for _, c := range categories {
		ref := c.ID.String()
		catRefByID[c.ID.String()] = ref
		hubCats = append(hubCats, domain.MapCategoryToHubrise(c, ref))
	}

	hubProducts := make([]domain.HubriseProduct, 0, len(products))
	for _, pr := range products {
		ref, ok := catRefByID[pr.CategoryID.String()]
		if !ok {
			continue
		}
		hubProducts = append(hubProducts, domain.MapProductToHubrise(pr, ref))
	}

	return &domain.HubriseCatalog{
		Name: "Menu principal",
		Data: domain.HubriseCatalogData{
			Categories: hubCats,
			Products:   hubProducts,
		},
	}, nil
}

func (p *CatalogPusher) markSuccess(ctx context.Context) {
	now := time.Now()
	status := "success"
	_ = p.syncRepo.Upsert(ctx, &domain.CatalogSyncState{
		ClientName:     ClientName,
		LastPushedAt:   &now,
		LastPushStatus: &status,
	})
}

func (p *CatalogPusher) markFailed(ctx context.Context, err error) {
	now := time.Now()
	status := "failed"
	msg := err.Error()
	_ = p.syncRepo.Upsert(ctx, &domain.CatalogSyncState{
		ClientName:     ClientName,
		LastPushedAt:   &now,
		LastPushStatus: &status,
		LastError:      &msg,
	})
}
