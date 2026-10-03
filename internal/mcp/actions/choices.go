package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"tsb-service/internal/mcp/money"
	"tsb-service/internal/mcp/upstream"
)

const (
	KindChoiceGroupUpsert = "choice_group.upsert"
	KindChoiceGroupDelete = "choice_group.delete"
	KindChoiceUpsert      = "choice.upsert"
	KindChoiceDelete      = "choice.delete"
)

// findGroup locates a choice group and its product in the catalogue.
func findGroup(ctx context.Context, env *Env, groupID string) (*upstream.Product, *upstream.ChoiceGroup, error) {
	all, err := env.Up.Products(ctx)
	if err != nil {
		return nil, nil, err
	}
	for i := range all {
		for j := range all[i].ChoiceGroups {
			if all[i].ChoiceGroups[j].ID == groupID {
				return &all[i], &all[i].ChoiceGroups[j], nil
			}
		}
	}
	return nil, nil, Userf("No choice group with id %q. Use get_product to list the product's choice groups.", groupID)
}

// findChoice locates a choice, its group and its product.
func findChoice(ctx context.Context, env *Env, choiceID string) (*upstream.Product, *upstream.ChoiceGroup, *upstream.Choice, error) {
	all, err := env.Up.Products(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	for i := range all {
		for j := range all[i].ChoiceGroups {
			g := &all[i].ChoiceGroups[j]
			for k := range g.Choices {
				if g.Choices[k].ID == choiceID {
					return &all[i], g, &g.Choices[k], nil
				}
			}
		}
	}
	return nil, nil, nil, Userf("No choice with id %q. Use get_product to list the product's choices.", choiceID)
}

func choiceNames(ts []upstream.ChoiceTranslation) map[string]string {
	m := map[string]string{}
	for _, t := range ts {
		m[t.Locale] = t.Name
	}
	return m
}

// mergeNames applies changes onto current names and returns the upstream
// translation list plus a description of the differences.
func mergeNames(cur, changes map[string]string) ([]upstream.ChoiceTranslation, lines, error) {
	merged := maps.Clone(cur)
	if merged == nil {
		merged = map[string]string{}
	}
	var diffs lines
	for _, l := range slices.Sorted(maps.Keys(changes)) {
		if err := validLanguage(l); err != nil {
			return nil, lines{}, err
		}
		n := strings.TrimSpace(changes[l])
		if n == "" {
			return nil, lines{}, Userf("The %s name cannot be empty.", l)
		}
		if merged[l] != n {
			diffs.add(fmt.Sprintf("name (%s): %q -> %q", l, merged[l], n), langZh[l]+"名称："+quoteOrNoneZh(merged[l])+arrowZh+quoteZh(n))
			merged[l] = n
		}
	}
	var ts []upstream.ChoiceTranslation
	for _, l := range slices.Sorted(maps.Keys(merged)) {
		ts = append(ts, upstream.ChoiceTranslation{Locale: l, Name: merged[l]})
	}
	return ts, diffs, nil
}

// --- choice groups ------------------------------------------------------------

// ChoiceGroupParams creates a group (ProductID set, GroupID empty) or updates
// one (GroupID set).
type ChoiceGroupParams struct {
	GroupID       string            `json:"group_id,omitempty"`
	ProductID     string            `json:"product_id,omitempty"`
	Names         map[string]string `json:"names,omitempty"`
	MinSelections *int              `json:"min_selections,omitempty"`
	MaxSelections *int              `json:"max_selections,omitempty"`
	SortOrder     *int              `json:"sort_order,omitempty"`
}

type groupState struct {
	Exists        bool              `json:"exists"`
	Names         map[string]string `json:"names,omitempty"`
	MinSelections int               `json:"min_selections"`
	MaxSelections int               `json:"max_selections"`
	SortOrder     int               `json:"sort_order"`
}

type createdID struct {
	ID string `json:"id"`
}

func choiceGroupUpsert() handler {
	return spec[ChoiceGroupParams, groupState]{
		Kind: KindChoiceGroupUpsert,
		Risk: RiskSensitive,
		Prepare: func(ctx context.Context, env *Env, p *ChoiceGroupParams) (*prepared, error) {
			var (
				prod           *upstream.Product
				cur            groupState
				label, labelZh string
			)
			if p.GroupID != "" {
				var g *upstream.ChoiceGroup
				var err error
				prod, g, err = findGroup(ctx, env, p.GroupID)
				if err != nil {
					return nil, err
				}
				cur = groupState{Exists: true, Names: choiceNames(g.Translations), MinSelections: g.MinSelections, MaxSelections: g.MaxSelections, SortOrder: g.SortOrder}
				label = fmt.Sprintf("%s, choice group %q", ProductLabel(prod), g.Name)
				labelZh = productLabelZh(prod) + "的选项组" + choiceLabelZh(g.Translations, g.Name)
			} else {
				var err error
				prod, err = getProduct(ctx, env, p.ProductID)
				if err != nil {
					return nil, err
				}
				if len(p.Names) == 0 || strings.TrimSpace(p.Names["fr"]) == "" {
					return nil, Userf("A new choice group needs at least a French name (names.fr).")
				}
				cur = groupState{MinSelections: 1, MaxSelections: 1, SortOrder: len(prod.ChoiceGroups)}
				label = fmt.Sprintf("%s, new choice group", ProductLabel(prod))
				labelZh = productLabelZh(prod) + "的新选项组"
				if p.SortOrder == nil {
					p.SortOrder = &cur.SortOrder
				}
			}
			_, diffs, err := mergeNames(cur.Names, p.Names)
			if err != nil {
				return nil, err
			}
			minSel, maxSel := cur.MinSelections, cur.MaxSelections
			if p.MinSelections != nil {
				minSel = *p.MinSelections
			}
			if p.MaxSelections != nil {
				maxSel = *p.MaxSelections
			}
			if minSel < 0 || maxSel < 1 || minSel > maxSel {
				return nil, Userf("Selections must satisfy 0 <= min <= max and max >= 1 (got min %d, max %d).", minSel, maxSel)
			}
			if minSel != cur.MinSelections || !cur.Exists {
				diffs.add(fmt.Sprintf("min selections: %d -> %d", cur.MinSelections, minSel), fmt.Sprintf("最少选择：%d%s%d", cur.MinSelections, arrowZh, minSel))
			}
			if maxSel != cur.MaxSelections || !cur.Exists {
				diffs.add(fmt.Sprintf("max selections: %d -> %d", cur.MaxSelections, maxSel), fmt.Sprintf("最多选择：%d%s%d", cur.MaxSelections, arrowZh, maxSel))
			}
			if cur.Exists && p.SortOrder != nil && *p.SortOrder != cur.SortOrder {
				diffs.add(fmt.Sprintf("position: %d -> %d", cur.SortOrder, *p.SortOrder), fmt.Sprintf("排序位置：%d%s%d", cur.SortOrder, arrowZh, *p.SortOrder))
			}
			entityID := p.GroupID
			if entityID == "" {
				entityID = "new:" + prod.ID
			}
			pr := &prepared{EntityType: "choice_group", EntityID: entityID, Before: cur}
			if cur.Exists && diffs.empty() {
				pr.NoOp = true
				pr.Summary = label + " already has these settings. Nothing changed."
				pr.SummaryZh = labelZh + "已经是这些设置，无需更改。"
				return pr, nil
			}
			pr.Summary = label + ": " + diffs.enJoined()
			pr.SummaryZh = labelZh + "：" + diffs.zhJoined()
			return pr, nil
		},
		Execute: func(ctx context.Context, env *Env, p ChoiceGroupParams, _ []byte) (any, error) {
			var curNames map[string]string
			if p.GroupID != "" {
				_, g, err := findGroup(ctx, env, p.GroupID)
				if err != nil {
					return nil, err
				}
				curNames = choiceNames(g.Translations)
			}
			ts, _, err := mergeNames(curNames, p.Names)
			if err != nil {
				return nil, err
			}
			in := upstream.ChoiceGroupInput{MinSelections: p.MinSelections, MaxSelections: p.MaxSelections, SortOrder: p.SortOrder}
			if len(p.Names) > 0 {
				in.Translations = ts
			}
			if p.GroupID != "" {
				g, err := env.Up.UpdateChoiceGroup(ctx, p.GroupID, in)
				if err != nil {
					return nil, err
				}
				return createdID{ID: g.ID}, nil
			}
			one, zero := 1, 0
			if in.MinSelections == nil {
				in.MinSelections = &one
			}
			if in.MaxSelections == nil {
				in.MaxSelections = &one
			}
			if in.SortOrder == nil {
				in.SortOrder = &zero
			}
			in.ProductID, in.Translations = p.ProductID, ts
			g, err := env.Up.CreateChoiceGroup(ctx, in)
			if err != nil {
				return nil, err
			}
			return createdID{ID: g.ID}, nil
		},
		Inverse: func(p ChoiceGroupParams, b groupState, after json.RawMessage) (string, any, error) {
			if !b.Exists {
				var a createdID
				if err := json.Unmarshal(after, &a); err != nil || a.ID == "" {
					return "", nil, ErrNotUndoable
				}
				return KindChoiceGroupDelete, ChoiceGroupRef{GroupID: a.ID}, nil
			}
			inv := ChoiceGroupParams{GroupID: p.GroupID, MinSelections: &b.MinSelections, MaxSelections: &b.MaxSelections}
			if p.SortOrder != nil {
				inv.SortOrder = &b.SortOrder
			}
			if len(p.Names) > 0 {
				inv.Names = map[string]string{}
				for l := range p.Names {
					if b.Names[l] == "" {
						return "", nil, Userf("The last change added a new translation, which cannot be removed automatically.")
					}
					inv.Names[l] = b.Names[l]
				}
			}
			return KindChoiceGroupUpsert, inv, nil
		},
	}
}

// ChoiceGroupRef targets a group for deletion.
type ChoiceGroupRef struct {
	GroupID string `json:"group_id"`
}

func choiceGroupDelete() handler {
	return spec[ChoiceGroupRef, groupState]{
		Kind: KindChoiceGroupDelete,
		Risk: RiskSensitive,
		Prepare: func(ctx context.Context, env *Env, p *ChoiceGroupRef) (*prepared, error) {
			prod, g, err := findGroup(ctx, env, p.GroupID)
			if err != nil {
				return nil, err
			}
			names := make([]string, 0, len(g.Choices))
			namesZh := make([]string, 0, len(g.Choices))
			for _, c := range g.Choices {
				names = append(names, c.Name)
				namesZh = append(namesZh, nameZh(choiceNames(c.Translations), c.Name))
			}
			return &prepared{EntityType: "choice_group", EntityID: g.ID,
				Before:    groupState{Exists: true, Names: choiceNames(g.Translations), MinSelections: g.MinSelections, MaxSelections: g.MaxSelections, SortOrder: g.SortOrder},
				Summary:   fmt.Sprintf("%s: delete choice group %q and its %d choices (%s). This cannot be undone.", ProductLabel(prod), g.Name, len(g.Choices), strings.Join(names, ", ")),
				SummaryZh: fmt.Sprintf("%s：删除选项组%s及其 %d 个选项（%s）。此操作无法撤销。", productLabelZh(prod), choiceLabelZh(g.Translations, g.Name), len(g.Choices), strings.Join(namesZh, "、"))}, nil
		},
		Execute: func(ctx context.Context, env *Env, p ChoiceGroupRef, _ []byte) (any, error) {
			return map[string]bool{"deleted": true}, env.Up.DeleteChoiceGroup(ctx, p.GroupID)
		},
	}
}

// --- choices ------------------------------------------------------------------

// ChoiceParams creates a choice (GroupID set, ChoiceID empty) or updates one.
type ChoiceParams struct {
	ChoiceID           string            `json:"choice_id,omitempty"`
	GroupID            string            `json:"group_id,omitempty"`
	Names              map[string]string `json:"names,omitempty"`
	PriceModifierCents *int64            `json:"price_modifier_cents,omitempty"`
	SortOrder          *int              `json:"sort_order,omitempty"`
	SkipBounds         bool              `json:"skip_bounds,omitempty"`
}

type choiceState struct {
	Exists             bool              `json:"exists"`
	Names              map[string]string `json:"names,omitempty"`
	PriceModifierCents int64             `json:"price_modifier_cents"`
	SortOrder          int               `json:"sort_order"`
}

func choiceUpsert() handler {
	return spec[ChoiceParams, choiceState]{
		Kind: KindChoiceUpsert,
		Risk: RiskSensitive,
		Prepare: func(ctx context.Context, env *Env, p *ChoiceParams) (*prepared, error) {
			var (
				cur            choiceState
				label, labelZh string
			)
			if p.ChoiceID != "" {
				prod, _, c, err := findChoice(ctx, env, p.ChoiceID)
				if err != nil {
					return nil, err
				}
				cur = choiceState{Exists: true, Names: choiceNames(c.Translations), PriceModifierCents: money.MustCents(c.PriceModifier), SortOrder: c.SortOrder}
				label = fmt.Sprintf("%s, choice %q", ProductLabel(prod), c.Name)
				labelZh = productLabelZh(prod) + "的选项" + choiceLabelZh(c.Translations, c.Name)
			} else {
				prod, g, err := findGroup(ctx, env, p.GroupID)
				if err != nil {
					return nil, err
				}
				if len(p.Names) == 0 || strings.TrimSpace(p.Names["fr"]) == "" {
					return nil, Userf("A new choice needs at least a French name (names.fr).")
				}
				cur = choiceState{SortOrder: len(g.Choices)}
				label = fmt.Sprintf("%s, group %q, new choice", ProductLabel(prod), g.Name)
				labelZh = productLabelZh(prod) + "的选项组" + choiceLabelZh(g.Translations, g.Name) + "中的新选项"
				if p.SortOrder == nil {
					p.SortOrder = &cur.SortOrder
				}
				if p.PriceModifierCents == nil {
					var zero int64
					p.PriceModifierCents = &zero
				}
			}
			_, diffs, err := mergeNames(cur.Names, p.Names)
			if err != nil {
				return nil, err
			}
			if p.PriceModifierCents != nil {
				v := *p.PriceModifierCents
				if v < 0 {
					return nil, Userf("A choice surcharge cannot be negative.")
				}
				if cur.Exists && cur.PriceModifierCents > 0 && v > 0 && !p.SkipBounds {
					if err := money.CheckPriceChange(cur.PriceModifierCents, v, env.PriceMaxPct); err != nil {
						return nil, &UserError{Msg: "Surcharge change refused: " + strings.TrimPrefix(err.Error(), money.ErrOutOfBounds.Error()+": ") + "."}
					}
				}
				if v != cur.PriceModifierCents || !cur.Exists {
					diffs.add(fmt.Sprintf("surcharge: %s -> %s", money.Format(cur.PriceModifierCents), money.Format(v)), "加价："+moneyZh(cur.PriceModifierCents)+arrowZh+moneyZh(v))
				}
			}
			if cur.Exists && p.SortOrder != nil && *p.SortOrder != cur.SortOrder {
				diffs.add(fmt.Sprintf("position: %d -> %d", cur.SortOrder, *p.SortOrder), fmt.Sprintf("排序位置：%d%s%d", cur.SortOrder, arrowZh, *p.SortOrder))
			}
			entityID := p.ChoiceID
			if entityID == "" {
				entityID = "new:" + p.GroupID
			}
			pr := &prepared{EntityType: "choice", EntityID: entityID, Before: cur}
			if cur.Exists && diffs.empty() {
				pr.NoOp = true
				pr.Summary = label + " already has these settings. Nothing changed."
				pr.SummaryZh = labelZh + "已经是这些设置，无需更改。"
				return pr, nil
			}
			pr.Summary = label + ": " + diffs.enJoined()
			pr.SummaryZh = labelZh + "：" + diffs.zhJoined()
			return pr, nil
		},
		Execute: func(ctx context.Context, env *Env, p ChoiceParams, _ []byte) (any, error) {
			var curNames map[string]string
			if p.ChoiceID != "" {
				_, _, c, err := findChoice(ctx, env, p.ChoiceID)
				if err != nil {
					return nil, err
				}
				curNames = choiceNames(c.Translations)
			}
			ts, _, err := mergeNames(curNames, p.Names)
			if err != nil {
				return nil, err
			}
			in := upstream.ChoiceInput{SortOrder: p.SortOrder}
			if p.PriceModifierCents != nil {
				s := money.FromCents(*p.PriceModifierCents)
				in.PriceModifier = &s
			}
			if len(p.Names) > 0 {
				in.Translations = ts
			}
			if p.ChoiceID != "" {
				c, err := env.Up.UpdateChoice(ctx, p.ChoiceID, in)
				if err != nil {
					return nil, err
				}
				return createdID{ID: c.ID}, nil
			}
			zero, zeroPrice := 0, "0.00"
			if in.PriceModifier == nil {
				in.PriceModifier = &zeroPrice
			}
			if in.SortOrder == nil {
				in.SortOrder = &zero
			}
			in.ChoiceGroupID, in.Translations = p.GroupID, ts
			c, err := env.Up.CreateChoice(ctx, in)
			if err != nil {
				return nil, err
			}
			return createdID{ID: c.ID}, nil
		},
		Inverse: func(p ChoiceParams, b choiceState, after json.RawMessage) (string, any, error) {
			if !b.Exists {
				var a createdID
				if err := json.Unmarshal(after, &a); err != nil || a.ID == "" {
					return "", nil, ErrNotUndoable
				}
				return KindChoiceDelete, ChoiceRef{ChoiceID: a.ID}, nil
			}
			inv := ChoiceParams{ChoiceID: p.ChoiceID, SkipBounds: true}
			if p.PriceModifierCents != nil {
				inv.PriceModifierCents = &b.PriceModifierCents
			}
			if p.SortOrder != nil {
				inv.SortOrder = &b.SortOrder
			}
			if len(p.Names) > 0 {
				inv.Names = map[string]string{}
				for l := range p.Names {
					if b.Names[l] == "" {
						return "", nil, Userf("The last change added a new translation, which cannot be removed automatically.")
					}
					inv.Names[l] = b.Names[l]
				}
			}
			return KindChoiceUpsert, inv, nil
		},
	}
}

// ChoiceRef targets a choice for deletion.
type ChoiceRef struct {
	ChoiceID string `json:"choice_id"`
}

func choiceDelete() handler {
	return spec[ChoiceRef, choiceState]{
		Kind: KindChoiceDelete,
		Risk: RiskSensitive,
		Prepare: func(ctx context.Context, env *Env, p *ChoiceRef) (*prepared, error) {
			prod, g, c, err := findChoice(ctx, env, p.ChoiceID)
			if err != nil {
				return nil, err
			}
			return &prepared{EntityType: "choice", EntityID: c.ID,
				Before:    choiceState{Exists: true, Names: choiceNames(c.Translations), PriceModifierCents: money.MustCents(c.PriceModifier), SortOrder: c.SortOrder},
				Summary:   fmt.Sprintf("%s: delete choice %q from group %q. This cannot be undone.", ProductLabel(prod), c.Name, g.Name),
				SummaryZh: fmt.Sprintf("%s：从选项组%s中删除选项%s。此操作无法撤销。", productLabelZh(prod), choiceLabelZh(g.Translations, g.Name), choiceLabelZh(c.Translations, c.Name))}, nil
		},
		Execute: func(ctx context.Context, env *Env, p ChoiceRef, _ []byte) (any, error) {
			return map[string]bool{"deleted": true}, env.Up.DeleteChoice(ctx, p.ChoiceID)
		},
	}
}
