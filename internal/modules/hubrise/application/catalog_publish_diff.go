package application

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/pkg/hubrise"
)

// projection is the part of one catalog item the shop owns, as comparable text per field. The same
// function reads the HubRise catalog as it is and the catalog to PUT, so the difference of the two is
// exactly what the publish changes.
type projection struct {
	ref    string
	name   string
	fields map[string]string
}

// finish completes the plan once the catalog is assembled: problems, changes, hashes.
func (p *pubPlanner) finish(data jobj) {
	pl := p.plan
	raw := marshalJSON(data)
	pl.Data = raw
	pl.Blockers = append(pl.Blockers, p.base.issues...)
	pl.Blockers = append(pl.Blockers, p.validate(data)...)

	baseProj := p.project(p.base.data())
	outProj := p.project(data)
	p.diff(baseProj, outProj)

	pl.PublishedSkus = len(p.publishedKeys[domain.EntityProduct])
	pl.RemovalsNeedConfirm = domain.TripsMassRetire(pl.RemovedSkus, pl.PublishedSkus)
	pl.NeedsPut = !slices.Equal(canonicalJSON(p.base.data()), canonicalJSON(data))
	pl.PlanHash = planHashOf(pl.Changes, pl.Foreign)
	pl.LocalMenuHash = LocalMenuHash(p.in.Local, p.in.Settings)
	pl.RemoteCatalogHash = RemoteCatalogHash(p.in.Base)
	for _, sec := range []struct {
		name string
		n    int
	}{{"variants", len(p.base.variants)}, {"deals", len(p.base.deals)}, {"discounts", len(p.base.discounts)}, {"charges", len(p.base.charges)}} {
		if sec.n > 0 {
			pl.Preserved = append(pl.Preserved, fmt.Sprintf("%s (%d)", sec.name, sec.n))
		}
	}
	sort.SliceStable(pl.Desired, func(i, j int) bool {
		a, b := pl.Desired[i], pl.Desired[j]
		if a.Entity != b.Entity {
			return a.Entity < b.Entity
		}
		return a.Key < b.Key
	})
}

// ---- projection ----

type projKey struct {
	entity domain.EntityType
	key    string
}

func vatOf(product, sku jobj) string {
	if cf, _ := sku["custom_fields"].(jobj); cf != nil {
		if v := strings.TrimSpace(jstr(cf, "vat_category")); domain.ValidVatCategory(v) {
			return v
		}
	}
	tr, _ := product["tax_rate"].(jobj)
	if tr == nil {
		return ""
	}
	return vatFromRates(&hubrise.TaxRate{Delivery: jstr(tr, "delivery"), Collection: jstr(tr, "collection"), EatIn: jstr(tr, "eat_in")})
}

func (p *pubPlanner) effectivePrice(sku jobj) string {
	price, err := hubrise.Money(jstr(sku, "price")).Decimal()
	if err != nil {
		return "?"
	}
	ov := jobjs(sku, "price_overrides")
	if i := effectiveOverride(ov, p.in.Settings.VariantRef); i >= 0 {
		if d, err := hubrise.Money(jstr(ov[i], "price")).Decimal(); err == nil {
			price = d
		}
	}
	return price.StringFixed(2)
}

func priceText(o jobj) string {
	m := jstr(o, "price")
	if m == "" {
		return "0.00"
	}
	d, err := hubrise.Money(m).Decimal()
	if err != nil {
		return "?"
	}
	return d.StringFixed(2)
}

// ownedImages is the image ids of a HubRise product that are the shop's (empty when images are off).
func (p *pubPlanner) ownedImages(product jobj) string {
	if p.in.Images == nil {
		return ""
	}
	var owned []string
	for _, id := range jstrs(product, "image_ids") {
		if _, ok := p.in.Images.Owned[id]; ok {
			owned = append(owned, id)
		}
	}
	slices.Sort(owned)
	return strings.Join(owned, ",")
}

// project reads the shop-owned view of a catalog.
func (p *pubPlanner) project(data jobj) map[projKey]*projection {
	out := map[projKey]*projection{}
	put := func(e domain.EntityType, key, ref, name string, fields map[string]string) {
		out[projKey{e, key}] = &projection{ref: ref, name: name, fields: fields}
	}
	cats, _ := data["categories"].([]any)
	catRank := rankOf(cats, p.desiredKeys[domain.EntityCategory])
	for _, c := range jobjs(data, "categories") {
		ref := refOf(c)
		put(domain.EntityCategory, ref, ref, jstr(c, "name"), map[string]string{
			FieldPubName: jstr(c, "name"), FieldPubTranslations: categoryTranslations(c), FieldPubSort: catRank[ref],
		})
	}
	lists := map[string]jobj{}
	for _, l := range jobjs(data, "option_lists") {
		lists[refOf(l)] = l
	}
	for _, pr := range jobjs(data, "products") {
		for _, s := range jobjs(pr, "skus") {
			ref := refOf(s)
			if ref == "" {
				continue
			}
			cf, _ := s["custom_fields"].(jobj)
			tr, _ := cf["translations"].(jobj)
			trText := ""
			if tr != nil {
				trText = string(canonicalJSON(tr))
			}
			desc := strings.TrimSpace(jstr(pr, "description"))
			if desc == "" && tr != nil {
				if e, _ := tr[p.lang].(jobj); e != nil {
					desc = strings.TrimSpace(jstr(e, "description"))
				}
			}
			vis := "visible"
			if hasAnyTag(append(tagSet(pr), tagSet(s)...), hiddenTags) {
				vis = "hidden"
			}
			put(domain.EntityProduct, ref, ref, skuFullName(pr, s), map[string]string{
				FieldPubName: skuFullName(pr, s), FieldPubDescription: desc, FieldPubPrice: p.effectivePrice(s),
				FieldPubCategory: jstr(pr, "category_ref"), FieldPubVat: vatOf(pr, s), FieldPubCode: strings.TrimSpace(jstr(cf, "code")),
				FieldPubVisibility: vis, FieldPubTranslations: trText, FieldPubImage: p.ownedImages(pr),
			})
			for _, lref := range jstrs(s, "option_list_refs") {
				l := lists[lref]
				if l == nil {
					continue
				}
				opts := jobjs(l, "options")
				mn, _ := jint(l, "min_selections")
				mul, _ := l["multiple_selection"].(bool)
				gkey := ref + "|" + lref
				put(domain.EntityChoiceGroup, gkey, lref, jstr(l, "name"), map[string]string{
					FieldPubName:   jstr(l, "name"),
					FieldPubLimits: fmt.Sprintf("%d:%d:%t", max(mn, 0), effectiveMax(l, len(opts)), mul),
				})
				rank := rankOf(anySlice(opts), p.desiredOptionRefs(lref))
				for _, o := range opts {
					put(domain.EntityChoice, gkey+"|"+refOf(o), refOf(o), jstr(o, "name"), map[string]string{
						FieldPubName: jstr(o, "name"), FieldPubPrice: priceText(o), FieldPubSort: rank[refOf(o)],
					})
				}
			}
		}
	}
	return out
}

func anySlice(in []jobj) []any { return objsToAny(in) }

// desiredOptionRefs are the option refs the shop writes in a list.
func (p *pubPlanner) desiredOptionRefs(listRef string) map[string]bool {
	out := map[string]bool{}
	if dl := p.listByRef[listRef]; dl != nil {
		for _, c := range dl.options {
			out[c.ref] = true
		}
	}
	return out
}

// rankOf numbers the elements whose ref is wanted, in order: the relative order of the owned
// elements, which does not move when somebody else's element is added around them.
func rankOf(list []any, want map[string]bool) map[string]string {
	out := map[string]string{}
	n := 0
	for _, e := range list {
		o, _ := e.(jobj)
		ref := refOf(o)
		if want[ref] {
			out[ref] = fmt.Sprint(n)
			n++
		}
	}
	return out
}

// categoryTranslations is the comparable form of the description of a category.
func categoryTranslations(c jobj) string {
	desc := strings.TrimSpace(jstr(c, "description"))
	if strings.HasPrefix(desc, "{") {
		if o, err := decodeObject([]byte(desc)); err == nil {
			return string(canonicalJSON(o["translations"]))
		}
	}
	return "text:" + desc
}

var fieldOrder = []string{FieldPubName, FieldPubDescription, FieldPubPrice, FieldPubCategory, FieldPubVat, FieldPubCode,
	FieldPubVisibility, FieldPubTranslations, FieldPubLimits, FieldPubSort, FieldPubImage}

// diff turns two projections into the changes of the plan.
func (p *pubPlanner) diff(base, out map[projKey]*projection) {
	pl := p.plan
	removedKey := func(e domain.EntityType, key string) bool {
		if p.rekeySources[linkKey{e, key}] {
			return false
		}
		if _, ok := p.publishedKeys[e][key]; ok {
			return true
		}
		return e == domain.EntityProduct && p.removeSku[key]
	}
	var keys []projKey
	seen := map[projKey]bool{}
	for k := range base {
		keys = append(keys, k)
		seen[k] = true
	}
	for k := range out {
		if !seen[k] {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].entity != keys[j].entity {
			return keys[i].entity < keys[j].entity
		}
		return keys[i].key < keys[j].key
	})
	for _, k := range keys {
		b, o := base[k], out[k]
		desired := p.desiredKeys[k.entity][k.key]
		switch {
		case o != nil && desired && p.rekeyTargets[linkKey{k.entity, k.key}] && b == nil:
			if k.entity == domain.EntityChoiceGroup {
				pl.Changes = append(pl.Changes, PublishChange{Entity: k.entity, Action: ActionPublishUpdate, Key: k.key, Ref: o.ref, Name: o.name, Fields: []string{FieldPubListSplit}})
			}
		case o != nil && desired && b == nil:
			pl.Changes = append(pl.Changes, PublishChange{Entity: k.entity, Action: ActionPublishAdd, Key: k.key, Ref: o.ref, Name: o.name})
		case o != nil && desired:
			var fields []string
			for _, f := range fieldOrder {
				if b.fields[f] != o.fields[f] {
					fields = append(fields, f)
				}
			}
			if len(fields) > 0 {
				pl.Changes = append(pl.Changes, PublishChange{Entity: k.entity, Action: ActionPublishUpdate, Key: k.key, Ref: o.ref, Name: o.name, Fields: fields})
			} else {
				pl.Unchanged++
			}
		case o == nil && b != nil && removedKey(k.entity, k.key):
			pl.Changes = append(pl.Changes, PublishChange{Entity: k.entity, Action: ActionPublishRemove, Key: k.key, Ref: b.ref, Name: b.name})
		}
	}
	// Removals first, as the preview shows them.
	sort.SliceStable(pl.Changes, func(i, j int) bool {
		return actionRank(pl.Changes[i].Action) < actionRank(pl.Changes[j].Action)
	})
}

func actionRank(a PublishAction) int {
	switch a {
	case ActionPublishRemove:
		return 0
	case ActionPublishAdd:
		return 1
	}
	return 2
}

// ---- validation: the rules HubRise would answer 422 for ----

func (p *pubPlanner) validate(data jobj) []domain.Warning {
	var out []domain.Warning
	add := func(code domain.WarningCode, key, msg string) {
		out = append(out, domain.Warning{Code: code, Key: key, Message: msg})
	}
	dupe := func(kind string, seen map[string]bool, ref string) {
		if ref == "" {
			return
		}
		if seen[ref] {
			add(domain.BlockDuplicateRef, ref, "the "+kind+" ref "+ref+" is used twice")
		}
		seen[ref] = true
	}
	checkMoney := func(key, field string, o jobj, required bool) {
		m := jstr(o, field)
		if m == "" {
			if required {
				add(domain.BlockInvalidMoney, key, field+" is missing")
			}
			return
		}
		if _, err := hubrise.Money(m).Decimal(); err != nil {
			add(domain.BlockInvalidMoney, key, field+" "+m+" is not an amount")
		}
	}
	cats, lists, skus, prods := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, c := range jobjs(data, "categories") {
		dupe("category", cats, refOf(c))
	}
	for _, l := range jobjs(data, "option_lists") {
		dupe("option list", lists, refOf(l))
	}
	for _, c := range jobjs(data, "categories") {
		if parent := jstr(c, "parent_ref"); parent != "" && !cats[parent] {
			add(domain.BlockDanglingRef, refOf(c), "the category "+refOf(c)+" has an unknown parent "+parent)
		}
	}
	skuNames := map[string]string{}
	for _, pr := range jobjs(data, "products") {
		dupe("product", prods, refOf(pr))
		key := refOf(pr)
		if cr := jstr(pr, "category_ref"); cr == "" || !cats[cr] {
			add(domain.BlockDanglingRef, key, "the product "+jstr(pr, "name")+" has an unknown category "+cr)
		}
		ps := jobjs(pr, "skus") // a product without SKU is dropped by the merge, none reaches this point
		if tr, ok := pr["tax_rate"].(jobj); ok {
			n := 0
			for _, k := range []string{"delivery", "collection", "eat_in"} {
				if jstr(tr, k) != "" {
					n++
					if _, err := hubrise.ParseTaxRate(jstr(tr, k)); err != nil {
						add(domain.BlockTaxRatePartial, key, "the tax rate "+k+" is not a percentage")
					}
				}
			}
			if n != 0 && n != 3 {
				add(domain.BlockTaxRatePartial, key, "the tax rate of "+jstr(pr, "name")+" must set delivery, collection and eat_in together")
			}
		}
		names := map[string]bool{}
		for _, s := range ps {
			ref := refOf(s)
			dupe("SKU", skus, ref)
			if ref == "" || len(ref) > 255 {
				add(domain.BlockInvalidRef, key, "a SKU of "+jstr(pr, "name")+" has no usable ref")
			}
			n := strings.TrimSpace(jstr(s, "name"))
			if names[n] {
				add(domain.BlockSkuNameDuplicate, ref, "two SKUs of "+jstr(pr, "name")+" are named \""+n+"\"")
			}
			names[n] = true
			skuNames[ref] = n
			checkMoney(ref, "price", s, true)
			for _, b := range jstrs(s, "barcodes") {
				if (len(b) != 8 && len(b) != 12 && len(b) != 13) || strings.Trim(b, "0123456789") != "" {
					add(domain.BlockInvalidBarcode, ref, "the barcode "+b+" must be 8, 12 or 13 digits")
				}
			}
			for _, lr := range jstrs(s, "option_list_refs") {
				if !lists[lr] {
					add(domain.BlockDanglingRef, ref, "the SKU "+ref+" names the unknown option list "+lr)
				}
			}
		}
	}
	for _, l := range jobjs(data, "option_lists") {
		ref := refOf(l)
		opts := jobjs(l, "options")
		if len(opts) == 0 {
			add(domain.BlockEmptyOptionList, ref, "the option list "+jstr(l, "name")+" has no option")
		}
		mn, _ := jint(l, "min_selections")
		mx, hasMax := jint(l, "max_selections")
		mul, _ := l["multiple_selection"].(bool)
		if hasMax && mx > 0 {
			if mn > mx {
				add(domain.BlockOptionLimits, ref, "the minimum of "+jstr(l, "name")+" is above its maximum")
			}
			if mx > len(opts) && !mul {
				add(domain.BlockOptionLimits, ref, "the maximum of "+jstr(l, "name")+" is above its number of options")
			}
			if mul && mx <= 1 {
				add(domain.BlockOptionLimits, ref, "an option list with a maximum of 1 cannot allow repeated picks")
			}
		}
		if mn > len(opts) && !mul {
			add(domain.BlockOptionLimits, ref, "the minimum of "+jstr(l, "name")+" is above its number of options")
		}
		defaults := 0
		seen := map[string]bool{}
		for _, o := range opts {
			dupe("option", seen, refOf(o))
			checkMoney(ref+"|"+refOf(o), "price", o, true)
			if d, _ := o["default"].(bool); d {
				defaults++
			}
		}
		if hasMax && mx > 0 && defaults > mx {
			add(domain.BlockOptionLimits, ref, "more default options than the maximum of "+jstr(l, "name"))
		}
	}
	for _, deal := range jobjs(data, "deals") {
		for _, line := range jobjs(deal, "lines") {
			for _, s := range jobjs(line, "skus") {
				if r := jstr(s, "ref"); r == "" || !skus[r] {
					add(domain.BlockDanglingRef, jstr(deal, "ref"), "the deal "+jstr(deal, "name")+" uses the unknown SKU "+r)
				}
			}
		}
	}
	for _, c := range jobjs(data, "charges") {
		checkMoney(jstr(c, "ref"), "price", c, false)
	}
	return out
}
