package hubrise

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"
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

// RawCatalog is a catalog whose content is kept as raw JSON, so that a read, merge and write cycle
// cannot lose a key the typed structs do not model.
type RawCatalog struct {
	ID         string          `json:"id"`
	LocationID string          `json:"location_id,omitempty"`
	AccountID  string          `json:"account_id,omitempty"`
	Name       string          `json:"name"`
	CreatedAt  time.Time       `json:"created_at,omitzero"`
	Data       json.RawMessage `json:"data,omitempty"`
}

// GetCatalogRaw retrieves a catalog with its content as raw JSON (GET /catalogs/:id). It is in the
// 10 a minute bucket like GetCatalog.
func (c *Client) GetCatalogRaw(ctx context.Context, id string) (*RawCatalog, error) {
	var out RawCatalog
	if _, err := c.do(ctx, call{method: http.MethodGet, path: "/catalogs/" + esc(id), bucket: BucketCatalog, out: &out}); err != nil {
		return nil, err
	}
	return &out, nil
}

// replaceCatalogBody is the body of PUT /catalogs/:id. The name is deliberately absent: HubRise
// keeps the name when it is not sent, so a publish can never rename the catalog.
type replaceCatalogBody struct {
	Data json.RawMessage `json:"data"`
}

// ReplaceCatalog clears the content of a catalog and recreates it from data (PUT /catalogs/:id).
// Ids of nested elements may change, refs are the stable keys. It is not in the 10 a minute
// bucket. It returns the catalog as HubRise now holds it.
func (c *Client) ReplaceCatalog(ctx context.Context, id string, data json.RawMessage) (*RawCatalog, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, &Error{Kind: KindProtocol, Op: "PUT /catalogs/:id", Message: "empty catalog data"}
	}
	var out RawCatalog
	if _, err := c.do(ctx, call{method: http.MethodPut, path: "/catalogs/" + esc(id), in: replaceCatalogBody{Data: data}, out: &out}); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListImages lists the images of a catalog (GET /catalogs/:id/images), all pages. A non empty
// privateRef keeps only the images with that private_ref.
func (c *Client) ListImages(ctx context.Context, catalogID, privateRef string) ([]Image, error) {
	q := url.Values{"count": {"100"}}
	if privateRef != "" {
		q.Set("private_ref", privateRef)
	}
	var all []Image
	cursor := ""
	for range maxImagePages {
		page, err := listPage[Image](ctx, c, call{method: http.MethodGet, path: "/catalogs/" + esc(catalogID) + "/images", query: q}, cursor)
		if err != nil {
			return nil, err
		}
		all = append(all, page.Items...)
		if page.NextCursor == "" {
			return all, nil
		}
		cursor = page.NextCursor
	}
	return all, nil
}

// maxImagePages bounds ListImages (100 images a page).
const maxImagePages = 50

// UploadImage uploads an image (POST /catalogs/:id/images?private_ref=...): the raw bytes with the
// image type as Content-Type, at most 1 MB, JPEG, PNG, WEBP, GIF or BMP. privateRef is unique per
// catalog, which is what lets a caller reuse an image instead of uploading it again.
func (c *Client) UploadImage(ctx context.Context, catalogID, privateRef, contentType string, data []byte) (*Image, error) {
	if len(data) == 0 {
		return nil, &Error{Kind: KindProtocol, Op: "POST /catalogs/:id/images", Message: "empty image"}
	}
	q := url.Values{}
	if privateRef != "" {
		q.Set("private_ref", privateRef)
	}
	var out Image
	if _, err := c.do(ctx, call{method: http.MethodPost, path: "/catalogs/" + esc(catalogID) + "/images", query: q, raw: data, rawType: contentType, out: &out}); err != nil {
		return nil, err
	}
	return &out, nil
}
