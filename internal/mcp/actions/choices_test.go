package actions

import (
	"errors"
	"strings"
	"testing"

	"tsb-service/internal/mcp/changes"
)

func (f *fixture) group(productID, groupID string) (g struct {
	Min, Max, Sort int
	Names          map[string]string
	Choices        int
}, ok bool) {
	for _, gr := range f.fake.ProductByID(productID).ChoiceGroups {
		if gr.ID == groupID {
			g.Min, g.Max, g.Sort, g.Choices = gr.MinSelections, gr.MaxSelections, gr.SortOrder, len(gr.Choices)
			g.Names = choiceNames(gr.Translations)
			return g, true
		}
	}
	return g, false
}

func TestFindGroupAndChoice(t *testing.T) {
	f := newFixture(t)
	prod, g, err := findGroup(f.ctx, f.svc.env, "g-sauce")
	if err != nil || prod.ID != "p-maki-box" || g.ID != "g-sauce" {
		t.Fatalf("findGroup: %v %v %v", prod, g, err)
	}
	_, _, err = findGroup(f.ctx, f.svc.env, "g-nope")
	wantUserErr(t, err, `No choice group with id "g-nope"`)

	prod, g, c, err := findChoice(f.ctx, f.svc.env, "c-spicy")
	if err != nil || prod.ID != "p-maki-box" || g.ID != "g-sauce" || c.ID != "c-spicy" {
		t.Fatalf("findChoice: %v %v %v %v", prod, g, c, err)
	}
	_, _, _, err = findChoice(f.ctx, f.svc.env, "c-nope")
	wantUserErr(t, err, `No choice with id "c-nope"`)

	f.fail("McpProducts")
	_, _, err = findGroup(f.ctx, f.svc.env, "g-sauce")
	wantUpstreamErr(t, err)
	_, _, _, err = findChoice(f.ctx, f.svc.env, "c-soja")
	wantUpstreamErr(t, err)
}

func TestMergeNames(t *testing.T) {
	ts, diffs, err := mergeNames(map[string]string{"fr": "Sauce"}, map[string]string{"fr": " Sauce ", "en": "Sauce EN", "zh": "酱"})
	if err != nil {
		t.Fatal(err)
	}
	// fr is unchanged after trimming: only en and zh are differences.
	if len(diffs.en) != 2 || len(diffs.zh) != 2 {
		t.Errorf("diffs = %+v", diffs)
	}
	got := map[string]string{}
	for _, tr := range ts {
		got[tr.Locale] = tr.Name
	}
	if len(ts) != 3 || got["fr"] != "Sauce" || got["en"] != "Sauce EN" || got["zh"] != "酱" {
		t.Errorf("merged = %+v", ts)
	}
	// Translations come back sorted by locale so the request is deterministic.
	if ts[0].Locale != "en" || ts[1].Locale != "fr" || ts[2].Locale != "zh" {
		t.Errorf("order = %+v", ts)
	}

	if _, _, err := mergeNames(nil, map[string]string{"de": "x"}); err == nil {
		t.Error("unknown language must be refused")
	}
	_, _, err = mergeNames(nil, map[string]string{"fr": "  "})
	wantUserErr(t, err, "The fr name cannot be empty")

	// Nothing to merge on a nil current map.
	ts, diffs, err = mergeNames(nil, nil)
	if err != nil || len(ts) != 0 || !diffs.empty() {
		t.Errorf("empty merge: %v %+v %v", ts, diffs, err)
	}
}

func TestChoiceGroupUpsertValidation(t *testing.T) {
	f := newFixture(t)
	tests := []struct {
		name string
		p    ChoiceGroupParams
		want string
	}{
		{"unknown group", ChoiceGroupParams{GroupID: "g-nope"}, "No choice group"},
		{"unknown product", ChoiceGroupParams{ProductID: "p-nope", Names: map[string]string{"fr": "X"}}, "No product with id"},
		{"no product at all", ChoiceGroupParams{Names: map[string]string{"fr": "X"}}, "product_id is required"},
		{"new group without names", ChoiceGroupParams{ProductID: "p-maki-saumon"}, "French name"},
		{"new group without french", ChoiceGroupParams{ProductID: "p-maki-saumon", Names: map[string]string{"en": "Size"}}, "French name"},
		{"new group blank french", ChoiceGroupParams{ProductID: "p-maki-saumon", Names: map[string]string{"fr": "  "}}, "French name"},
		{"unknown language", ChoiceGroupParams{GroupID: "g-sauce", Names: map[string]string{"de": "Soße"}}, "Unknown language"},
		{"empty name on update", ChoiceGroupParams{GroupID: "g-sauce", Names: map[string]string{"zh": " "}}, "cannot be empty"},
		{"min above max", ChoiceGroupParams{GroupID: "g-sauce", MinSelections: ip(3)}, "0 <= min <= max"},
		{"max zero", ChoiceGroupParams{GroupID: "g-sauce", MinSelections: ip(0), MaxSelections: ip(0)}, "0 <= min <= max"},
		{"negative min", ChoiceGroupParams{GroupID: "g-sauce", MinSelections: ip(-1)}, "0 <= min <= max"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.prepare(t, KindChoiceGroupUpsert, tt.p)
			wantUserErr(t, err, tt.want)
		})
	}

	t.Run("upstream failure on update is not a user error", func(t *testing.T) {
		f.fail("McpProducts")
		defer f.unfail("McpProducts")
		_, err := f.prepare(t, KindChoiceGroupUpsert, ChoiceGroupParams{GroupID: "g-sauce", MinSelections: ip(0)})
		wantUpstreamErr(t, err)
	})
	t.Run("upstream failure on create is not a user error", func(t *testing.T) {
		f.fail("McpProduct")
		defer f.unfail("McpProduct")
		_, err := f.prepare(t, KindChoiceGroupUpsert, ChoiceGroupParams{ProductID: "p-maki-saumon", Names: map[string]string{"fr": "X"}})
		wantUpstreamErr(t, err)
	})
}

func TestChoiceGroupUpsertNoOp(t *testing.T) {
	f := newFixture(t)
	for name, p := range map[string]ChoiceGroupParams{
		"nothing asked":  {GroupID: "g-sauce"},
		"same values":    {GroupID: "g-sauce", Names: map[string]string{"fr": "Sauce", "zh": "酱汁"}, MinSelections: ip(1), MaxSelections: ip(1), SortOrder: ip(0)},
		"names trimmed":  {GroupID: "g-sauce", Names: map[string]string{"fr": " Sauce "}},
		"only same sort": {GroupID: "g-sauce", SortOrder: ip(0)},
	} {
		t.Run(name, func(t *testing.T) {
			pr, err := f.prepare(t, KindChoiceGroupUpsert, p)
			if err != nil || !pr.NoOp {
				t.Fatalf("want no-op, got %+v %v", pr, err)
			}
			contains(t, "summary", pr.Summary, "already has these settings", `"Sauce"`)
			contains(t, "summary_zh", pr.SummaryZh, "无需更改", "酱汁")
		})
	}
	pr, err := f.svc.Propose(f.ctx, "t", KindChoiceGroupUpsert, ChoiceGroupParams{GroupID: "g-sauce"}, nil, "", nil)
	if err != nil || !pr.NoOp || pr.ChangeID != "" {
		t.Fatalf("a no-op must not create a pending change: %+v %v", pr, err)
	}
}

func TestChoiceGroupUpdateAndUndo(t *testing.T) {
	f := newFixture(t)
	p := f.propose(t, KindChoiceGroupUpsert, ChoiceGroupParams{GroupID: "g-sauce", Names: map[string]string{"zh": "调味汁", "en": "Sauces"},
		MinSelections: ip(0), MaxSelections: ip(2), SortOrder: ip(3)})
	contains(t, "summary", p.Summary, `choice group "Sauce"`, `name (zh): "酱汁" -> "调味汁"`, `name (en): "" -> "Sauces"`,
		"min selections: 1 -> 0", "max selections: 1 -> 2", "position: 0 -> 3")
	contains(t, "summary_zh", p.SummaryZh, "最少选择：1 → 0", "最多选择：1 → 2", "排序位置：0 → 3")
	if g, _ := f.group("p-maki-box", "g-sauce"); g.Max != 1 {
		t.Fatal("proposal must not touch upstream")
	}
	if _, err := f.svc.ApplyPending(f.ctx, p.ChangeID, ""); err != nil {
		t.Fatal(err)
	}
	g, _ := f.group("p-maki-box", "g-sauce")
	if g.Min != 0 || g.Max != 2 || g.Sort != 3 || g.Names["zh"] != "调味汁" || g.Names["en"] != "Sauces" || g.Names["fr"] != "Sauce" {
		t.Fatalf("group after update: %+v", g)
	}

	// The English name did not exist before: it cannot be removed automatically.
	_, err := f.svc.Undo(f.ctx, "")
	wantUserErr(t, err, "added a new translation")
}

func TestChoiceGroupUndoRestoresPreviousSettings(t *testing.T) {
	f := newFixture(t)
	f.applyChange(t, KindChoiceGroupUpsert, ChoiceGroupParams{GroupID: "g-sauce", Names: map[string]string{"zh": "调味汁"}, MinSelections: ip(0), MaxSelections: ip(2), SortOrder: ip(3)})
	u, err := f.svc.Undo(f.ctx, "annule")
	if err != nil || u.Mode != "pending" || u.Proposal == nil {
		t.Fatalf("undo of a sensitive change must be pending: %+v %v", u, err)
	}
	if g, _ := f.group("p-maki-box", "g-sauce"); g.Max != 2 {
		t.Fatal("pending undo must not apply yet")
	}
	if _, err := f.svc.ApplyPending(f.ctx, u.Proposal.ChangeID, ""); err != nil {
		t.Fatal(err)
	}
	g, _ := f.group("p-maki-box", "g-sauce")
	if g.Min != 1 || g.Max != 1 || g.Sort != 0 || g.Names["zh"] != "酱汁" {
		t.Fatalf("undo did not restore the group: %+v", g)
	}
}

func TestChoiceGroupUndoKeepsPositionUntouchedWhenNotChanged(t *testing.T) {
	f := newFixture(t)
	// Only selections change: the inverse must not carry a sort order.
	_, inv, err := f.inverse(t, KindChoiceGroupUpsert, ChoiceGroupParams{GroupID: "g-sauce", MaxSelections: ip(2)},
		groupState{Exists: true, MinSelections: 1, MaxSelections: 1, SortOrder: 4}, "{}")
	if err != nil {
		t.Fatal(err)
	}
	got := inv.(ChoiceGroupParams)
	if got.SortOrder != nil || *got.MaxSelections != 1 || *got.MinSelections != 1 || got.Names != nil {
		t.Errorf("inverse = %+v", got)
	}
}

func TestChoiceGroupCreateAndUndo(t *testing.T) {
	f := newFixture(t)
	p := f.propose(t, KindChoiceGroupUpsert, ChoiceGroupParams{ProductID: "p-maki-saumon", Names: map[string]string{"fr": "Taille", "en": "Size"}})
	contains(t, "summary", p.Summary, "new choice group", "min selections: 1 -> 1", "max selections: 1 -> 1")
	contains(t, "summary_zh", p.SummaryZh, "新选项组")
	c, err := f.svc.ApplyPending(f.ctx, p.ChangeID, "")
	if err != nil || c.Status != changes.StatusApplied {
		t.Fatal(c, err)
	}
	prod := f.fake.ProductByID("p-maki-saumon")
	if len(prod.ChoiceGroups) != 1 {
		t.Fatalf("groups = %+v", prod.ChoiceGroups)
	}
	g := prod.ChoiceGroups[0]
	if g.MinSelections != 1 || g.MaxSelections != 1 || g.SortOrder != 0 || len(g.Translations) != 2 || g.Translations[0].Locale != "en" {
		t.Errorf("created group: %+v", g)
	}

	u, err := f.svc.Undo(f.ctx, "")
	if err != nil || u.Mode != "pending" {
		t.Fatalf("undo: %+v %v", u, err)
	}
	contains(t, "undo summary", u.Proposal.Summary, "delete choice group")
	if _, err := f.svc.ApplyPending(f.ctx, u.Proposal.ChangeID, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.group("p-maki-saumon", g.ID); ok {
		t.Error("undo of a creation must delete the group")
	}
}

func TestChoiceGroupCreateWithExplicitSettings(t *testing.T) {
	f := newFixture(t)
	// p-maki-box already has one group, so the default position is 1.
	f.applyChange(t, KindChoiceGroupUpsert, ChoiceGroupParams{ProductID: "p-maki-box", Names: map[string]string{"fr": "Extras"}, MinSelections: ip(0), MaxSelections: ip(3)})
	groups := f.fake.ProductByID("p-maki-box").ChoiceGroups
	if len(groups) != 2 || groups[1].SortOrder != 1 || groups[1].MinSelections != 0 || groups[1].MaxSelections != 3 {
		t.Fatalf("groups = %+v", groups)
	}
	f.applyChange(t, KindChoiceGroupUpsert, ChoiceGroupParams{ProductID: "p-maki-box", Names: map[string]string{"fr": "Dessert"}, SortOrder: ip(7)})
	groups = f.fake.ProductByID("p-maki-box").ChoiceGroups
	if groups[2].SortOrder != 7 || groups[2].MinSelections != 1 || groups[2].MaxSelections != 1 {
		t.Fatalf("explicit position/defaults: %+v", groups[2])
	}
}

func TestChoiceGroupExecuteDefaultsWhenCalledDirectly(t *testing.T) {
	f := newFixture(t)
	// Execute fills the documented defaults even if the params never went
	// through Prepare.
	out, err := f.execute(t, KindChoiceGroupUpsert, ChoiceGroupParams{ProductID: "p-maki-saumon", Names: map[string]string{"fr": "Taille"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	g, ok := f.group("p-maki-saumon", out.(createdID).ID)
	if !ok || g.Min != 1 || g.Max != 1 || g.Sort != 0 {
		t.Fatalf("defaults: %+v %v", g, ok)
	}
}

func TestChoiceGroupExecuteErrors(t *testing.T) {
	f := newFixture(t)
	_, err := f.execute(t, KindChoiceGroupUpsert, ChoiceGroupParams{GroupID: "g-nope"}, nil)
	wantUserErr(t, err, "No choice group")
	_, err = f.execute(t, KindChoiceGroupUpsert, ChoiceGroupParams{GroupID: "g-sauce", Names: map[string]string{"de": "x"}}, nil)
	wantUserErr(t, err, "Unknown language")

	f.fail("McpUpdateChoiceGroup")
	_, err = f.execute(t, KindChoiceGroupUpsert, ChoiceGroupParams{GroupID: "g-sauce", MaxSelections: ip(2)}, nil)
	wantUpstreamErr(t, err)
	f.fail("McpCreateChoiceGroup")
	_, err = f.execute(t, KindChoiceGroupUpsert, ChoiceGroupParams{ProductID: "p-maki-saumon", Names: map[string]string{"fr": "X"}}, nil)
	wantUpstreamErr(t, err)
}

func TestChoiceGroupApplyFailureIsRecorded(t *testing.T) {
	f := newFixture(t)
	p := f.propose(t, KindChoiceGroupUpsert, ChoiceGroupParams{GroupID: "g-sauce", MaxSelections: ip(2)})
	f.fail("McpUpdateChoiceGroup")
	c, err := f.svc.ApplyPending(f.ctx, p.ChangeID, "")
	wantUpstreamErr(t, err)
	if c.Status != changes.StatusFailed {
		t.Errorf("status = %s", c.Status)
	}
	if g, _ := f.group("p-maki-box", "g-sauce"); g.Max != 1 {
		t.Error("failed change must leave the group untouched")
	}
	audit, _ := f.svc.Store().RecentAudit(f.ctx, 1)
	if audit[0].Outcome != changes.OutcomeFailed || audit[0].Error == "" {
		t.Errorf("audit: %+v", audit[0])
	}
}

func TestChoiceGroupInverseErrors(t *testing.T) {
	f := newFixture(t)
	for _, after := range []string{"", "not json", `{"id":""}`, "null"} {
		_, _, err := f.inverse(t, KindChoiceGroupUpsert, ChoiceGroupParams{ProductID: "p"}, groupState{}, after)
		if !errors.Is(err, ErrNotUndoable) {
			t.Errorf("after %q: want ErrNotUndoable, got %v", after, err)
		}
	}
	kind, inv, err := f.inverse(t, KindChoiceGroupUpsert, ChoiceGroupParams{ProductID: "p"}, groupState{}, `{"id":"g-9"}`)
	if err != nil || kind != KindChoiceGroupDelete || inv.(ChoiceGroupRef).GroupID != "g-9" {
		t.Errorf("inverse of a creation: %v %v %v", kind, inv, err)
	}
	// Restoring names: a name that did not exist before cannot be removed.
	_, _, err = f.inverse(t, KindChoiceGroupUpsert, ChoiceGroupParams{GroupID: "g", Names: map[string]string{"en": "x"}}, groupState{Exists: true, Names: map[string]string{"fr": "a"}}, "{}")
	wantUserErr(t, err, "added a new translation")
	kind, inv, err = f.inverse(t, KindChoiceGroupUpsert, ChoiceGroupParams{GroupID: "g", Names: map[string]string{"fr": "b"}, SortOrder: ip(2)},
		groupState{Exists: true, Names: map[string]string{"fr": "a"}, SortOrder: 5, MinSelections: 0, MaxSelections: 4}, "{}")
	if err != nil || kind != KindChoiceGroupUpsert {
		t.Fatal(kind, err)
	}
	got := inv.(ChoiceGroupParams)
	if got.Names["fr"] != "a" || *got.SortOrder != 5 || *got.MinSelections != 0 || *got.MaxSelections != 4 {
		t.Errorf("inverse = %+v", got)
	}
}

func TestChoiceGroupDelete(t *testing.T) {
	f := newFixture(t)
	_, err := f.prepare(t, KindChoiceGroupDelete, ChoiceGroupRef{GroupID: "g-nope"})
	wantUserErr(t, err, "No choice group")

	p := f.propose(t, KindChoiceGroupDelete, ChoiceGroupRef{GroupID: "g-sauce"})
	contains(t, "summary", p.Summary, `delete choice group "Sauce" and its 2 choices (Soja, Mayo épicée)`, "cannot be undone")
	contains(t, "summary_zh", p.SummaryZh, "删除选项组「酱汁」", "2 个选项", "无法撤销")

	f.fail("McpDeleteChoiceGroup")
	c, err := f.svc.ApplyPending(f.ctx, p.ChangeID, "")
	wantUpstreamErr(t, err)
	if c.Status != changes.StatusFailed {
		t.Errorf("status = %s", c.Status)
	}
	f.unfail("McpDeleteChoiceGroup")

	f.applyChange(t, KindChoiceGroupDelete, ChoiceGroupRef{GroupID: "g-sauce"})
	if _, ok := f.group("p-maki-box", "g-sauce"); ok {
		t.Error("group not deleted")
	}
	_, err = f.svc.Undo(f.ctx, "")
	wantUserErr(t, err, "cannot be undone automatically")
}

func TestChoiceUpsertValidation(t *testing.T) {
	f := newFixture(t)
	tests := []struct {
		name string
		p    ChoiceParams
		want string
	}{
		{"unknown choice", ChoiceParams{ChoiceID: "c-nope"}, "No choice with id"},
		{"unknown group", ChoiceParams{GroupID: "g-nope", Names: map[string]string{"fr": "X"}}, "No choice group"},
		{"new choice without names", ChoiceParams{GroupID: "g-sauce"}, "French name"},
		{"new choice without french", ChoiceParams{GroupID: "g-sauce", Names: map[string]string{"en": "X"}}, "French name"},
		{"unknown language", ChoiceParams{ChoiceID: "c-soja", Names: map[string]string{"de": "X"}}, "Unknown language"},
		{"empty name", ChoiceParams{ChoiceID: "c-soja", Names: map[string]string{"fr": " "}}, "cannot be empty"},
		{"negative surcharge", ChoiceParams{ChoiceID: "c-soja", PriceModifierCents: i64(-1)}, "cannot be negative"},
		// c-spicy costs 0.50: +50% is 0.75.
		{"surcharge too high", ChoiceParams{ChoiceID: "c-spicy", PriceModifierCents: i64(76)}, "Surcharge change refused"},
		{"surcharge too low", ChoiceParams{ChoiceID: "c-spicy", PriceModifierCents: i64(24)}, "Surcharge change refused"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.prepare(t, KindChoiceUpsert, tt.p)
			wantUserErr(t, err, tt.want)
		})
	}
	_, err := f.prepare(t, KindChoiceUpsert, ChoiceParams{ChoiceID: "c-spicy", PriceModifierCents: i64(76)})
	contains(t, "bounds message", err.Error(), "between 0.25 EUR and 0.75 EUR")

	f.fail("McpProducts")
	_, err = f.prepare(t, KindChoiceUpsert, ChoiceParams{ChoiceID: "c-spicy"})
	wantUpstreamErr(t, err)
	_, err = f.prepare(t, KindChoiceUpsert, ChoiceParams{GroupID: "g-sauce", Names: map[string]string{"fr": "X"}})
	wantUpstreamErr(t, err)
}

func TestChoiceSurchargeBoundaries(t *testing.T) {
	f := newFixture(t)
	for _, cents := range []int64{25, 50, 75} {
		if _, err := f.prepare(t, KindChoiceUpsert, ChoiceParams{ChoiceID: "c-spicy", PriceModifierCents: i64(cents)}); err != nil {
			t.Errorf("%d cents must be allowed: %v", cents, err)
		}
	}
	// A free choice has no reference price: any surcharge may be set.
	if _, err := f.prepare(t, KindChoiceUpsert, ChoiceParams{ChoiceID: "c-soja", PriceModifierCents: i64(500)}); err != nil {
		t.Errorf("surcharge on a free choice: %v", err)
	}
	// Dropping a surcharge to free is not a percentage change.
	if _, err := f.prepare(t, KindChoiceUpsert, ChoiceParams{ChoiceID: "c-spicy", PriceModifierCents: i64(0)}); err != nil {
		t.Errorf("surcharge to free: %v", err)
	}
	// Undo may exceed the bounds.
	if _, err := f.prepare(t, KindChoiceUpsert, ChoiceParams{ChoiceID: "c-spicy", PriceModifierCents: i64(500), SkipBounds: true}); err != nil {
		t.Errorf("skip bounds: %v", err)
	}
}

func TestChoiceUpsertNoOp(t *testing.T) {
	f := newFixture(t)
	for name, p := range map[string]ChoiceParams{
		"nothing asked": {ChoiceID: "c-spicy"},
		"same values":   {ChoiceID: "c-spicy", Names: map[string]string{"fr": "Mayo épicée"}, PriceModifierCents: i64(50), SortOrder: ip(1)},
	} {
		t.Run(name, func(t *testing.T) {
			pr, err := f.prepare(t, KindChoiceUpsert, p)
			if err != nil || !pr.NoOp {
				t.Fatalf("want no-op, got %+v %v", pr, err)
			}
			contains(t, "summary", pr.Summary, "already has these settings", `choice "Mayo épicée"`)
			contains(t, "summary_zh", pr.SummaryZh, "无需更改")
		})
	}
}

func TestChoiceUpdateAndUndo(t *testing.T) {
	f := newFixture(t)
	// 0.50 -> 0.25 is -50%, within bounds; the undo goes back +100% and must
	// not be blocked by the guardrail.
	p := f.propose(t, KindChoiceUpsert, ChoiceParams{ChoiceID: "c-spicy", PriceModifierCents: i64(25), SortOrder: ip(4), Names: map[string]string{"fr": "Mayo forte"}})
	contains(t, "summary", p.Summary, `choice "Mayo épicée"`, `name (fr): "Mayo épicée" -> "Mayo forte"`, "surcharge: 0.50 EUR -> 0.25 EUR", "position: 1 -> 4")
	contains(t, "summary_zh", p.SummaryZh, "加价：0.50 欧元 → 0.25 欧元", "排序位置：1 → 4")
	if _, err := f.svc.ApplyPending(f.ctx, p.ChangeID, ""); err != nil {
		t.Fatal(err)
	}
	c := f.fake.ProductByID("p-maki-box").ChoiceGroups[0].Choices[1]
	if c.PriceModifier != "0.25" || c.SortOrder != 4 || c.Name != "Mayo forte" {
		t.Fatalf("choice after update: %+v", c)
	}

	u, err := f.svc.Undo(f.ctx, "")
	if err != nil || u.Mode != "pending" {
		t.Fatalf("undo: %+v %v", u, err)
	}
	if _, err := f.svc.ApplyPending(f.ctx, u.Proposal.ChangeID, ""); err != nil {
		t.Fatal(err)
	}
	c = f.fake.ProductByID("p-maki-box").ChoiceGroups[0].Choices[1]
	if c.PriceModifier != "0.5" || c.SortOrder != 1 || c.Name != "Mayo épicée" {
		t.Fatalf("choice after undo: %+v", c)
	}
}

func TestChoiceCreateAndUndo(t *testing.T) {
	f := newFixture(t)
	p := f.propose(t, KindChoiceUpsert, ChoiceParams{GroupID: "g-sauce", Names: map[string]string{"fr": "Wasabi", "zh": "芥末"}, PriceModifierCents: i64(100)})
	contains(t, "summary", p.Summary, `group "Sauce", new choice`, "surcharge: 0.00 EUR -> 1.00 EUR")
	contains(t, "summary_zh", p.SummaryZh, "中的新选项")
	f.applyChange(t, KindChoiceUpsert, ChoiceParams{GroupID: "g-sauce", Names: map[string]string{"fr": "Wasabi"}, PriceModifierCents: i64(100)})
	choices := f.fake.ProductByID("p-maki-box").ChoiceGroups[0].Choices
	if len(choices) != 3 || choices[2].Name != "Wasabi" || choices[2].PriceModifier != "1" || choices[2].SortOrder != 2 {
		t.Fatalf("choices = %+v", choices)
	}

	u, err := f.svc.Undo(f.ctx, "")
	if err != nil || u.Mode != "pending" {
		t.Fatalf("undo: %+v %v", u, err)
	}
	contains(t, "undo summary", u.Proposal.Summary, `delete choice "Wasabi"`)
	if _, err := f.svc.ApplyPending(f.ctx, u.Proposal.ChangeID, ""); err != nil {
		t.Fatal(err)
	}
	if got := len(f.fake.ProductByID("p-maki-box").ChoiceGroups[0].Choices); got != 2 {
		t.Errorf("choices after undo = %d", got)
	}
}

func TestChoiceCreateDefaults(t *testing.T) {
	f := newFixture(t)
	// No price and no position: free, appended at the end.
	pr, err := f.prepare(t, KindChoiceUpsert, ChoiceParams{GroupID: "g-sauce", Names: map[string]string{"fr": "Sésame"}})
	if err != nil || pr.NoOp {
		t.Fatal(pr, err)
	}
	f.applyChange(t, KindChoiceUpsert, ChoiceParams{GroupID: "g-sauce", Names: map[string]string{"fr": "Sésame"}, SortOrder: ip(9)})
	c := f.fake.ProductByID("p-maki-box").ChoiceGroups[0].Choices[2]
	if c.PriceModifier != "0" || c.SortOrder != 9 {
		t.Errorf("choice = %+v", c)
	}
	// Execute applies the documented defaults when called without Prepare.
	out, err := f.execute(t, KindChoiceUpsert, ChoiceParams{GroupID: "g-sauce", Names: map[string]string{"fr": "Gingembre"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.(createdID).ID == "" {
		t.Error("created id missing")
	}
	c = f.fake.ProductByID("p-maki-box").ChoiceGroups[0].Choices[3]
	if c.PriceModifier != "0" || c.SortOrder != 0 {
		t.Errorf("defaults: %+v", c)
	}
}

func TestChoiceExecuteErrors(t *testing.T) {
	f := newFixture(t)
	_, err := f.execute(t, KindChoiceUpsert, ChoiceParams{ChoiceID: "c-nope"}, nil)
	wantUserErr(t, err, "No choice with id")
	_, err = f.execute(t, KindChoiceUpsert, ChoiceParams{ChoiceID: "c-soja", Names: map[string]string{"de": "x"}}, nil)
	wantUserErr(t, err, "Unknown language")
	f.fail("McpUpdateChoice")
	_, err = f.execute(t, KindChoiceUpsert, ChoiceParams{ChoiceID: "c-soja", SortOrder: ip(3)}, nil)
	wantUpstreamErr(t, err)
	f.fail("McpCreateChoice")
	_, err = f.execute(t, KindChoiceUpsert, ChoiceParams{GroupID: "g-sauce", Names: map[string]string{"fr": "X"}}, nil)
	wantUpstreamErr(t, err)
}

func TestChoiceInverseErrors(t *testing.T) {
	f := newFixture(t)
	for _, after := range []string{"", "garbage", `{"id":""}`} {
		_, _, err := f.inverse(t, KindChoiceUpsert, ChoiceParams{GroupID: "g"}, choiceState{}, after)
		if !errors.Is(err, ErrNotUndoable) {
			t.Errorf("after %q: want ErrNotUndoable, got %v", after, err)
		}
	}
	kind, inv, err := f.inverse(t, KindChoiceUpsert, ChoiceParams{GroupID: "g"}, choiceState{}, `{"id":"c-9"}`)
	if err != nil || kind != KindChoiceDelete || inv.(ChoiceRef).ChoiceID != "c-9" {
		t.Errorf("inverse of a creation: %v %v %v", kind, inv, err)
	}
	_, _, err = f.inverse(t, KindChoiceUpsert, ChoiceParams{ChoiceID: "c", Names: map[string]string{"zh": "x"}}, choiceState{Exists: true, Names: map[string]string{"fr": "a"}}, "{}")
	wantUserErr(t, err, "added a new translation")

	kind, inv, err = f.inverse(t, KindChoiceUpsert, ChoiceParams{ChoiceID: "c", Names: map[string]string{"fr": "b"}, PriceModifierCents: i64(1), SortOrder: ip(1)},
		choiceState{Exists: true, Names: map[string]string{"fr": "a"}, PriceModifierCents: 50, SortOrder: 3}, "{}")
	if err != nil || kind != KindChoiceUpsert {
		t.Fatal(kind, err)
	}
	got := inv.(ChoiceParams)
	if !got.SkipBounds || got.Names["fr"] != "a" || *got.PriceModifierCents != 50 || *got.SortOrder != 3 || got.ChoiceID != "c" {
		t.Errorf("inverse = %+v", got)
	}
	// Only the fields that were changed are restored.
	_, inv, _ = f.inverse(t, KindChoiceUpsert, ChoiceParams{ChoiceID: "c", SortOrder: ip(1)}, choiceState{Exists: true, PriceModifierCents: 50, SortOrder: 3}, "{}")
	got = inv.(ChoiceParams)
	if got.PriceModifierCents != nil || got.Names != nil || *got.SortOrder != 3 {
		t.Errorf("partial inverse = %+v", got)
	}
}

func TestChoiceDelete(t *testing.T) {
	f := newFixture(t)
	_, err := f.prepare(t, KindChoiceDelete, ChoiceRef{ChoiceID: "c-nope"})
	wantUserErr(t, err, "No choice with id")

	p := f.propose(t, KindChoiceDelete, ChoiceRef{ChoiceID: "c-spicy"})
	contains(t, "summary", p.Summary, `delete choice "Mayo épicée" from group "Sauce"`, "cannot be undone")
	contains(t, "summary_zh", p.SummaryZh, "删除选项", "无法撤销")

	f.fail("McpDeleteChoice")
	c, err := f.svc.ApplyPending(f.ctx, p.ChangeID, "")
	wantUpstreamErr(t, err)
	if c.Status != changes.StatusFailed {
		t.Errorf("status = %s", c.Status)
	}
	f.unfail("McpDeleteChoice")

	f.applyChange(t, KindChoiceDelete, ChoiceRef{ChoiceID: "c-spicy"})
	choices := f.fake.ProductByID("p-maki-box").ChoiceGroups[0].Choices
	if len(choices) != 1 || choices[0].ID != "c-soja" {
		t.Errorf("choices = %+v", choices)
	}
	_, err = f.svc.Undo(f.ctx, "")
	wantUserErr(t, err, "cannot be undone automatically")
}

func TestChoiceNamesSortedInSummaries(t *testing.T) {
	// Summaries list name changes in language order, whatever the map order.
	f := newFixture(t)
	p := f.propose(t, KindChoiceUpsert, ChoiceParams{ChoiceID: "c-soja", Names: map[string]string{"zh": "酱油", "en": "Soy", "nl": "Soja NL"}})
	en, nl, zh := strings.Index(p.Summary, "(en)"), strings.Index(p.Summary, "(nl)"), strings.Index(p.Summary, "(zh)")
	if en < 0 || en >= nl || nl >= zh {
		t.Errorf("summary order: %s", p.Summary)
	}
}
