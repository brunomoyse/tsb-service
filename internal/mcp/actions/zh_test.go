package actions

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode"

	"tsb-service/internal/mcp/upstream"
)

func hasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// TestSummaryZhEveryKind prepares one change per registered kind and checks
// the Chinese summary: present, in Chinese, using the seeded Chinese names.
func TestSummaryZhEveryKind(t *testing.T) {
	f := newFixture(t)
	until := time.Date(2026, 10, 31, 23, 0, 0, 0, brussels)
	sauceZh, note := "辣酱", "装修"
	cat, code, pieces, yes := "cat-sashimi", "M9", 8, true
	week := upstream.Week{
		"tuesday": {Open: "11:30", Close: "14:30", DinnerOpen: "18:00", DinnerClose: "22:00"},
		"monday":  {Open: "11:30", Close: "14:30"},
	}
	cases := map[string]struct {
		params any
		want   []string
	}{
		KindProductAvailability:     {ProductToggleParams{ProductID: "p-maki-saumon", Value: false}, []string{"卷「三文鱼卷」", "可售 → 售罄"}},
		KindProductVisibility:       {ProductToggleParams{ProductID: "p-maki-saumon", Value: false}, []string{"显示 → 隐藏"}},
		KindProductAvailabilityBulk: {BulkAvailabilityParams{Items: []BulkAvailabilityItem{{ProductID: "p-maki-saumon"}, {ProductID: "p-creme"}}}, []string{"1 个商品", "卷「三文鱼卷」：可售 → 售罄", "已是该状态：套餐「焦糖布丁」"}},
		KindProductPrice:            {PriceParams{ProductID: "p-maki-saumon", NewPriceCents: 500}, []string{"卷「三文鱼卷」价格：4.50 欧元 → 5.00 欧元"}},
		KindProductVAT:              {VATParams{ProductID: "p-maki-saumon", VatCategory: "beverage"}, []string{"餐食 → 饮料"}},
		KindProductUpdate: {ProductUpdateParams{ProductID: "p-maki-saumon", Names: map[string]string{"zh": "鲑鱼卷"}, CategoryID: &cat, Code: &code, PieceCount: &pieces, IsSpicy: &yes},
			[]string{"中文名称：「三文鱼卷」 → 「鲑鱼卷」", "分类：卷 → 刺身", "编号：「M1」 → 「M9」", "件数：6 → 8", "辣：否 → 是"}},
		KindProductCreate: {ProductCreateParams{CategoryID: "cat-maki", Names: map[string]string{"fr": "Maki thon", "en": "Tuna maki", "zh": "金枪鱼卷"}, PriceCents: 480, Available: true, Visible: true},
			[]string{"新商品「金枪鱼卷」", "分类卷", "4.80 欧元", "可售", "显示"}},
		KindProductImage:       {ImageParams{ProductID: "p-maki-saumon", ImageURL: "https://example.com/a.jpg", Filename: "a.jpg", ContentType: "image/jpeg", SizeBytes: 2048, SHA256: strings.Repeat("a", 64)}, []string{"卷「三文鱼卷」", "无法恢复"}},
		KindChoiceGroupUpsert:  {ChoiceGroupParams{GroupID: "g-sauce", MaxSelections: ptrTo(2)}, []string{"套餐「卷寿司套餐」的选项组「酱汁」", "最多选择：1 → 2"}},
		KindChoiceGroupDelete:  {ChoiceGroupRef{GroupID: "g-sauce"}, []string{"删除选项组「酱汁」", "2 个选项", "Soja、Mayo épicée"}},
		KindChoiceUpsert:       {ChoiceParams{ChoiceID: "c-spicy", Names: map[string]string{"zh": sauceZh}, PriceModifierCents: ptrTo[int64](60)}, []string{"中文名称：无 → 「辣酱」", "加价：0.50 欧元 → 0.60 欧元"}},
		KindChoiceDelete:       {ChoiceRef{ChoiceID: "c-soja"}, []string{"从选项组「酱汁」中删除选项「Soja」"}},
		KindPreparationMinutes: {PreparationParams{Minutes: 45}, []string{"备餐时间：30 分钟 → 45 分钟"}},
		KindOpeningHours:       {HoursParams{Week: week}, []string{"营业时间：", "周一：休息 → 11:30至14:30", "周三：11:30至14:30，18:00至22:00 → 休息"}},
		KindOrderingHours:      {HoursParams{Week: week}, []string{"接单时间："}},
		KindSchedule: {ScheduleParams{Origin: OriginOverride, OrderingEnabled: ptrTo(false), Upserts: []OverrideSpec{{Date: "2026-10-05", Closed: true, Note: &note}}},
			[]string{"在线点餐：开启 → 关闭", "10月5日（周一）：正常营业时间（休息） → 全天休息（备注：装修）"}},
		KindCouponCreate: {CouponCreateParams{Code: "NEW15", Discount: CouponValue{Type: "percentage", PercentOff: 15}, MinOrderCents: ptrTo[int64](2000), ValidUntil: &until, Active: true},
			[]string{"新优惠码 NEW15：优惠 15%", "最低消费 20.00 欧元", "10月31日 23:00 止", "启用"}},
		KindCouponUpdate:     {CouponUpdateParams{CouponID: "cp-welcome", Discount: &CouponValue{Type: "fixed", AmountOffCent: 300}, MaxUses: ptrTo(50)}, []string{"优惠码 WELCOME10", "折扣：优惠 10% → 减 3.00 欧元", "最多使用次数：无 → 50"}},
		KindCouponActivate:   {CouponRef{CouponID: "cp-old"}, []string{"优惠码 SUMMER5（减 5.00 欧元）：停用 → 启用"}},
		KindCouponDeactivate: {CouponRef{CouponID: "cp-welcome"}, []string{"优惠码 WELCOME10（优惠 10%）：启用 → 停用"}},
	}
	for _, h := range registry() {
		t.Run(h.kind(), func(t *testing.T) {
			c, ok := cases[h.kind()]
			if !ok {
				t.Fatalf("no Chinese summary case for kind %s", h.kind())
			}
			raw, err := json.Marshal(c.params)
			if err != nil {
				t.Fatal(err)
			}
			pr, _, err := h.prepare(f.ctx, f.svc.env, raw)
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			if pr.NoOp {
				t.Fatalf("unexpected no-op: %s", pr.Summary)
			}
			if !hasHan(pr.SummaryZh) {
				t.Fatalf("summary_zh %q is not Chinese", pr.SummaryZh)
			}
			for _, w := range c.want {
				if !strings.Contains(pr.SummaryZh, w) {
					t.Errorf("summary_zh %q does not contain %q", pr.SummaryZh, w)
				}
			}
		})
	}
}

func TestSummaryZhNoOp(t *testing.T) {
	f := newFixture(t)
	p, err := f.svc.Propose(f.ctx, "propose_price_change", KindProductPrice, PriceParams{ProductID: "p-maki-saumon", NewPriceCents: 450}, nil, "", nil)
	if err != nil || !p.NoOp {
		t.Fatalf("want no-op, got %+v %v", p, err)
	}
	if p.SummaryZh != "卷「三文鱼卷」的价格已经是 4.50 欧元，无需更改。" {
		t.Errorf("summary_zh %q", p.SummaryZh)
	}
}

func TestSummaryZhFallsBackToFrench(t *testing.T) {
	f := newFixture(t)
	f.fake.Lock()
	for _, p := range f.fake.Products {
		if p.ID == "p-maki-saumon" {
			p.Translations = []upstream.Translation{{Language: "fr", Name: "Maki saumon"}}
		}
	}
	f.fake.Unlock()
	p, err := f.svc.Propose(f.ctx, "propose_price_change", KindProductPrice, PriceParams{ProductID: "p-maki-saumon", NewPriceCents: 500}, nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.SummaryZh, "卷「Maki saumon」价格") {
		t.Errorf("summary_zh %q", p.SummaryZh)
	}
}

func ptrTo[T any](v T) *T { return &v }

func TestProductLabelZhNamesTheCategory(t *testing.T) {
	// The same Chinese name in two categories must read differently.
	f := newFixture(t)
	f.fake.Lock()
	for _, p := range f.fake.Products {
		if p.ID == "p-maki-saumon" || p.ID == "p-sashimi-saumon" {
			p.Translations = []upstream.Translation{{Language: "fr", Name: p.Name}, {Language: "zh", Name: "三文鱼"}}
		}
	}
	f.fake.Unlock()
	for id, want := range map[string]string{"p-maki-saumon": "卷「三文鱼」价格", "p-sashimi-saumon": "刺身「三文鱼」价格"} {
		p, err := f.svc.Propose(f.ctx, "propose_price_change", KindProductPrice, PriceParams{ProductID: id, NewPriceCents: 600}, nil, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(p.SummaryZh, want) {
			t.Errorf("%s: summary_zh %q, want prefix %q", id, p.SummaryZh, want)
		}
	}
}
