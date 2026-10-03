package tools

import (
	"context"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"tsb-service/internal/mcp/actions"
	"tsb-service/internal/mcp/money"
	"tsb-service/internal/mcp/search"
	"tsb-service/internal/mcp/upstream"
)

// CouponOut is a coupon in tool output.
type CouponOut struct {
	CouponID       string `json:"coupon_id"`
	Code           string `json:"code"`
	DiscountType   string `json:"discount_type" jsonschema:"percentage or fixed"`
	PercentOff     int    `json:"percent_off,omitempty"`
	AmountOffCents int64  `json:"amount_off_cents,omitempty"`
	Currency       string `json:"currency"`
	MinOrderCents  int64  `json:"min_order_cents,omitempty"`
	MaxUses        int    `json:"max_uses,omitempty"`
	MaxUsesPerUser int    `json:"max_uses_per_user,omitempty"`
	UsedCount      int    `json:"used_count"`
	Active         bool   `json:"active"`
	Status         string `json:"status" jsonschema:"ACTIVE, INACTIVE, SCHEDULED, EXPIRED or EXHAUSTED"`
	ValidFrom      string `json:"valid_from,omitempty"`
	ValidUntil     string `json:"valid_until,omitempty"`
}

func (d *Deps) couponOut(c *upstream.Coupon) CouponOut {
	o := CouponOut{CouponID: c.ID, Code: c.Code, Currency: money.Currency, UsedCount: c.UsedCount, Active: c.IsActive, Status: c.Status,
		ValidFrom: d.fmtTimePtr(c.ValidFrom), ValidUntil: d.fmtTimePtr(c.ValidUntil)}
	if strings.EqualFold(c.DiscountType, "percentage") {
		o.DiscountType, o.PercentOff = "percentage", int(money.MustCents(c.DiscountValue)/100)
	} else {
		o.DiscountType, o.AmountOffCents = "fixed", money.MustCents(c.DiscountValue)
	}
	if c.MinOrderAmount != nil {
		o.MinOrderCents = money.MustCents(*c.MinOrderAmount)
	}
	if c.MaxUses != nil {
		o.MaxUses = *c.MaxUses
	}
	if c.MaxUsesPerUser != nil {
		o.MaxUsesPerUser = *c.MaxUsesPerUser
	}
	return o
}

func discountValue(typ string, percent *int, amount *int64) (*actions.CouponValue, error) {
	typ = strings.ToLower(strings.TrimSpace(typ))
	switch typ {
	case "":
		if percent != nil || amount != nil {
			return nil, actions.Userf("discount_type is required with percent_off or amount_off_cents.")
		}
		return nil, nil
	case "percentage":
		if percent == nil {
			return nil, actions.Userf("percent_off is required for a percentage discount.")
		}
		return &actions.CouponValue{Type: typ, PercentOff: *percent}, nil
	case "fixed":
		if amount == nil {
			return nil, actions.Userf("amount_off_cents is required for a fixed discount.")
		}
		return &actions.CouponValue{Type: typ, AmountOffCent: *amount}, nil
	}
	return nil, actions.Userf("discount_type must be percentage or fixed.")
}

func registerCoupons(s *mcp.Server, d *Deps) {
	type SearchIn struct {
		Query  string `json:"query,omitempty" jsonschema:"coupon code or part of it; omit to list all"`
		Status string `json:"status,omitempty" jsonschema:"filter: ACTIVE, INACTIVE, SCHEDULED, EXPIRED or EXHAUSTED"`
	}
	type SearchOut struct {
		Coupons []CouponOut `json:"coupons"`
	}
	add(s, d, &mcp.Tool{
		Name: "search_coupons", Annotations: readOnly,
		Description: describe(`List discount coupons, optionally filtered by code (partial, case-insensitive) and status. Use it to find a coupon_id.`,
			`search_coupons({"query": "welcome", "status": "ACTIVE"})`),
	}, func(ctx context.Context, in SearchIn) (SearchOut, error) {
		all, err := d.Up.Coupons(ctx)
		if err != nil {
			return SearchOut{}, err
		}
		status := strings.ToUpper(strings.TrimSpace(in.Status))
		byID := map[string]*upstream.Coupon{}
		var cands []search.Candidate
		var ordered []string
		for i := range all {
			c := &all[i]
			if status != "" && c.Status != status {
				continue
			}
			byID[c.ID] = c
			cands = append(cands, search.Candidate{ID: c.ID, Primary: []string{c.Code}, Preferred: c.IsActive, SortKey: c.Code})
			ordered = append(ordered, c.ID)
		}
		if strings.TrimSpace(in.Query) != "" {
			ordered = ordered[:0]
			for _, r := range search.Rank(in.Query, cands, 0) {
				ordered = append(ordered, r.ID)
			}
		}
		out := SearchOut{Coupons: []CouponOut{}}
		for _, id := range ordered {
			out.Coupons = append(out.Coupons, d.couponOut(byID[id]))
		}
		return out, nil
	})

	type GetIn struct {
		CouponID string `json:"coupon_id"`
	}
	add(s, d, &mcp.Tool{
		Name: "get_coupon", Annotations: readOnly,
		Description: describe(`Details of one coupon: discount, limits, usage, validity and status.`,
			`get_coupon({"coupon_id": "3b2f5c1d-0a9e-4c8b-9d7f-abcdef012345"})`),
	}, func(ctx context.Context, in GetIn) (CouponOut, error) {
		c, err := d.Up.Coupon(ctx, in.CouponID)
		if upstream.IsNotFound(err) {
			return CouponOut{}, actions.Userf("No coupon with id %q. Use search_coupons to find the id.", in.CouponID)
		}
		if err != nil {
			return CouponOut{}, err
		}
		return d.couponOut(c), nil
	})

	type RefIn struct {
		CouponID string `json:"coupon_id"`
		WriteContext
	}
	add(s, d, &mcp.Tool{
		Name: "deactivate_coupon", Annotations: lowRisk,
		Description: describe(`Deactivate a coupon so customers can no longer use it. Applied immediately and logged; reversible with undo_last_change. Coupons cannot be deleted.`,
			`deactivate_coupon({"coupon_id": "3b2f5c1d-0a9e-4c8b-9d7f-abcdef012345", "request_context": "停用WELCOME10"})`),
	}, func(ctx context.Context, in RefIn) (ApplyOut, error) {
		return d.applyNow(ctx, "deactivate_coupon", actions.KindCouponDeactivate, actions.CouponRef{CouponID: in.CouponID}, in.RequestContext)
	})
	add(s, d, &mcp.Tool{
		Name: "propose_coupon_activation", Annotations: proposeOnly,
		Description: describe(`Propose activating a coupon so customers can use it again. Creates a pending change only.`,
			`propose_coupon_activation({"coupon_id": "3b2f5c1d-0a9e-4c8b-9d7f-abcdef012345", "request_context": "重新开启SUMMER5"})`),
	}, func(ctx context.Context, in RefIn) (actions.Proposal, error) {
		return d.propose(ctx, "propose_coupon_activation", actions.KindCouponActivate, actions.CouponRef{CouponID: in.CouponID}, nil, in.RequestContext)
	})

	type CreateIn struct {
		Code           string `json:"code,omitempty" jsonschema:"3-32 letters/digits; omit to generate one"`
		DiscountType   string `json:"discount_type" jsonschema:"percentage or fixed"`
		PercentOff     *int   `json:"percent_off,omitempty" jsonschema:"1-100, for percentage"`
		AmountOffCents *int64 `json:"amount_off_cents,omitempty" jsonschema:"for fixed, in cents"`
		MinOrderCents  *int64 `json:"min_order_cents,omitempty"`
		MaxUses        *int   `json:"max_uses,omitempty" jsonschema:"total uses allowed"`
		MaxUsesPerUser *int   `json:"max_uses_per_user,omitempty"`
		Active         *bool  `json:"active,omitempty" jsonschema:"default true"`
		ValidFrom      string `json:"valid_from,omitempty" jsonschema:"ISO 8601"`
		ValidUntil     string `json:"valid_until,omitempty" jsonschema:"ISO 8601"`
		WriteContext
	}
	add(s, d, &mcp.Tool{
		Name: "propose_coupon_creation", Annotations: proposeOnly,
		Description: describe(`Propose a new discount coupon (percentage or fixed amount) with optional minimum order, usage limits and validity period. Creates a pending change only.`,
			`propose_coupon_creation({"code": "NOEL15", "discount_type": "percentage", "percent_off": 15, "min_order_cents": 3000, "valid_until": "2026-12-31T23:59", "request_context": "圣诞节优惠码15%"})`),
	}, func(ctx context.Context, in CreateIn) (actions.Proposal, error) {
		v, err := discountValue(in.DiscountType, in.PercentOff, in.AmountOffCents)
		if err != nil {
			return actions.Proposal{}, err
		}
		if v == nil {
			return actions.Proposal{}, actions.Userf("discount_type is required.")
		}
		p := actions.CouponCreateParams{Code: in.Code, Discount: *v, MinOrderCents: in.MinOrderCents, MaxUses: in.MaxUses, MaxUsesPerUser: in.MaxUsesPerUser, Active: valueOr(in.Active, true)}
		if p.ValidFrom, err = d.optTime("valid_from", in.ValidFrom); err != nil {
			return actions.Proposal{}, err
		}
		if p.ValidUntil, err = d.optTime("valid_until", in.ValidUntil); err != nil {
			return actions.Proposal{}, err
		}
		return d.propose(ctx, "propose_coupon_creation", actions.KindCouponCreate, p, nil, in.RequestContext)
	})

	type UpdateIn struct {
		CouponID       string `json:"coupon_id"`
		Code           string `json:"code,omitempty"`
		DiscountType   string `json:"discount_type,omitempty" jsonschema:"percentage or fixed, with percent_off or amount_off_cents"`
		PercentOff     *int   `json:"percent_off,omitempty"`
		AmountOffCents *int64 `json:"amount_off_cents,omitempty"`
		MinOrderCents  *int64 `json:"min_order_cents,omitempty"`
		MaxUses        *int   `json:"max_uses,omitempty"`
		MaxUsesPerUser *int   `json:"max_uses_per_user,omitempty"`
		ValidFrom      string `json:"valid_from,omitempty"`
		ValidUntil     string `json:"valid_until,omitempty"`
		WriteContext
	}
	add(s, d, &mcp.Tool{
		Name: "propose_coupon_update", Annotations: proposeOnly,
		Description: describe(`Propose changing a coupon's code, discount, minimum order, usage limits or validity. Only the given fields change; a limit cannot be removed once set. To switch a coupon on or off use propose_coupon_activation / deactivate_coupon. Creates a pending change only.`,
			`propose_coupon_update({"coupon_id": "3b2f5c1d-0a9e-4c8b-9d7f-abcdef012345", "valid_until": "2027-01-15T23:59", "request_context": "优惠码延长到一月十五号"})`),
	}, func(ctx context.Context, in UpdateIn) (actions.Proposal, error) {
		v, err := discountValue(in.DiscountType, in.PercentOff, in.AmountOffCents)
		if err != nil {
			return actions.Proposal{}, err
		}
		p := actions.CouponUpdateParams{CouponID: in.CouponID, Discount: v, MinOrderCents: in.MinOrderCents, MaxUses: in.MaxUses, MaxUsesPerUser: in.MaxUsesPerUser}
		if in.Code != "" {
			p.Code = &in.Code
		}
		if p.ValidFrom, err = d.optTime("valid_from", in.ValidFrom); err != nil {
			return actions.Proposal{}, err
		}
		if p.ValidUntil, err = d.optTime("valid_until", in.ValidUntil); err != nil {
			return actions.Proposal{}, err
		}
		return d.propose(ctx, "propose_coupon_update", actions.KindCouponUpdate, p, nil, in.RequestContext)
	})
}

func (d *Deps) optTime(field, s string) (*time.Time, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	t, err := d.parseTime(field, s)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
