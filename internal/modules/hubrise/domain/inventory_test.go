package domain_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise/domain"
)

func TestInventoryDedupeKeyIsPerSku(t *testing.T) {
	assert.Equal(t, "inv:sku:MAKI-1", domain.InventoryDedupeKey("MAKI-1"))
	assert.NotEqual(t, domain.InventoryDedupeKey("A"), domain.InventoryDedupeKey("B"))
}

func TestInventoryPayloadCarriesTheVersionAndNoStock(t *testing.T) {
	id := uuid.New()
	raw := domain.InventoryPayload{ProductID: id, SkuRef: "MAKI-1"}.Marshal()

	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, map[string]any{"v": float64(domain.InventoryPayloadVersion), "product_id": id.String(), "sku_ref": "MAKI-1"}, got,
		"the stock is decided at send time, never stored")
	var back domain.InventoryPayload
	require.NoError(t, json.Unmarshal(raw, &back))
	assert.Equal(t, domain.InventoryPayload{Version: domain.InventoryPayloadVersion, ProductID: id, SkuRef: "MAKI-1"}, back)
}
