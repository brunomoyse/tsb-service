package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerResources exposes static read-only views the LLM can pull in
// at the start of a session to prime context without a tool call.
// Mirrors the dashboard's "Read the current state" shortcut.
func registerResources(s *mcpsdk.Server, deps Deps) {
	s.AddResource(
		&mcpsdk.Resource{
			URI:         "tsb://config",
			Name:        "Restaurant configuration",
			Description: "Current restaurant config: ordering toggle, weekly hours, preparation time.",
			MIMEType:    "application/json",
		},
		func(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
			cfg, err := deps.Restaurant.GetConfig(ctx)
			if err != nil {
				return nil, fmt.Errorf("get config: %w", err)
			}
			body, err := json.Marshal(toRestaurantConfigOut(cfg))
			if err != nil {
				return nil, fmt.Errorf("marshal config: %w", err)
			}
			return &mcpsdk.ReadResourceResult{
				Contents: []*mcpsdk.ResourceContents{{
					URI:      req.Params.URI,
					MIMEType: "application/json",
					Text:     string(body),
				}},
			}, nil
		},
	)

	s.AddResource(
		&mcpsdk.Resource{
			URI:         "tsb://products",
			Name:        "All products",
			Description: "Snapshot of every product with translations. Use for browsing the catalog; for filters or search call list_products instead.",
			MIMEType:    "application/json",
		},
		func(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
			products, err := deps.Product.GetProducts(ctx)
			if err != nil {
				return nil, fmt.Errorf("fetch products: %w", err)
			}
			out := make([]productOut, len(products))
			for i, p := range products {
				out[i] = toProductOut(p)
			}
			body, err := json.Marshal(out)
			if err != nil {
				return nil, fmt.Errorf("marshal products: %w", err)
			}
			return &mcpsdk.ReadResourceResult{
				Contents: []*mcpsdk.ResourceContents{{
					URI:      req.Params.URI,
					MIMEType: "application/json",
					Text:     string(body),
				}},
			}, nil
		},
	)

	s.AddResource(
		&mcpsdk.Resource{
			URI:         "tsb://coupons/active",
			Name:        "Active coupons",
			Description: "All coupons whose computed status is 'active'.",
			MIMEType:    "application/json",
		},
		func(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
			coupons, err := deps.Coupon.GetAllCoupons(ctx)
			if err != nil {
				return nil, fmt.Errorf("fetch coupons: %w", err)
			}
			out := make([]couponOut, 0, len(coupons))
			for _, c := range coupons {
				co := toCouponOut(c)
				if co.Status == "active" {
					out = append(out, co)
				}
			}
			body, err := json.Marshal(out)
			if err != nil {
				return nil, fmt.Errorf("marshal coupons: %w", err)
			}
			return &mcpsdk.ReadResourceResult{
				Contents: []*mcpsdk.ResourceContents{{
					URI:      req.Params.URI,
					MIMEType: "application/json",
					Text:     string(body),
				}},
			}, nil
		},
	)
}
