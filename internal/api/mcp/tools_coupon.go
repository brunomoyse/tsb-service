package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	couponDomain "tsb-service/internal/modules/coupon/domain"
)

type listCouponsIn struct {
	Status       *string `json:"status,omitempty" jsonschema:"filter by computed status: active, inactive, scheduled, expired, exhausted"`
	DiscountType *string `json:"discountType,omitempty" jsonschema:"filter by discount type: percentage or fixed"`
}

type listCouponsOut struct {
	Coupons []couponOut `json:"coupons"`
	Total   int         `json:"total"`
}

type getCouponIn struct {
	ID string `json:"id"`
}

type createCouponIn struct {
	Code           string     `json:"code" jsonschema:"unique coupon code shown to customers"`
	DiscountType   string     `json:"discountType" jsonschema:"either 'percentage' or 'fixed'"`
	DiscountValue  string     `json:"discountValue" jsonschema:"decimal string; for percentage it's 1-100, for fixed it's an EUR amount"`
	MinOrderAmount *string    `json:"minOrderAmount,omitempty" jsonschema:"minimum order subtotal required to use this coupon"`
	MaxUses        *int       `json:"maxUses,omitempty" jsonschema:"global cap; omit for unlimited"`
	MaxUsesPerUser *int       `json:"maxUsesPerUser,omitempty"`
	IsActive       bool       `json:"isActive"`
	ValidFrom      *time.Time `json:"validFrom,omitempty"`
	ValidUntil     *time.Time `json:"validUntil,omitempty"`
}

type updateCouponIn struct {
	ID             string     `json:"id"`
	Code           *string    `json:"code,omitempty"`
	DiscountType   *string    `json:"discountType,omitempty"`
	DiscountValue  *string    `json:"discountValue,omitempty"`
	MinOrderAmount *string    `json:"minOrderAmount,omitempty"`
	MaxUses        *int       `json:"maxUses,omitempty"`
	MaxUsesPerUser *int       `json:"maxUsesPerUser,omitempty"`
	IsActive       *bool      `json:"isActive,omitempty"`
	ValidFrom      *time.Time `json:"validFrom,omitempty"`
	ValidUntil     *time.Time `json:"validUntil,omitempty"`
}

func registerCouponTools(s *mcpsdk.Server, deps Deps) {
	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "list_coupons",
			Description: "List all coupons with computed status. Optionally filter by status or discount type.",
		},
		func(ctx context.Context, _ *mcpsdk.CallToolRequest, args listCouponsIn) (*mcpsdk.CallToolResult, listCouponsOut, error) {
			coupons, err := deps.Coupon.GetAllCoupons(ctx)
			if err != nil {
				return errorResult(fmt.Sprintf("fetch coupons: %v", err)), listCouponsOut{}, nil
			}
			out := listCouponsOut{Coupons: make([]couponOut, 0, len(coupons))}
			for _, c := range coupons {
				co := toCouponOut(c)
				if args.Status != nil && co.Status != strings.ToLower(*args.Status) {
					continue
				}
				if args.DiscountType != nil && co.DiscountType != strings.ToLower(*args.DiscountType) {
					continue
				}
				out.Coupons = append(out.Coupons, co)
			}
			out.Total = len(out.Coupons)
			return nil, out, nil
		},
	)

	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "get_coupon",
			Description: "Fetch a single coupon by UUID with its full state.",
		},
		func(ctx context.Context, _ *mcpsdk.CallToolRequest, args getCouponIn) (*mcpsdk.CallToolResult, couponOut, error) {
			id, err := uuid.Parse(args.ID)
			if err != nil {
				return errorResult(fmt.Sprintf("invalid id: %v", err)), couponOut{}, nil
			}
			c, err := deps.Coupon.GetCoupon(ctx, id)
			if err != nil {
				return errorResult(fmt.Sprintf("coupon not found: %v", err)), couponOut{}, nil
			}
			return nil, toCouponOut(c), nil
		},
	)

	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "create_coupon",
			Description: "Create a new coupon. Percentage discounts must be 1-100; fixed discounts must be positive EUR amounts.",
		},
		func(ctx context.Context, _ *mcpsdk.CallToolRequest, args createCouponIn) (*mcpsdk.CallToolResult, couponOut, error) {
			discountType := couponDomain.DiscountType(strings.ToLower(args.DiscountType))
			if discountType != couponDomain.DiscountTypePercentage && discountType != couponDomain.DiscountTypeFixed {
				return errorResult("discountType must be 'percentage' or 'fixed'"), couponOut{}, nil
			}
			value, err := decimal.NewFromString(strings.TrimSpace(args.DiscountValue))
			if err != nil {
				return errorResult(fmt.Sprintf("invalid discountValue: %v", err)), couponOut{}, nil
			}
			if value.LessThanOrEqual(decimal.Zero) {
				return errorResult("discountValue must be positive"), couponOut{}, nil
			}
			if discountType == couponDomain.DiscountTypePercentage && value.GreaterThan(decimal.NewFromInt(100)) {
				return errorResult("percentage discount cannot exceed 100"), couponOut{}, nil
			}

			c := &couponDomain.Coupon{
				ID:             uuid.New(),
				Code:           strings.ToUpper(strings.TrimSpace(args.Code)),
				DiscountType:   discountType,
				DiscountValue:  value,
				IsActive:       args.IsActive,
				ValidFrom:      args.ValidFrom,
				ValidUntil:     args.ValidUntil,
				MaxUses:        args.MaxUses,
				MaxUsesPerUser: args.MaxUsesPerUser,
			}
			if args.MinOrderAmount != nil {
				m, err := decimal.NewFromString(strings.TrimSpace(*args.MinOrderAmount))
				if err != nil {
					return errorResult(fmt.Sprintf("invalid minOrderAmount: %v", err)), couponOut{}, nil
				}
				c.MinOrderAmount = &m
			}
			if err := deps.Coupon.CreateCoupon(ctx, c); err != nil {
				return errorResult(fmt.Sprintf("create coupon: %v", err)), couponOut{}, nil
			}
			return nil, toCouponOut(c), nil
		},
	)

	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "update_coupon",
			Description: "Partial-update a coupon. Use isActive=false to disable without deleting.",
		},
		func(ctx context.Context, _ *mcpsdk.CallToolRequest, args updateCouponIn) (*mcpsdk.CallToolResult, couponOut, error) {
			id, err := uuid.Parse(args.ID)
			if err != nil {
				return errorResult(fmt.Sprintf("invalid id: %v", err)), couponOut{}, nil
			}
			c, err := deps.Coupon.GetCoupon(ctx, id)
			if err != nil {
				return errorResult(fmt.Sprintf("coupon not found: %v", err)), couponOut{}, nil
			}
			if args.Code != nil {
				c.Code = strings.ToUpper(strings.TrimSpace(*args.Code))
			}
			if args.DiscountType != nil {
				dt := couponDomain.DiscountType(strings.ToLower(*args.DiscountType))
				if dt != couponDomain.DiscountTypePercentage && dt != couponDomain.DiscountTypeFixed {
					return errorResult("discountType must be 'percentage' or 'fixed'"), couponOut{}, nil
				}
				c.DiscountType = dt
			}
			if args.DiscountValue != nil {
				v, err := decimal.NewFromString(strings.TrimSpace(*args.DiscountValue))
				if err != nil {
					return errorResult(fmt.Sprintf("invalid discountValue: %v", err)), couponOut{}, nil
				}
				if v.LessThanOrEqual(decimal.Zero) {
					return errorResult("discountValue must be positive"), couponOut{}, nil
				}
				if c.DiscountType == couponDomain.DiscountTypePercentage && v.GreaterThan(decimal.NewFromInt(100)) {
					return errorResult("percentage discount cannot exceed 100"), couponOut{}, nil
				}
				c.DiscountValue = v
			}
			if args.MinOrderAmount != nil {
				m, err := decimal.NewFromString(strings.TrimSpace(*args.MinOrderAmount))
				if err != nil {
					return errorResult(fmt.Sprintf("invalid minOrderAmount: %v", err)), couponOut{}, nil
				}
				c.MinOrderAmount = &m
			}
			if args.MaxUses != nil {
				c.MaxUses = args.MaxUses
			}
			if args.MaxUsesPerUser != nil {
				c.MaxUsesPerUser = args.MaxUsesPerUser
			}
			if args.IsActive != nil {
				c.IsActive = *args.IsActive
			}
			if args.ValidFrom != nil {
				c.ValidFrom = args.ValidFrom
			}
			if args.ValidUntil != nil {
				c.ValidUntil = args.ValidUntil
			}
			if err := deps.Coupon.UpdateCoupon(ctx, c); err != nil {
				return errorResult(fmt.Sprintf("update coupon: %v", err)), couponOut{}, nil
			}
			return nil, toCouponOut(c), nil
		},
	)

	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "disable_coupon",
			Description: "Soft-disable a coupon by flipping isActive to false. Preserves usage history. Use this instead of deleting.",
		},
		func(ctx context.Context, _ *mcpsdk.CallToolRequest, args getCouponIn) (*mcpsdk.CallToolResult, couponOut, error) {
			id, err := uuid.Parse(args.ID)
			if err != nil {
				return errorResult(fmt.Sprintf("invalid id: %v", err)), couponOut{}, nil
			}
			c, err := deps.Coupon.GetCoupon(ctx, id)
			if err != nil {
				return errorResult(fmt.Sprintf("coupon not found: %v", err)), couponOut{}, nil
			}
			c.IsActive = false
			if err := deps.Coupon.UpdateCoupon(ctx, c); err != nil {
				return errorResult(fmt.Sprintf("disable coupon: %v", err)), couponOut{}, nil
			}
			return nil, toCouponOut(c), nil
		},
	)
}
