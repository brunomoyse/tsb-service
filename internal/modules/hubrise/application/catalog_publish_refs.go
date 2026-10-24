package application

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// Refs the shop generates for the items it publishes (amendment A15.4). A ref is stored in the link
// tables before the PUT and never changes afterwards, so orders, inventory entries and later
// publishes always name the same thing, even when HubRise changes the ids.

// skuRefRe is what a product code must match to be used as a SKU ref.
var skuRefRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,40}$`)

// refSet is a set of refs compared case-insensitively: two refs that differ only by case would be
// two items for the shop and one for most POS systems.
type refSet map[string]bool

func (s refSet) has(ref string) bool { return s[strings.ToLower(ref)] }
func (s refSet) add(ref string)      { s[strings.ToLower(ref)] = true }

// refAllocator hands out new refs that collide with nothing known: not with the base catalog, not
// with an existing link, not with another ref given in the same run.
type refAllocator struct {
	categories refSet
	// skus holds product refs and SKU refs together: the product ref of a new item is its SKU ref.
	skus  refSet
	lists refSet
}

func newRefAllocator() *refAllocator {
	return &refAllocator{categories: refSet{}, skus: refSet{}, lists: refSet{}}
}

// shortID is the first n hex digits of a uuid.
func shortID(id uuid.UUID, n int) string { return strings.ReplaceAll(id.String(), "-", "")[:n] }

// category: the slug (unique, immutable, URL safe), or c-<8 hex> on a collision.
func (a *refAllocator) category(slug string, id uuid.UUID) string {
	if skuRefRe.MatchString(slug) && !a.categories.has(slug) {
		a.categories.add(slug)
		return slug
	}
	return pickRef(a.categories, "c-", id)
}

// sku: the product code when it is a valid, unused ref, otherwise p-<8 hex> (12 on a collision).
func (a *refAllocator) sku(code string, id uuid.UUID) string {
	code = strings.TrimSpace(code)
	if skuRefRe.MatchString(code) && !a.skus.has(code) {
		a.skus.add(code)
		return code
	}
	return pickRef(a.skus, "p-", id)
}

// list: g-<8 hex of the group uuid>.
func (a *refAllocator) list(id uuid.UUID) string { return pickRef(a.lists, "g-", id) }

// splitList names the option list a SKU gets when its group diverged from the others sharing the list.
func (a *refAllocator) splitList(listRef, skuRef string) string {
	candidate := listRef + "~" + skuRef
	for n := 2; a.lists.has(candidate); n++ {
		candidate = listRef + "~" + skuRef + "~" + strconv.Itoa(n)
	}
	a.lists.add(candidate)
	return candidate
}

// option: o-<8 hex of the choice uuid>, unique inside the list (used holds the option refs already in it).
func optionRef(used refSet, id uuid.UUID) string { return pickRef(used, "o-", id) }

// pickRef tries 8, then 12, then 32 hex digits, then numbers, and records the winner.
func pickRef(used refSet, prefix string, id uuid.UUID) string {
	for _, n := range []int{8, 12, 32} {
		if ref := prefix + shortID(id, n); !used.has(ref) {
			used.add(ref)
			return ref
		}
	}
	for i := 2; ; i++ {
		if ref := prefix + shortID(id, 32) + "-" + strconv.Itoa(i); !used.has(ref) {
			used.add(ref)
			return ref
		}
	}
}
