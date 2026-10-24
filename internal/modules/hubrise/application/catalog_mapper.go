package application

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/shopspring/decimal"

	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/pkg/hubrise"
)

// MapOptions are the settings that shape the neutral catalog.
type MapOptions struct {
	// Language is the catalog language, the language the HubRise names are written in (fr when
	// empty or unknown).
	Language string
	// VariantRef selects the price list: the price overrides of this variant apply. Empty means
	// the base price only.
	VariantRef string
}

// catalogLanguages are the languages of the shop.
var catalogLanguages = []string{domain.LanguageFR, domain.LanguageEN, domain.LanguageNL, domain.LanguageZH}

// maxMultipleSelections is the group maximum when HubRise allows picking an option several times
// and sets no limit.
const maxMultipleSelections = 99

// Tag conventions on products and SKUs (the owner's POS and the HubRise back office let people tag
// them). Tags only seed a new product; they never overwrite what the shop decided afterwards.
var (
	tagsHidden      = []string{"hidden", "web:hidden"}
	tagsHalal       = []string{"halal"}
	tagsVegetarian  = []string{"vegetarian", "vegan"}
	tagsSpicy       = []string{"spicy"}
	tagsNotDiscount = []string{"no-discount", "nodiscount", "not-discountable"}
)

// categoryTranslationsKey is the key of the translations in the JSON a category description may hold.
const categoryTranslationsKey = "translations"

// MapCatalog reduces a HubRise catalog to the neutral catalog: the category tree is flattened to the
// categories that hold products, every SKU becomes a product with its own option groups, prices are
// resolved for the configured variant, and everything the import cannot express is reported as a
// warning instead of being guessed. Entries that cannot be imported (no ref, unreadable price,
// unknown category) are skipped with a warning.
func MapCatalog(c *hubrise.Catalog, opt MapOptions) *domain.Catalog {
	lang := opt.Language
	if !slices.Contains(catalogLanguages, lang) {
		lang = domain.LanguageFR
	}
	m := &mapper{lang: lang, variant: opt.VariantRef, out: &domain.Catalog{Language: lang, Categories: []domain.CatalogCategory{}, Products: []domain.CatalogProduct{}}}
	if c != nil && c.Data != nil {
		m.run(c.Data)
	}
	return m.out
}

type mapper struct {
	lang    string
	variant string
	out     *domain.Catalog
}

func (m *mapper) warn(code domain.WarningCode, key, msg string) {
	m.out.Warnings = append(m.out.Warnings, domain.Warning{Code: code, Key: key, Message: msg})
}

func (m *mapper) run(d *hubrise.CatalogData) {
	cats := m.walkCategories(d.Categories)

	listsByID, listsByRef := map[string]*hubrise.OptionList{}, map[string]*hubrise.OptionList{}
	for i := range d.OptionLists {
		l := &d.OptionLists[i]
		if l.ID != "" {
			listsByID[l.ID] = l
		}
		if l.Ref != "" {
			listsByRef[l.Ref] = l
		}
	}

	used := map[string]bool{} // category keys that hold products
	seenSku := map[string]bool{}
	for i := range d.Products {
		p := &d.Products[i]
		cat, ok := cats.find(p.CategoryID, p.CategoryRef)
		if !ok {
			m.warn(domain.WarnUnknownCategory, p.Ref, "product "+p.Name+" has no known category, skipped")
			continue
		}
		for j := range p.Skus {
			sku := &p.Skus[j]
			if sku.Ref == "" {
				m.warn(domain.WarnMissingRef, p.Ref, "a SKU of "+p.Name+" has no ref, skipped")
				continue
			}
			if seenSku[sku.Ref] {
				m.warn(domain.WarnDuplicateRef, sku.Ref, "SKU ref "+sku.Ref+" is used twice, the second is skipped")
				continue
			}
			seenSku[sku.Ref] = true
			if prod, ok := m.product(p, sku, cat.Ref, listsByID, listsByRef); ok {
				m.out.Products = append(m.out.Products, prod)
				used[cat.Ref] = true
			}
		}
	}

	for _, c := range cats.ordered {
		if !used[c.Ref] {
			continue // parents without products are ignored
		}
		m.out.Categories = append(m.out.Categories, domain.CatalogCategory{
			Key: c.Ref, HubriseID: c.ID, Names: m.categoryNames(c), Order: cats.position[c.Ref],
		})
	}
}

// categoryIndex is the flattened category tree.
type categoryIndex struct {
	ordered  []hubrise.Category // depth first
	position map[string]int
	byID     map[string]*hubrise.Category
	byRef    map[string]*hubrise.Category
}

func (ci *categoryIndex) find(id, ref string) (*hubrise.Category, bool) {
	if c, ok := ci.byID[id]; ok && id != "" {
		return c, true
	}
	if c, ok := ci.byRef[ref]; ok && ref != "" {
		return c, true
	}
	return nil, false
}

// walkCategories orders the categories depth first (a parent, then its children, in catalog order).
func (m *mapper) walkCategories(in []hubrise.Category) *categoryIndex {
	ci := &categoryIndex{position: map[string]int{}, byID: map[string]*hubrise.Category{}, byRef: map[string]*hubrise.Category{}}
	for i := range in {
		c := &in[i]
		if c.Ref == "" {
			m.warn(domain.WarnMissingRef, c.ID, "category "+c.Name+" has no ref, skipped")
			continue
		}
		ci.byRef[c.Ref] = c
		if c.ID != "" {
			ci.byID[c.ID] = c
		}
	}
	children := map[string][]*hubrise.Category{}
	var roots []*hubrise.Category
	for i := range in {
		c := &in[i]
		if c.Ref == "" {
			continue
		}
		parent, hasParent := ci.find(c.ParentID, c.ParentRef)
		if hasParent && parent.Ref != c.Ref {
			children[parent.Ref] = append(children[parent.Ref], c)
		} else {
			roots = append(roots, c)
		}
	}
	visited := map[string]bool{}
	var walk func(c *hubrise.Category)
	walk = func(c *hubrise.Category) {
		if visited[c.Ref] { // a cycle in the parents
			return
		}
		visited[c.Ref] = true
		ci.position[c.Ref] = len(ci.ordered)
		ci.ordered = append(ci.ordered, *c)
		for _, ch := range children[c.Ref] {
			walk(ch)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	for i := range in { // categories only reachable through a cycle
		if c := &in[i]; c.Ref != "" && !visited[c.Ref] {
			walk(c)
		}
	}
	return ci
}

// categoryNames: the category name in the catalog language, and the other languages from the
// description convention {"translations": {"fr": "...", "en": "..."}}.
func (m *mapper) categoryNames(c hubrise.Category) map[string]string {
	names := map[string]string{}
	desc := strings.TrimSpace(c.Description)
	if strings.HasPrefix(desc, "{") {
		var payload map[string]map[string]string
		if err := json.Unmarshal([]byte(desc), &payload); err == nil {
			for lang, name := range payload[categoryTranslationsKey] {
				if name = strings.TrimSpace(name); name != "" && slices.Contains(catalogLanguages, lang) {
					names[lang] = name
				}
			}
		}
	}
	if name := strings.TrimSpace(c.Name); name != "" {
		names[m.lang] = name
	}
	return names
}

func (m *mapper) product(p *hubrise.Product, sku *hubrise.Sku, categoryKey string, byID, byRef map[string]*hubrise.OptionList) (domain.CatalogProduct, bool) {
	price, ok := m.price(sku.Ref, sku.Price, sku.PriceOverrides)
	if !ok {
		return domain.CatalogProduct{}, false
	}

	name := strings.TrimSpace(p.Name)
	if skuName := strings.TrimSpace(sku.Name); skuName != "" {
		name += " " + skuName
	}
	texts := map[string]domain.Text{}
	custom := parseCustomFields(sku.CustomFields)
	for lang, t := range custom.translations {
		if lang != m.lang && strings.TrimSpace(t.Name) != "" {
			texts[lang] = domain.Text{Name: strings.TrimSpace(t.Name), Description: strings.TrimSpace(t.Description)}
		}
	}
	description := strings.TrimSpace(p.Description)
	if description == "" {
		description = strings.TrimSpace(custom.translations[m.lang].Description)
	}
	texts[m.lang] = domain.Text{Name: name, Description: description}

	disabled := !sku.Restrictions.IsEnabled() || m.offVariant(sku.Restrictions)
	m.reportRestrictions(sku.Ref, sku.Restrictions)

	tags := lowerTags(p.Tags, sku.Tags)
	vat := m.vat(sku.Ref, custom.vat, p.TaxRate)
	cp := domain.CatalogProduct{
		Key: sku.Ref, HubriseID: sku.ID, CategoryKey: categoryKey, Texts: texts, Price: price,
		Code: custom.code, VatCategory: vat,
		Visible:  !disabled && !hasTag(tags, tagsHidden) && vat != "",
		Disabled: disabled,
		Halal:    hasTag(tags, tagsHalal), Vegetarian: hasTag(tags, tagsVegetarian), Spicy: hasTag(tags, tagsSpicy),
		Discountable: !hasTag(tags, tagsNotDiscount),
		ImageID:      firstNonEmpty(p.ImageIDs, p.ImagesIDs),
	}

	listIDs := sku.OptionListIDs
	if len(listIDs) == 0 {
		listIDs = sku.OptionListRefs
	}
	for i, id := range listIDs {
		list := byID[id]
		if list == nil {
			list = byRef[id]
		}
		if list == nil {
			m.warn(domain.WarnUnknownOptionList, sku.Ref, "SKU "+sku.Ref+" names an option list the catalog does not have, skipped")
			continue
		}
		if g, ok := m.group(sku.Ref, list, i); ok {
			cp.Groups = append(cp.Groups, g)
		}
	}
	return cp, true
}

func (m *mapper) group(skuRef string, list *hubrise.OptionList, sort int) (domain.CatalogGroup, bool) {
	if list.Ref == "" {
		m.warn(domain.WarnMissingRef, skuRef, "an option list of "+skuRef+" has no ref, skipped")
		return domain.CatalogGroup{}, false
	}
	key := skuRef + "|" + list.Ref
	g := domain.CatalogGroup{Key: key, ListRef: list.Ref, HubriseID: list.ID, Sort: sort,
		Names: map[string]string{m.lang: strings.TrimSpace(list.Name)}}
	seen := map[string]bool{}
	for i := range list.Options {
		o := &list.Options[i]
		if o.Ref == "" {
			m.warn(domain.WarnMissingRef, key, "an option of "+list.Name+" has no ref, skipped")
			continue
		}
		if seen[o.Ref] {
			m.warn(domain.WarnDuplicateRef, key+"|"+o.Ref, "option ref "+o.Ref+" is used twice in "+list.Name+", the second is skipped")
			continue
		}
		seen[o.Ref] = true
		g.Choices = append(g.Choices, m.choice(key, o, len(g.Choices)))
	}
	if len(g.Choices) == 0 {
		m.warn(domain.WarnEmptyOptionList, key, "option list "+list.Name+" has no usable option, skipped")
		return domain.CatalogGroup{}, false
	}

	g.Max = maxSelections(list, len(g.Choices))
	g.Min = max(list.MinSelections, 0)
	if g.Min > g.Max {
		m.warn(domain.WarnSelectionLimits, key, "option list "+list.Name+": minimum above maximum, minimum lowered")
		g.Min = g.Max
	}
	return g, true
}

// maxSelections: HubRise's null or zero maximum means "no limit": the number of options, or a
// generous 99 when the same option may be picked several times.
func maxSelections(list *hubrise.OptionList, options int) int {
	if list.MaxSelections != nil && *list.MaxSelections > 0 {
		return *list.MaxSelections
	}
	if list.MultipleSelection {
		return maxMultipleSelections
	}
	return options
}

func (m *mapper) choice(groupKey string, o *hubrise.Option, sort int) domain.CatalogChoice {
	key := groupKey + "|" + o.Ref
	price := decimal.Zero
	if o.Price != "" {
		if p, ok := m.price(key, o.Price, o.PriceOverrides); ok {
			price = p
		}
	}
	if price.IsNegative() {
		m.warn(domain.WarnNegativePrice, key, "option "+o.Name+" has a negative price, raised to zero")
		price = decimal.Zero
	}
	m.reportRestrictions(key, o.Restrictions)
	return domain.CatalogChoice{
		Key: key, Ref: o.Ref, HubriseID: o.ID, Names: map[string]string{m.lang: strings.TrimSpace(o.Name)},
		PriceModifier: price, Sort: sort,
		Disabled: !o.Restrictions.IsEnabled() || m.offVariant(o.Restrictions),
	}
}

// price resolves base through the overrides of the configured variant: the last matching rule
// wins. A rule that depends on a time window, a day or a service type is ignored and reported, as
// is a rule for another variant.
func (m *mapper) price(key string, base hubrise.Money, overrides []hubrise.PriceOverride) (decimal.Decimal, bool) {
	price, err := base.Decimal()
	if err != nil {
		m.warn(domain.WarnInvalidPrice, key, "unreadable price "+string(base)+", skipped")
		return decimal.Zero, false
	}
	for i := range overrides {
		o := &overrides[i]
		if o.DOW != "" || o.StartTime != "" || o.EndTime != "" || o.StartDate != "" || o.EndDate != "" ||
			len(o.ServiceTypes) > 0 || len(o.ServiceTypeRefs) > 0 {
			m.warn(domain.WarnPriceWindow, key, "a price rule with a time, date or service condition is ignored")
			continue
		}
		if !m.variantMatches(o.VariantRefs) {
			continue
		}
		p, err := o.Price.Decimal()
		if err != nil {
			m.warn(domain.WarnInvalidPrice, key, "unreadable price override "+string(o.Price)+", ignored")
			continue
		}
		price = p
	}
	return price, true
}

// variantMatches: a rule without variants applies to every price list, a rule with variants only to
// the configured one.
func (m *mapper) variantMatches(refs []string) bool {
	if len(refs) == 0 {
		return true
	}
	return m.variant != "" && slices.Contains(refs, m.variant)
}

// offVariant: the entry is restricted to variants that do not include the configured one.
func (m *mapper) offVariant(r *hubrise.Restrictions) bool {
	return r != nil && m.variant != "" && len(r.VariantRefs) > 0 && !slices.Contains(r.VariantRefs, m.variant)
}

// reportRestrictions warns about the restrictions the import does not model.
func (m *mapper) reportRestrictions(key string, r *hubrise.Restrictions) {
	if r == nil {
		return
	}
	if r.DOW != "" || r.StartTime != "" || r.EndTime != "" || r.StartDate != "" || r.EndDate != "" ||
		len(r.ServiceTypes) > 0 || len(r.ServiceTypeRefs) > 0 || r.MaxPerOrder != nil || r.MaxPerCustomer != nil || r.MinOrderAmount != "" {
		m.warn(domain.WarnRestrictionIgnored, key, "a time, service or quantity restriction is not modelled and is ignored")
	}
}

// vat derives the VAT category: the SKU custom field wins; otherwise the tax rates of the product
// per service type map to a category when they are exactly the Belgian ones. Anything else is
// unmapped (empty) because VAT drives the invoices.
func (m *mapper) vat(key, custom string, rate *hubrise.TaxRate) string {
	if domain.ValidVatCategory(custom) {
		return custom
	}
	if v := vatFromRates(rate); v != "" {
		return v
	}
	m.warn(domain.WarnVATUnmapped, key, "no VAT category can be derived for "+key+": the product is hidden until it has a vat_category custom field or standard tax rates")
	return ""
}

func vatFromRates(rate *hubrise.TaxRate) string {
	if rate == nil || rate.Collection == "" || rate.EatIn == "" {
		return ""
	}
	collection, err1 := hubrise.ParseTaxRate(rate.Collection)
	eatIn, err2 := hubrise.ParseTaxRate(rate.EatIn)
	if err1 != nil || err2 != nil {
		return ""
	}
	is := func(d decimal.Decimal, n int64) bool { return d.Equal(decimal.NewFromInt(n)) }
	switch {
	case is(collection, 6) && is(eatIn, 12):
		return domain.VatFood
	case is(collection, 21) && is(eatIn, 21):
		return domain.VatBeverage
	case is(collection, 0) && is(eatIn, 0):
		return domain.VatZeroRated
	}
	return ""
}

type parsedCustomFields struct {
	code         string
	vat          string
	translations map[string]domain.Text
}

// parseCustomFields reads the conventions of the owner's POS from a SKU's custom fields; malformed
// content is ignored.
func parseCustomFields(raw json.RawMessage) parsedCustomFields {
	out := parsedCustomFields{translations: map[string]domain.Text{}}
	if len(raw) == 0 {
		return out
	}
	var fields struct {
		Code         any                          `json:"code"`
		VAT          string                       `json:"vat_category"`
		Translations map[string]map[string]string `json:"translations"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return out
	}
	if code, ok := fields.Code.(string); ok {
		out.code = strings.TrimSpace(code)
	}
	out.vat = strings.TrimSpace(fields.VAT)
	for lang, t := range fields.Translations {
		if slices.Contains(catalogLanguages, lang) {
			out.translations[lang] = domain.Text{Name: t["name"], Description: t["description"]}
		}
	}
	return out
}

func lowerTags(lists ...[]string) []string {
	var out []string
	for _, l := range lists {
		for _, t := range l {
			out = append(out, strings.ToLower(strings.TrimSpace(t)))
		}
	}
	return out
}

func hasTag(tags, wanted []string) bool {
	return slices.ContainsFunc(wanted, func(w string) bool { return slices.Contains(tags, w) })
}

func firstNonEmpty(lists ...[]string) string {
	for _, l := range lists {
		for _, s := range l {
			if s != "" {
				return s
			}
		}
	}
	return ""
}
