package application

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/pkg/hubrise"
)

// This file is the pure planner of the catalog publisher (amendment A15.4): the local menu, the
// links, what the last publish contained and the HubRise catalog as it is now go in; the catalog to
// PUT, the refs to store first, the list of changes and the problems come out. Nothing here does I/O.

// PublishSettings are the connection settings that shape the published catalog.
type PublishSettings struct {
	// Language is the catalog language, the language the HubRise names are written in (fr when empty).
	Language string
	// VariantRef is the price list the shop's prices are written for (the importer reads the same one).
	VariantRef string
	// Images says whether product images travel with the catalog (phase 2).
	Images bool
}

func (s PublishSettings) language() string {
	if slices.Contains(catalogLanguages, s.Language) {
		return s.Language
	}
	return domain.LanguageFR
}

// PublishInput is everything PlanPublish reads.
type PublishInput struct {
	Local *domain.LocalSnapshot
	// Links are all the catalog links, retired ones included.
	Links []domain.CatalogLink
	// Published is what the last successful PUT contained for the items the shop owns.
	Published []domain.PublishedItem
	Settings  PublishSettings
	// Base is the data of GET /catalogs/:id as raw JSON; empty means an empty catalog.
	Base json.RawMessage
	// RemoveForeign also removes the items only HubRise has (a product created in the EPOS).
	RemoveForeign bool
	// Images maps a product to the HubRise image id it should carry (phase 2); nil leaves every
	// image_ids list as HubRise has it.
	Images map[uuid.UUID]string
}

// PublishAction is what a publish does with one item.
type PublishAction string

const (
	ActionPublishAdd    PublishAction = "ADD"
	ActionPublishUpdate PublishAction = "UPDATE"
	ActionPublishRemove PublishAction = "REMOVE"
)

// Fields a PublishChange names.
const (
	FieldPubName         = "name"
	FieldPubDescription  = "description"
	FieldPubPrice        = "price"
	FieldPubCategory     = "category"
	FieldPubVat          = "vatCategory"
	FieldPubCode         = "code"
	FieldPubVisibility   = "visibility"
	FieldPubTranslations = "translations"
	FieldPubLimits       = "limits"
	FieldPubSort         = "sortOrder"
	FieldPubImage        = "image"
	FieldPubListSplit    = "listSplit"
)

// PublishChange is one planned difference between what HubRise holds and what the publish writes.
type PublishChange struct {
	Entity domain.EntityType
	Action PublishAction
	// Key is the HubRise key (ref, SKU ref, "sku|list", "sku|list|option").
	Key    string
	Ref    string
	Name   string
	Fields []string
}

// ForeignItem is an item only HubRise has: no link, no published record, no local counterpart.
type ForeignItem struct {
	Entity domain.EntityType
	Ref    string
	Name   string
}

// PlannedLink is a link to create before the PUT, with a ref the shop generated.
type PlannedLink struct {
	Entity  domain.EntityType
	LocalID uuid.UUID
	Key     string
	Ref     string
}

// PlannedRekey changes the key and ref of an existing link (the split of a shared option list).
type PlannedRekey struct {
	LinkID int64
	Entity domain.EntityType
	OldKey string
	NewKey string
	NewRef string
}

// DesiredItem is an item the publish owns and writes: after a successful PUT it is what the published
// items table holds.
type DesiredItem struct {
	Entity  domain.EntityType
	Key     string
	Ref     string
	Hash    string
	Name    string
	LocalID uuid.UUID
}

// PublishPlan is the result of PlanPublish.
type PublishPlan struct {
	// Data is the catalog to PUT.
	Data json.RawMessage
	// NeedsPut is false when Data is the catalog HubRise already holds (modulo ids).
	NeedsPut bool

	NewLinks []PlannedLink
	Rekeys   []PlannedRekey
	// Unretire lists the links of retired imports the publish brings back.
	Unretire []int64
	Desired  []DesiredItem

	Changes   []PublishChange
	Unchanged int
	Foreign   []ForeignItem
	// Preserved names the sections of the HubRise catalog that are carried over untouched, with their
	// counts ("deals (2)").
	Preserved []string
	Warnings  []domain.Warning
	Blockers  []domain.Warning

	// RemovedSkus counts the SKUs this publish removes from HubRise (deleted locally, plus the foreign
	// ones when asked): the figure of the mass-removal threshold. PublishedSkus is its base.
	RemovedSkus   int
	PublishedSkus int
	// RemovalsNeedConfirm is TripsMassRetire(RemovedSkus, PublishedSkus).
	RemovalsNeedConfirm bool

	PlanHash          string
	LocalMenuHash     string
	RemoteCatalogHash string

	// SkuRefs maps a local product to the SKU ref it has (or will have) at HubRise.
	SkuRefs map[uuid.UUID]string
}

// Count counts the changes of an action.
func (p *PublishPlan) Count(a PublishAction) int {
	n := 0
	for _, c := range p.Changes {
		if c.Action == a {
			n++
		}
	}
	return n
}

// ---- desired state, built from the local menu ----

type dCategory struct {
	local *domain.LocalCategory
	ref   string
	link  *domain.CatalogLink
	name  string
	hash  string
	// names are the non empty names by language, for the translations of the description.
	names map[string]string
}

type dChoice struct {
	local *domain.LocalChoice
	ref   string
	key   string
	link  *domain.CatalogLink
	name  string
	price decimal.Decimal
	hash  string
}

type dGroup struct {
	local   *domain.LocalGroup
	product *dProduct
	// base0 is the list ref before any split.
	base0   string
	listRef string
	key     string
	link    *domain.CatalogLink
	name    string
	choices []*dChoice
	hash    string
}

type dProduct struct {
	local       *domain.LocalProduct
	sku         string
	link        *domain.CatalogLink
	cat         *dCategory
	name        string
	description string
	hash        string
	groups      []*dGroup
}

// dList is one HubRise option list the publish writes: the groups of the SKUs that share it.
type dList struct {
	ref       string
	groups    []*dGroup
	splitFrom string
	name      string
	min, max  int
	options   []*dChoice
}

type pubPlanner struct {
	in   PublishInput
	lang string
	base *baseCatalog

	plan *PublishPlan

	cats        []*dCategory
	catByID     map[uuid.UUID]*dCategory
	prods       []*dProduct
	lists       []*dList
	listByRef   map[string]*dList
	linkByLocal map[localKey]*domain.CatalogLink
	linkByKey   map[linkKey]*domain.CatalogLink
	alloc       *refAllocator
	optRefs     refSet

	// published keys by entity, and the removal sets derived from them.
	publishedKeys map[domain.EntityType]map[string]domain.PublishedItem
	desiredKeys   map[domain.EntityType]map[string]bool
	rekeySources  map[linkKey]bool
	rekeyTargets  map[linkKey]bool

	// removal bookkeeping filled while the catalog is assembled.
	removeSku   map[string]bool // SKU refs to remove (published and gone, or foreign when asked)
	removedList map[string]bool // list refs whose group was removed
	removedOpts map[string]map[string]bool
	removeCat   map[string]bool
	removedSkus map[string]bool // SKUs really removed from the output
	retained    map[string]bool
	foreignSku  map[string]bool
	foreignList map[string]bool
	foreignCat  map[string]bool
	baseSkuName map[string]string // product + SKU name of the removed, for the preview
}

// PlanPublish builds the plan. It returns domain.ErrEmptyMenu for a menu with no product (a bug that
// wiped the menu must never wipe the EPOS) and an error for an unreadable base.
func PlanPublish(in PublishInput) (*PublishPlan, error) {
	if in.Local == nil || len(in.Local.Products) == 0 {
		return nil, domain.ErrEmptyMenu
	}
	base, err := parseBaseCatalog(in.Base)
	if err != nil {
		return nil, fmt.Errorf("hubrise: read the catalog to publish around: %w", err)
	}
	p := &pubPlanner{
		in: in, lang: in.Settings.language(), base: base, plan: &PublishPlan{SkuRefs: map[uuid.UUID]string{}},
		catByID: map[uuid.UUID]*dCategory{}, listByRef: map[string]*dList{},
		linkByLocal: map[localKey]*domain.CatalogLink{}, linkByKey: map[linkKey]*domain.CatalogLink{},
		alloc: newRefAllocator(), optRefs: refSet{},
		publishedKeys: map[domain.EntityType]map[string]domain.PublishedItem{},
		desiredKeys:   map[domain.EntityType]map[string]bool{},
		rekeySources:  map[linkKey]bool{}, rekeyTargets: map[linkKey]bool{},
		removeSku: map[string]bool{}, removedList: map[string]bool{}, removedOpts: map[string]map[string]bool{},
		removeCat: map[string]bool{}, removedSkus: map[string]bool{}, retained: map[string]bool{},
		foreignSku: map[string]bool{}, foreignList: map[string]bool{}, foreignCat: map[string]bool{},
		baseSkuName: map[string]string{},
	}
	p.index()
	p.buildCategories()
	p.buildProducts()
	p.buildLists()
	p.classify()
	data := p.assemble()
	p.finish(data)
	return p.plan, nil
}

// localName picks a name: the catalog language, then French, English, Dutch, Chinese.
func pickText(texts map[string]domain.LocalText, lang string) (domain.LocalText, bool) {
	for _, l := range append([]string{lang}, catalogLanguages...) {
		if t, ok := texts[l]; ok && strings.TrimSpace(t.Name) != "" {
			return domain.LocalText{Name: strings.TrimSpace(t.Name), Description: strings.TrimSpace(t.Description), Source: t.Source}, true
		}
	}
	return domain.LocalText{}, false
}

func (p *pubPlanner) warn(code domain.WarningCode, key, msg string) {
	p.plan.Warnings = append(p.plan.Warnings, domain.Warning{Code: code, Key: key, Message: msg})
}

// ---- indexes ----

func (p *pubPlanner) index() {
	for i := range p.in.Links {
		l := &p.in.Links[i]
		p.linkByLocal[localKey{l.EntityType, l.LocalID()}] = l
		p.linkByKey[linkKey{l.EntityType, l.HubriseKey}] = l
	}
	for _, it := range p.in.Published {
		if p.publishedKeys[it.EntityType] == nil {
			p.publishedKeys[it.EntityType] = map[string]domain.PublishedItem{}
		}
		p.publishedKeys[it.EntityType][it.HubriseKey] = it
	}
	// Reserve every ref in use, so a new ref collides with nothing.
	for _, c := range p.base.categories {
		p.alloc.categories.add(jstr(c, "ref"))
	}
	for _, l := range p.base.lists {
		p.alloc.lists.add(jstr(l, "ref"))
		for _, o := range jobjs(l, "options") {
			p.optRefs.add(jstr(o, "ref"))
		}
	}
	for _, pr := range p.base.products {
		p.alloc.skus.add(jstr(pr, "ref"))
		for _, s := range jobjs(pr, "skus") {
			p.alloc.skus.add(jstr(s, "ref"))
		}
	}
	for i := range p.in.Links {
		l := &p.in.Links[i]
		switch l.EntityType {
		case domain.EntityCategory:
			p.alloc.categories.add(l.HubriseRef)
		case domain.EntityProduct:
			p.alloc.skus.add(l.HubriseRef)
		case domain.EntityChoiceGroup:
			p.alloc.lists.add(l.HubriseRef)
		case domain.EntityChoice:
			p.optRefs.add(l.HubriseRef)
		}
	}
	for _, it := range p.in.Published {
		switch it.EntityType {
		case domain.EntityCategory:
			p.alloc.categories.add(it.HubriseRef)
		case domain.EntityProduct:
			p.alloc.skus.add(it.HubriseRef)
		case domain.EntityChoiceGroup:
			p.alloc.lists.add(it.HubriseRef)
		case domain.EntityChoice:
			p.optRefs.add(it.HubriseRef)
		}
	}
}

func (p *pubPlanner) addDesired(e domain.EntityType, key, ref, hash, name string, id uuid.UUID) {
	if p.desiredKeys[e] == nil {
		p.desiredKeys[e] = map[string]bool{}
	}
	p.desiredKeys[e][key] = true
	p.plan.Desired = append(p.plan.Desired, DesiredItem{Entity: e, Key: key, Ref: ref, Hash: hash, Name: name, LocalID: id})
}

// linkFor returns the link of a local row and notes a retired one to bring back.
func (p *pubPlanner) linkFor(e domain.EntityType, id uuid.UUID) *domain.CatalogLink {
	l := p.linkByLocal[localKey{e, id}]
	if l != nil && l.RetiredAt != nil && !slices.Contains(p.plan.Unretire, l.ID) {
		p.plan.Unretire = append(p.plan.Unretire, l.ID)
	}
	return l
}

func (p *pubPlanner) newLink(e domain.EntityType, id uuid.UUID, key, ref string) {
	p.plan.NewLinks = append(p.plan.NewLinks, PlannedLink{Entity: e, LocalID: id, Key: key, Ref: ref})
}

// ---- categories ----

func (p *pubPlanner) buildCategories() {
	for i := range p.in.Local.Categories {
		c := &p.in.Local.Categories[i]
		d := &dCategory{local: c, hash: categoryItemHash(c), names: map[string]string{}}
		for lang, t := range c.Names {
			if n := strings.TrimSpace(t.Name); n != "" {
				d.names[lang] = n
			}
		}
		if t, ok := pickText(c.Names, p.lang); ok {
			d.name = t.Name
		} else {
			d.name = c.Slug
			p.warn(domain.WarnNameMissing, c.Slug, "the category "+c.Slug+" has no name, its slug is published")
		}
		if l := p.linkFor(domain.EntityCategory, c.ID); l != nil {
			d.link, d.ref = l, l.HubriseRef
		} else {
			d.ref = p.alloc.category(c.Slug, c.ID)
			p.newLink(domain.EntityCategory, c.ID, d.ref, d.ref)
		}
		p.cats = append(p.cats, d)
		p.catByID[c.ID] = d
		p.addDesired(domain.EntityCategory, d.ref, d.ref, d.hash, d.name, c.ID)
	}
}

// ---- products, groups and choices ----

func (p *pubPlanner) buildProducts() {
	groupsOf := map[uuid.UUID][]*domain.LocalGroup{}
	for i := range p.in.Local.Groups {
		g := &p.in.Local.Groups[i]
		groupsOf[g.ProductID] = append(groupsOf[g.ProductID], g)
	}
	choicesOf := map[uuid.UUID][]*domain.LocalChoice{}
	for i := range p.in.Local.Choices {
		c := &p.in.Local.Choices[i]
		choicesOf[c.GroupID] = append(choicesOf[c.GroupID], c)
	}
	for i := range p.in.Local.Products {
		lp := &p.in.Local.Products[i]
		cat := p.catByID[lp.CategoryID]
		if cat == nil { // the foreign key forbids it; a snapshot race would be refused by the validation
			continue
		}
		d := &dProduct{local: lp, cat: cat, hash: productItemHash(lp)}
		if t, ok := pickText(lp.Texts, p.lang); ok {
			d.name, d.description = t.Name, t.Description
		} else {
			d.name = "Product " + shortID(lp.ID, 8)
			p.warn(domain.WarnNameMissing, lp.ID.String(), "a product has no name, a placeholder is published")
		}
		if l := p.linkFor(domain.EntityProduct, lp.ID); l != nil {
			d.link, d.sku = l, l.HubriseRef
		} else {
			d.sku = p.alloc.sku(lp.Code, lp.ID)
			p.newLink(domain.EntityProduct, lp.ID, d.sku, d.sku)
		}
		p.plan.SkuRefs[lp.ID] = d.sku
		p.addDesired(domain.EntityProduct, d.sku, d.sku, d.hash, d.name, lp.ID)

		for _, g := range groupsOf[lp.ID] {
			dg := &dGroup{local: g, product: d, hash: groupItemHash(g)}
			if t, ok := pickText(g.Names, p.lang); ok {
				dg.name = t.Name
			} else {
				dg.name = "Options"
			}
			if l := p.linkFor(domain.EntityChoiceGroup, g.ID); l != nil {
				dg.link, dg.base0 = l, l.HubriseRef
			} else {
				dg.base0 = p.alloc.list(g.ID)
			}
			for _, c := range choicesOf[g.ID] {
				dc := &dChoice{local: c, price: c.PriceModifier, hash: choiceItemHash(c)}
				if t, ok := pickText(c.Names, p.lang); ok {
					dc.name = t.Name
				} else {
					dc.name = "Option"
				}
				if l := p.linkFor(domain.EntityChoice, c.ID); l != nil {
					dc.link, dc.ref = l, l.HubriseRef
				} else {
					dc.ref = optionRef(p.optRefs, c.ID)
				}
				dg.choices = append(dg.choices, dc)
			}
			d.groups = append(d.groups, dg)
		}
		p.prods = append(p.prods, d)
	}
}

// listSignature is what two groups must share to share one HubRise option list.
func listSignature(name string, min, max int, options []*dChoice) string {
	type opt struct{ Ref, Name, Price string }
	o := make([]opt, len(options))
	for i, c := range options {
		o[i] = opt{c.ref, c.name, c.price.StringFixed(2)}
	}
	return hashValue(map[string]any{"name": name, "min": min, "max": max, "options": o})
}

// buildLists decides the option lists: one HubRise list per local group, except that groups that
// shared a list at HubRise and still agree keep sharing it. The groups of one list ref that diverged
// are split: the largest identical subset (preferring the one that still equals the HubRise list)
// keeps the ref, every other group gets a list of its own, `ref~sku`.
func (p *pubPlanner) buildLists() {
	byBase := map[string][]*dGroup{}
	var order []string
	for _, d := range p.prods {
		for _, g := range d.groups {
			if _, ok := byBase[g.base0]; !ok {
				order = append(order, g.base0)
			}
			byBase[g.base0] = append(byBase[g.base0], g)
		}
	}
	baseLists := map[string]jobj{}
	for _, l := range p.base.lists {
		baseLists[jstr(l, "ref")] = l
	}
	for _, ref0 := range order {
		groups := byBase[ref0]
		bySig := map[string][]*dGroup{}
		var sigs []string
		for _, g := range groups {
			s := listSignature(g.name, g.local.Min, g.local.Max, g.choices)
			if _, ok := bySig[s]; !ok {
				sigs = append(sigs, s)
			}
			bySig[s] = append(bySig[s], g)
		}
		keep := sigs[0]
		if len(sigs) > 1 {
			keep = p.chooseKeeper(sigs, bySig, baseLists[ref0])
		}
		for _, s := range sigs {
			members := bySig[s]
			ref := ref0
			from := ""
			if s != keep {
				ref = p.alloc.splitList(ref0, members[0].product.sku)
				from = ref0
				for _, g := range members {
					g.listRef = ref // all members of a diverged subset share the one new list
				}
				p.warn(domain.WarnListSplit, ref, "the option list "+ref0+" no longer matches for "+members[0].product.sku+", it gets its own list "+ref)
			} else {
				for _, g := range members {
					g.listRef = ref
				}
			}
			dl := &dList{ref: ref, groups: members, splitFrom: from, name: members[0].name,
				min: members[0].local.Min, max: members[0].local.Max, options: members[0].choices}
			p.lists = append(p.lists, dl)
			p.listByRef[ref] = dl
		}
	}
	// Keys, links and rekeys now that every list ref is final.
	for _, d := range p.prods {
		for _, g := range d.groups {
			g.key = d.sku + "|" + g.listRef
			p.settleLink(domain.EntityChoiceGroup, g.link, g.local.ID, g.key, g.listRef)
			p.addDesired(domain.EntityChoiceGroup, g.key, g.listRef, g.hash, g.name, g.local.ID)
			for _, c := range g.choices {
				c.key = g.key + "|" + c.ref
				p.settleLink(domain.EntityChoice, c.link, c.local.ID, c.key, c.ref)
				p.addDesired(domain.EntityChoice, c.key, c.ref, c.hash, c.name, c.local.ID)
			}
		}
	}
}

// chooseKeeper picks which subset of a diverged list keeps the ref: the one that still equals the
// HubRise list, else the largest, else the one with the smallest SKU ref.
func (p *pubPlanner) chooseKeeper(sigs []string, bySig map[string][]*dGroup, base jobj) string {
	if base != nil {
		if baseSig, ok := baseListSignature(base, p.lang); ok {
			for _, s := range sigs {
				if s == baseSig {
					return s
				}
			}
		}
	}
	best := sigs[0]
	smallest := func(gs []*dGroup) string {
		m := gs[0].product.sku
		for _, g := range gs {
			if g.product.sku < m {
				m = g.product.sku
			}
		}
		return m
	}
	for _, s := range sigs[1:] {
		a, b := bySig[s], bySig[best]
		if len(a) > len(b) || len(a) == len(b) && smallest(a) < smallest(b) {
			best = s
		}
	}
	return best
}

// baseListSignature computes the signature the shop would give the HubRise list, to tell which
// subset of a diverged list is the one that still matches it.
func baseListSignature(l jobj, lang string) (string, bool) {
	_ = lang
	options := jobjs(l, "options")
	min, _ := jint(l, "min_selections")
	max := effectiveMax(l, len(options))
	cs := make([]*dChoice, 0, len(options))
	for _, o := range options {
		price := decimal.Zero
		if m := jstr(o, "price"); m != "" {
			d, err := hubrise.Money(m).Decimal()
			if err != nil {
				return "", false
			}
			price = d
		}
		cs = append(cs, &dChoice{ref: jstr(o, "ref"), name: strings.TrimSpace(jstr(o, "name")), price: price})
	}
	return listSignature(strings.TrimSpace(jstr(l, "name")), max0(min), max, cs), true
}

func max0(n int) int { return max(n, 0) }

// effectiveMax is the maximum the importer reads from a HubRise list (maxSelections in the mapper).
func effectiveMax(l jobj, options int) int {
	if n, ok := jint(l, "max_selections"); ok && n > 0 {
		return n
	}
	if m, _ := l["multiple_selection"].(bool); m {
		return maxMultipleSelections
	}
	return options
}

// settleLink records the link of an item under its final key: a new link for a new item, a rekey for
// an existing link whose key changed (a split list).
func (p *pubPlanner) settleLink(e domain.EntityType, l *domain.CatalogLink, id uuid.UUID, key, ref string) {
	if l == nil {
		p.newLink(e, id, key, ref)
		return
	}
	if l.HubriseKey != key || l.HubriseRef != ref {
		p.plan.Rekeys = append(p.plan.Rekeys, PlannedRekey{LinkID: l.ID, Entity: e, OldKey: l.HubriseKey, NewKey: key, NewRef: ref})
		p.rekeySources[linkKey{e, l.HubriseKey}] = true
		p.rekeyTargets[linkKey{e, key}] = true
	}
}

// ---- classification: what is removed, what is foreign ----

func (p *pubPlanner) classify() {
	// Published and gone: removed.
	for key, it := range p.publishedKeys[domain.EntityProduct] {
		if !p.desiredKeys[domain.EntityProduct][key] {
			p.removeSku[it.HubriseRef] = true
		}
	}
	for key, it := range p.publishedKeys[domain.EntityChoiceGroup] {
		if !p.desiredKeys[domain.EntityChoiceGroup][key] && !p.rekeySources[linkKey{domain.EntityChoiceGroup, key}] {
			p.removedList[it.HubriseRef] = true
		}
	}
	for key, it := range p.publishedKeys[domain.EntityChoice] {
		if p.desiredKeys[domain.EntityChoice][key] || p.rekeySources[linkKey{domain.EntityChoice, key}] {
			continue
		}
		parts := strings.Split(key, "|")
		if len(parts) != 3 {
			continue
		}
		if p.removedOpts[parts[1]] == nil {
			p.removedOpts[parts[1]] = map[string]bool{}
		}
		p.removedOpts[parts[1]][it.HubriseRef] = true
	}
	for key, it := range p.publishedKeys[domain.EntityCategory] {
		if !p.desiredKeys[domain.EntityCategory][key] {
			p.removeCat[it.HubriseRef] = true
		}
	}

	// Foreign: in HubRise, owned by nobody.
	ownedSku := map[string]bool{}
	ownedCat := map[string]bool{}
	ownedList := map[string]bool{}
	for _, d := range p.prods {
		ownedSku[d.sku] = true
	}
	for _, l := range p.in.Links {
		switch l.EntityType {
		case domain.EntityProduct:
			ownedSku[l.HubriseRef] = true
		case domain.EntityCategory:
			ownedCat[l.HubriseRef] = true
		case domain.EntityChoiceGroup:
			ownedList[l.HubriseRef] = true
		}
	}
	for _, it := range p.in.Published {
		switch it.EntityType {
		case domain.EntityProduct:
			ownedSku[it.HubriseRef] = true
		case domain.EntityCategory:
			ownedCat[it.HubriseRef] = true
		case domain.EntityChoiceGroup:
			ownedList[it.HubriseRef] = true
		}
	}
	for _, c := range p.cats {
		ownedCat[c.ref] = true
	}
	for ref := range p.listByRef {
		ownedList[ref] = true
	}
	for _, pr := range p.base.products {
		for _, s := range jobjs(pr, "skus") {
			ref := jstr(s, "ref")
			if ref != "" && !ownedSku[ref] {
				p.foreignSku[ref] = true
				p.plan.Foreign = append(p.plan.Foreign, ForeignItem{Entity: domain.EntityProduct, Ref: ref, Name: skuFullName(pr, s)})
				p.baseSkuName[ref] = skuFullName(pr, s)
			}
		}
	}
	for _, c := range p.base.categories {
		if ref := jstr(c, "ref"); ref != "" && !ownedCat[ref] {
			p.foreignCat[ref] = true
			p.plan.Foreign = append(p.plan.Foreign, ForeignItem{Entity: domain.EntityCategory, Ref: ref, Name: jstr(c, "name")})
		}
	}
	for _, l := range p.base.lists {
		if ref := jstr(l, "ref"); ref != "" && !ownedList[ref] {
			p.foreignList[ref] = true
			p.plan.Foreign = append(p.plan.Foreign, ForeignItem{Entity: domain.EntityChoiceGroup, Ref: ref, Name: jstr(l, "name")})
		}
	}
	sort.SliceStable(p.plan.Foreign, func(i, j int) bool {
		a, b := p.plan.Foreign[i], p.plan.Foreign[j]
		if a.Entity != b.Entity {
			return a.Entity < b.Entity
		}
		return a.Ref < b.Ref
	})
	if p.in.RemoveForeign {
		for ref := range p.foreignSku {
			p.removeSku[ref] = true
		}
		for ref := range p.foreignCat {
			p.removeCat[ref] = true
		}
		for ref := range p.foreignList {
			p.removedList[ref] = true
		}
	}
}

// skuFullName is the name the importer would give a SKU: the product name, then the SKU name.
func skuFullName(product, sku jobj) string {
	name := strings.TrimSpace(jstr(product, "name"))
	if s := strings.TrimSpace(jstr(sku, "name")); s != "" {
		name += " " + s
	}
	return name
}
