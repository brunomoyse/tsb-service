package application

import (
	"strings"

	"github.com/google/uuid"
	"github.com/gosimple/slug"

	"tsb-service/internal/modules/hubrise/domain"
)

// LinkIndex finds the catalog links by HubRise key and by local row.
type LinkIndex struct {
	byKey   map[linkKey]*domain.CatalogLink
	byLocal map[localKey]*domain.CatalogLink
	byType  map[domain.EntityType][]*domain.CatalogLink
}

type linkKey struct {
	t   domain.EntityType
	key string
}

type localKey struct {
	t  domain.EntityType
	id uuid.UUID
}

// NewLinkIndex indexes links (retired ones included).
func NewLinkIndex(links []domain.CatalogLink) *LinkIndex {
	idx := &LinkIndex{
		byKey:   make(map[linkKey]*domain.CatalogLink, len(links)),
		byLocal: make(map[localKey]*domain.CatalogLink, len(links)),
		byType:  map[domain.EntityType][]*domain.CatalogLink{},
	}
	for i := range links {
		l := &links[i]
		idx.byKey[linkKey{l.EntityType, l.HubriseKey}] = l
		idx.byLocal[localKey{l.EntityType, l.LocalID()}] = l
		idx.byType[l.EntityType] = append(idx.byType[l.EntityType], l)
	}
	return idx
}

func (x *LinkIndex) get(t domain.EntityType, key string) *domain.CatalogLink {
	return x.byKey[linkKey{t, key}]
}

func (x *LinkIndex) linkedLocal(t domain.EntityType, id uuid.UUID) bool {
	return x.byLocal[localKey{t, id}] != nil
}

// PlanInput is everything Plan reads.
type PlanInput struct {
	Catalog *domain.Catalog
	Local   *domain.LocalSnapshot
	Links   *LinkIndex
	Mode    domain.SyncMode
}

// Plan compares the HubRise catalog with the local menu and the existing links and says what an
// import would do. It is a pure function: it reads its input and writes nothing. For DRY_RUN and
// FULL the result is the full plan; for LINK_ONLY only the adoptions are kept.
//
// Matching of an unlinked HubRise entry with an existing local row goes: an explicit link (a manual
// link is a link row like any other), then the product code, then the normalized name inside the
// matched category. An ambiguous match is a Conflict and the entry is left alone, never guessed.
func Plan(in PlanInput) *domain.Diff {
	p := &planner{
		in: in, lang: in.Catalog.Language,
		d:       &domain.Diff{CatalogHash: in.Catalog.Hash(), Warnings: in.Catalog.Warnings},
		cats:    map[uuid.UUID]*domain.LocalCategory{},
		prods:   map[uuid.UUID]*domain.LocalProduct{},
		groups:  map[uuid.UUID]*domain.LocalGroup{},
		choices: map[uuid.UUID]*domain.LocalChoice{},

		groupsOf:  map[uuid.UUID][]*domain.LocalGroup{},
		choicesOf: map[uuid.UUID][]*domain.LocalChoice{},
		claimed:   map[localKey]bool{},
		catLocal:  map[string]uuid.UUID{},

		ambiguousCats: map[string]bool{},
	}
	p.index()
	p.planCategories()
	p.planProducts()
	p.planRetirements()

	if in.Mode == domain.SyncLinkOnly {
		kept := p.d.Changes[:0]
		for _, c := range p.d.Changes {
			if c.Action == domain.ActionLink {
				kept = append(kept, c)
			}
		}
		p.d.Changes = kept
		p.d.RetiringProducts = 0
	}
	p.d.RetireNeedsConfirm = in.Mode != domain.SyncLinkOnly && domain.TripsMassRetire(p.d.RetiringProducts, p.d.LinkedProducts)
	return p.d
}

type planner struct {
	in   PlanInput
	d    *domain.Diff
	lang string

	cats      map[uuid.UUID]*domain.LocalCategory
	prods     map[uuid.UUID]*domain.LocalProduct
	groups    map[uuid.UUID]*domain.LocalGroup
	choices   map[uuid.UUID]*domain.LocalChoice
	groupsOf  map[uuid.UUID][]*domain.LocalGroup  // by product
	choicesOf map[uuid.UUID][]*domain.LocalChoice // by group

	claimed  map[localKey]bool    // local rows already matched to a HubRise entry in this plan
	catLocal map[string]uuid.UUID // category key -> local category (uuid.Nil: created by this import)
	// ambiguousCats are the categories with several candidates: their products wait for a human.
	ambiguousCats map[string]bool
	// maxOrder is the highest category order of the local menu; new categories follow it.
	maxOrder      int
	newCategories int
}

func (p *planner) index() {
	s := p.in.Local
	for i := range s.Categories {
		p.cats[s.Categories[i].ID] = &s.Categories[i]
		p.maxOrder = max(p.maxOrder, s.Categories[i].Order)
	}
	for i := range s.Products {
		p.prods[s.Products[i].ID] = &s.Products[i]
	}
	for i := range s.Groups {
		g := &s.Groups[i]
		p.groups[g.ID] = g
		p.groupsOf[g.ProductID] = append(p.groupsOf[g.ProductID], g)
	}
	for i := range s.Choices {
		c := &s.Choices[i]
		p.choices[c.ID] = c
		p.choicesOf[c.GroupID] = append(p.choicesOf[c.GroupID], c)
	}
	for _, l := range p.in.Links.byType[domain.EntityProduct] {
		if l.RetiredAt == nil {
			p.d.LinkedProducts++
		}
	}
}

func (p *planner) emit(c domain.Change) { p.d.Changes = append(p.d.Changes, c) }

// normalize is the comparison form of a name: transliterated like a French slug, so case, accents
// and punctuation do not matter.
func normalize(name string) string { return slug.MakeLang(strings.TrimSpace(name), "fr") }

// localName is the name of a local row in the catalog language (French as the fallback).
func (p *planner) localName(names map[string]domain.LocalText) string {
	if t, ok := names[p.lang]; ok && t.Name != "" {
		return t.Name
	}
	return names[domain.LanguageFR].Name
}

// textsNeedWrite reports whether the import has something to write: a language the local row does
// not have, or a translation the import wrote earlier and HubRise has changed since. A manual row
// is never a reason.
func textsNeedWrite(desired map[string]domain.Text, local map[string]domain.LocalText) bool {
	for lang, t := range desired {
		if t.Name == "" {
			continue
		}
		lt, ok := local[lang]
		if !ok {
			return true
		}
		if lt.Source == domain.SourceHubrise && (lt.Name != t.Name || lt.Description != t.Description) {
			return true
		}
	}
	return false
}

func namesAsTexts(names map[string]string) map[string]domain.Text {
	out := make(map[string]domain.Text, len(names))
	for lang, name := range names {
		out[lang] = domain.Text{Name: name}
	}
	return out
}

// ---- categories ----

func (p *planner) planCategories() {
	for i := range p.in.Catalog.Categories {
		c := &p.in.Catalog.Categories[i]
		hash := c.Hash()
		change := domain.Change{Entity: domain.EntityCategory, Key: c.Key, Name: c.Names[p.lang], Hash: hash, Batch: c.Key, Category: c}

		link := p.in.Links.get(domain.EntityCategory, c.Key)
		if link != nil {
			if local := p.cats[link.LocalID()]; local != nil {
				p.catLocal[c.Key] = local.ID
				revive := link.RetiredAt != nil
				needsNames := textsNeedWrite(namesAsTexts(c.Names), local.Names)
				if !revive && !needsNames && link.ContentHash == hash {
					p.d.Unchanged++
					continue
				}
				change.Action, change.LocalID = domain.ActionUpdate, local.ID
				if revive {
					change.Action = domain.ActionRevive
				}
				if needsNames {
					change.Fields = []string{domain.FieldNames}
				}
				p.emit(change)
				continue
			}
		}

		match, ambiguous := p.matchCategory(c)
		switch {
		case ambiguous != nil:
			p.ambiguousCats[c.Key] = true
			p.d.Conflicts = append(p.d.Conflicts, domain.Conflict{Entity: domain.EntityCategory, Key: c.Key, Name: c.Names[p.lang],
				Reason: "several local categories have this name", Candidates: ambiguous})
		case match != nil:
			p.claimed[localKey{domain.EntityCategory, match.ID}] = true
			p.catLocal[c.Key] = match.ID
			change.Action, change.LocalID, change.Origin = domain.ActionLink, match.ID, domain.OriginAdopted
			if textsNeedWrite(namesAsTexts(c.Names), match.Names) {
				change.Fields = []string{domain.FieldNames}
			}
			p.emit(change)
		default:
			p.catLocal[c.Key] = uuid.Nil
			p.newCategories++
			change.Action, change.Origin, change.Order = domain.ActionCreate, domain.OriginImport, p.maxOrder+p.newCategories
			p.emit(change)
		}
	}
}

// matchCategory finds the unlinked local category with the same normalized name. ambiguous lists the
// names when several match.
func (p *planner) matchCategory(c *domain.CatalogCategory) (*domain.LocalCategory, []string) {
	want := normalize(c.Names[p.lang])
	if want == "" {
		return nil, nil
	}
	var found []*domain.LocalCategory
	for i := range p.in.Local.Categories {
		lc := &p.in.Local.Categories[i]
		if p.in.Links.linkedLocal(domain.EntityCategory, lc.ID) || p.claimed[localKey{domain.EntityCategory, lc.ID}] {
			continue
		}
		if normalize(p.localName(lc.Names)) == want {
			found = append(found, lc)
		}
	}
	switch len(found) {
	case 0:
		return nil, nil
	case 1:
		return found[0], nil
	}
	names := make([]string, len(found))
	for i, f := range found {
		names[i] = f.Slug
	}
	return nil, names
}

// ---- products, groups, choices ----

func (p *planner) planProducts() {
	for i := range p.in.Catalog.Products {
		prod := &p.in.Catalog.Products[i]
		if p.ambiguousCats[prod.CategoryKey] {
			continue
		}
		local, link, origin, ok := p.resolveProduct(prod)
		if !ok {
			continue // a conflict was reported, the product and its options are left alone
		}
		localID := uuid.Nil
		if local != nil {
			localID = local.ID
		}
		p.planProduct(prod, local, link, origin)
		for gi := range prod.Groups {
			p.planGroup(prod, &prod.Groups[gi], localID)
		}
	}
}

// resolveProduct finds the local product of a HubRise product: through its link, or by adoption.
// ok is false for an ambiguous match (a Conflict was added).
func (p *planner) resolveProduct(prod *domain.CatalogProduct) (local *domain.LocalProduct, link *domain.CatalogLink, origin domain.LinkOrigin, ok bool) {
	if link = p.in.Links.get(domain.EntityProduct, prod.Key); link != nil {
		if local = p.prods[link.LocalID()]; local != nil {
			return local, link, link.Origin, true
		}
		link = nil
	}
	local, candidates := p.matchProduct(prod)
	if candidates != nil {
		p.d.Conflicts = append(p.d.Conflicts, domain.Conflict{Entity: domain.EntityProduct, Key: prod.Key, Name: prod.Texts[p.lang].Name,
			Reason: "several local products match this entry, or the match is already taken", Candidates: candidates})
		return nil, nil, "", false
	}
	if local != nil {
		p.claimed[localKey{domain.EntityProduct, local.ID}] = true
		return local, nil, domain.OriginAdopted, true
	}
	return nil, nil, domain.OriginImport, true
}

// matchProduct looks for an unlinked local product: the same code first, then the same normalized
// name in the matched category. candidates is non-nil for an ambiguous or contested match.
func (p *planner) matchProduct(prod *domain.CatalogProduct) (*domain.LocalProduct, []string) {
	free := func(lp *domain.LocalProduct) bool {
		return !p.in.Links.linkedLocal(domain.EntityProduct, lp.ID)
	}
	pick := func(found []*domain.LocalProduct) (*domain.LocalProduct, []string) {
		switch len(found) {
		case 0:
			return nil, nil
		case 1:
			if p.claimed[localKey{domain.EntityProduct, found[0].ID}] {
				return nil, []string{found[0].Code + " " + p.localName(found[0].Texts) + " (taken)"}
			}
			return found[0], nil
		}
		names := make([]string, len(found))
		for i, f := range found {
			names[i] = f.Code + " " + p.localName(f.Texts)
		}
		return nil, names
	}

	if prod.Code != "" {
		var found []*domain.LocalProduct
		for i := range p.in.Local.Products {
			lp := &p.in.Local.Products[i]
			if free(lp) && strings.EqualFold(strings.TrimSpace(lp.Code), prod.Code) {
				found = append(found, lp)
			}
		}
		if m, c := pick(found); m != nil || c != nil {
			return m, c
		}
	}

	catID := p.catLocal[prod.CategoryKey]
	want := normalize(prod.Texts[p.lang].Name)
	if catID == uuid.Nil || want == "" {
		return nil, nil
	}
	var found []*domain.LocalProduct
	for i := range p.in.Local.Products {
		lp := &p.in.Local.Products[i]
		if free(lp) && lp.CategoryID == catID && normalize(p.localName(lp.Texts)) == want {
			found = append(found, lp)
		}
	}
	return pick(found)
}

func (p *planner) planProduct(prod *domain.CatalogProduct, local *domain.LocalProduct, link *domain.CatalogLink, origin domain.LinkOrigin) {
	hash := prod.ProductHash()
	change := domain.Change{
		Entity: domain.EntityProduct, Key: prod.Key, Name: prod.Texts[p.lang].Name, ParentKey: prod.CategoryKey,
		Hash: hash, Origin: origin, Batch: prod.CategoryKey, Disabled: prod.Disabled, Product: prod,
	}

	if local == nil {
		avail := !prod.Disabled
		change.Action, change.SetVisible, change.SetAvailable = domain.ActionCreate, &prod.Visible, &avail
		p.emit(change)
		return
	}
	change.LocalID = local.ID

	fields := p.productFields(prod, local)
	switch {
	case link == nil: // adoption
		change.Action = domain.ActionLink
		if !prod.Visible && local.Visible {
			change.SetVisible = boolPtr(false)
			fields = append(fields, domain.FieldVisibility)
		}
		if prod.Disabled && local.Available {
			change.SetAvailable = boolPtr(false)
			fields = append(fields, domain.FieldAvailable)
		}
		if textsNeedWrite(prod.Texts, local.Texts) {
			fields = append(fields, domain.FieldNames)
		}
	case link.RetiredAt != nil:
		change.Action = domain.ActionRevive
		change.SetVisible = &prod.Visible
		avail := !prod.Disabled && !link.InventorySoldOut
		change.SetAvailable = &avail
		fields = append(fields, domain.FieldVisibility, domain.FieldAvailable)
		if textsNeedWrite(prod.Texts, local.Texts) {
			fields = append(fields, domain.FieldNames)
		}
	default:
		change.Action = domain.ActionUpdate
		before, known := domain.HashVisible(link.ContentHash)
		switch {
		case known && before != prod.Visible && local.Visible != prod.Visible:
			change.SetVisible = &prod.Visible
			fields = append(fields, domain.FieldVisibility)
		case !known && !prod.Visible && local.Visible:
			change.SetVisible = boolPtr(false)
			fields = append(fields, domain.FieldVisibility)
		}
		if prod.Disabled != link.CatalogDisabled {
			switch {
			case prod.Disabled && local.Available:
				change.SetAvailable = boolPtr(false)
				fields = append(fields, domain.FieldAvailable)
			case !prod.Disabled && !local.Available && !link.InventorySoldOut:
				// HubRise took it off sale earlier, the shop gives it back.
				change.SetAvailable = boolPtr(true)
				fields = append(fields, domain.FieldAvailable)
			}
		}
		if textsNeedWrite(prod.Texts, local.Texts) {
			fields = append(fields, domain.FieldNames)
		}
		if len(fields) == 0 && link.ContentHash == hash && link.CatalogDisabled == prod.Disabled {
			p.d.Unchanged++
			return
		}
		if len(fields) == 0 {
			fields = append(fields, domain.FieldContent)
		}
	}
	change.Fields = fields
	p.emit(change)
}

// productFields lists the HubRise-owned fields of a product that differ from the local row.
func (p *planner) productFields(prod *domain.CatalogProduct, local *domain.LocalProduct) []string {
	var fields []string
	if !prod.Price.Equal(local.Price) {
		fields = append(fields, domain.FieldPrice)
	}
	if catID := p.catLocal[prod.CategoryKey]; catID != local.CategoryID {
		fields = append(fields, domain.FieldCategory)
	}
	if prod.VatCategory != "" && prod.VatCategory != local.VatCategory {
		fields = append(fields, domain.FieldVat)
	}
	if prod.Code != "" && prod.Code != local.Code {
		fields = append(fields, domain.FieldCode)
	}
	return fields
}

func (p *planner) planGroup(prod *domain.CatalogProduct, g *domain.CatalogGroup, productID uuid.UUID) {
	hash := g.Hash()
	change := domain.Change{Entity: domain.EntityChoiceGroup, Key: g.Key, Name: g.Names[p.lang], ParentKey: prod.Key, ProductKey: prod.Key,
		Hash: hash, Batch: prod.CategoryKey, Group: g}

	var local *domain.LocalGroup
	var link *domain.CatalogLink
	if link = p.in.Links.get(domain.EntityChoiceGroup, g.Key); link != nil {
		if local = p.groups[link.LocalID()]; local == nil {
			link = nil
		}
	}
	origin := domain.OriginImport
	if local == nil && productID != uuid.Nil {
		var candidates []string
		local, candidates = p.matchGroup(productID, g)
		if candidates != nil {
			p.d.Conflicts = append(p.d.Conflicts, domain.Conflict{Entity: domain.EntityChoiceGroup, Key: g.Key, Name: g.Names[p.lang],
				Reason: "several local choice groups of the product have this name", Candidates: candidates})
			return
		}
		if local != nil {
			p.claimed[localKey{domain.EntityChoiceGroup, local.ID}] = true
			origin = domain.OriginAdopted
		}
	}
	change.Origin = origin

	if local == nil {
		change.Action = domain.ActionCreate
		p.emit(change)
		p.planChoices(prod, g, uuid.Nil)
		return
	}
	change.LocalID = local.ID
	var fields []string
	if local.Min != g.Min || local.Max != g.Max {
		fields = append(fields, domain.FieldLimits)
	}
	if local.Sort != g.Sort {
		fields = append(fields, domain.FieldOrder)
	}
	if textsNeedWrite(namesAsTexts(g.Names), local.Names) {
		fields = append(fields, domain.FieldNames)
	}
	switch {
	case link == nil:
		change.Action = domain.ActionLink
	case link.RetiredAt != nil:
		change.Action, change.Origin = domain.ActionRevive, link.Origin
	default:
		change.Origin = link.Origin
		if len(fields) == 0 && link.ContentHash == hash {
			p.d.Unchanged++
			p.planChoices(prod, g, local.ID)
			return
		}
		change.Action = domain.ActionUpdate
		if len(fields) == 0 {
			fields = append(fields, domain.FieldContent)
		}
	}
	change.Fields = fields
	p.emit(change)
	p.planChoices(prod, g, local.ID)
}

func (p *planner) matchGroup(productID uuid.UUID, g *domain.CatalogGroup) (*domain.LocalGroup, []string) {
	want := normalize(g.Names[p.lang])
	if want == "" {
		return nil, nil
	}
	var found []*domain.LocalGroup
	for _, lg := range p.groupsOf[productID] {
		if !p.in.Links.linkedLocal(domain.EntityChoiceGroup, lg.ID) && !p.claimed[localKey{domain.EntityChoiceGroup, lg.ID}] &&
			normalize(p.localName(lg.Names)) == want {
			found = append(found, lg)
		}
	}
	switch len(found) {
	case 0:
		return nil, nil
	case 1:
		return found[0], nil
	}
	names := make([]string, len(found))
	for i, f := range found {
		names[i] = p.localName(f.Names)
	}
	return nil, names
}

func (p *planner) planChoices(prod *domain.CatalogProduct, g *domain.CatalogGroup, groupID uuid.UUID) {
	for i := range g.Choices {
		ch := &g.Choices[i]
		hash := ch.Hash()
		change := domain.Change{Entity: domain.EntityChoice, Key: ch.Key, Name: ch.Names[p.lang], ParentKey: g.Key, ProductKey: prod.Key,
			Hash: hash, Batch: prod.CategoryKey, Disabled: ch.Disabled, Choice: ch}

		var local *domain.LocalChoice
		var link *domain.CatalogLink
		if link = p.in.Links.get(domain.EntityChoice, ch.Key); link != nil {
			if local = p.choices[link.LocalID()]; local == nil {
				link = nil
			}
		}
		origin := domain.OriginImport
		if local == nil && groupID != uuid.Nil {
			var candidates []string
			local, candidates = p.matchChoice(groupID, ch)
			if candidates != nil {
				p.d.Conflicts = append(p.d.Conflicts, domain.Conflict{Entity: domain.EntityChoice, Key: ch.Key, Name: ch.Names[p.lang],
					Reason: "several local choices of the group have this name", Candidates: candidates})
				continue
			}
			if local != nil {
				p.claimed[localKey{domain.EntityChoice, local.ID}] = true
				origin = domain.OriginAdopted
			}
		}
		change.Origin = origin

		if local == nil {
			avail := !ch.Disabled
			change.Action, change.SetAvailable = domain.ActionCreate, &avail
			p.emit(change)
			continue
		}
		change.LocalID = local.ID
		var fields []string
		if !local.PriceModifier.Equal(ch.PriceModifier) {
			fields = append(fields, domain.FieldPrice)
		}
		if local.Sort != ch.Sort {
			fields = append(fields, domain.FieldOrder)
		}
		if textsNeedWrite(namesAsTexts(ch.Names), local.Names) {
			fields = append(fields, domain.FieldNames)
		}
		switch {
		case link == nil:
			change.Action = domain.ActionLink
			if ch.Disabled && !local.Unavailable {
				change.SetAvailable = boolPtr(false)
				fields = append(fields, domain.FieldAvailable)
			}
		case link.RetiredAt != nil:
			change.Action, change.Origin = domain.ActionRevive, link.Origin
			avail := !ch.Disabled && !link.InventorySoldOut
			change.SetAvailable = &avail
			fields = append(fields, domain.FieldAvailable)
		default:
			change.Origin = link.Origin
			if ch.Disabled != link.CatalogDisabled {
				switch {
				case ch.Disabled && !local.Unavailable:
					change.SetAvailable = boolPtr(false)
					fields = append(fields, domain.FieldAvailable)
				case !ch.Disabled && local.Unavailable && !link.InventorySoldOut:
					change.SetAvailable = boolPtr(true)
					fields = append(fields, domain.FieldAvailable)
				}
			}
			if len(fields) == 0 && link.ContentHash == hash && link.CatalogDisabled == ch.Disabled {
				p.d.Unchanged++
				continue
			}
			change.Action = domain.ActionUpdate
			if len(fields) == 0 {
				fields = append(fields, domain.FieldContent)
			}
		}
		change.Fields = fields
		p.emit(change)
	}
}

func (p *planner) matchChoice(groupID uuid.UUID, ch *domain.CatalogChoice) (*domain.LocalChoice, []string) {
	want := normalize(ch.Names[p.lang])
	if want == "" {
		return nil, nil
	}
	var found []*domain.LocalChoice
	for _, lc := range p.choicesOf[groupID] {
		if !p.in.Links.linkedLocal(domain.EntityChoice, lc.ID) && !p.claimed[localKey{domain.EntityChoice, lc.ID}] &&
			normalize(p.localName(lc.Names)) == want {
			found = append(found, lc)
		}
	}
	switch len(found) {
	case 0:
		return nil, nil
	case 1:
		return found[0], nil
	}
	names := make([]string, len(found))
	for i, f := range found {
		names[i] = p.localName(f.Names)
	}
	return nil, names
}

// ---- retirements ----

// planRetirements retires the linked entries the catalog no longer has. Nothing is deleted: a
// product is hidden and unavailable, a choice unavailable, a choice group stops requiring a
// selection, a category loses its link.
func (p *planner) planRetirements() {
	present := map[linkKey]bool{}
	for _, c := range p.in.Catalog.Categories {
		present[linkKey{domain.EntityCategory, c.Key}] = true
	}
	for i := range p.in.Catalog.Products {
		prod := &p.in.Catalog.Products[i]
		present[linkKey{domain.EntityProduct, prod.Key}] = true
		for _, g := range prod.Groups {
			present[linkKey{domain.EntityChoiceGroup, g.Key}] = true
			for _, ch := range g.Choices {
				present[linkKey{domain.EntityChoice, ch.Key}] = true
			}
		}
	}
	// A conflicted product is still in the catalog: it has no link, so nothing of it retires.
	for _, t := range []domain.EntityType{domain.EntityProduct, domain.EntityChoiceGroup, domain.EntityChoice, domain.EntityCategory} {
		for _, l := range p.in.Links.byType[t] {
			if l.RetiredAt != nil || present[linkKey{t, l.HubriseKey}] {
				continue
			}
			name, exists := p.localLabel(l)
			if !exists {
				continue
			}
			p.emit(domain.Change{Entity: t, Action: domain.ActionRetire, Key: l.HubriseKey, Name: name, LocalID: l.LocalID(), Batch: domain.RetireBatch})
			if t == domain.EntityProduct {
				p.d.RetiringProducts++
			}
		}
	}
}

// localLabel names the local row of a link, and reports whether it still exists.
func (p *planner) localLabel(l *domain.CatalogLink) (string, bool) {
	switch l.EntityType {
	case domain.EntityProduct:
		if lp := p.prods[l.LocalID()]; lp != nil {
			return p.localName(lp.Texts), true
		}
	case domain.EntityChoiceGroup:
		if lg := p.groups[l.LocalID()]; lg != nil {
			return p.localName(lg.Names), true
		}
	case domain.EntityChoice:
		if lc := p.choices[l.LocalID()]; lc != nil {
			return p.localName(lc.Names), true
		}
	case domain.EntityCategory:
		if lc := p.cats[l.LocalID()]; lc != nil {
			return p.localName(lc.Names), true
		}
	}
	return "", false
}

func boolPtr(b bool) *bool { return &b }
