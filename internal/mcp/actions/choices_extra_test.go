package actions

import (
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"tsb-service/internal/mcp/changes"
	"tsb-service/internal/mcp/upstream"
)

// --- helpers --------------------------------------------------------------------

// lastCall returns the variables of the last upstream call of op.
func (f *fixture) lastCall(t *testing.T, op string) map[string]any {
	t.Helper()
	f.fake.Lock()
	defer f.fake.Unlock()
	for i := len(f.fake.Calls) - 1; i >= 0; i-- {
		if f.fake.Calls[i].Op == op {
			return f.fake.Calls[i].Vars
		}
	}
	t.Fatalf("no upstream call %s", op)
	return nil
}

// writes lists the mutating upstream operations recorded so far.
func (f *fixture) writes() []string {
	var out []string
	for _, op := range f.fake.Ops() {
		if strings.Contains(upstream.Documents[op], "mutation ") {
			out = append(out, op)
		}
	}
	return out
}

// conflicted applies a pending change and expects a clean conflict: the change
// is marked as conflicting and nothing was written upstream.
func (f *fixture) conflicted(t *testing.T, id string, wantInMsg string) {
	t.Helper()
	before := len(f.writes())
	c, err := f.svc.ApplyPending(f.ctx, id, "")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("want a conflict, got %v", err)
	}
	if c.Status != changes.StatusConflict {
		t.Errorf("status = %s", c.Status)
	}
	if wantInMsg != "" && !strings.Contains(err.Error(), wantInMsg) {
		t.Errorf("conflict %q does not mention %q", err.Error(), wantInMsg)
	}
	if got := len(f.writes()); got != before {
		t.Errorf("a stale change wrote upstream: %v", f.writes())
	}
}

// addProductWithChoices adds a second product holding a group that reuses the
// same group and choice names as p-maki-box (a menu repeats "Sauce"/"Soja").
func (f *fixture) addSecondBox() {
	f.fake.Lock()
	defer f.fake.Unlock()
	cat := f.fake.Products[0].Category
	f.fake.Products = append(f.fake.Products, &upstream.Product{
		ID: "p-other-box", Slug: "other-box", Name: "Other box", Price: "9", VatCategory: "food", IsAvailable: true, IsVisible: true,
		Category: cat, Translations: []upstream.Translation{{Language: "fr", Name: "Other box"}, {Language: "zh", Name: "另一个套餐"}},
		ChoiceGroups: []upstream.ChoiceGroup{{ID: "g-other-sauce", ProductID: "p-other-box", MinSelections: 0, MaxSelections: 3, Name: "Sauce",
			Translations: []upstream.ChoiceTranslation{{Locale: "fr", Name: "Sauce"}, {Locale: "zh", Name: "酱汁"}},
			Choices: []upstream.Choice{
				{ID: "c-other-soja", ChoiceGroupID: "g-other-sauce", PriceModifier: "0", Name: "Soja", Translations: []upstream.ChoiceTranslation{{Locale: "fr", Name: "Soja"}, {Locale: "zh", Name: "酱油"}}},
			}}}})
}

// --- stale proposals ---------------------------------------------------------------

// A confirmation can arrive minutes after the proposal. When the choice or
// group moved in between, the change must fail as a conflict, never write.
func TestChoiceStaleProposals(t *testing.T) {
	tests := []struct {
		name   string
		kind   string
		params any
		drift  func(f *fixture)
		want   string
	}{
		{"update of a deleted choice", KindChoiceUpsert, ChoiceParams{ChoiceID: "c-spicy", PriceModifierCents: i64(60)},
			func(f *fixture) { f.applyChange(t, KindChoiceDelete, ChoiceRef{ChoiceID: "c-spicy"}) }, "No choice with id"},
		{"deletion of an already deleted choice", KindChoiceDelete, ChoiceRef{ChoiceID: "c-spicy"},
			func(f *fixture) { f.applyChange(t, KindChoiceDelete, ChoiceRef{ChoiceID: "c-spicy"}) }, "No choice with id"},
		{"choice surcharge changed meanwhile", KindChoiceUpsert, ChoiceParams{ChoiceID: "c-spicy", PriceModifierCents: i64(60)},
			func(f *fixture) {
				f.applyChange(t, KindChoiceUpsert, ChoiceParams{ChoiceID: "c-spicy", PriceModifierCents: i64(70)})
			}, "price_modifier_cents"},
		{"choice renamed meanwhile", KindChoiceUpsert, ChoiceParams{ChoiceID: "c-spicy", PriceModifierCents: i64(60)},
			func(f *fixture) {
				f.applyChange(t, KindChoiceUpsert, ChoiceParams{ChoiceID: "c-spicy", Names: map[string]string{"fr": "Mayo forte"}})
			}, "names"},
		{"choice deletion after a surcharge change", KindChoiceDelete, ChoiceRef{ChoiceID: "c-spicy"},
			func(f *fixture) {
				f.applyChange(t, KindChoiceUpsert, ChoiceParams{ChoiceID: "c-spicy", PriceModifierCents: i64(70)})
			}, ""},
		{"new choice in a deleted group", KindChoiceUpsert, ChoiceParams{GroupID: "g-sauce", Names: map[string]string{"fr": "Wasabi"}},
			func(f *fixture) { f.applyChange(t, KindChoiceGroupDelete, ChoiceGroupRef{GroupID: "g-sauce"}) }, "No choice group"},
		{"group update after its deletion", KindChoiceGroupUpsert, ChoiceGroupParams{GroupID: "g-sauce", MaxSelections: ip(2)},
			func(f *fixture) { f.applyChange(t, KindChoiceGroupDelete, ChoiceGroupRef{GroupID: "g-sauce"}) }, "No choice group"},
		{"group deleted twice", KindChoiceGroupDelete, ChoiceGroupRef{GroupID: "g-sauce"},
			func(f *fixture) { f.applyChange(t, KindChoiceGroupDelete, ChoiceGroupRef{GroupID: "g-sauce"}) }, "No choice group"},
		{"group max changed meanwhile", KindChoiceGroupUpsert, ChoiceGroupParams{GroupID: "g-sauce", MaxSelections: ip(2)},
			func(f *fixture) {
				f.applyChange(t, KindChoiceGroupUpsert, ChoiceGroupParams{GroupID: "g-sauce", MaxSelections: ip(3)})
			}, "max_selections"},
		{"group min changed meanwhile", KindChoiceGroupUpsert, ChoiceGroupParams{GroupID: "g-sauce", MaxSelections: ip(2)},
			func(f *fixture) {
				f.applyChange(t, KindChoiceGroupUpsert, ChoiceGroupParams{GroupID: "g-sauce", MinSelections: ip(0)})
			}, "min_selections"},
		// The owner confirmed deleting a group of two choices: a third one added
		// meanwhile must not be wiped silently.
		{"group deletion after a choice was added", KindChoiceGroupDelete, ChoiceGroupRef{GroupID: "g-sauce"},
			func(f *fixture) {
				f.applyChange(t, KindChoiceUpsert, ChoiceParams{GroupID: "g-sauce", Names: map[string]string{"fr": "Wasabi"}})
			}, "choices"},
		{"group deletion after a choice was removed", KindChoiceGroupDelete, ChoiceGroupRef{GroupID: "g-sauce"},
			func(f *fixture) { f.applyChange(t, KindChoiceDelete, ChoiceRef{ChoiceID: "c-soja"}) }, "choices"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			p := f.propose(t, tt.kind, tt.params)
			tt.drift(f)
			f.conflicted(t, p.ChangeID, tt.want)
		})
	}
}

func TestChoiceStaleProposalSurvivesUnrelatedChanges(t *testing.T) {
	f := newFixture(t)
	p := f.propose(t, KindChoiceUpsert, ChoiceParams{ChoiceID: "c-spicy", PriceModifierCents: i64(60)})
	// Another choice, another group and the product price move: this choice is
	// untouched, the proposal stays valid.
	f.applyChange(t, KindChoiceUpsert, ChoiceParams{ChoiceID: "c-soja", PriceModifierCents: i64(10)})
	f.applyChange(t, KindChoiceGroupUpsert, ChoiceGroupParams{ProductID: "p-maki-box", Names: map[string]string{"fr": "Boisson"}})
	if _, err := f.svc.ApplyPending(f.ctx, p.ChangeID, ""); err != nil {
		t.Fatalf("unrelated changes must not stale the proposal: %v", err)
	}
	if got := f.fake.ProductByID("p-maki-box").ChoiceGroups[0].Choices[1].PriceModifier; got != "0.6" {
		t.Errorf("surcharge = %s", got)
	}
}

func TestChoiceStalePendingUndo(t *testing.T) {
	f := newFixture(t)
	f.applyChange(t, KindChoiceUpsert, ChoiceParams{ChoiceID: "c-spicy", PriceModifierCents: i64(70)})
	u, err := f.svc.Undo(f.ctx, "")
	if err != nil || u.Mode != "pending" {
		t.Fatalf("undo: %+v %v", u, err)
	}
	// The choice is deleted before the undo is confirmed.
	f.applyChange(t, KindChoiceDelete, ChoiceRef{ChoiceID: "c-spicy"})
	f.conflicted(t, u.Proposal.ChangeID, "No choice with id")
}

// --- the exact upstream input -------------------------------------------------------

func TestChoiceApplyUpstreamInputs(t *testing.T) {
	t.Run("surcharge only", func(t *testing.T) {
		f := newFixture(t)
		f.applyChange(t, KindChoiceUpsert, ChoiceParams{ChoiceID: "c-spicy", PriceModifierCents: i64(70)})
		v := f.lastCall(t, "McpUpdateChoice")
		in, _ := v["input"].(map[string]any)
		if v["id"] != "c-spicy" || in["priceModifier"] != "0.70" || len(in) != 1 {
			t.Errorf("only the surcharge may be sent: %v", v)
		}
	})
	t.Run("rename merges the other languages", func(t *testing.T) {
		f := newFixture(t)
		f.applyChange(t, KindChoiceUpsert, ChoiceParams{ChoiceID: "c-soja", Names: map[string]string{"zh": "酱油", "en": "Soy"}})
		c := f.fake.ProductByID("p-maki-box").ChoiceGroups[0].Choices[0]
		if got := choiceNames(c.Translations); len(got) != 3 || got["fr"] != "Soja" || got["zh"] != "酱油" || got["en"] != "Soy" {
			t.Errorf("translations = %v", got)
		}
		in := f.lastCall(t, "McpUpdateChoice")["input"].(map[string]any)
		if _, sent := in["priceModifier"]; sent {
			t.Errorf("a rename must not resend the surcharge: %v", in)
		}
	})
	t.Run("selections only", func(t *testing.T) {
		f := newFixture(t)
		f.applyChange(t, KindChoiceGroupUpsert, ChoiceGroupParams{GroupID: "g-sauce", MaxSelections: ip(2)})
		in := f.lastCall(t, "McpUpdateChoiceGroup")["input"].(map[string]any)
		if in["maxSelections"] != float64(2) || len(in) != 1 {
			t.Errorf("only maxSelections may be sent: %v", in)
		}
		g, _ := f.group("p-maki-box", "g-sauce")
		if g.Min != 1 || g.Names["zh"] != "酱汁" || g.Names["fr"] != "Sauce" {
			t.Errorf("untouched fields changed: %+v", g)
		}
	})
	t.Run("creation sends product, defaults and translations", func(t *testing.T) {
		f := newFixture(t)
		f.applyChange(t, KindChoiceGroupUpsert, ChoiceGroupParams{ProductID: "p-maki-saumon", Names: map[string]string{"fr": " Taille ", "zh": "大小"}, MinSelections: ip(0), MaxSelections: ip(1)})
		in := f.lastCall(t, "McpCreateChoiceGroup")["input"].(map[string]any)
		if in["productId"] != "p-maki-saumon" || in["minSelections"] != float64(0) || in["maxSelections"] != float64(1) || in["sortOrder"] != float64(0) {
			t.Errorf("input = %v", in)
		}
		g := f.fake.ProductByID("p-maki-saumon").ChoiceGroups[0]
		if got := choiceNames(g.Translations); got["fr"] != "Taille" || got["zh"] != "大小" {
			t.Errorf("names must be trimmed: %v", got)
		}
	})
	t.Run("a choice is created in the right group", func(t *testing.T) {
		f := newFixture(t)
		f.addSecondBox()
		f.applyChange(t, KindChoiceUpsert, ChoiceParams{GroupID: "g-other-sauce", Names: map[string]string{"fr": "Ponzu"}, PriceModifierCents: i64(30)})
		if got := len(f.fake.ProductByID("p-maki-box").ChoiceGroups[0].Choices); got != 2 {
			t.Errorf("the other product's group grew: %d", got)
		}
		cs := f.fake.ProductByID("p-other-box").ChoiceGroups[0].Choices
		if len(cs) != 2 || cs[1].Name != "Ponzu" || cs[1].PriceModifier != "0.3" || cs[1].SortOrder != 1 {
			t.Errorf("choices = %+v", cs)
		}
	})
}

func TestChoiceChangesNeverTouchTheProductPrice(t *testing.T) {
	f := newFixture(t)
	steps := []struct {
		kind   string
		params any
	}{
		{KindChoiceUpsert, ChoiceParams{ChoiceID: "c-spicy", PriceModifierCents: i64(70)}},
		{KindChoiceUpsert, ChoiceParams{GroupID: "g-sauce", Names: map[string]string{"fr": "Wasabi"}, PriceModifierCents: i64(300)}},
		{KindChoiceGroupUpsert, ChoiceGroupParams{GroupID: "g-sauce", MaxSelections: ip(2)}},
		{KindChoiceGroupUpsert, ChoiceGroupParams{ProductID: "p-maki-box", Names: map[string]string{"fr": "Boisson"}}},
		{KindChoiceDelete, ChoiceRef{ChoiceID: "c-soja"}},
		{KindChoiceGroupDelete, ChoiceGroupRef{GroupID: "g-sauce"}},
	}
	for _, s := range steps {
		f.applyChange(t, s.kind, s.params)
	}
	if p := f.fake.ProductByID("p-maki-box"); p.Price != "13.9" || !p.IsAvailable || !p.IsVisible {
		t.Errorf("product after choice changes: price %s available %v visible %v", p.Price, p.IsAvailable, p.IsVisible)
	}
	for _, op := range f.writes() {
		if strings.HasPrefix(op, "McpUpdateProduct") || op == "McpCreateProduct" {
			t.Errorf("a choice change called %s", op)
		}
	}
}

// --- validation ------------------------------------------------------------------------

func TestChoiceGroupSelectionRules(t *testing.T) {
	f := newFixture(t)
	ok := []struct {
		name string
		p    ChoiceGroupParams
	}{
		{"optional single pick", ChoiceGroupParams{GroupID: "g-sauce", MinSelections: ip(0)}},
		{"required single pick, moved", ChoiceGroupParams{GroupID: "g-sauce", MinSelections: ip(1), MaxSelections: ip(1), SortOrder: ip(2)}},
		{"optional up to three", ChoiceGroupParams{GroupID: "g-sauce", MinSelections: ip(0), MaxSelections: ip(3)}},
		{"at least five, at most eight", ChoiceGroupParams{GroupID: "g-sauce", MinSelections: ip(5), MaxSelections: ip(8)}},
		{"exactly five", ChoiceGroupParams{GroupID: "g-sauce", MinSelections: ip(5), MaxSelections: ip(5)}},
		{"large max", ChoiceGroupParams{GroupID: "g-sauce", MaxSelections: ip(1000)}},
		{"new composer", ChoiceGroupParams{ProductID: "p-maki-saumon", Names: map[string]string{"fr": "Ingrédients"}, MinSelections: ip(5), MaxSelections: ip(8)}},
		{"new optional group", ChoiceGroupParams{ProductID: "p-maki-saumon", Names: map[string]string{"fr": "Extras"}, MinSelections: ip(0), MaxSelections: ip(4)}},
		{"new group with all four languages", ChoiceGroupParams{ProductID: "p-maki-saumon", Names: map[string]string{"fr": "Taille", "en": "Size", "nl": "Maat", "zh": "大小"}}},
	}
	for _, tt := range ok {
		t.Run("ok/"+tt.name, func(t *testing.T) {
			pr, err := f.prepare(t, KindChoiceGroupUpsert, tt.p)
			if err != nil || pr.NoOp {
				t.Fatalf("want a real change, got %+v %v", pr, err)
			}
		})
	}
	bad := []struct {
		name string
		p    ChoiceGroupParams
	}{
		{"min above the current max", ChoiceGroupParams{GroupID: "g-sauce", MinSelections: ip(2)}},
		{"min above max", ChoiceGroupParams{GroupID: "g-sauce", MinSelections: ip(4), MaxSelections: ip(3)}},
		{"max below one", ChoiceGroupParams{GroupID: "g-sauce", MaxSelections: ip(0)}},
		{"negative max", ChoiceGroupParams{GroupID: "g-sauce", MaxSelections: ip(-2)}},
		{"negative min", ChoiceGroupParams{GroupID: "g-sauce", MinSelections: ip(-1), MaxSelections: ip(2)}},
		// A new group starts at 1/1, so asking for a minimum alone is refused.
		{"new group, min alone above the default max", ChoiceGroupParams{ProductID: "p-maki-saumon", Names: map[string]string{"fr": "X"}, MinSelections: ip(5)}},
		{"new group, max zero", ChoiceGroupParams{ProductID: "p-maki-saumon", Names: map[string]string{"fr": "X"}, MaxSelections: ip(0)}},
	}
	for _, tt := range bad {
		t.Run("refused/"+tt.name, func(t *testing.T) {
			_, err := f.prepare(t, KindChoiceGroupUpsert, tt.p)
			wantUserErr(t, err, "0 <= min <= max")
		})
	}
	// The refusal explains itself with the values it received.
	_, err := f.prepare(t, KindChoiceGroupUpsert, ChoiceGroupParams{GroupID: "g-sauce", MinSelections: ip(4), MaxSelections: ip(3)})
	contains(t, "message", err.Error(), "min 4", "max 3")
}

func TestChoiceGroupRequiredAndOptionalSummaries(t *testing.T) {
	f := newFixture(t)
	p := f.propose(t, KindChoiceGroupUpsert, ChoiceGroupParams{GroupID: "g-sauce", MinSelections: ip(0)})
	contains(t, "optional", p.Summary, "min selections: 1 -> 0")
	contains(t, "optional zh", p.SummaryZh, "最少选择：1 → 0")
	if strings.Contains(p.Summary, "max selections") {
		t.Errorf("the max did not change: %s", p.Summary)
	}
	p = f.propose(t, KindChoiceGroupUpsert, ChoiceGroupParams{GroupID: "g-sauce", MinSelections: ip(1), MaxSelections: ip(3)})
	contains(t, "max only", p.Summary, "max selections: 1 -> 3")
	if strings.Contains(p.Summary, "min selections") {
		t.Errorf("the min did not change: %s", p.Summary)
	}
}

func TestChoiceUpsertValueRules(t *testing.T) {
	f := newFixture(t)
	for _, tt := range []struct {
		name  string
		p     ChoiceParams
		wantE string
	}{
		{"positive surcharge on a free choice", ChoiceParams{ChoiceID: "c-soja", PriceModifierCents: i64(1)}, ""},
		{"surcharge back to free", ChoiceParams{ChoiceID: "c-spicy", PriceModifierCents: i64(0)}, ""},
		{"free new choice", ChoiceParams{GroupID: "g-sauce", Names: map[string]string{"fr": "Nature"}, PriceModifierCents: i64(0)}, ""},
		{"new choice with a surcharge", ChoiceParams{GroupID: "g-sauce", Names: map[string]string{"fr": "Truffe"}, PriceModifierCents: i64(250)}, ""},
		{"new choice without a surcharge", ChoiceParams{GroupID: "g-sauce", Names: map[string]string{"fr": "Nature"}}, ""},
		{"negative on a new choice", ChoiceParams{GroupID: "g-sauce", Names: map[string]string{"fr": "Remise"}, PriceModifierCents: i64(-50)}, "cannot be negative"},
		{"hugely negative", ChoiceParams{ChoiceID: "c-soja", PriceModifierCents: i64(math.MinInt64)}, "cannot be negative"},
		{"rename in zh only", ChoiceParams{ChoiceID: "c-soja", Names: map[string]string{"zh": "酱油"}}, ""},
		{"blank french name on a new choice", ChoiceParams{GroupID: "g-sauce", Names: map[string]string{"fr": "\t", "zh": "芥末"}}, "French name"},
		{"blank zh name on an existing choice", ChoiceParams{ChoiceID: "c-soja", Names: map[string]string{"zh": ""}}, "cannot be empty"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pr, err := f.prepare(t, KindChoiceUpsert, tt.p)
			if tt.wantE != "" {
				wantUserErr(t, err, tt.wantE)
				return
			}
			if err != nil || pr.NoOp {
				t.Fatalf("want a real change, got %+v %v", pr, err)
			}
		})
	}
}

// Every surcharge a user can type must either be refused cleanly or applied,
// never panic or wrap around: this includes values far beyond a price.
func TestChoiceSurchargeExtremeValues(t *testing.T) {
	f := newFixture(t)
	for _, cents := range []int64{math.MaxInt64, math.MaxInt64 - 1, 1 << 62, 999999999999, 100000000} {
		pr, err := f.prepare(t, KindChoiceUpsert, ChoiceParams{ChoiceID: "c-soja", PriceModifierCents: i64(cents)})
		if err != nil {
			t.Errorf("%d cents on a free choice: %v", cents, err)
			continue
		}
		if !strings.Contains(pr.Summary, "surcharge: 0.00 EUR -> ") {
			t.Errorf("%d cents: summary %q", cents, pr.Summary)
		}
		// A priced choice cannot jump to such a value: the percentage guard holds.
		_, err = f.prepare(t, KindChoiceUpsert, ChoiceParams{ChoiceID: "c-spicy", PriceModifierCents: i64(cents)})
		wantUserErr(t, err, "Surcharge change refused")
	}
}

func TestChoiceSurchargeRoundingBounds(t *testing.T) {
	f := newFixture(t)
	// Make the reference surcharge 0.33 so +/-50% falls between cents.
	f.applyChange(t, KindChoiceUpsert, ChoiceParams{ChoiceID: "c-spicy", PriceModifierCents: i64(33), SkipBounds: true})
	for _, cents := range []int64{17, 33, 49} {
		if _, err := f.prepare(t, KindChoiceUpsert, ChoiceParams{ChoiceID: "c-spicy", PriceModifierCents: i64(cents)}); err != nil {
			t.Errorf("%d cents must be allowed from 33: %v", cents, err)
		}
	}
	for _, cents := range []int64{9, 16, 50, 51, 52, 100} {
		if _, err := f.prepare(t, KindChoiceUpsert, ChoiceParams{ChoiceID: "c-spicy", PriceModifierCents: i64(cents)}); err == nil {
			t.Errorf("%d cents must be refused from 33", cents)
		}
	}
}

func TestChoiceNoOpVariants(t *testing.T) {
	f := newFixture(t)
	for name, p := range map[string]ChoiceGroupParams{
		"same selections":   {GroupID: "g-sauce", MinSelections: ip(1), MaxSelections: ip(1)},
		"same names":        {GroupID: "g-sauce", Names: map[string]string{"fr": "Sauce", "zh": "酱汁"}},
		"names with spaces": {GroupID: "g-sauce", Names: map[string]string{"fr": "  Sauce  "}},
		"same position":     {GroupID: "g-sauce", SortOrder: ip(0)},
	} {
		t.Run("group/"+name, func(t *testing.T) {
			pr, err := f.prepare(t, KindChoiceGroupUpsert, p)
			if err != nil || !pr.NoOp {
				t.Fatalf("want a no-op, got %+v %v", pr, err)
			}
			contains(t, "summary_zh", pr.SummaryZh, "套餐「卷寿司套餐」的选项组「酱汁」", "无需更改")
		})
	}
	// A no-op proposal is never confirmable and writes nothing.
	p, err := f.svc.Propose(f.ctx, "t", KindChoiceUpsert, ChoiceParams{ChoiceID: "c-spicy", PriceModifierCents: i64(50)}, nil, "", nil)
	if err != nil || !p.NoOp || p.ChangeID != "" {
		t.Fatalf("no-op proposal: %+v %v", p, err)
	}
	if len(f.writes()) != 0 {
		t.Errorf("writes = %v", f.writes())
	}
}

// --- summaries ---------------------------------------------------------------------------

// The owner reads the Chinese summary: a product is always its category plus
// its name, and so is every choice summary.
func TestChoiceSummariesNameCategoryAndProduct(t *testing.T) {
	f := newFixture(t)
	const prodFr = `Boxes "Maki box" (套餐「卷寿司套餐」)`
	const prodZh = "套餐「卷寿司套餐」"
	tests := []struct {
		name   string
		kind   string
		params any
		en, zh []string
	}{
		{"group update", KindChoiceGroupUpsert, ChoiceGroupParams{GroupID: "g-sauce", MaxSelections: ip(2)},
			[]string{prodFr + `, choice group "Sauce": max selections: 1 -> 2`}, []string{prodZh + "的选项组「酱汁」：最多选择：1 → 2"}},
		{"group rename", KindChoiceGroupUpsert, ChoiceGroupParams{GroupID: "g-sauce", Names: map[string]string{"zh": "蘸酱"}},
			[]string{`name (zh): "酱汁" -> "蘸酱"`}, []string{prodZh + "的选项组「酱汁」：中文名称：「酱汁」 → 「蘸酱」"}},
		{"group creation", KindChoiceGroupUpsert, ChoiceGroupParams{ProductID: "p-maki-box", Names: map[string]string{"fr": "Dessert", "zh": "甜点"}, MinSelections: ip(0), MaxSelections: ip(1)},
			[]string{prodFr + ", new choice group: ", `name (fr): "" -> "Dessert"`, "min selections: 1 -> 0", "max selections: 1 -> 1"}, []string{prodZh + "的新选项组：", "最少选择：1 → 0"}},
		{"group deletion", KindChoiceGroupDelete, ChoiceGroupRef{GroupID: "g-sauce"},
			[]string{prodFr + `: delete choice group "Sauce" and its 2 choices (Soja, Mayo épicée). This cannot be undone.`},
			[]string{prodZh + "：删除选项组「酱汁」及其 2 个选项（Soja、Mayo épicée）。此操作无法撤销。"}},
		{"choice surcharge", KindChoiceUpsert, ChoiceParams{ChoiceID: "c-spicy", PriceModifierCents: i64(70)},
			[]string{prodFr + `, choice "Mayo épicée": surcharge: 0.50 EUR -> 0.70 EUR`}, []string{prodZh + "的选项「Mayo épicée」：加价：0.50 欧元 → 0.70 欧元"}},
		{"choice rename in zh", KindChoiceUpsert, ChoiceParams{ChoiceID: "c-soja", Names: map[string]string{"zh": "酱油"}},
			[]string{`choice "Soja": name (zh): "" -> "酱油"`}, []string{prodZh + "的选项「Soja」：中文名称：无 → 「酱油」"}},
		{"choice creation", KindChoiceUpsert, ChoiceParams{GroupID: "g-sauce", Names: map[string]string{"fr": "Wasabi", "zh": "芥末"}, PriceModifierCents: i64(100)},
			[]string{prodFr + `, group "Sauce", new choice: `, "surcharge: 0.00 EUR -> 1.00 EUR"}, []string{prodZh + "的选项组「酱汁」中的新选项：", "加价：0.00 欧元 → 1.00 欧元"}},
		{"choice deletion", KindChoiceDelete, ChoiceRef{ChoiceID: "c-spicy"},
			[]string{prodFr + `: delete choice "Mayo épicée" from group "Sauce". This cannot be undone.`},
			[]string{prodZh + "：从选项组「酱汁」中删除选项「Mayo épicée」。此操作无法撤销。"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := f.propose(t, tt.kind, tt.params)
			contains(t, "summary", p.Summary, tt.en...)
			contains(t, "summary_zh", p.SummaryZh, tt.zh...)
		})
	}
}

// A product that is only named in French still gets a readable Chinese
// summary: no empty quotes.
func TestChoiceSummariesWithoutChineseNames(t *testing.T) {
	f := newFixture(t)
	f.fake.Lock()
	p := f.fake.Products[2]
	p.Translations = []upstream.Translation{{Language: "fr", Name: "Maki box"}}
	p.ChoiceGroups[0].Translations = []upstream.ChoiceTranslation{{Locale: "fr", Name: "Sauce"}}
	f.fake.Unlock()
	pr := f.propose(t, KindChoiceUpsert, ChoiceParams{ChoiceID: "c-spicy", PriceModifierCents: i64(70)})
	if strings.Contains(pr.SummaryZh, "「」") || strings.Contains(pr.Summary, `""`) {
		t.Errorf("empty name rendered: %q / %q", pr.Summary, pr.SummaryZh)
	}
	contains(t, "summary_zh", pr.SummaryZh, "套餐", "Maki box")
	pr = f.propose(t, KindChoiceGroupDelete, ChoiceGroupRef{GroupID: "g-sauce"})
	contains(t, "group deletion zh", pr.SummaryZh, "「Sauce」", "Soja、Mayo épicée")
}

func TestChoiceSummariesSameNamesInTwoProducts(t *testing.T) {
	f := newFixture(t)
	f.addSecondBox()
	a := f.propose(t, KindChoiceDelete, ChoiceRef{ChoiceID: "c-soja"})
	b := f.propose(t, KindChoiceDelete, ChoiceRef{ChoiceID: "c-other-soja"})
	contains(t, "first", a.SummaryZh, "「卷寿司套餐」")
	contains(t, "second", b.SummaryZh, "「另一个套餐」", "「酱油」")
	if strings.Contains(b.SummaryZh, "卷寿司套餐") || strings.Contains(a.SummaryZh, "另一个套餐") {
		t.Errorf("summaries mixed products up:\n%s\n%s", a.SummaryZh, b.SummaryZh)
	}
	// Each id deletes exactly its own choice.
	f.applyChange(t, KindChoiceDelete, ChoiceRef{ChoiceID: "c-other-soja"})
	if got := len(f.fake.ProductByID("p-other-box").ChoiceGroups[0].Choices); got != 0 {
		t.Errorf("other box choices = %d", got)
	}
	if got := len(f.fake.ProductByID("p-maki-box").ChoiceGroups[0].Choices); got != 2 {
		t.Errorf("maki box choices = %d", got)
	}
}

func TestFindChoiceAcrossProducts(t *testing.T) {
	f := newFixture(t)
	f.addSecondBox()
	prod, g, c, err := findChoice(f.ctx, f.svc.env, "c-other-soja")
	if err != nil || prod.ID != "p-other-box" || g.ID != "g-other-sauce" || c.Name != "Soja" {
		t.Fatalf("findChoice: %v %v %v %v", prod, g, c, err)
	}
	prod, g, err = findGroup(f.ctx, f.svc.env, "g-other-sauce")
	if err != nil || prod.ID != "p-other-box" || g.MaxSelections != 3 {
		t.Fatalf("findGroup: %v %v %v", prod, g, err)
	}
	// A choice id is not a group id, and the other way round.
	_, _, err = findGroup(f.ctx, f.svc.env, "c-soja")
	wantUserErr(t, err, "No choice group")
	_, _, _, err = findChoice(f.ctx, f.svc.env, "g-sauce")
	wantUserErr(t, err, "No choice with id")
	_, _, _, err = findChoice(f.ctx, f.svc.env, "")
	wantUserErr(t, err, "No choice with id")
}

// --- undo ---------------------------------------------------------------------------------

func TestChoiceUndoOnlyRestoresWhatChanged(t *testing.T) {
	f := newFixture(t)
	f.applyChange(t, KindChoiceUpsert, ChoiceParams{ChoiceID: "c-spicy", PriceModifierCents: i64(70)})
	u, err := f.svc.Undo(f.ctx, "")
	if err != nil || u.Mode != "pending" {
		t.Fatalf("undo: %+v %v", u, err)
	}
	contains(t, "undo summary", u.Proposal.Summary, "surcharge: 0.70 EUR -> 0.50 EUR")
	contains(t, "undo summary_zh", u.Proposal.SummaryZh, "加价：0.70 欧元 → 0.50 欧元")
	if strings.Contains(u.Proposal.Summary, "name") || strings.Contains(u.Proposal.Summary, "position") {
		t.Errorf("undo touches more than the surcharge: %s", u.Proposal.Summary)
	}
	if _, err := f.svc.ApplyPending(f.ctx, u.Proposal.ChangeID, ""); err != nil {
		t.Fatal(err)
	}
	if got := f.fake.ProductByID("p-maki-box").ChoiceGroups[0].Choices[1].PriceModifier; got != "0.5" {
		t.Errorf("surcharge after undo = %s", got)
	}
}

// Undoing a surcharge that was raised by more than the guard allows still
// works: the guard protects typos, not the way back.
func TestChoiceUndoBeyondTheSurchargeGuard(t *testing.T) {
	f := newFixture(t)
	f.applyChange(t, KindChoiceUpsert, ChoiceParams{ChoiceID: "c-soja", PriceModifierCents: i64(500)})
	u, err := f.svc.Undo(f.ctx, "")
	if err != nil || u.Mode != "pending" {
		t.Fatalf("undo: %+v %v", u, err)
	}
	if _, err := f.svc.ApplyPending(f.ctx, u.Proposal.ChangeID, ""); err != nil {
		t.Fatal(err)
	}
	if got := f.fake.ProductByID("p-maki-box").ChoiceGroups[0].Choices[0].PriceModifier; got != "0" {
		t.Errorf("surcharge after undo = %s", got)
	}
}

func TestChoiceDeletionsAreNotUndoable(t *testing.T) {
	f := newFixture(t)
	for _, tt := range []struct {
		kind   string
		params any
	}{
		{KindChoiceDelete, ChoiceRef{ChoiceID: "c-soja"}},
		{KindChoiceGroupDelete, ChoiceGroupRef{GroupID: "g-sauce"}},
	} {
		f.applyChange(t, tt.kind, tt.params)
		_, err := f.svc.Undo(f.ctx, "")
		wantUserErr(t, err, "cannot be undone automatically")
	}
}

func TestChoiceUndoIsMarkedAndNotRepeatable(t *testing.T) {
	f := newFixture(t)
	f.applyChange(t, KindChoiceGroupUpsert, ChoiceGroupParams{GroupID: "g-sauce", MaxSelections: ip(2)})
	u, err := f.svc.Undo(f.ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	// Asking again while the first undo waits returns the same proposal.
	again, err := f.svc.Undo(f.ctx, "")
	if err != nil || again.Proposal == nil || again.Proposal.ChangeID != u.Proposal.ChangeID {
		t.Fatalf("second undo: %+v %v", again, err)
	}
	if _, err := f.svc.ApplyPending(f.ctx, u.Proposal.ChangeID, ""); err != nil {
		t.Fatal(err)
	}
	// The undone change is consumed: there is nothing left to undo.
	_, err = f.svc.Undo(f.ctx, "")
	if err == nil {
		t.Error("an undone change must not be undoable again")
	}
}

func TestChoiceCreatedThenRenamedThenUndone(t *testing.T) {
	f := newFixture(t)
	f.applyChange(t, KindChoiceUpsert, ChoiceParams{GroupID: "g-sauce", Names: map[string]string{"fr": "Wasabi"}})
	id := f.fake.ProductByID("p-maki-box").ChoiceGroups[0].Choices[2].ID
	f.applyChange(t, KindChoiceUpsert, ChoiceParams{ChoiceID: id, Names: map[string]string{"fr": "Wasabi fort"}})
	u, err := f.svc.Undo(f.ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ApplyPending(f.ctx, u.Proposal.ChangeID, ""); err != nil {
		t.Fatal(err)
	}
	if got := f.fake.ProductByID("p-maki-box").ChoiceGroups[0].Choices[2].Name; got != "Wasabi" {
		t.Errorf("name after undo = %q", got)
	}
}

// --- failures --------------------------------------------------------------------------------

func TestChoiceUpstreamFailuresOnEveryKind(t *testing.T) {
	tests := []struct {
		op     string
		kind   string
		params any
	}{
		{"McpUpdateChoice", KindChoiceUpsert, ChoiceParams{ChoiceID: "c-spicy", PriceModifierCents: i64(70)}},
		{"McpCreateChoice", KindChoiceUpsert, ChoiceParams{GroupID: "g-sauce", Names: map[string]string{"fr": "Wasabi"}}},
		{"McpDeleteChoice", KindChoiceDelete, ChoiceRef{ChoiceID: "c-spicy"}},
		{"McpUpdateChoiceGroup", KindChoiceGroupUpsert, ChoiceGroupParams{GroupID: "g-sauce", MaxSelections: ip(2)}},
		{"McpCreateChoiceGroup", KindChoiceGroupUpsert, ChoiceGroupParams{ProductID: "p-maki-box", Names: map[string]string{"fr": "Boisson"}}},
		{"McpDeleteChoiceGroup", KindChoiceGroupDelete, ChoiceGroupRef{GroupID: "g-sauce"}},
	}
	for _, tt := range tests {
		t.Run(tt.op, func(t *testing.T) {
			f := newFixture(t)
			p := f.propose(t, tt.kind, tt.params)
			f.fail(tt.op)
			before := f.fake.ProductByID("p-maki-box")
			c, err := f.svc.ApplyPending(f.ctx, p.ChangeID, "")
			wantUpstreamErr(t, err)
			if c.Status != changes.StatusFailed {
				t.Errorf("status = %s", c.Status)
			}
			after, _ := json.Marshal(f.fake.ProductByID("p-maki-box"))
			was, _ := json.Marshal(before)
			if string(after) != string(was) {
				t.Error("a failed change altered the product")
			}
			// A failed change cannot be retried, and is not undoable.
			if _, err := f.svc.ApplyPending(f.ctx, p.ChangeID, ""); !errors.Is(err, ErrNotPending) {
				t.Errorf("retry of a failed change: %v", err)
			}
		})
	}
}

// If the catalogue cannot be read when the owner confirms, the change stays
// pending so the confirmation can be retried.
func TestChoiceApplyStaysPendingWhenUpstreamIsDown(t *testing.T) {
	f := newFixture(t)
	p := f.propose(t, KindChoiceUpsert, ChoiceParams{ChoiceID: "c-spicy", PriceModifierCents: i64(70)})
	f.fail("McpProducts")
	c, err := f.svc.ApplyPending(f.ctx, p.ChangeID, "")
	wantUpstreamErr(t, err)
	if c.Status != changes.StatusPending {
		t.Errorf("status = %s", c.Status)
	}
	f.unfail("McpProducts")
	if _, err := f.svc.ApplyPending(f.ctx, p.ChangeID, ""); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

func TestChoiceRejectedProposalWritesNothing(t *testing.T) {
	f := newFixture(t)
	for _, tt := range []struct {
		kind   string
		params any
	}{
		{KindChoiceUpsert, ChoiceParams{ChoiceID: "c-spicy", PriceModifierCents: i64(70)}},
		{KindChoiceDelete, ChoiceRef{ChoiceID: "c-spicy"}},
		{KindChoiceGroupUpsert, ChoiceGroupParams{GroupID: "g-sauce", MaxSelections: ip(2)}},
		{KindChoiceGroupDelete, ChoiceGroupRef{GroupID: "g-sauce"}},
	} {
		p := f.propose(t, tt.kind, tt.params)
		c, err := f.svc.Reject(f.ctx, p.ChangeID, "不要")
		if err != nil || c.Status != changes.StatusRejected {
			t.Fatalf("reject %s: %+v %v", tt.kind, c, err)
		}
		if _, err := f.svc.ApplyPending(f.ctx, p.ChangeID, ""); !errors.Is(err, ErrNotPending) {
			t.Errorf("apply after reject: %v", err)
		}
	}
	if len(f.writes()) != 0 {
		t.Errorf("writes = %v", f.writes())
	}
	g, _ := f.group("p-maki-box", "g-sauce")
	if g.Max != 1 || g.Choices != 2 {
		t.Errorf("group = %+v", g)
	}
}

func TestChoiceExpiredProposalCannotBeApplied(t *testing.T) {
	f := newFixture(t)
	p := f.propose(t, KindChoiceDelete, ChoiceRef{ChoiceID: "c-spicy"})
	f.clock.Advance(11 * time.Minute)
	_, err := f.svc.ApplyPending(f.ctx, p.ChangeID, "")
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("want an expiry error, got %v", err)
	}
	if got := len(f.fake.ProductByID("p-maki-box").ChoiceGroups[0].Choices); got != 2 {
		t.Errorf("an expired deletion deleted a choice: %d", got)
	}
}

func TestChoiceGroupOrdering(t *testing.T) {
	f := newFixture(t)
	f.applyChange(t, KindChoiceGroupUpsert, ChoiceGroupParams{ProductID: "p-maki-box", Names: map[string]string{"fr": "Boisson"}})
	f.applyChange(t, KindChoiceGroupUpsert, ChoiceGroupParams{ProductID: "p-maki-box", Names: map[string]string{"fr": "Dessert"}})
	var sorts []int
	for _, g := range f.fake.ProductByID("p-maki-box").ChoiceGroups {
		sorts = append(sorts, g.SortOrder)
	}
	if !slices.Equal(sorts, []int{0, 1, 2}) {
		t.Errorf("group positions = %v", sorts)
	}
	// Moving the last group to the front changes only that group.
	last := f.fake.ProductByID("p-maki-box").ChoiceGroups[2].ID
	p := f.propose(t, KindChoiceGroupUpsert, ChoiceGroupParams{GroupID: last, SortOrder: ip(0)})
	contains(t, "summary", p.Summary, "position: 2 -> 0")
	contains(t, "summary_zh", p.SummaryZh, "排序位置：2 → 0")
	// Choices are ordered the same way.
	f.applyChange(t, KindChoiceUpsert, ChoiceParams{ChoiceID: "c-spicy", SortOrder: ip(0)})
	if got := f.fake.ProductByID("p-maki-box").ChoiceGroups[0].Choices[1].SortOrder; got != 0 {
		t.Errorf("choice position = %d", got)
	}
}
