package hubrisetest

import (
	"crypto/md5" //nolint:gosec // HubRise images carry an MD5, it is not used for security
	"encoding/hex"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"tsb-service/pkg/hubrise"
)

// freshCatalog is the catalog a new mock starts with: the sushi menu recorded by the moyun-pos
// integration (SeedCatalog).
func (s *Server) freshCatalog() hubrise.Catalog {
	return hubrise.Catalog{
		ID: CatalogID, LocationID: LocationID, AccountID: AccountID, Name: CatalogName,
		CreatedAt: s.now(), Data: storedCatalog(SeedCatalog()),
	}
}

// storedCatalog is a catalog as HubRise stores it: every element has an id, and the refs an upload
// links elements with (parent_ref, category_ref, option_list_refs) become those ids (parent_id,
// category_id, option_list_ids); responses do not carry the link refs.
func storedCatalog(in hubrise.CatalogData) *hubrise.CatalogData {
	d := in
	id := func(prefix, ref string) string { return prefix + "-" + strings.ToLower(ref) }

	d.Categories = slices.Clone(in.Categories)
	categoryIDs := map[string]string{}
	for i := range d.Categories {
		c := &d.Categories[i]
		if c.ID == "" {
			c.ID = id("cat", c.Ref)
		}
		categoryIDs[c.Ref] = c.ID
	}
	for i := range d.Categories {
		c := &d.Categories[i]
		if c.ParentRef != "" {
			c.ParentID, c.ParentRef = categoryIDs[c.ParentRef], ""
		}
	}

	d.OptionLists = slices.Clone(in.OptionLists)
	listIDs := map[string]string{}
	for i := range d.OptionLists {
		l := &d.OptionLists[i]
		if l.ID == "" {
			l.ID = id("ol", l.Ref)
		}
		listIDs[l.Ref] = l.ID
		l.Options = slices.Clone(l.Options)
		for j := range l.Options {
			o := &l.Options[j]
			if o.ID == "" {
				o.ID = id("opt", l.Ref+"-"+o.Ref)
			}
			o.OptionListID = l.ID
		}
	}

	d.Products = slices.Clone(in.Products)
	for i := range d.Products {
		p := &d.Products[i]
		if p.ID == "" {
			p.ID = id("prod", p.Ref)
		}
		if p.CategoryRef != "" {
			p.CategoryID, p.CategoryRef = categoryIDs[p.CategoryRef], ""
		}
		p.Skus = slices.Clone(p.Skus)
		for j := range p.Skus {
			sku := &p.Skus[j]
			if sku.ID == "" {
				sku.ID = id("sku", sku.Ref)
			}
			sku.ProductID = p.ID
			for _, ref := range sku.OptionListRefs {
				sku.OptionListIDs = append(slices.Clone(sku.OptionListIDs), listIDs[ref])
			}
			sku.OptionListRefs = nil
		}
	}
	d.Charges = slices.Clone(in.Charges)
	for i := range d.Charges {
		if d.Charges[i].ID == "" {
			d.Charges[i].ID = id("charge", d.Charges[i].Ref)
		}
	}
	return &d
}

// validateCatalog refuses a catalog the way HubRise does: a dangling ref, a duplicate ref, a
// product without SKU, an empty option list, min above max, a malformed amount. The whole upload
// is refused, nothing is stored.
func validateCatalog(d hubrise.CatalogData) *apiError {
	var errs []hubrise.FieldError
	add := func(field, msg string) { errs = append(errs, hubrise.FieldError{Field: field, Message: msg}) }
	dup := func(field string, seen map[string]bool, ref string) {
		if ref == "" {
			return
		}
		if seen[ref] {
			add(field, fmt.Sprintf("duplicate ref %q", ref))
		}
		seen[ref] = true
	}

	categories, lists, skus := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for i, c := range d.Categories {
		dup(fmt.Sprintf("categories[%d].ref", i), categories, c.Ref)
	}
	for i, l := range d.OptionLists {
		dup(fmt.Sprintf("option_lists[%d].ref", i), lists, l.Ref)
	}
	for i, c := range d.Categories {
		if c.ParentRef != "" && !categories[c.ParentRef] {
			add(fmt.Sprintf("categories[%d].parent_ref", i), fmt.Sprintf("unknown category ref %q", c.ParentRef))
		}
	}
	for i, p := range d.Products {
		f := fmt.Sprintf("products[%d]", i)
		if p.CategoryRef != "" && !categories[p.CategoryRef] {
			add(f+".category_ref", fmt.Sprintf("unknown category ref %q", p.CategoryRef))
		}
		if len(p.Skus) == 0 {
			add(f+".skus", "a product needs at least one sku")
		}
		if p.TaxRate != nil && (p.TaxRate.Delivery == "" || p.TaxRate.Collection == "" || p.TaxRate.EatIn == "") {
			add(f+".tax_rate", "must set delivery, collection and eat_in together")
		}
		for j, sku := range p.Skus {
			g := fmt.Sprintf("%s.skus[%d]", f, j)
			dup(g+".ref", skus, sku.Ref)
			money(g+".price", sku.Price, true, &errs)
			for _, ref := range sku.OptionListRefs {
				if !lists[ref] {
					add(g+".option_list_refs", fmt.Sprintf("unknown option list ref %q", ref))
				}
			}
		}
	}
	for i, l := range d.OptionLists {
		f := fmt.Sprintf("option_lists[%d]", i)
		if len(l.Options) == 0 {
			add(f+".options", "an option list needs at least one option")
		}
		if l.MaxSelections != nil && l.MinSelections > *l.MaxSelections {
			add(f+".min_selections", "must not exceed max_selections")
		}
		for j, o := range l.Options {
			money(fmt.Sprintf("%s.options[%d].price", f, j), o.Price, false, &errs)
		}
	}
	for i, c := range d.Charges {
		money(fmt.Sprintf("charges[%d].price", i), c.Price, false, &errs)
	}
	return errFields(errs)
}

// ---- images ----

func getImage(s *Server, _ *client, r *http.Request, _ []byte, _ http.Header) (int, any, *apiError) {
	img, ok := s.findImage(r.PathValue("id"), r.PathValue("image"))
	if !ok {
		return 0, nil, errStatus(http.StatusNotFound, "image not found")
	}
	return http.StatusOK, img.meta, nil
}

func getImageData(s *Server, _ *client, r *http.Request, _ []byte, _ http.Header) (int, any, *apiError) {
	img, ok := s.findImage(r.PathValue("id"), r.PathValue("image"))
	if !ok {
		return 0, nil, errStatus(http.StatusNotFound, "image not found")
	}
	return http.StatusOK, rawResponse{contentType: img.meta.Type, data: img.data}, nil
}

func (s *Server) findImage(catalogID, imageID string) (image, bool) {
	if catalogID != s.catalog.ID {
		return image{}, false
	}
	img, ok := s.images[imageID]
	return img, ok
}

// AddImage stores a catalog image and returns its metadata.
func (s *Server) AddImage(id, contentType string, data []byte) hubrise.Image {
	s.mu.Lock()
	defer s.mu.Unlock()
	sum := md5.Sum(data) //nolint:gosec // see import
	meta := hubrise.Image{ID: id, Type: contentType, Size: len(data), MD5: hex.EncodeToString(sum[:])}
	if _, known := s.images[id]; !known {
		s.imageSeq = append(s.imageSeq, id)
	}
	s.images[id] = image{meta: meta, data: slices.Clone(data)}
	return meta
}

// ---- catalog control ----

// Catalog returns the catalog as the API serves it.
func (s *Server) Catalog() hubrise.Catalog {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.catalog
}

// ReplaceCatalog replaces the content of the catalog as the other side (the EPOS or the menu
// editor) and tells the clients that registered for catalog.update. A structurally invalid
// catalog is refused whole.
func (s *Server) ReplaceCatalog(data hubrise.CatalogData) error {
	if err := validateCatalog(data); err != nil {
		return fmt.Errorf("hubrisetest: replace catalog: %s", errString(err))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.catalog.Data = storedCatalog(data)
	s.rawData = nil
	s.emit(EPOSToken, "catalog", "update", func(e *hubrise.Event, _ string) { e.CatalogID = s.catalog.ID })
	return nil
}

// DeleteCatalog tells the clients that registered for catalog.delete that the catalog is gone (the
// mock keeps serving it, as the integration must not depend on it).
func (s *Server) DeleteCatalog() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emit(EPOSToken, "catalog", "delete", func(e *hubrise.Event, _ string) { e.CatalogID = s.catalog.ID })
}

func errString(e *apiError) string {
	parts := []string{e.message}
	for _, f := range e.fields {
		parts = append(parts, f.Field+": "+f.Message)
	}
	return strings.Join(parts, "; ")
}

// ---- inventory ----

// inventoryCatalog checks the catalog and location named by a scoped inventory route.
func (s *Server) inventoryCatalog(r *http.Request) *apiError {
	if id := r.PathValue("id"); id != "" && id != s.catalog.ID {
		return errStatus(http.StatusNotFound, "catalog not found")
	}
	if loc := r.PathValue("loc"); loc != "" && loc != LocationID {
		return errStatus(http.StatusNotFound, "location not found")
	}
	return nil
}

func getInventory(s *Server, _ *client, r *http.Request, _ []byte, _ http.Header) (int, any, *apiError) {
	if err := s.inventoryCatalog(r); err != nil {
		return 0, nil, err
	}
	return http.StatusOK, nonNilEntries(s.inventory), nil
}

func putInventory(s *Server, c *client, r *http.Request, body []byte, _ http.Header) (int, any, *apiError) {
	if err := s.inventoryCatalog(r); err != nil {
		return 0, nil, err
	}
	var entries []hubrise.InventoryEntry
	if err := decodeStrict(body, &entries); err != nil {
		return 0, nil, err
	}
	if err := s.validateInventory(entries); err != nil {
		return 0, nil, err
	}
	s.inventory = withoutRemovals(entries)
	s.emit(c.token, "inventory", "update", func(e *hubrise.Event, _ string) {
		e.CatalogID = s.catalog.ID
	})
	return http.StatusOK, nonNilEntries(s.inventory), nil
}

func patchInventory(s *Server, c *client, r *http.Request, body []byte, _ http.Header) (int, any, *apiError) {
	if err := s.inventoryCatalog(r); err != nil {
		return 0, nil, err
	}
	var entries []hubrise.InventoryEntry
	if err := decodeStrict(body, &entries); err != nil {
		return 0, nil, err
	}
	if err := s.validateInventory(entries); err != nil {
		return 0, nil, err
	}
	s.mergeInventory(entries)
	s.emit(c.token, "inventory", "patch", func(e *hubrise.Event, _ string) {
		e.CatalogID = s.catalog.ID
		e.StateChange = mustJSON(entries)
	})
	return http.StatusOK, nonNilEntries(entries), nil
}

// mergeInventory applies a PATCH: an entry replaces the one for the same ref, a nil stock removes it.
func (s *Server) mergeInventory(entries []hubrise.InventoryEntry) {
	for _, e := range entries {
		s.inventory = slices.DeleteFunc(s.inventory, func(x hubrise.InventoryEntry) bool {
			return x.SkuRef == e.SkuRef && x.OptionRef == e.OptionRef
		})
		if e.Stock != nil {
			s.inventory = append(s.inventory, e)
		}
	}
}

// withoutRemovals drops the entries with a nil stock from a PUT body.
func withoutRemovals(entries []hubrise.InventoryEntry) []hubrise.InventoryEntry {
	out := make([]hubrise.InventoryEntry, 0, len(entries))
	for _, e := range entries {
		if e.Stock != nil {
			out = append(out, e)
		}
	}
	return out
}

func nonNilEntries(e []hubrise.InventoryEntry) []hubrise.InventoryEntry {
	if e == nil {
		return []hubrise.InventoryEntry{}
	}
	return e
}

// validateInventory checks the entries: exactly one of sku_ref and option_ref, naming a ref of
// the catalog; a stock that is a non negative decimal with at most three decimals; expires_at only
// with a stock of 0.
func (s *Server) validateInventory(entries []hubrise.InventoryEntry) *apiError {
	skuRefs, optionRefs := map[string]bool{}, map[string]bool{}
	if d := s.catalog.Data; d != nil {
		for _, p := range d.Products {
			for _, sku := range p.Skus {
				skuRefs[sku.Ref] = true
			}
		}
		for _, l := range d.OptionLists {
			for _, o := range l.Options {
				optionRefs[o.Ref] = true
			}
		}
	}
	var errs []hubrise.FieldError
	add := func(field, msg string) { errs = append(errs, hubrise.FieldError{Field: field, Message: msg}) }
	for i, e := range entries {
		f := fmt.Sprintf("[%d]", i)
		switch {
		case e.SkuRef != "" && e.OptionRef != "":
			add(f, "exactly one of sku_ref and option_ref is allowed")
		case e.SkuRef == "" && e.OptionRef == "":
			add(f, "one of sku_ref and option_ref is required")
		case e.SkuRef != "" && !skuRefs[e.SkuRef]:
			add(f+".sku_ref", fmt.Sprintf("unknown sku ref %q", e.SkuRef))
		case e.OptionRef != "" && !optionRefs[e.OptionRef]:
			add(f+".option_ref", fmt.Sprintf("unknown option ref %q", e.OptionRef))
		}
		zero := false
		if e.Stock != nil {
			d, err := hubrise.ParseStock(*e.Stock)
			if err != nil {
				add(f+".stock", fmt.Sprintf("%q must be a non negative decimal with at most 3 decimals", *e.Stock))
			} else {
				zero = d.IsZero()
			}
		}
		if e.ExpiresAt != nil && !zero {
			add(f+".expires_at", "is only allowed when stock is 0")
		}
	}
	return errFields(errs)
}

// Inventory returns the stock entries.
func (s *Server) Inventory() []hubrise.InventoryEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.inventory)
}

// SetInventory replaces the whole inventory as the other side and tells the clients that
// registered for inventory.update.
func (s *Server) SetInventory(entries []hubrise.InventoryEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validateInventory(entries); err != nil {
		return fmt.Errorf("hubrisetest: set inventory: %s", errString(err))
	}
	s.inventory = withoutRemovals(entries)
	s.emit(EPOSToken, "inventory", "update", func(e *hubrise.Event, _ string) { e.CatalogID = s.catalog.ID })
	return nil
}

// PatchInventory merges entries into the inventory as the other side (a nil stock removes the
// entry) and tells the clients that registered for inventory.patch.
func (s *Server) PatchInventory(entries []hubrise.InventoryEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validateInventory(entries); err != nil {
		return fmt.Errorf("hubrisetest: patch inventory: %s", errString(err))
	}
	s.mergeInventory(entries)
	s.emit(EPOSToken, "inventory", "patch", func(e *hubrise.Event, _ string) {
		e.CatalogID = s.catalog.ID
		e.StateChange = mustJSON(entries)
	})
	return nil
}

// ExpireInventory removes the entries whose expires_at has passed and announces them as the
// inventory.patch that HubRise sends when a sold out entry expires.
func (s *Server) ExpireInventory() []hubrise.InventoryEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	var expired []hubrise.InventoryEntry
	s.inventory = slices.DeleteFunc(s.inventory, func(e hubrise.InventoryEntry) bool {
		if e.ExpiresAt != nil && !e.ExpiresAt.After(now) {
			expired = append(expired, e)
			return true
		}
		return false
	})
	if len(expired) > 0 {
		s.emit("", "inventory", "patch", func(e *hubrise.Event, _ string) {
			e.CatalogID = s.catalog.ID
			e.StateChange = mustJSON(expired)
		})
	}
	return expired
}
