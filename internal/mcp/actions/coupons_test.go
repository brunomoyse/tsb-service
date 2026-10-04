package actions

import (
	"errors"
	"testing"
	"time"

	"tsb-service/internal/mcp/changes"
	"tsb-service/internal/mcp/upstream"
)

func (f *fixture) coupon(id string) upstream.Coupon {
	f.fake.Lock()
	defer f.fake.Unlock()
	for _, c := range f.fake.Coupons {
		if c.ID == id {
			return *c
		}
	}
	return upstream.Coupon{}
}

func (f *fixture) editCoupon(id string, edit func(c *upstream.Coupon)) {
	f.fake.Lock()
	defer f.fake.Unlock()
	for _, c := range f.fake.Coupons {
		if c.ID == id {
			edit(c)
		}
	}
}

func TestGetCoupon(t *testing.T) {
	f := newFixture(t)
	c, err := getCoupon(f.ctx, f.svc.env, "cp-welcome")
	if err != nil || c.Code != "WELCOME10" {
		t.Fatal(c, err)
	}
	for _, id := range []string{"", "   "} {
		_, err = getCoupon(f.ctx, f.svc.env, id)
		wantUserErr(t, err, "coupon_id is required")
	}
	_, err = getCoupon(f.ctx, f.svc.env, "cp-nope")
	wantUserErr(t, err, `No coupon with id "cp-nope"`)
	f.fail("McpCoupon")
	_, err = getCoupon(f.ctx, f.svc.env, "cp-welcome")
	wantUpstreamErr(t, err)
}

func TestCouponValue(t *testing.T) {
	tests := []struct {
		name    string
		v       CouponValue
		wantErr string
		typ     string
		val     string
		str     string
	}{
		{"1 percent", CouponValue{Type: "percentage", PercentOff: 1}, "", "percentage", "1", "1% off"},
		{"100 percent", CouponValue{Type: "percentage", PercentOff: 100}, "", "percentage", "100", "100% off"},
		{"0 percent", CouponValue{Type: "percentage"}, "between 1 and 100", "", "", ""},
		{"101 percent", CouponValue{Type: "percentage", PercentOff: 101}, "between 1 and 100", "", "", ""},
		{"fixed", CouponValue{Type: "fixed", AmountOffCent: 550}, "", "fixed", "5.50", "5.50 EUR off"},
		{"fixed zero", CouponValue{Type: "fixed"}, "greater than 0", "", "", ""},
		{"fixed negative", CouponValue{Type: "fixed", AmountOffCent: -5}, "greater than 0", "", "", ""},
		{"unknown type", CouponValue{Type: "bogo", PercentOff: 10}, `"percentage" or "fixed"`, "", "", ""},
		{"empty type", CouponValue{}, `"percentage" or "fixed"`, "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.v.validate()
			if tt.wantErr != "" {
				wantUserErr(t, err, tt.wantErr)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			typ, val := tt.v.upstream()
			if typ != tt.typ || val != tt.val || tt.v.String() != tt.str {
				t.Errorf("upstream = %q %q, String = %q", typ, val, tt.v.String())
			}
		})
	}
}

func TestCouponValueOf(t *testing.T) {
	if v := couponValueOf(&upstream.Coupon{DiscountType: "PERCENTAGE", DiscountValue: "15"}); v != (CouponValue{Type: "percentage", PercentOff: 15}) {
		t.Errorf("percentage: %+v", v)
	}
	if v := couponValueOf(&upstream.Coupon{DiscountType: "percentage", DiscountValue: "7.00"}); v.PercentOff != 7 {
		t.Errorf("percentage with decimals: %+v", v)
	}
	if v := couponValueOf(&upstream.Coupon{DiscountType: "FIXED", DiscountValue: "5.5"}); v != (CouponValue{Type: "fixed", AmountOffCent: 550}) {
		t.Errorf("fixed: %+v", v)
	}
}

func TestValidateLimits(t *testing.T) {
	d1 := time.Date(2026, 11, 1, 0, 0, 0, 0, brussels)
	d2 := d1.Add(time.Hour)
	tests := []struct {
		name    string
		min     *int64
		uses    *int
		per     *int
		from    *time.Time
		until   *time.Time
		wantErr string
	}{
		{"all empty", nil, nil, nil, nil, nil, ""},
		{"all valid", i64(0), ip(1), ip(1), &d1, &d2, ""},
		{"negative minimum", i64(-1), nil, nil, nil, nil, "minimum order cannot be negative"},
		{"zero max uses", nil, ip(0), nil, nil, nil, "max_uses must be at least 1"},
		{"zero per user", nil, nil, ip(0), nil, nil, "max_uses_per_user must be at least 1"},
		{"from after until", nil, nil, nil, &d2, &d1, "valid_from must be before valid_until"},
		{"from equals until", nil, nil, nil, &d1, &d1, "valid_from must be before valid_until"},
		{"only from", nil, nil, nil, &d2, nil, ""},
		{"only until", nil, nil, nil, nil, &d1, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateLimits(tt.min, tt.uses, tt.per, tt.from, tt.until)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			wantUserErr(t, err, tt.wantErr)
		})
	}
}

func TestCouponDetailsFormatting(t *testing.T) {
	from := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC) // 11:00 in Brussels (CET)
	until := time.Date(2026, 11, 30, 22, 30, 0, 0, time.UTC)
	if got := couponDetails(nil, nil, nil, nil, nil, brussels); got != "" {
		t.Errorf("empty = %q", got)
	}
	got := couponDetails(i64(1500), ip(100), ip(2), &from, &until, brussels)
	want := " (minimum order 15.00 EUR, max 100 uses, max 2 per customer, from 2026-11-01 11:00, until 2026-11-30 23:30)"
	if got != want {
		t.Errorf("details = %q, want %q", got, want)
	}
	gotZh := couponDetailsZh(i64(1500), ip(100), ip(2), &from, &until, brussels)
	wantZh := "（最低消费 15.00 欧元，最多使用 100 次，每位顾客最多 2 次，11月1日 11:00 起，11月30日 23:30 止）"
	if gotZh != wantZh {
		t.Errorf("details zh = %q, want %q", gotZh, wantZh)
	}
	if fmtTimePtr(&from, brussels) != "2026-11-01 11:00" || fmtTimePtr(nil, brussels) != "none" {
		t.Error("fmtTimePtr")
	}
	if fmtIntPtr(ip(3)) != "3" || fmtIntPtr(nil) != "none" {
		t.Error("fmtIntPtr")
	}
}

func TestCouponCreateValidation(t *testing.T) {
	f := newFixture(t)
	past := f.clock.Now().Add(-time.Hour)
	future := f.clock.Now().Add(24 * time.Hour)
	pct := CouponValue{Type: "percentage", PercentOff: 10}
	tests := []struct {
		name string
		p    CouponCreateParams
		want string
	}{
		{"code too short", CouponCreateParams{Code: "AB", Discount: pct}, "3 to 32"},
		{"code with space", CouponCreateParams{Code: "SPRING 10", Discount: pct}, "3 to 32"},
		{"code too long", CouponCreateParams{Code: "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456", Discount: pct}, "3 to 32"},
		{"bad discount", CouponCreateParams{Code: "SPRING", Discount: CouponValue{Type: "percentage", PercentOff: 0}}, "between 1 and 100"},
		{"negative minimum", CouponCreateParams{Code: "SPRING", Discount: pct, MinOrderCents: i64(-1)}, "cannot be negative"},
		{"zero uses", CouponCreateParams{Code: "SPRING", Discount: pct, MaxUses: ip(0)}, "max_uses"},
		{"expired", CouponCreateParams{Code: "SPRING", Discount: pct, ValidUntil: &past}, "valid_until is in the past"},
		{"inverted dates", CouponCreateParams{Code: "SPRING", Discount: pct, ValidFrom: &future, ValidUntil: new(future.Add(-time.Minute))}, "valid_from must be before"},
		{"duplicate", CouponCreateParams{Code: "welcome10", Discount: pct}, "already exists (id cp-welcome)"},
		{"duplicate with spaces", CouponCreateParams{Code: " Summer5 ", Discount: pct}, "already exists (id cp-old)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.prepare(t, KindCouponCreate, tt.p)
			wantUserErr(t, err, tt.want)
		})
	}
	f.fail("McpCoupons")
	_, err := f.prepare(t, KindCouponCreate, CouponCreateParams{Code: "SPRING", Discount: pct})
	wantUpstreamErr(t, err)
	// Without a code there is nothing to check for duplicates.
	if _, err := f.prepare(t, KindCouponCreate, CouponCreateParams{Discount: pct}); err != nil {
		t.Errorf("generated code: %v", err)
	}
}

func TestCouponCreateAndUndo(t *testing.T) {
	f := newFixture(t)
	until := f.clock.Now().Add(48 * time.Hour)
	params := CouponCreateParams{Code: " spring15 ", Discount: CouponValue{Type: "fixed", AmountOffCent: 1500}, MinOrderCents: i64(4000), MaxUses: ip(50), MaxUsesPerUser: ip(1), Active: true, ValidUntil: &until}
	p := f.propose(t, KindCouponCreate, params)
	contains(t, "summary", p.Summary, "New coupon SPRING15: 15.00 EUR off", "minimum order 40.00 EUR", "max 50 uses", "max 1 per customer", "until 2026-10-05 13:00", "active")
	contains(t, "summary_zh", p.SummaryZh, "新优惠码 SPRING15：减 15.00 欧元", "最低消费 40.00 欧元", "启用")
	if len(f.fake.Coupons) != 2 {
		t.Fatal("proposal must not create the coupon")
	}
	if _, err := f.svc.ApplyPending(f.ctx, p.ChangeID, ""); err != nil {
		t.Fatal(err)
	}
	var created *upstream.Coupon
	for _, c := range f.fake.Coupons {
		if c.Code == "SPRING15" {
			created = c
		}
	}
	if created == nil || created.DiscountType != "FIXED" || created.DiscountValue != "15" || *created.MinOrderAmount != "40" || *created.MaxUses != 50 || *created.MaxUsesPerUser != 1 || !created.IsActive || created.ValidUntil == nil {
		t.Fatalf("created coupon: %+v", created)
	}

	// Undo of a creation deactivates it (coupons cannot be deleted).
	u, err := f.svc.Undo(f.ctx, "")
	if err != nil || u.Mode != "pending" {
		t.Fatalf("undo: %+v %v", u, err)
	}
	contains(t, "undo summary", u.Proposal.Summary, "SPRING15", "active -> inactive")
	if _, err := f.svc.ApplyPending(f.ctx, u.Proposal.ChangeID, ""); err != nil {
		t.Fatal(err)
	}
	if f.coupon(created.ID).IsActive {
		t.Error("undo must deactivate the coupon")
	}
}

func TestCouponCreateWithGeneratedCodeAndPercentage(t *testing.T) {
	f := newFixture(t)
	p := f.propose(t, KindCouponCreate, CouponCreateParams{Discount: CouponValue{Type: "percentage", PercentOff: 20}})
	contains(t, "summary", p.Summary, "New coupon (generated code): 20% off, inactive")
	contains(t, "summary_zh", p.SummaryZh, "（自动生成）", "优惠 20%", "停用")
	f.applyChange(t, KindCouponCreate, CouponCreateParams{Discount: CouponValue{Type: "percentage", PercentOff: 20}})
	c := f.fake.Coupons[2]
	if c.DiscountType != "PERCENTAGE" || c.DiscountValue != "20" || c.IsActive || c.MinOrderAmount != nil || c.Code == "" {
		t.Errorf("created: %+v", c)
	}
}

func TestCouponCreateFailureAndInverse(t *testing.T) {
	f := newFixture(t)
	p := f.propose(t, KindCouponCreate, CouponCreateParams{Code: "FAIL10", Discount: CouponValue{Type: "percentage", PercentOff: 10}})
	f.fail("McpCreateCoupon")
	c, err := f.svc.ApplyPending(f.ctx, p.ChangeID, "")
	wantUpstreamErr(t, err)
	if c.Status != changes.StatusFailed || len(f.fake.Coupons) != 2 {
		t.Errorf("status %s, coupons %d", c.Status, len(f.fake.Coupons))
	}
	for _, after := range []string{"", "nope", `{"coupon_id":""}`} {
		_, _, err := f.inverse(t, KindCouponCreate, CouponCreateParams{}, map[string]any{}, after)
		if !errors.Is(err, ErrNotUndoable) {
			t.Errorf("after %q: %v", after, err)
		}
	}
	kind, inv, err := f.inverse(t, KindCouponCreate, CouponCreateParams{}, map[string]any{}, `{"coupon_id":"cp-9","code":"X"}`)
	if err != nil || kind != KindCouponDeactivate || inv.(CouponRef).CouponID != "cp-9" {
		t.Errorf("inverse: %v %v %v", kind, inv, err)
	}
}

func TestCouponUpdateValidation(t *testing.T) {
	f := newFixture(t)
	tests := []struct {
		name string
		p    CouponUpdateParams
		want string
	}{
		{"no id", CouponUpdateParams{}, "coupon_id is required"},
		{"unknown", CouponUpdateParams{CouponID: "cp-nope"}, "No coupon"},
		{"bad code", CouponUpdateParams{CouponID: "cp-welcome", Code: sp("a")}, "3 to 32"},
		{"bad discount", CouponUpdateParams{CouponID: "cp-welcome", Discount: &CouponValue{Type: "fixed"}}, "greater than 0"},
		{"negative min", CouponUpdateParams{CouponID: "cp-welcome", MinOrderCents: i64(-5)}, "cannot be negative"},
		{"zero uses", CouponUpdateParams{CouponID: "cp-welcome", MaxUses: ip(0)}, "max_uses"},
		{"zero per user", CouponUpdateParams{CouponID: "cp-welcome", MaxUsesPerUser: ip(0)}, "max_uses_per_user"},
		{"inverted dates", CouponUpdateParams{CouponID: "cp-welcome", ValidFrom: new(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)), ValidUntil: new(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))}, "valid_from must be before"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.prepare(t, KindCouponUpdate, tt.p)
			wantUserErr(t, err, tt.want)
		})
	}
	// A new valid_from after the stored valid_until is also refused.
	f.editCoupon("cp-welcome", func(c *upstream.Coupon) { c.ValidUntil = new(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) })
	_, err := f.prepare(t, KindCouponUpdate, CouponUpdateParams{CouponID: "cp-welcome", ValidFrom: new(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC))})
	wantUserErr(t, err, "valid_from must be before")
	_, err = f.prepare(t, KindCouponUpdate, CouponUpdateParams{CouponID: "cp-welcome", ValidUntil: new(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)), ValidFrom: new(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))})
	if err != nil {
		t.Errorf("valid window: %v", err)
	}
}

func TestCouponUpdateNoOp(t *testing.T) {
	f := newFixture(t)
	from := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	until := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	f.editCoupon("cp-old", func(c *upstream.Coupon) {
		c.MaxUses, c.MaxUsesPerUser, c.ValidFrom, c.ValidUntil = ip(10), ip(2), &from, &until
	})
	for name, p := range map[string]CouponUpdateParams{
		"nothing":         {CouponID: "cp-old"},
		"same code":       {CouponID: "cp-old", Code: sp(" summer5 ")},
		"same discount":   {CouponID: "cp-old", Discount: &CouponValue{Type: "fixed", AmountOffCent: 500}},
		"same minimum":    {CouponID: "cp-old", MinOrderCents: i64(3000)},
		"same max uses":   {CouponID: "cp-old", MaxUses: ip(10), MaxUsesPerUser: ip(2)},
		"same dates":      {CouponID: "cp-old", ValidUntil: new(until.In(brussels)), ValidFrom: new(from.In(brussels))},
		"everything same": {CouponID: "cp-old", Code: sp("SUMMER5"), MinOrderCents: i64(3000), MaxUses: ip(10), ValidFrom: &from},
	} {
		t.Run(name, func(t *testing.T) {
			pr, err := f.prepare(t, KindCouponUpdate, p)
			if err != nil || !pr.NoOp {
				t.Fatalf("want no-op, got %+v %v", pr, err)
			}
			contains(t, "summary", pr.Summary, "Coupon SUMMER5 already has these terms")
			contains(t, "summary_zh", pr.SummaryZh, "无需更改")
		})
	}
	p, err := f.svc.Propose(f.ctx, "t", KindCouponUpdate, CouponUpdateParams{CouponID: "cp-old"}, nil, "", nil)
	if err != nil || !p.NoOp || p.ChangeID != "" {
		t.Errorf("no-op must not be stored: %+v %v", p, err)
	}
}

func TestCouponUpdateFromEmptyAndUndo(t *testing.T) {
	f := newFixture(t)
	// cp-welcome has no minimum, no limits and no dates: setting them is
	// allowed, but undoing cannot remove a limit upstream.
	from := time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC)
	until := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
	params := CouponUpdateParams{CouponID: "cp-welcome", Code: sp("welcome15"), Discount: &CouponValue{Type: "percentage", PercentOff: 15},
		MinOrderCents: i64(2000), MaxUses: ip(100), MaxUsesPerUser: ip(1), ValidFrom: &from, ValidUntil: &until}
	p := f.propose(t, KindCouponUpdate, params)
	contains(t, "summary", p.Summary, "Coupon WELCOME10:", "code: WELCOME10 -> WELCOME15", "discount: 10% off -> 15% off", "minimum order: none -> 20.00 EUR",
		"max uses: none -> 100", "max uses per customer: none -> 1", "valid from: none -> 2026-10-10 10:00", "valid until: none -> 2026-10-31 21:00")
	contains(t, "summary_zh", p.SummaryZh, "优惠码：WELCOME10 → WELCOME15", "折扣：优惠 10% → 优惠 15%", "最低消费：无 → 20.00 欧元", "最多使用次数：无 → 100",
		"每位顾客最多使用次数：无 → 1", "生效时间：无 → 10月10日 10:00", "截止时间：无 → 10月31日 21:00")
	if _, err := f.svc.ApplyPending(f.ctx, p.ChangeID, ""); err != nil {
		t.Fatal(err)
	}
	c := f.coupon("cp-welcome")
	if c.Code != "WELCOME15" || c.DiscountValue != "15" || *c.MinOrderAmount != "20" || *c.MaxUses != 100 || *c.MaxUsesPerUser != 1 || !c.ValidFrom.Equal(from) || !c.ValidUntil.Equal(until) {
		t.Fatalf("coupon after update: %+v", c)
	}
	_, err := f.svc.Undo(f.ctx, "")
	wantUserErr(t, err, "no such limit")
}

func TestCouponUpdateAndUndoRestoresTerms(t *testing.T) {
	f := newFixture(t)
	until := time.Date(2026, 12, 31, 20, 0, 0, 0, time.UTC)
	from := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	f.editCoupon("cp-old", func(c *upstream.Coupon) {
		c.MaxUses, c.MaxUsesPerUser, c.ValidFrom, c.ValidUntil = ip(10), ip(2), &from, &until
	})
	newFrom, newUntil := from.Add(24*time.Hour), until.Add(24*time.Hour)
	p := f.propose(t, KindCouponUpdate, CouponUpdateParams{CouponID: "cp-old", Code: sp("SUMMER6"), Discount: &CouponValue{Type: "percentage", PercentOff: 5},
		MinOrderCents: i64(2500), MaxUses: ip(20), MaxUsesPerUser: ip(3), ValidFrom: &newFrom, ValidUntil: &newUntil})
	contains(t, "summary", p.Summary, "minimum order: 30.00 EUR -> 25.00 EUR", "max uses: 10 -> 20", "max uses per customer: 2 -> 3", "valid from: 2026-10-01 10:00 -> 2026-10-02 10:00")
	contains(t, "summary_zh", p.SummaryZh, "最低消费：30.00 欧元 → 25.00 欧元", "最多使用次数：10 → 20")
	if _, err := f.svc.ApplyPending(f.ctx, p.ChangeID, ""); err != nil {
		t.Fatal(err)
	}
	c := f.coupon("cp-old")
	if c.Code != "SUMMER6" || c.DiscountType != "PERCENTAGE" || *c.MinOrderAmount != "25" || *c.MaxUses != 20 {
		t.Fatalf("after update: %+v", c)
	}
	u, err := f.svc.Undo(f.ctx, "")
	if err != nil || u.Mode != "pending" {
		t.Fatalf("undo: %+v %v", u, err)
	}
	if _, err := f.svc.ApplyPending(f.ctx, u.Proposal.ChangeID, ""); err != nil {
		t.Fatal(err)
	}
	c = f.coupon("cp-old")
	if c.Code != "SUMMER5" || c.DiscountType != "FIXED" || c.DiscountValue != "5" || *c.MinOrderAmount != "30" || *c.MaxUses != 10 || *c.MaxUsesPerUser != 2 || !c.ValidFrom.Equal(from) || !c.ValidUntil.Equal(until) {
		t.Fatalf("undo did not restore the terms: %+v", c)
	}
}

func TestCouponUpdateInverseCannotRemoveLimits(t *testing.T) {
	f := newFixture(t)
	empty := couponTerms{}
	tm := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	cases := map[string]CouponUpdateParams{
		"min order":   {CouponID: "c", MinOrderCents: i64(1)},
		"max uses":    {CouponID: "c", MaxUses: ip(1)},
		"per user":    {CouponID: "c", MaxUsesPerUser: ip(1)},
		"valid from":  {CouponID: "c", ValidFrom: &tm},
		"valid until": {CouponID: "c", ValidUntil: &tm},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := f.inverse(t, KindCouponUpdate, p, empty, "{}")
			wantUserErr(t, err, "cannot be removed automatically")
		})
	}
	// Code and discount always have a previous value.
	kind, inv, err := f.inverse(t, KindCouponUpdate, CouponUpdateParams{CouponID: "c", Code: sp("X"), Discount: &CouponValue{Type: "fixed", AmountOffCent: 1}},
		couponTerms{Code: "OLD", Discount: CouponValue{Type: "percentage", PercentOff: 5}}, "{}")
	got := inv.(CouponUpdateParams)
	if err != nil || kind != KindCouponUpdate || *got.Code != "OLD" || got.Discount.PercentOff != 5 || got.MaxUses != nil {
		t.Errorf("inverse: %v %+v %v", kind, got, err)
	}
}

func TestCouponUpdateExecuteFailureAndDirect(t *testing.T) {
	f := newFixture(t)
	out, err := f.execute(t, KindCouponUpdate, CouponUpdateParams{CouponID: "cp-welcome", MinOrderCents: i64(1250)}, nil)
	if err != nil || *out.(couponTerms).MinOrderCents != 1250 {
		t.Fatalf("execute: %+v %v", out, err)
	}
	f.fail("McpUpdateCoupon")
	_, err = f.execute(t, KindCouponUpdate, CouponUpdateParams{CouponID: "cp-welcome", MinOrderCents: i64(1)}, nil)
	wantUpstreamErr(t, err)
	if got := *f.coupon("cp-welcome").MinOrderAmount; got != "12.5" {
		t.Errorf("minimum order = %s", got)
	}
}

func TestCouponActivation(t *testing.T) {
	f := newFixture(t)
	// Reactivating costs money (the discount becomes usable again): sensitive.
	if f.svc.RiskOf(KindCouponActivate) != RiskSensitive || f.svc.RiskOf(KindCouponDeactivate) != RiskLow {
		t.Fatal("activation must be sensitive, deactivation low")
	}
	_, err := f.prepare(t, KindCouponActivate, CouponRef{CouponID: "cp-nope"})
	wantUserErr(t, err, "No coupon")

	pr, err := f.prepare(t, KindCouponActivate, CouponRef{CouponID: "cp-welcome"})
	if err != nil || !pr.NoOp {
		t.Fatalf("already active: %+v %v", pr, err)
	}
	contains(t, "summary", pr.Summary, "Coupon WELCOME10 is already active")
	contains(t, "summary_zh", pr.SummaryZh, "已经是启用状态")
	pr, _ = f.prepare(t, KindCouponDeactivate, CouponRef{CouponID: "cp-old"})
	if !pr.NoOp {
		t.Fatal("already inactive must be a no-op")
	}
	contains(t, "summary", pr.Summary, "Coupon SUMMER5 is already inactive")

	// Expired coupons cannot be switched on without extending them first.
	f.editCoupon("cp-old", func(c *upstream.Coupon) { c.ValidUntil = new(f.clock.Now().Add(-time.Hour)) })
	_, err = f.prepare(t, KindCouponActivate, CouponRef{CouponID: "cp-old"})
	wantUserErr(t, err, "Coupon SUMMER5 expired on 2026-10-03. Extend valid_until first.")
	f.editCoupon("cp-old", func(c *upstream.Coupon) { c.ValidUntil = nil })

	p := f.propose(t, KindCouponActivate, CouponRef{CouponID: "cp-old"})
	contains(t, "summary", p.Summary, "Coupon SUMMER5 (5.00 EUR off): inactive -> active")
	contains(t, "summary_zh", p.SummaryZh, "SUMMER5（减 5.00 欧元）：停用 → 启用")
	if f.coupon("cp-old").IsActive {
		t.Fatal("proposal must not activate")
	}
	if _, err := f.svc.ApplyPending(f.ctx, p.ChangeID, ""); err != nil {
		t.Fatal(err)
	}
	if !f.coupon("cp-old").IsActive {
		t.Error("coupon not activated")
	}
	// Undoing an activation (a sensitive change) deactivates, which is low
	// risk but still goes through confirmation because the original was not.
	u, err := f.svc.Undo(f.ctx, "")
	if err != nil || u.Mode != "pending" || u.UndoneRisk != string(RiskSensitive) {
		t.Fatalf("undo: %+v %v", u, err)
	}
}

func TestCouponDeactivateAppliesImmediatelyAndUndoes(t *testing.T) {
	f := newFixture(t)
	r, err := f.svc.ApplyNow(f.ctx, "deactivate_coupon", KindCouponDeactivate, CouponRef{CouponID: "cp-welcome"}, "stop", nil)
	if err != nil || !r.Applied {
		t.Fatalf("deactivate: %+v %v", r, err)
	}
	if f.coupon("cp-welcome").IsActive {
		t.Fatal("coupon still active")
	}
	again, err := f.svc.ApplyNow(f.ctx, "deactivate_coupon", KindCouponDeactivate, CouponRef{CouponID: "cp-welcome"}, "", nil)
	if err != nil || !again.NoOp || again.Applied {
		t.Fatalf("repeat deactivate: %+v %v", again, err)
	}
	// Undoing a low-risk deactivation re-activates immediately: the owner
	// asked for it and the original was applied without confirmation.
	u, err := f.svc.Undo(f.ctx, "")
	if err != nil || u.Mode != "applied" {
		t.Fatalf("undo: %+v %v", u, err)
	}
	if !f.coupon("cp-welcome").IsActive {
		t.Error("undo must re-activate")
	}
	// Sensitive activation is never applied directly by a tool.
	_, err = f.svc.ApplyNow(f.ctx, "x", KindCouponActivate, CouponRef{CouponID: "cp-old"}, "", nil)
	if err == nil || f.coupon("cp-old").IsActive {
		t.Errorf("activate through ApplyNow must be refused, err=%v", err)
	}
}

func TestCouponActiveExecuteFailure(t *testing.T) {
	f := newFixture(t)
	f.fail("McpUpdateCoupon")
	r, err := f.svc.ApplyNow(f.ctx, "deactivate_coupon", KindCouponDeactivate, CouponRef{CouponID: "cp-welcome"}, "", nil)
	wantUpstreamErr(t, err)
	if r != nil {
		t.Errorf("result on failure: %+v", r)
	}
	audit, _ := f.svc.Store().RecentAudit(f.ctx, 1)
	if len(audit) != 1 || audit[0].Outcome != changes.OutcomeFailed || audit[0].Error == "" {
		t.Errorf("failure must be audited: %+v", audit)
	}
	if !f.coupon("cp-welcome").IsActive {
		t.Error("coupon changed despite failure")
	}
}
