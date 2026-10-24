package hubrise

import (
	"context"
	"net/http"
)

// inventoryPath is the unscoped inventory of the connection's location and catalog.
const inventoryPath = "/location/inventory"

// GetInventory retrieves the stock entries (GET /location/inventory). Only SKUs and options with
// a stock set appear; everything else has unlimited stock.
func (c *Client) GetInventory(ctx context.Context) ([]InventoryEntry, error) {
	var out []InventoryEntry
	if _, err := c.do(ctx, call{method: http.MethodGet, path: inventoryPath, out: &out}); err != nil {
		return nil, err
	}
	return out, nil
}

// ReplaceInventory replaces the whole inventory (PUT /location/inventory): entries not sent are
// removed.
func (c *Client) ReplaceInventory(ctx context.Context, entries []InventoryEntry) ([]InventoryEntry, error) {
	var out []InventoryEntry
	if _, err := c.do(ctx, call{method: http.MethodPut, path: inventoryPath, in: nonNil(entries), out: &out}); err != nil {
		return nil, err
	}
	return out, nil
}

// PatchInventory merges entries into the inventory (PATCH /location/inventory); an entry with a
// nil Stock removes it. It returns the entries that were modified.
func (c *Client) PatchInventory(ctx context.Context, entries []InventoryEntry) ([]InventoryEntry, error) {
	var out []InventoryEntry
	if _, err := c.do(ctx, call{method: http.MethodPatch, path: inventoryPath, in: nonNil(entries), out: &out}); err != nil {
		return nil, err
	}
	return out, nil
}

// nonNil makes sure an empty list is sent as [] and not null.
func nonNil(entries []InventoryEntry) []InventoryEntry {
	if entries == nil {
		return []InventoryEntry{}
	}
	return entries
}
