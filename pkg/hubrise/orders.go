package hubrise

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// GetLocation retrieves the location of the connection (GET /location).
func (c *Client) GetLocation(ctx context.Context) (*Location, error) {
	var loc Location
	if _, err := c.do(ctx, call{method: http.MethodGet, path: "/location", out: &loc}); err != nil {
		return nil, err
	}
	return &loc, nil
}

// CreateOrder pushes an order (POST /location/orders) and returns it as HubRise stored it, with its
// id and the total HubRise calculated. HubRise does not deduplicate: a caller that may be retrying
// must look the order up by private_ref first (FindOrderByPrivateRef).
func (c *Client) CreateOrder(ctx context.Context, order *Order) (*Order, error) {
	var out Order
	if _, err := c.do(ctx, call{method: http.MethodPost, path: "/location/orders", in: order, out: &out}); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetOrder retrieves one order (GET /location/orders/:id). Unlike the list endpoint it is not in
// the 10 a minute bucket.
func (c *Client) GetOrder(ctx context.Context, id string) (*Order, error) {
	var out Order
	if _, err := c.do(ctx, call{method: http.MethodGet, path: "/location/orders/" + esc(id), out: &out}); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateOrder patches an order (PATCH /location/orders/:id).
func (c *Client) UpdateOrder(ctx context.Context, id string, patch *OrderPatch) (*Order, error) {
	var out Order
	if _, err := c.do(ctx, call{method: http.MethodPatch, path: "/location/orders/" + esc(id), in: patch, out: &out}); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListOrders fetches one page of orders (GET /location/orders), newest first. Pass the previous
// page's NextCursor to continue. It is in the 10 a minute bucket, shared with private_ref lookups.
func (c *Client) ListOrders(ctx context.Context, f OrderFilter, cursor string) (Page[Order], error) {
	q := url.Values{}
	if f.PrivateRef != "" {
		q.Set("private_ref", f.PrivateRef)
	}
	if f.Status != "" {
		q.Set("status", string(f.Status))
	}
	if f.CreatedBy != "" {
		q.Set("created_by", f.CreatedBy)
	}
	if f.CustomerID != "" {
		q.Set("customer_id", f.CustomerID)
	}
	if !f.After.IsZero() {
		q.Set("after", f.After.Format(time.RFC3339))
	}
	if !f.Before.IsZero() {
		q.Set("before", f.Before.Format(time.RFC3339))
	}
	if f.Count > 0 {
		q.Set("count", strconv.Itoa(f.Count))
	}
	return listPage[Order](ctx, c, call{method: http.MethodGet, path: "/location/orders", query: q, bucket: BucketOrderLookup}, cursor)
}

// FindOrderByPrivateRef looks an order up by the private_ref this client set (GET
// /location/orders?private_ref=), which is how a push is made idempotent. It returns nil, nil when
// HubRise has no such order. private_ref is only visible to the client that set it, so a match is
// always an order this connection created. When several match (a double create), the earliest is
// returned.
func (c *Client) FindOrderByPrivateRef(ctx context.Context, privateRef string) (*Order, error) {
	page, err := c.ListOrders(ctx, OrderFilter{PrivateRef: privateRef, Count: 100}, "")
	if err != nil {
		return nil, err
	}
	var found *Order
	for i := range page.Items {
		o := &page.Items[i]
		if o.PrivateRef != privateRef {
			continue
		}
		if found == nil || o.CreatedAt.Before(found.CreatedAt) {
			found = o
		}
	}
	return found, nil
}
