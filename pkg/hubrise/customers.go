package hubrise

import (
	"context"
	"net/http"
	"net/url"
)

// customersPath is the customers of a customer list.
func customersPath(listID string) string { return "/customer_lists/" + esc(listID) + "/customers" }

// CreateCustomer adds a customer to a customer list (POST /customer_lists/:id/customers). Set
// PrivateRef to the id the shop knows the customer by: HubRise does not deduplicate, so a caller
// that may be retrying must look the customer up first (FindCustomerByPrivateRef). The response-only
// fields of c are not sent.
func (c *Client) CreateCustomer(ctx context.Context, listID string, customer *Customer) (*Customer, error) {
	var out Customer
	if _, err := c.do(ctx, call{method: http.MethodPost, path: customersPath(listID), in: customer, out: &out}); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateCustomer changes a customer (PATCH /customer_lists/:id/customers/:id). Only the fields set
// are sent, so a field cannot be cleared this way: an empty value means "leave it as it is".
func (c *Client) UpdateCustomer(ctx context.Context, listID, id string, customer *Customer) (*Customer, error) {
	var out Customer
	if _, err := c.do(ctx, call{method: http.MethodPatch, path: customersPath(listID) + "/" + esc(id), in: customer, out: &out}); err != nil {
		return nil, err
	}
	return &out, nil
}

// AnonymiseCustomer erases the personal data of a customer
// (POST /customer_lists/:id/customers/:id/anonymise). The customer stays in the list, flagged
// anonymised, so the order history that names it remains valid. It is idempotent.
func (c *Client) AnonymiseCustomer(ctx context.Context, listID, id string) (*Customer, error) {
	var out Customer
	if _, err := c.do(ctx, call{method: http.MethodPost, path: customersPath(listID) + "/" + esc(id) + "/anonymise", out: &out}); err != nil {
		return nil, err
	}
	return &out, nil
}

// FindCustomerByPrivateRef looks a customer up by the private_ref this client set (GET
// /customer_lists/:id/customers?private_ref=), which is how a customer creation is made idempotent.
// It returns nil, nil when the list has no such customer; when several match (a double create) the
// earliest is returned. The list endpoint is capped at 10 requests
// a minute, and the lookup is charged to the share of the order lookups.
func (c *Client) FindCustomerByPrivateRef(ctx context.Context, listID, privateRef string) (*Customer, error) {
	q := url.Values{"private_ref": {privateRef}, "count": {"100"}}
	page, err := listPage[Customer](ctx, c, call{method: http.MethodGet, path: customersPath(listID), query: q, bucket: BucketOrderLookup}, "")
	if err != nil {
		return nil, err
	}
	var found *Customer
	for i := range page.Items {
		cu := &page.Items[i]
		if cu.PrivateRef != privateRef {
			continue
		}
		if found == nil || cu.CreatedAt.Before(found.CreatedAt) {
			found = cu
		}
	}
	return found, nil
}
