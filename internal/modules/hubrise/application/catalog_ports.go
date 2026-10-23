package application

import (
	"context"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/pkg/hubrise"
)

// CatalogAPI is the part of the HubRise API the catalog import uses.
type CatalogAPI interface {
	GetCatalog(ctx context.Context, id string) (*hubrise.Catalog, error)
	GetImage(ctx context.Context, catalogID, imageID string) (*hubrise.Image, error)
	GetImageData(ctx context.Context, catalogID, imageID string) (*hubrise.ImageData, error)
}

// CatalogNotifier tells the dashboards that products changed (the productUpdated subscription).
type CatalogNotifier interface {
	ProductsChanged(ctx context.Context, ids []uuid.UUID)
}

// ImageUploader stores a product image. Failures are not fatal to the import.
type ImageUploader interface {
	UploadProductImage(ctx context.Context, productID uuid.UUID, filename string, data []byte) error
}

// ClientFactory builds the API client of a connection from its sealed token. The Limiter is shared
// by every client it builds, so all the calls of this process to one connection count together.
type ClientFactory struct {
	BaseURL string
	Codec   domain.TokenCodec
	Limiter *hubrise.Limiter
	HTTP    *http.Client
}

// Client opens the connection's token and returns a client for it.
func (f *ClientFactory) Client(conn *domain.Connection) (*hubrise.Client, error) {
	token, err := conn.AccessToken(f.Codec)
	if err != nil {
		return nil, err
	}
	opts := []hubrise.ClientOption{hubrise.WithLimiter(f.Limiter)}
	if f.HTTP != nil {
		opts = append(opts, hubrise.WithHTTPClient(f.HTTP))
	}
	c, err := hubrise.New(f.BaseURL, token.Reveal(), opts...)
	if err != nil {
		return nil, fmt.Errorf("hubrise: build client: %w", err)
	}
	return c, nil
}

// CatalogAPI is Client as the narrow interface the catalog import takes.
func (f *ClientFactory) CatalogAPI(conn *domain.Connection) (CatalogAPI, error) {
	return f.Client(conn)
}
