package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"tsb-service/internal/mcp/money"
	"tsb-service/internal/mcp/upstream"
)

const (
	KindCouponCreate     = "coupon.create"
	KindCouponUpdate     = "coupon.update"
	KindCouponActivate   = "coupon.activate"
	KindCouponDeactivate = "coupon.deactivate"
)

var couponCodeRe = regexp.MustCompile(`^[A-Z0-9_-]{3,32}$`)

func getCoupon(ctx context.Context, env *Env, id string) (*upstream.Coupon, error) {
	if strings.TrimSpace(id) == "" {
		return nil, Userf("coupon_id is required.")
	}
	c, err := env.Up.Coupon(ctx, id)
	if upstream.IsNotFound(err) {
		return nil, Userf("No coupon with id %q. Use search_coupons to find the id.", id)
	}
	return c, err
}

// CouponValue is a discount: either a percentage or a fixed amount in cents.
type CouponValue struct {
	Type          string `json:"type"` // "percentage" or "fixed"
	PercentOff    int    `json:"percent_off,omitempty"`
	AmountOffCent int64  `json:"amount_off_cents,omitempty"`
}

func (v CouponValue) validate() error {
	switch v.Type {
	case "percentage":
		if v.PercentOff < 1 || v.PercentOff > 100 {
			return Userf("A percentage discount must be between 1 and 100.")
		}
	case "fixed":
		if v.AmountOffCent <= 0 {
			return Userf("A fixed discount must be greater than 0.")
		}
	default:
		return Userf("discount type must be \"percentage\" or \"fixed\".")
	}
	return nil
}

func (v CouponValue) upstream() (string, string) {
	if v.Type == "percentage" {
		return "percentage", fmt.Sprint(v.PercentOff)
	}
	return "fixed", money.FromCents(v.AmountOffCent)
}

func (v CouponValue) String() string {
	if v.Type == "percentage" {
		return fmt.Sprintf("%d%% off", v.PercentOff)
	}
	return money.Format(v.AmountOffCent) + " off"
}

func couponValueOf(c *upstream.Coupon) CouponValue {
	if strings.EqualFold(c.DiscountType, "percentage") {
		cents := money.MustCents(c.DiscountValue)
		return CouponValue{Type: "percentage", PercentOff: int(cents / 100)}
	}
	return CouponValue{Type: "fixed", AmountOffCent: money.MustCents(c.DiscountValue)}
}

// --- create (sensitive) -------------------------------------------------------

type CouponCreateParams struct {
	Code           string      `json:"code,omitempty"`
	Discount       CouponValue `json:"discount"`
	MinOrderCents  *int64      `json:"min_order_cents,omitempty"`
	MaxUses        *int        `json:"max_uses,omitempty"`
	MaxUsesPerUser *int        `json:"max_uses_per_user,omitempty"`
	Active         bool        `json:"active"`
	ValidFrom      *time.Time  `json:"valid_from,omitempty"`
	ValidUntil     *time.Time  `json:"valid_until,omitempty"`
}

type couponCreated struct {
	CouponID string `json:"coupon_id"`
	Code     string `json:"code"`
}

func validateLimits(minOrder *int64, maxUses, perUser *int, from, until *time.Time) error {
	if minOrder != nil && *minOrder < 0 {
		return Userf("The minimum order cannot be negative.")
	}
	if maxUses != nil && *maxUses < 1 {
		return Userf("max_uses must be at least 1.")
	}
	if perUser != nil && *perUser < 1 {
		return Userf("max_uses_per_user must be at least 1.")
	}
	if from != nil && until != nil && !from.Before(*until) {
		return Userf("valid_from must be before valid_until.")
	}
	return nil
}

func couponDetails(min *int64, maxUses, perUser *int, from, until *time.Time, loc *time.Location) string {
	var parts []string
	if min != nil {
		parts = append(parts, "minimum order "+money.Format(*min))
	}
	if maxUses != nil {
		parts = append(parts, fmt.Sprintf("max %d uses", *maxUses))
	}
	if perUser != nil {
		parts = append(parts, fmt.Sprintf("max %d per customer", *perUser))
	}
	if from != nil {
		parts = append(parts, "from "+from.In(loc).Format("2006-01-02 15:04"))
	}
	if until != nil {
		parts = append(parts, "until "+until.In(loc).Format("2006-01-02 15:04"))
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

func couponCreate() handler {
	return spec[CouponCreateParams, map[string]any]{
		Kind: KindCouponCreate,
		Risk: RiskSensitive,
		Prepare: func(ctx context.Context, env *Env, p *CouponCreateParams) (*prepared, error) {
			p.Code = strings.ToUpper(strings.TrimSpace(p.Code))
			if p.Code != "" && !couponCodeRe.MatchString(p.Code) {
				return nil, Userf("A coupon code must be 3 to 32 letters, digits, '-' or '_'.")
			}
			if err := p.Discount.validate(); err != nil {
				return nil, err
			}
			if err := validateLimits(p.MinOrderCents, p.MaxUses, p.MaxUsesPerUser, p.ValidFrom, p.ValidUntil); err != nil {
				return nil, err
			}
			if p.ValidUntil != nil && p.ValidUntil.Before(env.Now()) {
				return nil, Userf("valid_until is in the past.")
			}
			if p.Code != "" {
				all, err := env.Up.Coupons(ctx)
				if err != nil {
					return nil, err
				}
				for _, c := range all {
					if strings.EqualFold(c.Code, p.Code) {
						return nil, Userf("A coupon with code %s already exists (id %s).", c.Code, c.ID)
					}
				}
			}
			code := p.Code
			if code == "" {
				code = "(generated code)"
			}
			state := "inactive"
			if p.Active {
				state = "active"
			}
			return &prepared{EntityType: "coupon", EntityID: "new", Before: map[string]any{},
				Summary: fmt.Sprintf("New coupon %s: %s%s, %s", code, p.Discount, couponDetails(p.MinOrderCents, p.MaxUses, p.MaxUsesPerUser, p.ValidFrom, p.ValidUntil, env.Loc), state)}, nil
		},
		Execute: func(ctx context.Context, env *Env, p CouponCreateParams, _ []byte) (any, error) {
			typ, val := p.Discount.upstream()
			in := upstream.CouponInput{DiscountType: &typ, DiscountValue: &val, MaxUses: p.MaxUses, MaxUsesPerUser: p.MaxUsesPerUser,
				IsActive: &p.Active, ValidFrom: p.ValidFrom, ValidUntil: p.ValidUntil}
			if p.Code != "" {
				in.Code = &p.Code
			}
			if p.MinOrderCents != nil {
				m := money.FromCents(*p.MinOrderCents)
				in.MinOrderAmount = &m
			}
			c, err := env.Up.CreateCoupon(ctx, in)
			if err != nil {
				return nil, err
			}
			return couponCreated{CouponID: c.ID, Code: c.Code}, nil
		},
		Inverse: func(_ CouponCreateParams, _ map[string]any, after json.RawMessage) (string, any, error) {
			var a couponCreated
			if err := json.Unmarshal(after, &a); err != nil || a.CouponID == "" {
				return "", nil, ErrNotUndoable
			}
			// Coupons cannot be deleted upstream: undo deactivates it.
			return KindCouponDeactivate, CouponRef{CouponID: a.CouponID}, nil
		},
	}
}

// --- update (sensitive) -------------------------------------------------------

// CouponUpdateParams changes coupon terms. Absent fields are kept. Upstream
// cannot clear an optional limit once set.
type CouponUpdateParams struct {
	CouponID       string       `json:"coupon_id"`
	Code           *string      `json:"code,omitempty"`
	Discount       *CouponValue `json:"discount,omitempty"`
	MinOrderCents  *int64       `json:"min_order_cents,omitempty"`
	MaxUses        *int         `json:"max_uses,omitempty"`
	MaxUsesPerUser *int         `json:"max_uses_per_user,omitempty"`
	ValidFrom      *time.Time   `json:"valid_from,omitempty"`
	ValidUntil     *time.Time   `json:"valid_until,omitempty"`
}

type couponTerms struct {
	Code           string      `json:"code"`
	Discount       CouponValue `json:"discount"`
	MinOrderCents  *int64      `json:"min_order_cents"`
	MaxUses        *int        `json:"max_uses"`
	MaxUsesPerUser *int        `json:"max_uses_per_user"`
	ValidFrom      *time.Time  `json:"valid_from"`
	ValidUntil     *time.Time  `json:"valid_until"`
}

func termsOf(c *upstream.Coupon) couponTerms {
	t := couponTerms{Code: c.Code, Discount: couponValueOf(c), MaxUses: c.MaxUses, MaxUsesPerUser: c.MaxUsesPerUser, ValidFrom: c.ValidFrom, ValidUntil: c.ValidUntil}
	if c.MinOrderAmount != nil {
		m := money.MustCents(*c.MinOrderAmount)
		t.MinOrderCents = &m
	}
	return t
}

func fmtIntPtr(p *int) string {
	if p == nil {
		return "none"
	}
	return fmt.Sprint(*p)
}

func fmtTimePtr(t *time.Time, loc *time.Location) string {
	if t == nil {
		return "none"
	}
	return t.In(loc).Format("2006-01-02 15:04")
}

func couponUpdate() handler {
	return spec[CouponUpdateParams, couponTerms]{
		Kind: KindCouponUpdate,
		Risk: RiskSensitive,
		Prepare: func(ctx context.Context, env *Env, p *CouponUpdateParams) (*prepared, error) {
			c, err := getCoupon(ctx, env, p.CouponID)
			if err != nil {
				return nil, err
			}
			cur := termsOf(c)
			var diffs []string
			if p.Code != nil {
				code := strings.ToUpper(strings.TrimSpace(*p.Code))
				if !couponCodeRe.MatchString(code) {
					return nil, Userf("A coupon code must be 3 to 32 letters, digits, '-' or '_'.")
				}
				if code == cur.Code {
					p.Code = nil
				} else {
					p.Code = &code
					diffs = append(diffs, fmt.Sprintf("code: %s -> %s", cur.Code, code))
				}
			}
			if p.Discount != nil {
				if err := p.Discount.validate(); err != nil {
					return nil, err
				}
				if *p.Discount == cur.Discount {
					p.Discount = nil
				} else {
					diffs = append(diffs, fmt.Sprintf("discount: %s -> %s", cur.Discount, *p.Discount))
				}
			}
			if err := validateLimits(p.MinOrderCents, p.MaxUses, p.MaxUsesPerUser, nil, nil); err != nil {
				return nil, err
			}
			if p.MinOrderCents != nil {
				if cur.MinOrderCents != nil && *cur.MinOrderCents == *p.MinOrderCents {
					p.MinOrderCents = nil
				} else {
					old := "none"
					if cur.MinOrderCents != nil {
						old = money.Format(*cur.MinOrderCents)
					}
					diffs = append(diffs, fmt.Sprintf("minimum order: %s -> %s", old, money.Format(*p.MinOrderCents)))
				}
			}
			intField := func(name string, want **int, cur *int) {
				if *want == nil {
					return
				}
				if cur != nil && *cur == **want {
					*want = nil
					return
				}
				diffs = append(diffs, fmt.Sprintf("%s: %s -> %d", name, fmtIntPtr(cur), **want))
			}
			intField("max uses", &p.MaxUses, cur.MaxUses)
			intField("max uses per customer", &p.MaxUsesPerUser, cur.MaxUsesPerUser)
			timeField := func(name string, want **time.Time, cur *time.Time) {
				if *want == nil {
					return
				}
				if cur != nil && cur.Equal(**want) {
					*want = nil
					return
				}
				diffs = append(diffs, fmt.Sprintf("%s: %s -> %s", name, fmtTimePtr(cur, env.Loc), fmtTimePtr(*want, env.Loc)))
			}
			timeField("valid from", &p.ValidFrom, cur.ValidFrom)
			timeField("valid until", &p.ValidUntil, cur.ValidUntil)
			from, until := cur.ValidFrom, cur.ValidUntil
			if p.ValidFrom != nil {
				from = p.ValidFrom
			}
			if p.ValidUntil != nil {
				until = p.ValidUntil
			}
			if err := validateLimits(nil, nil, nil, from, until); err != nil {
				return nil, err
			}

			pr := &prepared{EntityType: "coupon", EntityID: c.ID, Before: cur}
			if len(diffs) == 0 {
				pr.NoOp = true
				pr.Summary = fmt.Sprintf("Coupon %s already has these terms. Nothing changed.", c.Code)
				return pr, nil
			}
			pr.Summary = fmt.Sprintf("Coupon %s: %s", c.Code, strings.Join(diffs, "; "))
			return pr, nil
		},
		Execute: func(ctx context.Context, env *Env, p CouponUpdateParams, _ []byte) (any, error) {
			in := upstream.CouponInput{Code: p.Code, MaxUses: p.MaxUses, MaxUsesPerUser: p.MaxUsesPerUser, ValidFrom: p.ValidFrom, ValidUntil: p.ValidUntil}
			if p.Discount != nil {
				typ, val := p.Discount.upstream()
				in.DiscountType, in.DiscountValue = &typ, &val
			}
			if p.MinOrderCents != nil {
				m := money.FromCents(*p.MinOrderCents)
				in.MinOrderAmount = &m
			}
			c, err := env.Up.UpdateCoupon(ctx, p.CouponID, in)
			if err != nil {
				return nil, err
			}
			return termsOf(c), nil
		},
		Inverse: func(p CouponUpdateParams, b couponTerms, _ json.RawMessage) (string, any, error) {
			inv := CouponUpdateParams{CouponID: p.CouponID}
			cannot := func() (string, any, error) {
				return "", nil, Userf("The previous coupon had no such limit, and a limit cannot be removed automatically. Change it in the dashboard.")
			}
			if p.Code != nil {
				inv.Code = &b.Code
			}
			if p.Discount != nil {
				inv.Discount = &b.Discount
			}
			if p.MinOrderCents != nil {
				if b.MinOrderCents == nil {
					return cannot()
				}
				inv.MinOrderCents = b.MinOrderCents
			}
			if p.MaxUses != nil {
				if b.MaxUses == nil {
					return cannot()
				}
				inv.MaxUses = b.MaxUses
			}
			if p.MaxUsesPerUser != nil {
				if b.MaxUsesPerUser == nil {
					return cannot()
				}
				inv.MaxUsesPerUser = b.MaxUsesPerUser
			}
			if p.ValidFrom != nil {
				if b.ValidFrom == nil {
					return cannot()
				}
				inv.ValidFrom = b.ValidFrom
			}
			if p.ValidUntil != nil {
				if b.ValidUntil == nil {
					return cannot()
				}
				inv.ValidUntil = b.ValidUntil
			}
			return KindCouponUpdate, inv, nil
		},
	}
}

// --- activation ---------------------------------------------------------------

// CouponRef targets a coupon.
type CouponRef struct {
	CouponID string `json:"coupon_id"`
}

func couponActive(kind string, risk Risk, active bool, inverseKind string) handler {
	return spec[CouponRef, toggleBefore]{
		Kind: kind,
		Risk: risk,
		Prepare: func(ctx context.Context, env *Env, p *CouponRef) (*prepared, error) {
			c, err := getCoupon(ctx, env, p.CouponID)
			if err != nil {
				return nil, err
			}
			pr := &prepared{EntityType: "coupon", EntityID: c.ID, Before: toggleBefore{Value: c.IsActive}}
			word := map[bool]string{true: "active", false: "inactive"}
			if c.IsActive == active {
				pr.NoOp = true
				pr.Summary = fmt.Sprintf("Coupon %s is already %s. Nothing changed.", c.Code, word[active])
				return pr, nil
			}
			if active && c.ValidUntil != nil && c.ValidUntil.Before(env.Now()) {
				return nil, Userf("Coupon %s expired on %s. Extend valid_until first.", c.Code, c.ValidUntil.In(env.Loc).Format(time.DateOnly))
			}
			pr.Summary = fmt.Sprintf("Coupon %s (%s): %s -> %s", c.Code, couponValueOf(c), word[c.IsActive], word[active])
			return pr, nil
		},
		Execute: func(ctx context.Context, env *Env, p CouponRef, _ []byte) (any, error) {
			v := active
			c, err := env.Up.UpdateCoupon(ctx, p.CouponID, upstream.CouponInput{IsActive: &v})
			if err != nil {
				return nil, err
			}
			return toggleBefore{Value: c.IsActive}, nil
		},
		Inverse: func(p CouponRef, _ toggleBefore, _ json.RawMessage) (string, any, error) {
			return inverseKind, p, nil
		},
	}
}

// registry lists every action kind.
func registry() []handler {
	return []handler{
		productToggle(KindProductAvailability, "available", func(p *upstream.Product) bool { return p.IsAvailable }, availWord,
			func(in *upstream.UpdateProductInput, v bool) { in.IsAvailable = &v }),
		productToggle(KindProductVisibility, "visible", func(p *upstream.Product) bool { return p.IsVisible }, visWord,
			func(in *upstream.UpdateProductInput, v bool) { in.IsVisible = &v }),
		bulkAvailability(),
		priceChange(),
		vatChange(),
		productUpdate(),
		productCreate(),
		productImage(),
		choiceGroupUpsert(),
		choiceGroupDelete(),
		choiceUpsert(),
		choiceDelete(),
		preparationMinutes(),
		weeklyHours(KindOpeningHours, "Opening hours", func(c *upstream.RestaurantConfig) json.RawMessage { return c.OpeningHours }, (*upstream.Client).UpdateOpeningHours),
		weeklyHours(KindOrderingHours, "Ordering hours", func(c *upstream.RestaurantConfig) json.RawMessage { return c.OrderingHours }, (*upstream.Client).UpdateOrderingHours),
		schedule(),
		couponCreate(),
		couponUpdate(),
		couponActive(KindCouponActivate, RiskSensitive, true, KindCouponDeactivate),
		couponActive(KindCouponDeactivate, RiskLow, false, KindCouponActivate),
	}
}
