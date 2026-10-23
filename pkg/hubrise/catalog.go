package hubrise

import (
	"context"
	"net/http"
)

// GetCatalog retrieves a catalog with its content (GET /catalogs/:id). It is in the 10 a minute
// bucket: callers are the importer and the drift check, never a request path.
func (c *Client) GetCatalog(ctx context.Context, id string) (*Catalog, error) {
	var out Catalog
	if _, err := c.do(ctx, call{method: http.MethodGet, path: "/catalogs/" + esc(id), bucket: BucketCatalog, out: &out}); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetImage retrieves the metadata of a catalog image (GET /catalogs/:id/images/:id).
func (c *Client) GetImage(ctx context.Context, catalogID, imageID string) (*Image, error) {
	var out Image
	if _, err := c.do(ctx, call{method: http.MethodGet, path: "/catalogs/" + esc(catalogID) + "/images/" + esc(imageID), out: &out}); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetImageData downloads a catalog image (GET /catalogs/:id/images/:id/data).
func (c *Client) GetImageData(ctx context.Context, catalogID, imageID string) (*ImageData, error) {
	return c.doBinary(ctx, call{method: http.MethodGet, path: "/catalogs/" + esc(catalogID) + "/images/" + esc(imageID) + "/data"})
}
