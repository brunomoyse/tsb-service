package actions

import (
	"fmt"
	"strings"
	"time"

	"tsb-service/internal/mcp/money"
	"tsb-service/internal/mcp/upstream"
)

// Chinese summaries. Every summary also exists in Chinese (summary_zh), built
// from these fixed templates rather than by a model, so the owner confirms in
// WeChat exactly what will be applied. Names use the Chinese translation and
// fall back to the French name when it is missing.

// lines collects the parts of a summary in English and Chinese side by side.
type lines struct{ en, zh []string }

func (l *lines) add(en, zh string) {
	l.en = append(l.en, en)
	l.zh = append(l.zh, zh)
}

func (l *lines) empty() bool { return len(l.en) == 0 }

func (l *lines) enJoined() string { return strings.Join(l.en, "; ") }
func (l *lines) zhJoined() string { return strings.Join(l.zh, "；") }

const arrowZh = " → "

func quoteZh(s string) string { return "「" + s + "」" }

// quoteOrNoneZh quotes s, or returns 无 when it is empty.
func quoteOrNoneZh(s string) string {
	if s == "" {
		return "无"
	}
	return quoteZh(s)
}

// productLabelZh renders a product by its Chinese name and category, e.g.
// 「三文鱼」（刺身）.
func productLabelZh(p *upstream.Product) string {
	label := quoteZh(p.NameIn("zh"))
	if c := categoryRefZh(p.Category); c != "" {
		label += "（" + c + "）"
	}
	return label
}

// categoryRefZh is the Chinese name of a product's category, or its French
// name. Chinese product names repeat across categories (「三文鱼」 is a
// maki and a sashimi), so labels carry the category.
func categoryRefZh(c upstream.CategoryRef) string {
	for _, t := range c.Translations {
		if t.Language == "zh" && t.Name != "" {
			return t.Name
		}
	}
	return c.Name
}

// nameZh returns the Chinese entry of a name map, or fallback.
func nameZh(names map[string]string, fallback string) string {
	if n := names["zh"]; n != "" {
		return n
	}
	return fallback
}

// choiceLabelZh renders a choice or choice group by its Chinese name.
func choiceLabelZh(ts []upstream.ChoiceTranslation, fallback string) string {
	return quoteZh(nameZh(choiceNames(ts), fallback))
}

// categoryNameZh returns a category's Chinese name, or its French name.
func categoryNameZh(c *upstream.Category) string {
	for _, t := range c.Translations {
		if t.Language == "zh" && t.Name != "" {
			return t.Name
		}
	}
	return c.Name
}

func moneyZh(cents int64) string { return money.FromCents(cents) + " 欧元" }

func availZh(b bool) string {
	if b {
		return "可售"
	}
	return "售罄"
}

func visZh(b bool) string {
	if b {
		return "显示"
	}
	return "隐藏"
}

func yesNoZh(b bool) string {
	if b {
		return "是"
	}
	return "否"
}

func onOffZh(b bool) string {
	if b {
		return "开启"
	}
	return "关闭"
}

func activeZh(b bool) string {
	if b {
		return "启用"
	}
	return "停用"
}

var weekdayZh = map[string]string{
	"monday": "周一", "tuesday": "周二", "wednesday": "周三", "thursday": "周四",
	"friday": "周五", "saturday": "周六", "sunday": "周日",
}

var langZh = map[string]string{"fr": "法语", "en": "英语", "zh": "中文", "nl": "荷兰语"}

var vatZh = map[string]string{
	"food": "餐食", "beverage": "饮料", "zero_rated": "零税率", "out_of_scope": "不征增值税",
}

func vatLabelZh(v string) string {
	if z, ok := vatZh[v]; ok {
		return z
	}
	return v
}

// dateZh renders a yyyy-mm-dd date, e.g. 10月5日（周一）.
func dateZh(date string) string {
	d, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return date
	}
	return fmt.Sprintf("%d月%d日（%s）", int(d.Month()), d.Day(), weekdayZh[weekdayKey(d)])
}

// timeZh renders an instant in the restaurant timezone, e.g. 10月5日 15:00.
func timeZh(t *time.Time, loc *time.Location) string {
	if t == nil {
		return "无"
	}
	l := t.In(loc)
	return fmt.Sprintf("%d月%d日 %s", int(l.Month()), l.Day(), l.Format("15:04"))
}

// describeDayZh renders a day schedule, e.g. 11:30至14:30，18:00至22:00.
func describeDayZh(d *upstream.DaySchedule) string {
	if d == nil {
		return "休息"
	}
	s := d.Open + "至" + d.Close
	if d.DinnerOpen != "" {
		s += "，" + d.DinnerOpen + "至" + d.DinnerClose
	}
	return s
}

func describeOverrideZh(st *overrideState, regular *upstream.DaySchedule) string {
	if st == nil {
		return "正常营业时间（" + describeDayZh(regular) + "）"
	}
	if st.Closed {
		return "全天休息"
	}
	return "特殊营业时间 " + describeDayZh(st.Schedule)
}

func describeSpecZh(o OverrideSpec) string {
	s := "特殊营业时间 " + describeDayZh(o.Schedule)
	if o.Closed {
		s = "全天休息"
	}
	if o.Note != nil && *o.Note != "" {
		s += "（备注：" + *o.Note + "）"
	}
	return s
}

// couponValueZh renders a discount, e.g. 优惠 10% or 减 5.00 欧元.
func couponValueZh(v CouponValue) string {
	if v.Type == "percentage" {
		return fmt.Sprintf("优惠 %d%%", v.PercentOff)
	}
	return "减 " + moneyZh(v.AmountOffCent)
}

func couponDetailsZh(minOrder *int64, maxUses, perUser *int, from, until *time.Time, loc *time.Location) string {
	var parts []string
	if minOrder != nil {
		parts = append(parts, "最低消费 "+moneyZh(*minOrder))
	}
	if maxUses != nil {
		parts = append(parts, fmt.Sprintf("最多使用 %d 次", *maxUses))
	}
	if perUser != nil {
		parts = append(parts, fmt.Sprintf("每位顾客最多 %d 次", *perUser))
	}
	if from != nil {
		parts = append(parts, timeZh(from, loc)+" 起")
	}
	if until != nil {
		parts = append(parts, timeZh(until, loc)+" 止")
	}
	if len(parts) == 0 {
		return ""
	}
	return "（" + strings.Join(parts, "，") + "）"
}

func intPtrZh(p *int) string {
	if p == nil {
		return "无"
	}
	return fmt.Sprint(*p)
}
