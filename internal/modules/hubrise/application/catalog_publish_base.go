package application

import (
	"strings"

	"tsb-service/internal/modules/hubrise/domain"
)

// baseCatalog is the HubRise catalog as the publisher reads it: generic JSON, normalized to the
// upload spelling so that it can be merged with what the shop owns and sent back. A GET answers with
// ids where an upload uses refs (parent_id, category_id, option_list_ids), with ids on every element,
// with null for every absent optional field, with a deprecated "type" on option lists, and the
// documentation's examples spell option_lists and image_ids differently; none of that goes back.
// Every other key, at any depth, is left exactly as HubRise gave it.
type baseCatalog struct {
	extra      jobj
	variants   []jobj
	categories []jobj
	products   []jobj
	lists      []jobj
	deals      []jobj
	discounts  []jobj
	charges    []jobj
	// present records the top level keys the source had, so an empty list HubRise sent stays empty.
	present map[string]bool
	// issues are the blockers found while normalizing (a category or option list without a ref).
	issues []domain.Warning
}

var topLevelLists = []string{"variants", "categories", "products", "option_lists", "options_lists", "deals", "discounts", "charges"}

// parseBaseCatalog reads the data of GET /catalogs/:id.
func parseBaseCatalog(raw []byte) (*baseCatalog, error) {
	top, err := decodeObject(raw)
	if err != nil {
		return nil, err
	}
	b := &baseCatalog{extra: jobj{}, present: map[string]bool{}}
	for k, v := range top {
		if !isTopLevelList(k) {
			b.extra[k] = v
		}
	}

	// Ids first: the elements lose theirs while they are normalized.
	catRefByID, listRefByID := map[string]string{}, map[string]string{}
	for _, c := range jobjs(top, "categories") {
		if id := jstr(c, "id"); id != "" {
			catRefByID[id] = jstr(c, "ref")
		}
	}
	listsKey := "option_lists"
	if _, ok := top[listsKey]; !ok {
		listsKey = "options_lists"
	}
	for _, l := range jobjs(top, listsKey) {
		if id := jstr(l, "id"); id != "" {
			listRefByID[id] = jstr(l, "ref")
		}
	}
	for _, k := range topLevelLists {
		if _, ok := top[k]; ok {
			b.present[canonicalKey(k)] = true
		}
	}

	for _, c := range jobjs(top, "categories") {
		b.categories = append(b.categories, b.normCategory(c, catRefByID))
	}
	for _, p := range jobjs(top, "products") {
		b.products = append(b.products, b.normProduct(p, catRefByID, listRefByID))
	}
	for _, l := range jobjs(top, listsKey) {
		b.lists = append(b.lists, b.normList(l))
	}
	for _, d := range jobjs(top, "deals") {
		b.deals = append(b.deals, normDeal(d, catRefByID))
	}
	b.variants = normPlain(jobjs(top, "variants"))
	b.discounts = normPlain(jobjs(top, "discounts"))
	b.charges = normPlain(jobjs(top, "charges"))
	return b, nil
}

func isTopLevelList(k string) bool {
	for _, t := range topLevelLists {
		if t == k {
			return true
		}
	}
	return false
}

func canonicalKey(k string) string {
	if k == "options_lists" {
		return "option_lists"
	}
	return k
}

func (b *baseCatalog) issue(code domain.WarningCode, key, msg string) {
	b.issues = append(b.issues, domain.Warning{Code: code, Key: key, Message: msg})
}

// foldKey moves the value of an alternative spelling to the upload spelling.
func foldKey(o jobj, from, to string) {
	if v, ok := o[from]; ok {
		if _, has := o[to]; !has {
			o[to] = v
		}
		delete(o, from)
	}
}

func (b *baseCatalog) normCategory(c jobj, catRefByID map[string]string) jobj {
	c = cloneObj(c)
	if jstr(c, "ref") == "" {
		b.issue(domain.BlockBaseMissingRef, jstr(c, "id"), "the HubRise category "+jstr(c, "name")+" has no ref, so it cannot be published around")
	}
	if pid := jstr(c, "parent_id"); pid != "" {
		if _, has := c["parent_ref"]; !has {
			if ref := catRefByID[pid]; ref != "" {
				c["parent_ref"] = ref
			}
		}
	}
	delete(c, "parent_id")
	delete(c, "id")
	foldKey(c, "images_ids", "image_ids")
	dropNulls(c)
	return c
}

func (b *baseCatalog) normProduct(p jobj, catRefByID, listRefByID map[string]string) jobj {
	p = cloneObj(p)
	if cid := jstr(p, "category_id"); cid != "" {
		if _, has := p["category_ref"]; !has {
			if ref := catRefByID[cid]; ref != "" {
				p["category_ref"] = ref
			}
		}
	}
	delete(p, "category_id")
	delete(p, "id")
	foldKey(p, "images_ids", "image_ids")
	dropNulls(p)
	skus := make([]any, 0)
	for _, s := range jobjs(p, "skus") {
		s = cloneObj(s)
		delete(s, "id")
		delete(s, "product_id")
		if ids, ok := s["option_list_ids"].([]any); ok {
			if _, has := s["option_list_refs"]; !has {
				refs := make([]any, 0, len(ids))
				for _, id := range ids {
					sid, _ := id.(string)
					if ref := listRefByID[sid]; ref != "" {
						refs = append(refs, ref)
					} else {
						b.issue(domain.BlockBaseMissingRef, jstr(s, "ref"), "a SKU names an option list that has no ref")
					}
				}
				s["option_list_refs"] = refs
			}
		}
		delete(s, "option_list_ids")
		dropNulls(s)
		skus = append(skus, s)
	}
	p["skus"] = skus
	return p
}

func (b *baseCatalog) normList(l jobj) jobj {
	l = cloneObj(l)
	if jstr(l, "ref") == "" {
		b.issue(domain.BlockBaseMissingRef, jstr(l, "id"), "the HubRise option list "+jstr(l, "name")+" has no ref, so it cannot be published around")
	}
	delete(l, "id")
	delete(l, "type") // deprecated and read only: min and max say the same
	dropNulls(l)
	opts := make([]any, 0)
	for _, o := range jobjs(l, "options") {
		o = cloneObj(o)
		delete(o, "id")
		delete(o, "option_list_id")
		dropNulls(o)
		opts = append(opts, o)
	}
	l["options"] = opts
	return l
}

func normDeal(d jobj, catRefByID map[string]string) jobj {
	d = cloneObj(d)
	if cid := jstr(d, "category_id"); cid != "" {
		if _, has := d["category_ref"]; !has {
			if ref := catRefByID[cid]; ref != "" {
				d["category_ref"] = ref
			}
		}
	}
	delete(d, "category_id")
	delete(d, "id")
	// HubRise changes every id on a PUT and reads deal lines by SKU ref: a stale id must not travel.
	for _, line := range jobjs(d, "lines") {
		delete(line, "id")
		for _, sk := range jobjs(line, "skus") {
			delete(sk, "id")
		}
	}
	foldKey(d, "images_ids", "image_ids")
	dropNulls(d)
	return d
}

func normPlain(list []jobj) []jobj {
	out := make([]jobj, 0, len(list))
	for _, e := range list {
		e = cloneObj(e)
		delete(e, "id")
		foldKey(e, "images_ids", "image_ids")
		dropNulls(e)
		out = append(out, e)
	}
	return out
}

// clone deep copies the catalog, so a merge can mutate it freely.
func (b *baseCatalog) clone() *baseCatalog {
	cp := func(in []jobj) []jobj {
		out := make([]jobj, len(in))
		for i, e := range in {
			out[i] = cloneObj(e)
		}
		return out
	}
	present := make(map[string]bool, len(b.present))
	for k, v := range b.present {
		present[k] = v
	}
	return &baseCatalog{
		extra: cloneObj(b.extra), variants: cp(b.variants), categories: cp(b.categories), products: cp(b.products),
		lists: cp(b.lists), deals: cp(b.deals), discounts: cp(b.discounts), charges: cp(b.charges),
		present: present, issues: append([]domain.Warning(nil), b.issues...),
	}
}

// data assembles the upload form: the unknown top level keys as they were, and each list when it
// has elements or the source had it.
func (b *baseCatalog) data() jobj {
	out := cloneObj(b.extra)
	put := func(key string, list []jobj) {
		if len(list) > 0 || b.present[key] {
			out[key] = objsToAny(list)
		}
	}
	put("variants", b.variants)
	put("categories", b.categories)
	put("products", b.products)
	put("option_lists", b.lists)
	put("deals", b.deals)
	put("discounts", b.discounts)
	put("charges", b.charges)
	return out
}

// lowerTags returns the trimmed lower case tags of an element.
func tagSet(o jobj) []string {
	var out []string
	for _, t := range jstrs(o, "tags") {
		out = append(out, strings.ToLower(strings.TrimSpace(t)))
	}
	return out
}
