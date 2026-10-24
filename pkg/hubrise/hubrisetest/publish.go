package hubrisetest

import (
	"bytes"
	"crypto/md5" //nolint:gosec // HubRise images carry an MD5, it is not used for security
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"tsb-service/pkg/hubrise"
)

// This file is the write side of the catalog: PUT /catalogs/:id (clear and recreate), the image
// endpoints, and the control methods that play the account/scope situations the publisher has to
// survive. The catalog content is kept as generic JSON, so keys the typed structs do not model
// (and a real HubRise does model: deals, discounts, price overrides, custom fields) survive a
// round trip and tests can prove the publisher preserves them. Unknown keys are stored verbatim,
// which a real HubRise might not do; that is a convenience of the mock, not a rule to rely on.

type obj = map[string]any

// CatalogLevel says whose catalog the mock serves.
type CatalogLevel int

const (
	// LevelLocation is a catalog of the location: a location token may modify it.
	LevelLocation CatalogLevel = iota
	// LevelAccount is a catalog of the account: a location token can read it, and modifying it
	// answers 401 as the real API does.
	LevelAccount
)

// SetCatalogLevel switches the catalog between a location and an account catalog.
func (s *Server) SetCatalogLevel(l CatalogLevel) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accountLevel = l == LevelAccount
}

// SetTokenScopes limits the permissions ("catalog.read", "orders.write") of a token. Called with no
// scope it grants everything again. Only catalog.write is enforced (PUT /catalogs/:id and image
// uploads answer 403 without it).
func (s *Server) SetTokenScopes(token string, scopes ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.clients[token]; c != nil {
		c.scopes = slices.Clone(scopes)
	}
}

func (c *client) can(permission string) bool {
	if c.scopes == nil {
		return true
	}
	resource, _, _ := strings.Cut(permission, ".")
	return slices.Contains(c.scopes, permission) || slices.Contains(c.scopes, resource+".*")
}

// CatalogPuts is how many PUT /catalogs/:id the mock accepted.
func (s *Server) CatalogPuts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.puts
}

// HeavyUsed is how many requests of the 10 a minute list bucket (GET /catalogs/:id, order and
// customer lists) the token spent in the last minute.
func (s *Server) HeavyUsed(token string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	n := 0
	for _, t := range s.heavy[token] {
		if t.After(now.Add(-60e9)) {
			n++
		}
	}
	return n
}

// CatalogData returns the catalog content as the API serves it (ids included).
func (s *Server) CatalogData() json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return bytes.Clone(s.dataJSON())
}

// dataJSON is the served content: the raw stored form after a PUT, the typed catalog before.
func (s *Server) dataJSON() json.RawMessage {
	if s.rawData != nil {
		return s.rawData
	}
	return mustJSON(s.catalog.Data)
}

// ReplaceCatalogRaw replaces the content from raw JSON as the other side (the EPOS or the menu
// editor), with the same validation and storage as a PUT, and tells the clients that registered for
// catalog.update. Image ids are not checked.
func (s *Server) ReplaceCatalogRaw(data json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.storeCatalog(data, false); err != nil {
		return fmt.Errorf("hubrisetest: replace catalog: %s", errString(err))
	}
	s.emit(EPOSToken, "catalog", "update", func(e *hubrise.Event, _ string) { e.CatalogID = s.catalog.ID })
	return nil
}

func getCatalog(s *Server, _ *client, r *http.Request, _ []byte, _ http.Header) (int, any, *apiError) {
	if r.PathValue("id") != s.catalog.ID {
		return 0, nil, errStatus(http.StatusNotFound, "catalog not found")
	}
	hide := r.URL.Query().Get("hide_data") == "true"
	if s.rawData == nil && !s.accountLevel {
		out := s.catalog
		if hide {
			out.Data = nil
		}
		return http.StatusOK, out, nil
	}
	out := hubrise.RawCatalog{ID: s.catalog.ID, AccountID: s.catalog.AccountID, Name: s.catalog.Name, CreatedAt: s.catalog.CreatedAt}
	if !s.accountLevel {
		out.LocationID = s.catalog.LocationID
	}
	if !hide {
		out.Data = s.dataJSON()
	}
	return http.StatusOK, out, nil
}

func putCatalog(s *Server, c *client, r *http.Request, body []byte, _ http.Header) (int, any, *apiError) {
	if r.PathValue("id") != s.catalog.ID {
		return 0, nil, errStatus(http.StatusNotFound, "catalog not found")
	}
	if !c.can("catalog.write") {
		return 0, nil, errStatus(http.StatusForbidden, "the token has no catalog.write scope")
	}
	if s.accountLevel {
		return 0, nil, errStatus(http.StatusUnauthorized, "an account catalog needs an account access token")
	}
	var in struct {
		Name *string         `json:"name"`
		Data json.RawMessage `json:"data"`
	}
	if err := decodeStrict(body, &in); err != nil {
		return 0, nil, err
	}
	if len(bytes.TrimSpace(in.Data)) == 0 || bytes.Equal(bytes.TrimSpace(in.Data), []byte("null")) {
		return 0, nil, errField("data", "is required")
	}
	if err := s.storeCatalog(in.Data, true); err != nil {
		return 0, nil, err
	}
	if in.Name != nil && *in.Name != "" {
		s.catalog.Name = *in.Name
	}
	s.puts++
	s.emit(c.token, "catalog", "update", func(e *hubrise.Event, _ string) { e.CatalogID = s.catalog.ID })
	return http.StatusOK, hubrise.RawCatalog{
		ID: s.catalog.ID, LocationID: s.catalog.LocationID, AccountID: s.catalog.AccountID,
		Name: s.catalog.Name, CreatedAt: s.catalog.CreatedAt, Data: s.dataJSON(),
	}, nil
}

// storeCatalog validates an upload and stores it as HubRise does: cleared and recreated, with new
// ids for every element.
func (s *Server) storeCatalog(raw json.RawMessage, checkImages bool) *apiError {
	data, err := decodeObject(raw)
	if err != nil {
		return errField("data", "must be a JSON object: "+err.Error())
	}
	if e := s.validateUpload(raw, data, checkImages); e != nil {
		return e
	}
	stored := s.recreate(data)
	out, mErr := json.Marshal(stored)
	if mErr != nil {
		return errField("data", mErr.Error())
	}
	var typed hubrise.CatalogData
	if uErr := json.Unmarshal(out, &typed); uErr != nil {
		return errField("data", uErr.Error())
	}
	s.rawData = out
	s.catalog.Data = &typed
	s.markAttached(stored)
	return nil
}

func decodeObject(raw []byte) (obj, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	o, ok := v.(obj)
	if !ok {
		return nil, fmt.Errorf("got %T", v)
	}
	return o, nil
}

// ---- generic accessors ----

func objs(o obj, key string) []obj {
	list, _ := o[key].([]any)
	out := make([]obj, 0, len(list))
	for _, v := range list {
		if m, ok := v.(obj); ok {
			out = append(out, m)
		}
	}
	return out
}

func text(o obj, key string) string {
	switch v := o[key].(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	}
	return ""
}

func strs(o obj, key string) []string {
	list, _ := o[key].([]any)
	out := make([]string, 0, len(list))
	for _, v := range list {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func intOf(o obj, key string) (int, bool) {
	n, ok := o[key].(json.Number)
	if !ok {
		return 0, false
	}
	i, err := n.Int64()
	if err != nil {
		return 0, false
	}
	return int(i), true
}

var (
	barcodeRe = regexp.MustCompile(`^(\d{8}|\d{12}|\d{13})$`)
	imageType = map[string]bool{"image/jpeg": true, "image/png": true, "image/webp": true, "image/gif": true, "image/bmp": true}
)

const maxImageSize = 1 << 20

// validateUpload applies the rules of the HubRise catalog documentation. The typed validation
// (dangling refs, duplicate refs, empty option lists, malformed amounts) runs first, the rules the
// typed structs cannot express follow.
func (s *Server) validateUpload(raw json.RawMessage, data obj, checkImages bool) *apiError {
	var typed hubrise.CatalogData
	if err := json.Unmarshal(raw, &typed); err != nil {
		return errField("data", "invalid catalog: "+err.Error())
	}
	var errs []hubrise.FieldError
	if e := validateCatalog(typed); e != nil {
		errs = append(errs, e.fields...)
	}
	add := func(field, msg string) { errs = append(errs, hubrise.FieldError{Field: field, Message: msg}) }

	variants := map[string]bool{}
	for i, v := range objs(data, "variants") {
		f := fmt.Sprintf("variants[%d]", i)
		ref := text(v, "ref")
		if ref == "" || text(v, "name") == "" {
			add(f, "ref and name are required")
		}
		if variants[ref] {
			add(f+".ref", fmt.Sprintf("duplicate ref %q", ref))
		}
		variants[ref] = true
	}
	checkVariants := func(field string, o obj) {
		for _, ref := range strs(o, "variant_refs") {
			if !variants[ref] {
				add(field+".variant_refs", fmt.Sprintf("unknown variant ref %q", ref))
			}
		}
	}
	checkRestrictions := func(field string, o obj) {
		if r, ok := o["restrictions"].(obj); ok {
			checkVariants(field+".restrictions", r)
		}
	}
	checkOverrides := func(field string, o obj) {
		list, _ := o["price_overrides"].([]any)
		for k, v := range list {
			po, ok := v.(obj)
			f := fmt.Sprintf("%s.price_overrides[%d]", field, k)
			if !ok {
				add(f, "must be an object")
				continue
			}
			if text(po, "price") == "" {
				add(f+".price", "is required")
			}
			conds := 0
			for _, key := range []string{"variant_refs", "service_types", "service_type_refs"} {
				if arr, present := po[key].([]any); present {
					if len(arr) == 0 {
						add(f+"."+key, "must be omitted or contain at least one value")
					}
					seen := map[string]bool{}
					for _, x := range strs(po, key) {
						if seen[x] {
							add(f+"."+key, fmt.Sprintf("duplicate value %q", x))
						}
						seen[x] = true
					}
					conds++
				}
			}
			for _, key := range []string{"dow", "start_time", "end_time", "start_date", "end_date"} {
				if text(po, key) != "" {
					conds++
				}
			}
			if conds == 0 {
				add(f, "needs at least one condition besides price")
			}
			checkVariants(f, po)
		}
	}

	categories := map[string]bool{}
	for i, c := range objs(data, "categories") {
		f := fmt.Sprintf("categories[%d]", i)
		if text(c, "ref") == "" || text(c, "name") == "" {
			add(f, "ref and name are required")
		}
		categories[text(c, "ref")] = true
	}

	skuRefs := map[string]bool{}
	productRefs := map[string]bool{}
	for i, p := range objs(data, "products") {
		f := fmt.Sprintf("products[%d]", i)
		if text(p, "name") == "" {
			add(f+".name", "is required")
		}
		if text(p, "category_ref") == "" {
			add(f+".category_ref", "is required")
		}
		if ref := text(p, "ref"); ref != "" {
			if productRefs[ref] {
				add(f+".ref", fmt.Sprintf("duplicate ref %q", ref))
			}
			productRefs[ref] = true
		}
		if tr, ok := p["tax_rate"].(obj); ok {
			for _, k := range []string{"delivery", "collection", "eat_in"} {
				if v := text(tr, k); v != "" {
					if _, err := hubrise.ParseStock(v); err != nil {
						add(f+".tax_rate."+k, fmt.Sprintf("%q is not a decimal", v))
					}
				}
			}
		}
		names := map[string]bool{}
		for j, sku := range objs(p, "skus") {
			g := fmt.Sprintf("%s.skus[%d]", f, j)
			if ref := text(sku, "ref"); ref != "" {
				skuRefs[ref] = true
			}
			name := text(sku, "name")
			if names[name] {
				add(g+".name", fmt.Sprintf("duplicate sku name %q in the product", name))
			}
			names[name] = true
			for _, b := range strs(sku, "barcodes") {
				if !barcodeRe.MatchString(b) {
					add(g+".barcodes", fmt.Sprintf("%q must be 8, 12 or 13 digits", b))
				}
			}
			if cf, present := sku["custom_fields"]; present && cf != nil {
				if _, ok := cf.(obj); !ok {
					add(g+".custom_fields", "must be an object")
				}
			}
			checkRestrictions(g, sku)
			checkOverrides(g, sku)
		}
	}

	for i, l := range objs(data, "option_lists") {
		f := fmt.Sprintf("option_lists[%d]", i)
		if text(l, "name") == "" {
			add(f+".name", "is required")
		}
		options := objs(l, "options")
		minSel, _ := intOf(l, "min_selections")
		maxSel, hasMax := intOf(l, "max_selections")
		multiple, _ := l["multiple_selection"].(bool)
		if multiple && hasMax && maxSel <= 1 {
			add(f+".multiple_selection", "cannot be true when max_selections is 0 or 1")
		}
		if hasMax && maxSel == 0 {
			hasMax = false // 0 is autocorrected to null
		}
		if hasMax && maxSel < 0 {
			add(f+".max_selections", "must be greater than 0")
		}
		if minSel < 0 {
			add(f+".min_selections", "must not be negative")
		}
		if minSel > len(options) && !multiple {
			add(f+".min_selections", "must not exceed the number of options")
		}
		defaults := 0
		seen := map[string]bool{}
		for j, o := range options {
			g := fmt.Sprintf("%s.options[%d]", f, j)
			if text(o, "name") == "" {
				add(g+".name", "is required")
			}
			if text(o, "price") == "" {
				add(g+".price", "is required")
			}
			if ref := text(o, "ref"); ref != "" {
				if seen[ref] {
					add(g+".ref", fmt.Sprintf("duplicate option ref %q in the list", ref))
				}
				seen[ref] = true
			}
			if d, _ := o["default"].(bool); d {
				defaults++
			}
			checkRestrictions(g, o)
			checkOverrides(g, o)
		}
		if hasMax && defaults > maxSel {
			add(f+".options", "more default options than max_selections")
		}
	}

	imageIDs := func(field string, o obj) {
		if !checkImages {
			return
		}
		for _, id := range strs(o, "image_ids") {
			if _, ok := s.images[id]; !ok {
				add(field+".image_ids", fmt.Sprintf("unknown image id %q", id))
			}
		}
	}
	for i, c := range objs(data, "categories") {
		imageIDs(fmt.Sprintf("categories[%d]", i), c)
	}
	for i, p := range objs(data, "products") {
		imageIDs(fmt.Sprintf("products[%d]", i), p)
	}

	dealRefs := map[string]bool{}
	for i, d := range objs(data, "deals") {
		f := fmt.Sprintf("deals[%d]", i)
		if text(d, "name") == "" {
			add(f+".name", "is required")
		}
		if ref := text(d, "ref"); ref != "" {
			if dealRefs[ref] {
				add(f+".ref", fmt.Sprintf("duplicate ref %q", ref))
			}
			dealRefs[ref] = true
		}
		if ref := text(d, "category_ref"); ref != "" && !categories[ref] {
			add(f+".category_ref", fmt.Sprintf("unknown category ref %q", ref))
		}
		lines := objs(d, "lines")
		if len(lines) == 0 {
			add(f+".lines", "a deal needs at least one line")
		}
		for j, line := range lines {
			g := fmt.Sprintf("%s.lines[%d]", f, j)
			lineSkus := objs(line, "skus")
			if len(lineSkus) == 0 {
				add(g+".skus", "a line needs at least one sku")
			}
			for _, sk := range lineSkus {
				ref := text(sk, "ref")
				if _, has := sk["id"]; has {
					add(g+".skus", "a deal sku is referenced by ref only, ids change after every PUT")
				}
				if ref == "" {
					add(g+".skus", "a deal sku needs a ref")
				} else if !skuRefs[ref] {
					add(g+".skus", fmt.Sprintf("unknown sku ref %q", ref))
				}
			}
			switch text(line, "pricing_effect") {
			case "unchanged", "fixed_price", "price_off", "percentage_off", "free":
			default:
				add(g+".pricing_effect", "is not a known pricing effect")
			}
		}
		checkRestrictions(f, d)
	}
	for i, d := range objs(data, "discounts") {
		f := fmt.Sprintf("discounts[%d]", i)
		if text(d, "name") == "" {
			add(f+".name", "is required")
		}
		switch text(d, "pricing_effect") {
		case "price_off", "percentage_off":
		default:
			add(f+".pricing_effect", "must be price_off or percentage_off")
		}
		checkRestrictions(f, d)
	}
	for i, c := range objs(data, "charges") {
		f := fmt.Sprintf("charges[%d]", i)
		if text(c, "name") == "" {
			add(f+".name", "is required")
		}
		switch text(c, "type") {
		case "delivery", "payment_fee", "tip", "tax", "other":
		default:
			add(f+".type", "is not a known charge type")
		}
		checkRestrictions(f, c)
	}
	return errFields(dedupe(errs))
}

func dedupe(in []hubrise.FieldError) []hubrise.FieldError {
	seen := map[hubrise.FieldError]bool{}
	var out []hubrise.FieldError
	for _, e := range in {
		if !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	return out
}

// ---- storage: new ids, ref links turned into id links, the defaults a real GET shows ----

func (s *Server) newID() string { return s.nextID("") }

// recreate returns the stored form of an upload.
func (s *Server) recreate(in obj) obj {
	out := obj{}
	for k, v := range in {
		out[k] = v
	}
	// A GET names the option lists "option_lists" here (the documentation's "options_lists" is
	// an example typo the client tolerates).
	catIDs, listIDs := map[string]string{}, map[string]string{}

	cats := []any{}
	for _, c := range objs(in, "categories") {
		c = cloneObj(c)
		id := s.newID()
		catIDs[text(c, "ref")] = id
		c["id"] = id
		setDefault(c, "description", nil)
		setDefault(c, "tags", []any{})
		setDefault(c, "image_ids", []any{})
		cats = append(cats, c)
	}
	for _, c := range cats {
		co := c.(obj)
		parent := text(co, "parent_ref")
		delete(co, "parent_ref")
		if parent != "" {
			co["parent_id"] = catIDs[parent]
		} else {
			co["parent_id"] = nil
		}
	}
	out["categories"] = cats

	lists := []any{}
	for _, l := range objs(in, "option_lists") {
		l = cloneObj(l)
		id := s.newID()
		listIDs[text(l, "ref")] = id
		l["id"] = id
		setDefault(l, "min_selections", json.Number("0"))
		if n, ok := intOf(l, "max_selections"); ok && n == 0 {
			l["max_selections"] = nil
		}
		setDefault(l, "max_selections", nil)
		options := objs(l, "options")
		if n, ok := intOf(l, "max_selections"); ok && n > len(options) {
			if multiple, _ := l["multiple_selection"].(bool); !multiple {
				l["max_selections"] = json.Number(fmt.Sprint(len(options)))
			}
		}
		setDefault(l, "multiple_selection", false)
		setDefault(l, "tags", []any{})
		minSel, _ := intOf(l, "min_selections")
		maxSel, hasMax := intOf(l, "max_selections")
		if minSel == 1 && hasMax && maxSel == 1 {
			l["type"] = "single"
		} else {
			l["type"] = "multiple"
		}
		stored := []any{}
		for _, o := range options {
			o = cloneObj(o)
			o["id"] = s.newID()
			o["option_list_id"] = id
			setDefault(o, "default", false)
			setDefault(o, "nutrition", nil)
			setDefault(o, "price_overrides", []any{})
			setDefault(o, "restrictions", obj{})
			setDefault(o, "tags", []any{})
			setDefault(o, "volume", nil)
			setDefault(o, "weight", nil)
			stored = append(stored, o)
		}
		l["options"] = stored
		lists = append(lists, l)
	}
	out["option_lists"] = lists
	delete(out, "options_lists")

	prods := []any{}
	for _, p := range objs(in, "products") {
		p = cloneObj(p)
		id := s.newID()
		p["id"] = id
		p["category_id"] = catIDs[text(p, "category_ref")]
		delete(p, "category_ref")
		setDefault(p, "description", nil)
		setDefault(p, "image_ids", []any{})
		setDefault(p, "nutrition", nil)
		setDefault(p, "ref", nil)
		setDefault(p, "tags", []any{})
		if tr, ok := p["tax_rate"].(obj); ok {
			p["tax_rate"] = oneDecimal(tr)
		} else {
			p["tax_rate"] = nil
		}
		skus := []any{}
		for _, sku := range objs(p, "skus") {
			sku = cloneObj(sku)
			sku["id"] = s.newID()
			sku["product_id"] = id
			ids := []any{}
			for _, ref := range strs(sku, "option_list_refs") {
				ids = append(ids, listIDs[ref])
			}
			delete(sku, "option_list_refs")
			sku["option_list_ids"] = ids
			setDefault(sku, "barcodes", []any{})
			setDefault(sku, "custom_fields", obj{})
			setDefault(sku, "name", nil)
			setDefault(sku, "nb_servings", nil)
			setDefault(sku, "price_overrides", []any{})
			setDefault(sku, "restrictions", obj{})
			setDefault(sku, "tags", []any{})
			setDefault(sku, "volume", nil)
			setDefault(sku, "weight", nil)
			skus = append(skus, sku)
		}
		p["skus"] = skus
		prods = append(prods, p)
	}
	out["products"] = prods

	for _, kind := range []string{"deals", "discounts", "charges", "variants"} {
		list := []any{}
		for _, d := range objs(in, kind) {
			d = cloneObj(d)
			d["id"] = s.newID()
			if kind == "deals" {
				// A GET shows the ids HubRise gave the SKUs a deal line uses.
				for _, line := range objs(d, "lines") {
					for _, sk := range objs(line, "skus") {
						sk["id"] = s.newID()
					}
				}
				if ref := text(d, "category_ref"); ref != "" {
					d["category_id"] = catIDs[ref]
					delete(d, "category_ref")
				}
			}
			if kind != "variants" {
				setDefault(d, "restrictions", obj{})
			}
			list = append(list, d)
		}
		out[kind] = list
	}
	return out
}

func setDefault(o obj, key string, v any) {
	if _, ok := o[key]; !ok {
		o[key] = v
	}
}

func cloneObj(o obj) obj {
	b, _ := json.Marshal(o)
	out, _ := decodeObject(b)
	return out
}

// oneDecimal writes percentages the way a GET shows them: "6" becomes "6.0".
func oneDecimal(tr obj) obj {
	out := obj{}
	for k, v := range tr {
		if str, ok := v.(string); ok && !strings.Contains(str, ".") && str != "" {
			v = str + ".0"
		}
		out[k] = v
	}
	return out
}

// markAttached flags the images the stored catalog references.
func (s *Server) markAttached(stored obj) {
	used := map[string]bool{}
	for _, kind := range []string{"categories", "products", "deals", "discounts"} {
		for _, o := range objs(stored, kind) {
			for _, id := range strs(o, "image_ids") {
				used[id] = true
			}
		}
	}
	for id, img := range s.images {
		img.attached = used[id]
		if img.attached {
			img.meta.SecondsBeforeRemoval = 0
		} else if img.meta.SecondsBeforeRemoval == 0 {
			img.meta.SecondsBeforeRemoval = 30 * 24 * 3600
		}
		s.images[id] = img
	}
}

// ---- images ----

func postImage(s *Server, c *client, r *http.Request, body []byte, _ http.Header) (int, any, *apiError) {
	if r.PathValue("id") != s.catalog.ID {
		return 0, nil, errStatus(http.StatusNotFound, "catalog not found")
	}
	if !c.can("catalog.write") {
		return 0, nil, errStatus(http.StatusForbidden, "the token has no catalog.write scope")
	}
	ctype, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";")
	ctype = strings.TrimSpace(strings.ToLower(ctype))
	if !imageType[ctype] {
		return 0, nil, errStatus(http.StatusUnsupportedMediaType, "Content-Type must be image/jpeg, image/png, image/webp, image/gif or image/bmp")
	}
	if len(body) == 0 {
		return 0, nil, errField("body", "is empty")
	}
	if len(body) > maxImageSize {
		return 0, nil, errField("body", "an image is at most 1 MB")
	}
	ref := r.URL.Query().Get("private_ref")
	if ref != "" {
		for _, id := range s.imageSeq {
			if s.images[id].meta.PrivateRef == ref {
				return 0, nil, errField("private_ref", fmt.Sprintf("already used by image %s", id))
			}
		}
	}
	sum := md5.Sum(body) //nolint:gosec // see import
	id := s.newID()
	meta := hubrise.Image{ID: id, Type: ctype, Size: len(body), MD5: hex.EncodeToString(sum[:]), PrivateRef: ref, SecondsBeforeRemoval: 30 * 24 * 3600}
	s.images[id] = image{meta: meta, data: bytes.Clone(body)}
	s.imageSeq = append(s.imageSeq, id)
	return http.StatusCreated, meta, nil
}

func listImages(s *Server, _ *client, r *http.Request, _ []byte, h http.Header) (int, any, *apiError) {
	if r.PathValue("id") != s.catalog.ID {
		return 0, nil, errStatus(http.StatusNotFound, "catalog not found")
	}
	q := r.URL.Query()
	items := []hubrise.Image{}
	for _, id := range s.imageSeq {
		img := s.images[id].meta
		if ref := q.Get("private_ref"); ref != "" && img.PrivateRef != ref {
			continue
		}
		items = append(items, img)
	}
	return page(h, q.Get("count"), q.Get("cursor"), items)
}

// Images returns the metadata of the catalog images, oldest first.
func (s *Server) Images() []hubrise.Image {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]hubrise.Image, 0, len(s.imageSeq))
	for _, id := range s.imageSeq {
		out = append(out, s.images[id].meta)
	}
	return out
}

// ImageBytes returns the stored bytes of an image.
func (s *Server) ImageBytes(id string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	img, ok := s.images[id]
	return bytes.Clone(img.data), ok
}

// PurgeImages forgets every image, as HubRise does with the images no product carries any more.
func (s *Server) PurgeImages() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.images = map[string]image{}
	s.imageSeq = nil
}
