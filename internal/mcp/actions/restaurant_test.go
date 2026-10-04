package actions

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"tsb-service/internal/mcp/changes"
	"tsb-service/internal/mcp/upstream"
)

func (f *fixture) setOverride(date string, ov *upstream.ScheduleOverride) {
	f.fake.Lock()
	defer f.fake.Unlock()
	d, _ := time.Parse(time.DateOnly, date)
	ov.Date = d
	f.fake.Overrides[date] = ov
}

func (f *fixture) override(date string) *upstream.ScheduleOverride {
	f.fake.Lock()
	defer f.fake.Unlock()
	return f.fake.Overrides[date]
}

func TestParseHHMM(t *testing.T) {
	good := map[string]int{"00:00": 0, "09:05": 545, "11:30": 690, "23:59": 1439, "24:00": 1440}
	for in, want := range good {
		if got, err := parseHHMM(in); err != nil || got != want {
			t.Errorf("parseHHMM(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "9:30", "09:3", "0930", "09:60", "24:01", "25:00", "ab:cd", "-1:00", "09:-1", "09:30:00"} {
		if _, err := parseHHMM(in); err == nil || !strings.Contains(err.Error(), "expected HH:MM") {
			t.Errorf("parseHHMM(%q) must be refused, got %v", in, err)
		}
	}
	if fmtHHMM(545) != "09:05" || fmtHHMM(1440) != "24:00" {
		t.Error("fmtHHMM")
	}
}

func TestValidateDay(t *testing.T) {
	tests := []struct {
		name string
		d    *upstream.DaySchedule
		want string
	}{
		{"nil means closed", nil, ""},
		{"single block", &upstream.DaySchedule{Open: "11:30", Close: "22:00"}, ""},
		{"two blocks", &upstream.DaySchedule{Open: "11:30", Close: "14:30", DinnerOpen: "18:00", DinnerClose: "22:00"}, ""},
		{"back to back blocks", &upstream.DaySchedule{Open: "11:30", Close: "14:30", DinnerOpen: "14:30", DinnerClose: "22:00"}, ""},
		{"until midnight", &upstream.DaySchedule{Open: "17:00", Close: "24:00"}, ""},
		{"bad open", &upstream.DaySchedule{Open: "9h", Close: "22:00"}, `invalid time "9h"`},
		{"bad close", &upstream.DaySchedule{Open: "09:00", Close: "late"}, `invalid time "late"`},
		{"open after close", &upstream.DaySchedule{Open: "22:00", Close: "11:00"}, "Opening time 22:00 must be before closing time 11:00"},
		{"open equals close", &upstream.DaySchedule{Open: "11:00", Close: "11:00"}, "must be before closing time"},
		{"dinner open only", &upstream.DaySchedule{Open: "11:00", Close: "14:00", DinnerOpen: "18:00"}, "need both an opening and a closing time"},
		{"dinner close only", &upstream.DaySchedule{Open: "11:00", Close: "14:00", DinnerClose: "22:00"}, "need both an opening and a closing time"},
		{"bad dinner open", &upstream.DaySchedule{Open: "11:00", Close: "14:00", DinnerOpen: "6pm", DinnerClose: "22:00"}, `invalid time "6pm"`},
		{"bad dinner close", &upstream.DaySchedule{Open: "11:00", Close: "14:00", DinnerOpen: "18:00", DinnerClose: "25:00"}, `invalid time "25:00"`},
		{"dinner before midday close", &upstream.DaySchedule{Open: "11:00", Close: "14:00", DinnerOpen: "13:00", DinnerClose: "22:00"}, "must be after the midday closing 14:00"},
		{"dinner open after dinner close", &upstream.DaySchedule{Open: "11:00", Close: "14:00", DinnerOpen: "22:00", DinnerClose: "18:00"}, "must be before evening closing 18:00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateDay(tt.d)
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			wantUserErr(t, err, tt.want)
		})
	}
}

func TestDescribeDayAndSameDay(t *testing.T) {
	if DescribeDay(nil) != "closed" || DescribeDay(&upstream.DaySchedule{Open: "11:30", Close: "14:30"}) != "11:30-14:30" ||
		DescribeDay(&upstream.DaySchedule{Open: "11:30", Close: "14:30", DinnerOpen: "18:00", DinnerClose: "22:00"}) != "11:30-14:30, 18:00-22:00" {
		t.Error("DescribeDay")
	}
	a := &upstream.DaySchedule{Open: "11:30", Close: "14:30"}
	b := &upstream.DaySchedule{Open: "11:30", Close: "14:30"}
	c := &upstream.DaySchedule{Open: "11:30", Close: "15:00"}
	if !sameDay(a, b) || sameDay(a, c) || sameDay(a, nil) || sameDay(nil, a) || !sameDay(nil, nil) {
		t.Error("sameDay")
	}
	if describeDayZh(nil) != "休息" || describeDayZh(a) != "11:30至14:30" {
		t.Error("describeDayZh")
	}
}

func TestPreparationMinutes(t *testing.T) {
	f := newFixture(t)
	for _, m := range []int{0, -5, 241} {
		_, err := f.prepare(t, KindPreparationMinutes, PreparationParams{Minutes: m})
		wantUserErr(t, err, "between 1 and 240")
	}
	for _, m := range []int{1, 240} {
		if _, err := f.prepare(t, KindPreparationMinutes, PreparationParams{Minutes: m}); err != nil {
			t.Errorf("%d minutes must be allowed: %v", m, err)
		}
	}
	pr, err := f.prepare(t, KindPreparationMinutes, PreparationParams{Minutes: 30})
	if err != nil || !pr.NoOp {
		t.Fatalf("same value: %+v %v", pr, err)
	}
	contains(t, "summary", pr.Summary, "already 30 minutes")
	contains(t, "summary_zh", pr.SummaryZh, "已经是 30 分钟")

	r, err := f.svc.ApplyNow(f.ctx, "set_preparation_minutes", KindPreparationMinutes, PreparationParams{Minutes: 45}, "", nil)
	if err != nil || !r.Applied {
		t.Fatal(r, err)
	}
	contains(t, "summary", r.Summary, "30 min -> 45 min")
	contains(t, "summary_zh", r.SummaryZh, "30 分钟 → 45 分钟")
	if f.fake.Config.PreparationMinutes != 45 {
		t.Fatalf("minutes = %d", f.fake.Config.PreparationMinutes)
	}
	u, err := f.svc.Undo(f.ctx, "")
	if err != nil || u.Mode != "applied" || f.fake.Config.PreparationMinutes != 30 {
		t.Fatalf("undo: %+v %v (minutes %d)", u, err, f.fake.Config.PreparationMinutes)
	}

	f.fail("McpUpdatePreparationMinutes")
	_, err = f.svc.ApplyNow(f.ctx, "set_preparation_minutes", KindPreparationMinutes, PreparationParams{Minutes: 50}, "", nil)
	wantUpstreamErr(t, err)
	f.unfail("McpUpdatePreparationMinutes")
	f.fail("McpRestaurantConfig")
	_, err = f.prepare(t, KindPreparationMinutes, PreparationParams{Minutes: 50})
	wantUpstreamErr(t, err)
}

func weekCopy(t *testing.T, w upstream.Week) upstream.Week {
	t.Helper()
	out, err := upstream.ParseWeek(mustJSON(t, w))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestOpeningHoursValidation(t *testing.T) {
	f := newFixture(t)
	day := &upstream.DaySchedule{Open: "11:00", Close: "22:00"}
	for _, kind := range []string{KindOpeningHours, KindOrderingHours} {
		_, err := f.prepare(t, kind, HoursParams{Week: upstream.Week{"funday": day}})
		wantUserErr(t, err, `Unknown day "funday"`)
		_, err = f.prepare(t, kind, HoursParams{Week: upstream.Week{"monday": {Open: "23:00", Close: "22:00"}}})
		wantUserErr(t, err, "monday: Opening time 23:00 must be before closing time 22:00")
		_, err = f.prepare(t, kind, HoursParams{})
		wantUserErr(t, err, "at least one day a week")
		// A null day means closed, so a week of nulls is empty.
		_, err = f.prepare(t, kind, HoursParams{Week: upstream.Week{"monday": nil}})
		wantUserErr(t, err, "at least one day a week")
	}
	f.fail("McpRestaurantConfig")
	_, err := f.prepare(t, KindOpeningHours, HoursParams{Week: upstream.Week{"monday": day}})
	wantUpstreamErr(t, err)
}

func TestOpeningHoursChangeAndUndo(t *testing.T) {
	f := newFixture(t)
	before := weekCopy(t, f.fake.Opening)
	params := HoursParams{Week: upstream.Week{
		"saturday": {Open: "12:00", Close: "23:00"},
		"sunday":   {Open: "17:30", Close: "22:00"}, // unchanged
		"monday":   {Open: "18:00", Close: "22:00"}, // new
		"friday":   nil,                             // null: closed
	}}
	p := f.propose(t, KindOpeningHours, params)
	contains(t, "summary", p.Summary, "Opening hours:", "saturday: 11:30-22:30 -> 12:00-23:00", "monday: closed -> 18:00-22:00",
		"tuesday: 11:30-14:30, 18:00-22:00 -> closed", "friday: 11:30-14:30, 18:00-22:30 -> closed")
	if strings.Contains(p.Summary, "sunday") {
		t.Errorf("unchanged days must not be listed: %s", p.Summary)
	}
	contains(t, "summary_zh", p.SummaryZh, "营业时间：", "周六：11:30至22:30 → 12:00至23:00", "周一：休息 → 18:00至22:00")
	if _, err := f.svc.ApplyPending(f.ctx, p.ChangeID, ""); err != nil {
		t.Fatal(err)
	}
	got := weekCopy(t, f.fake.Opening)
	if len(got) != 3 || *got["saturday"] != (upstream.DaySchedule{Open: "12:00", Close: "23:00"}) || got["monday"] == nil || got["tuesday"] != nil {
		t.Fatalf("opening hours after change: %+v", got)
	}

	// No-op once applied.
	pr, err := f.prepare(t, KindOpeningHours, params)
	if err != nil || !pr.NoOp {
		t.Fatalf("repeat: %+v %v", pr, err)
	}
	contains(t, "summary", pr.Summary, "Opening hours are already set like this")
	contains(t, "summary_zh", pr.SummaryZh, "营业时间已经是这样设置的")

	u, err := f.svc.Undo(f.ctx, "")
	if err != nil || u.Mode != "pending" {
		t.Fatalf("undo: %+v %v", u, err)
	}
	if _, err := f.svc.ApplyPending(f.ctx, u.Proposal.ChangeID, ""); err != nil {
		t.Fatal(err)
	}
	restored := weekCopy(t, f.fake.Opening)
	if len(restored) != len(before) {
		t.Fatalf("restored %d days, want %d", len(restored), len(before))
	}
	for d, s := range before {
		if restored[d] == nil || *restored[d] != *s {
			t.Errorf("%s = %+v, want %+v", d, restored[d], s)
		}
	}
}

func TestOrderingHoursFollowingOpeningHours(t *testing.T) {
	f := newFixture(t)
	// Ordering hours are unset (they follow the opening hours); setting them
	// to the very same week is still a change.
	same := HoursParams{Week: weekCopy(t, f.fake.Opening)}
	p := f.propose(t, KindOrderingHours, same)
	contains(t, "summary", p.Summary, "Ordering hours: same as opening hours, now set explicitly")
	contains(t, "summary_zh", p.SummaryZh, "接单时间：与营业时间相同，现在单独设置")
	if _, err := f.svc.ApplyPending(f.ctx, p.ChangeID, ""); err != nil {
		t.Fatal(err)
	}
	if f.fake.Ordering == nil {
		t.Fatal("ordering hours not stored")
	}
	// Now they are explicit, the same week is a no-op.
	pr, err := f.prepare(t, KindOrderingHours, same)
	if err != nil || !pr.NoOp {
		t.Fatalf("repeat: %+v %v", pr, err)
	}
	contains(t, "summary", pr.Summary, "Ordering hours are already set like this")

	// The previous state ("follow the opening hours") cannot be restored.
	f2 := newFixture(t)
	f2.applyChange(t, KindOrderingHours, HoursParams{Week: upstream.Week{"saturday": {Open: "12:00", Close: "20:00"}}})
	_, err = f2.svc.Undo(f2.ctx, "")
	wantUserErr(t, err, "previous ordering hours followed the opening hours")
}

func TestOrderingHoursDiffAgainstOpeningHours(t *testing.T) {
	f := newFixture(t)
	// Ordering hours that are unset are compared with the opening hours.
	p := f.propose(t, KindOrderingHours, HoursParams{Week: upstream.Week{"saturday": {Open: "12:00", Close: "20:00"}}})
	contains(t, "summary", p.Summary, "saturday: 11:30-22:30 -> 12:00-20:00", "tuesday: 11:30-14:30, 18:00-22:00 -> closed")
}

func TestWeeklyHoursWriteFailure(t *testing.T) {
	f := newFixture(t)
	p := f.propose(t, KindOpeningHours, HoursParams{Week: upstream.Week{"saturday": {Open: "12:00", Close: "20:00"}}})
	f.fail("McpUpdateOpeningHours")
	c, err := f.svc.ApplyPending(f.ctx, p.ChangeID, "")
	wantUpstreamErr(t, err)
	if c.Status != changes.StatusFailed {
		t.Errorf("status = %s", c.Status)
	}
	if len(weekCopy(t, f.fake.Opening)) != 6 {
		t.Error("opening hours changed despite the failure")
	}
}

func TestWeeklyHoursCorruptCurrentHours(t *testing.T) {
	f := newFixture(t)
	h := weeklyHours("restaurant.test_hours", "Test hours", "测试", func(*upstream.RestaurantConfig) json.RawMessage { return json.RawMessage(`"oops"`) }, (*upstream.Client).UpdateOpeningHours)
	_, _, err := h.prepare(f.ctx, f.svc.env, mustJSON(t, HoursParams{Week: upstream.Week{"monday": {Open: "11:00", Close: "12:00"}}}))
	if err == nil {
		t.Fatal("unreadable current hours must fail the proposal")
	}
	if _, ok := errors.AsType[*UserError](err); ok {
		t.Errorf("not a user error: %v", err)
	}
}

func TestScheduleValidation(t *testing.T) {
	f := newFixture(t)
	day := &upstream.DaySchedule{Open: "12:00", Close: "15:00"}
	tests := []struct {
		name string
		p    ScheduleParams
		want string
	}{
		{"invalid date", ScheduleParams{Upserts: []OverrideSpec{{Date: "10/10/2026", Closed: true}}}, `Invalid date "10/10/2026"`},
		{"past", ScheduleParams{Upserts: []OverrideSpec{{Date: "2026-10-02", Closed: true}}}, "2026-10-02 is in the past"},
		{"over a year", ScheduleParams{Upserts: []OverrideSpec{{Date: "2027-10-05", Closed: true}}}, "more than a year ahead"},
		{"twice", ScheduleParams{Upserts: []OverrideSpec{{Date: "2026-10-10", Closed: true}, {Date: "2026-10-10", Schedule: day}}}, "2026-10-10 appears twice"},
		{"no hours and not closed", ScheduleParams{Upserts: []OverrideSpec{{Date: "2026-10-10"}}}, "give the special hours, or mark the day as closed"},
		{"invalid hours", ScheduleParams{Upserts: []OverrideSpec{{Date: "2026-10-10", Schedule: &upstream.DaySchedule{Open: "25:00", Close: "26:00"}}}}, "2026-10-10: invalid time"},
		{"delete invalid date", ScheduleParams{Deletes: []string{"tomorrow"}}, `Invalid date "tomorrow"`},
		{"delete a date also upserted", ScheduleParams{Upserts: []OverrideSpec{{Date: "2026-10-10", Closed: true}}, Deletes: []string{"2026-10-10"}}, "appears twice"},
		{"delete twice", ScheduleParams{Deletes: []string{"2026-10-10", "2026-10-10"}}, "appears twice"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.prepare(t, KindSchedule, tt.p)
			wantUserErr(t, err, tt.want)
		})
	}
	// Today and a year ahead are the boundaries.
	for _, d := range []string{"2026-10-03", "2027-10-04"} {
		if _, err := f.prepare(t, KindSchedule, ScheduleParams{Upserts: []OverrideSpec{{Date: d, Closed: true}}}); err != nil {
			t.Errorf("%s must be accepted: %v", d, err)
		}
	}
	f.fail("McpRestaurantConfig")
	_, err := f.prepare(t, KindSchedule, ScheduleParams{Upserts: []OverrideSpec{{Date: "2026-10-10", Closed: true}}})
	wantUpstreamErr(t, err)
	f.unfail("McpRestaurantConfig")
	f.fail("McpScheduleOverrides")
	_, err = f.prepare(t, KindSchedule, ScheduleParams{Upserts: []OverrideSpec{{Date: "2026-10-10", Closed: true}}})
	wantUpstreamErr(t, err)
}

func TestScheduleUpsertSummariesAndNormalisation(t *testing.T) {
	f := newFixture(t)
	f.setOverride("2026-10-11", &upstream.ScheduleOverride{Closed: true, Note: sp("vacances")})
	blank := "   "
	note := " fête "
	p := f.propose(t, KindSchedule, ScheduleParams{Origin: OriginOverride, Upserts: []OverrideSpec{
		{Date: "2026-10-10", Schedule: &upstream.DaySchedule{Open: "12:00", Close: "15:00"}, Note: &note},
		{Date: "2026-10-11", Closed: true, Schedule: &upstream.DaySchedule{Open: "12:00", Close: "13:00"}, Note: &blank}, // schedule ignored when closed, blank note dropped
		{Date: "2026-10-13", Closed: true},
	}})
	contains(t, "summary", p.Summary,
		`2026-10-10 (saturday): regular hours (11:30-22:30) -> special hours 12:00-15:00 (note: "fête")`,
		`2026-10-11 (sunday): closed all day -> closed all day`,
		"2026-10-13 (tuesday): regular hours (11:30-14:30, 18:00-22:00) -> closed all day")
	contains(t, "summary_zh", p.SummaryZh, "10月10日（周六）：正常营业时间（11:30至22:30） → 特殊营业时间 12:00至15:00（备注：fête）", "10月13日（周二）：")
	ch, _ := f.svc.GetChange(f.ctx, p.ChangeID)
	var stored ScheduleParams
	if err := json.Unmarshal(ch.Params, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Upserts[0].Note == nil || *stored.Upserts[0].Note != "fête" || stored.Upserts[1].Note != nil || stored.Upserts[1].Schedule != nil {
		t.Errorf("normalised params: %+v", stored.Upserts)
	}
}

func TestScheduleNoOps(t *testing.T) {
	f := newFixture(t)
	f.setOverride("2026-10-10", &upstream.ScheduleOverride{Schedule: &upstream.DaySchedule{Open: "12:00", Close: "15:00"}, Note: sp("x")})
	f.fake.Config.OrderingEnabled = true
	pr, err := f.prepare(t, KindSchedule, ScheduleParams{
		OrderingEnabled: bp(true),
		Upserts:         []OverrideSpec{{Date: "2026-10-10", Schedule: &upstream.DaySchedule{Open: "12:00", Close: "15:00"}, Note: sp("x")}},
		Deletes:         []string{"2026-10-20"}, // no override there: nothing to delete
	})
	if err != nil || !pr.NoOp {
		t.Fatalf("want no-op, got %+v %v", pr, err)
	}
	contains(t, "summary", pr.Summary, "The schedule is already like this")
	contains(t, "summary_zh", pr.SummaryZh, "营业安排已经是这样")
	// A different note on identical hours is a change.
	pr, err = f.prepare(t, KindSchedule, ScheduleParams{Upserts: []OverrideSpec{{Date: "2026-10-10", Schedule: &upstream.DaySchedule{Open: "12:00", Close: "15:00"}, Note: sp("y")}}})
	if err != nil || pr.NoOp {
		t.Fatalf("different note must be a change: %+v %v", pr, err)
	}
	// Different hours are a change too.
	pr, _ = f.prepare(t, KindSchedule, ScheduleParams{Upserts: []OverrideSpec{{Date: "2026-10-10", Schedule: &upstream.DaySchedule{Open: "12:00", Close: "16:00"}, Note: sp("x")}}})
	if pr.NoOp || !strings.Contains(pr.Summary, "special hours 12:00-15:00 -> special hours 12:00-16:00") {
		t.Errorf("different hours: %+v", pr)
	}
	// Open vs closed.
	pr, _ = f.prepare(t, KindSchedule, ScheduleParams{Upserts: []OverrideSpec{{Date: "2026-10-10", Closed: true, Note: sp("x")}}})
	if pr.NoOp {
		t.Error("closing a day with special hours is a change")
	}
}

func TestScheduleApplyDeleteAndUndo(t *testing.T) {
	f := newFixture(t)
	f.setOverride("2026-10-12", &upstream.ScheduleOverride{Closed: true, Note: sp("vacances")})
	f.setOverride("2026-10-14", &upstream.ScheduleOverride{Schedule: &upstream.DaySchedule{Open: "12:00", Close: "15:00"}})

	off := false
	params := ScheduleParams{OrderingEnabled: &off, Origin: OriginOverride,
		Upserts: []OverrideSpec{{Date: "2026-10-10", Closed: true}, {Date: "2026-10-14", Schedule: &upstream.DaySchedule{Open: "13:00", Close: "16:00"}}},
		Deletes: []string{"2026-10-12"}}
	p := f.propose(t, KindSchedule, params)
	contains(t, "summary", p.Summary, "online ordering: on -> off", "2026-10-10 (saturday)", "2026-10-12 (monday): closed all day -> regular hours (closed)",
		"2026-10-14 (wednesday): special hours 12:00-15:00 -> special hours 13:00-16:00")
	contains(t, "summary_zh", p.SummaryZh, "在线点餐：开启 → 关闭", "10月12日（周一）：全天休息 → 正常营业时间（休息）")
	if _, err := f.svc.ApplyPending(f.ctx, p.ChangeID, ""); err != nil {
		t.Fatal(err)
	}
	if f.fake.Config.OrderingEnabled || !f.override("2026-10-10").Closed || f.override("2026-10-12") != nil || f.override("2026-10-14").Schedule.Close != "16:00" {
		t.Fatalf("after apply: ordering=%v overrides=%v", f.fake.Config.OrderingEnabled, f.fake.Overrides)
	}

	u, err := f.svc.Undo(f.ctx, "")
	if err != nil || u.Mode != "pending" {
		t.Fatalf("undo: %+v %v", u, err)
	}
	if _, err := f.svc.ApplyPending(f.ctx, u.Proposal.ChangeID, ""); err != nil {
		t.Fatal(err)
	}
	if !f.fake.Config.OrderingEnabled {
		t.Error("undo must switch ordering back on")
	}
	if f.override("2026-10-10") != nil {
		t.Error("undo must remove the override that did not exist before")
	}
	if o := f.override("2026-10-12"); o == nil || !o.Closed || o.Note == nil || *o.Note != "vacances" {
		t.Errorf("undo must restore the deleted override: %+v", o)
	}
	if o := f.override("2026-10-14"); o == nil || o.Schedule == nil || o.Schedule.Close != "15:00" {
		t.Errorf("undo must restore the previous hours: %+v", o)
	}
}

func TestScheduleExecutePartialFailures(t *testing.T) {
	f := newFixture(t)
	off := false
	params := ScheduleParams{OrderingEnabled: &off,
		Upserts: []OverrideSpec{{Date: "2026-10-10", Closed: true}},
		Deletes: []string{"2026-10-12"}}

	f.fail("McpUpdateOrderingEnabled")
	_, err := f.execute(t, KindSchedule, params, nil)
	if err == nil || strings.Contains(err.Error(), "already applied") {
		t.Fatalf("first step failure must be plain: %v", err)
	}
	wantUpstreamErr(t, err)
	f.unfail("McpUpdateOrderingEnabled")

	f.fail("McpUpsertScheduleOverride")
	_, err = f.execute(t, KindSchedule, params, nil)
	if err == nil || !strings.Contains(err.Error(), "(1 of 3 schedule steps were already applied)") {
		t.Fatalf("upsert failure after the switch: %v", err)
	}
	f.unfail("McpUpsertScheduleOverride")
	f.fake.Config.OrderingEnabled = true

	f.fail("McpDeleteScheduleOverride")
	_, err = f.execute(t, KindSchedule, params, nil)
	if err == nil || !strings.Contains(err.Error(), "(2 of 3 schedule steps were already applied)") {
		t.Fatalf("delete failure after two steps: %v", err)
	}

	// Without the switch, a failing first upsert reports nothing applied.
	f.unfail("McpDeleteScheduleOverride")
	f.fail("McpUpsertScheduleOverride")
	_, err = f.execute(t, KindSchedule, ScheduleParams{Upserts: []OverrideSpec{{Date: "2026-10-10", Closed: true}}}, nil)
	if err == nil || strings.Contains(err.Error(), "already applied") {
		t.Errorf("plain failure expected: %v", err)
	}
}

func TestScheduleApplyFailureIsMarkedFailed(t *testing.T) {
	f := newFixture(t)
	p := f.propose(t, KindSchedule, ScheduleParams{Origin: OriginOverride, Upserts: []OverrideSpec{{Date: "2026-10-10", Closed: true}}})
	f.fail("McpUpsertScheduleOverride")
	c, err := f.svc.ApplyPending(f.ctx, p.ChangeID, "")
	wantUpstreamErr(t, err)
	if c.Status != changes.StatusFailed || f.override("2026-10-10") != nil {
		t.Errorf("status %s, override %+v", c.Status, f.override("2026-10-10"))
	}
}

func TestBlocksAndSubtract(t *testing.T) {
	if blocksOf(nil) != nil {
		t.Error("nil day has no blocks")
	}
	// Unparseable times are skipped rather than failing a read.
	if got := blocksOf(&upstream.DaySchedule{Open: "bad", Close: "22:00"}); len(got) != 0 {
		t.Errorf("bad times: %+v", got)
	}
	// A close time at or before the open time runs to the end of the day.
	if got := blocksOf(&upstream.DaySchedule{Open: "18:00", Close: "02:00"}); len(got) != 1 || got[0] != (block{18 * 60, 24 * 60}) {
		t.Errorf("after midnight: %+v", got)
	}
	bs := []block{{600, 900}, {1000, 1200}}
	tests := []struct {
		cs, ce int
		want   []block
	}{
		{0, 500, bs},                      // before everything
		{1300, 1440, bs},                  // after everything
		{600, 900, []block{{1000, 1200}}}, // removes the first exactly
		{700, 800, []block{{600, 700}, {800, 900}, {1000, 1200}}}, // punches a hole
		{0, 1440, nil},
		{900, 1000, bs}, // touches both without overlapping
	}
	for _, tt := range tests {
		got := subtract(bs, tt.cs, tt.ce)
		if len(got) != len(tt.want) || (len(got) > 0 && !equalBlocks(got, tt.want)) {
			t.Errorf("subtract(%d,%d) = %+v, want %+v", tt.cs, tt.ce, got, tt.want)
		}
	}
	if d, err := dayFromBlocks(nil); d != nil || err != nil {
		t.Error("no blocks means closed")
	}
	if _, err := dayFromBlocks([]block{{1, 2}, {3, 4}, {5, 6}}); err == nil {
		t.Error("three blocks cannot be expressed")
	}
}

func TestPlanClosureEdgeCases(t *testing.T) {
	now := hm(13, 0)
	week := upstream.Week{
		"saturday": {Open: "11:30", Close: "14:30", DinnerOpen: "18:00", DinnerClose: "22:30"},
		"sunday":   {Open: "17:30", Close: "22:00"},
	}
	// A day already closed by an override is skipped; the note of an existing
	// override is kept when no reason is given.
	overrides := map[string]*upstream.ScheduleOverride{
		"2026-10-04": {Closed: true},
		"2026-10-03": {Schedule: &upstream.DaySchedule{Open: "12:00", Close: "21:00"}, Note: sp("travaux")},
	}
	got, err := PlanClosure(now, brussels, week, overrides, now, time.Date(2026, 10, 5, 12, 0, 0, 0, brussels), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Upserts) != 1 || got.Upserts[0].Date != "2026-10-03" || got.Upserts[0].Closed || deref(got.Upserts[0].Note) != "travaux" {
		t.Errorf("upserts: %+v", got.Upserts)
	}
	if got.Origin != OriginClosure {
		t.Errorf("origin = %s", got.Origin)
	}
	// A closing time a few minutes in the past is tolerated (clock skew).
	if _, err := PlanClosure(now, brussels, week, nil, now.Add(-4*time.Minute), hm(23, 0), nil); err != nil {
		t.Errorf("4 minutes in the past: %v", err)
	}
	if _, err := PlanClosure(now, brussels, week, nil, now.Add(-6*time.Minute), hm(23, 0), nil); err == nil {
		t.Error("6 minutes in the past must be refused")
	}
	// Exactly 7 days is the limit.
	if _, err := PlanClosure(now, brussels, week, nil, now, now.Add(MaxClosureAhead), nil); err != nil {
		t.Errorf("exactly 7 days: %v", err)
	}
	// Closing past midnight keeps the last block until the end of the day.
	got, err = PlanClosure(now, brussels, upstream.Week{"saturday": {Open: "18:00", Close: "02:00"}}, nil, hm(20, 0), hm(23, 0), nil)
	if err != nil || len(got.Upserts) != 1 || got.Upserts[0].Schedule.Open != "18:00" || got.Upserts[0].Schedule.Close != "20:00" || got.Upserts[0].Schedule.DinnerOpen != "23:00" || got.Upserts[0].Schedule.DinnerClose != "24:00" {
		t.Errorf("after midnight: %+v %v", got.Upserts, err)
	}
}

func (f *fixture) closeUntil(t *testing.T, reopen time.Time) *changes.Change {
	t.Helper()
	reason := "test"
	params, err := PlanClosure(f.clock.Now(), brussels, f.fake.Opening, map[string]*upstream.ScheduleOverride{}, f.clock.Now(), reopen, &reason)
	if err != nil {
		t.Fatal(err)
	}
	return f.applyChange(t, KindSchedule, params)
}

func TestPlanReopeningErrors(t *testing.T) {
	f := newFixture(t)
	f.fail("McpRestaurantConfig")
	_, _, err := f.svc.PlanReopening(f.ctx)
	wantUpstreamErr(t, err)
	f.unfail("McpRestaurantConfig")

	// Nothing to cancel: a user-facing message, not a failure.
	_, notes, err := f.svc.PlanReopening(f.ctx)
	wantUserErr(t, err, "Online ordering is already on and there is no closure made by the assistant to cancel.")
	if len(notes) != 0 {
		t.Errorf("notes = %v", notes)
	}

	f.closeUntil(t, hm(18, 0))
	f.fail("McpScheduleOverrides")
	_, _, err = f.svc.PlanReopening(f.ctx)
	wantUpstreamErr(t, err)
}

func TestPlanReopeningIgnoresOtherChanges(t *testing.T) {
	f := newFixture(t)
	// A change made through propose_schedule_override is not a closure: it
	// must not be undone by "reopen".
	f.applyChange(t, KindSchedule, ScheduleParams{Origin: OriginOverride, Upserts: []OverrideSpec{{Date: "2026-10-10", Closed: true}}})
	// Entries that cannot be decoded are skipped.
	if _, err := f.svc.Store().AppendAudit(f.ctx, &changes.AuditEntry{At: f.clock.Now().UTC(), Source: "test", Kind: KindSchedule, Risk: "sensitive", EntityType: "restaurant", EntityID: "schedule",
		Params: json.RawMessage(`garbage`), Before: json.RawMessage(`{}`), Summary: "x", Outcome: changes.OutcomeApplied}); err != nil {
		t.Fatal(err)
	}
	_, _, err := f.svc.PlanReopening(f.ctx)
	wantUserErr(t, err, "no closure made by the assistant")
	if f.override("2026-10-10") == nil {
		t.Error("the override must be left alone")
	}
}

func TestPlanReopeningSkipsUndoneAndPastClosures(t *testing.T) {
	f := newFixture(t)
	f.closeUntil(t, hm(18, 0))
	// Undoing the closure (through the confirmed undo) removes the overrides:
	// there is nothing left to reopen.
	u, err := f.svc.Undo(f.ctx, "")
	if err != nil || u.Mode != "pending" {
		t.Fatalf("undo: %+v %v", u, err)
	}
	if _, err := f.svc.ApplyPending(f.ctx, u.Proposal.ChangeID, ""); err != nil {
		t.Fatal(err)
	}
	_, _, err = f.svc.PlanReopening(f.ctx)
	wantUserErr(t, err, "no closure made by the assistant")

	// A closure whose days are all in the past is ignored.
	f2 := newFixture(t)
	f2.closeUntil(t, hm(18, 0))
	f2.clock.Advance(26 * time.Hour)
	_, _, err = f2.svc.PlanReopening(f2.ctx)
	wantUserErr(t, err, "no closure made by the assistant")
}

func TestPlanReopeningRestoresOldestStateAcrossClosures(t *testing.T) {
	f := newFixture(t)
	// A special-hours override exists before the assistant closes anything.
	f.setOverride("2026-10-03", &upstream.ScheduleOverride{Schedule: &upstream.DaySchedule{Open: "12:00", Close: "21:00"}, Note: sp("base")})
	f.closeUntil(t, hm(18, 0))
	f.clock.Advance(time.Minute)
	f.closeUntil(t, hm(19, 0)) // a second closure on the same day
	plan, notes, err := f.svc.PlanReopening(f.ctx)
	if err != nil || len(notes) != 0 {
		t.Fatal(plan, notes, err)
	}
	if len(plan.Deletes) != 0 || len(plan.Upserts) != 1 || plan.Upserts[0].Date != "2026-10-03" {
		t.Fatalf("plan: %+v", plan)
	}
	got := plan.Upserts[0]
	if got.Closed || got.Schedule == nil || *got.Schedule != (upstream.DaySchedule{Open: "12:00", Close: "21:00"}) || deref(got.Note) != "base" {
		t.Errorf("must restore the state before the first closure, got %+v %+v", got, got.Schedule)
	}
}

func TestPlanReopeningNotesTodayClosedInDashboard(t *testing.T) {
	f := newFixture(t)
	f.setOverride("2026-10-03", &upstream.ScheduleOverride{Closed: true, Note: sp("panne")})
	f.fake.Lock()
	f.fake.Config.OrderingEnabled = false
	f.fake.Unlock()
	plan, notes, err := f.svc.PlanReopening(f.ctx)
	if err != nil || plan.OrderingEnabled == nil || !*plan.OrderingEnabled {
		t.Fatalf("plan %+v %v", plan, err)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], `today has a closure set in the dashboard (note: "panne")`) {
		t.Errorf("notes = %v", notes)
	}

	// With nothing else to do the message carries the note.
	f.fake.Lock()
	f.fake.Config.OrderingEnabled = true
	f.fake.Unlock()
	_, notes, err = f.svc.PlanReopening(f.ctx)
	wantUserErr(t, err, "Note: today has a closure set in the dashboard")
	if len(notes) != 1 {
		t.Errorf("notes = %v", notes)
	}

	// A failing lookup of today's override only drops the note.
	f.fake.Lock()
	f.fake.Config.OrderingEnabled = false
	f.fake.Unlock()
	f.fail("McpScheduleOverrides")
	plan, notes, err = f.svc.PlanReopening(f.ctx)
	if err != nil || plan.OrderingEnabled == nil || len(notes) != 0 {
		t.Errorf("plan %+v notes %v err %v", plan, notes, err)
	}
}

func TestPlanReopeningDoesNotNoteTodayWhenClosureCoversIt(t *testing.T) {
	f := newFixture(t)
	f.closeUntil(t, time.Date(2026, 10, 5, 12, 0, 0, 0, brussels))
	// Saturday (partial) and Sunday (closed) overrides come from the closure.
	plan, notes, err := f.svc.PlanReopening(f.ctx)
	if err != nil || len(notes) != 0 || len(plan.Deletes) != 2 {
		t.Fatalf("plan %+v notes %v err %v", plan, notes, err)
	}
}

func TestPlanReopeningStoreFailure(t *testing.T) {
	f := newFixture(t)
	if err := f.svc.Store().Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.PlanReopening(f.ctx); err == nil {
		t.Fatal("a broken audit store must fail the plan, not silently report nothing to do")
	}
}
