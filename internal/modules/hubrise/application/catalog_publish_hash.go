package application

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/shopspring/decimal"

	"tsb-service/internal/modules/hubrise/domain"
)

// The hashes of the catalog publisher. ItemHash covers the fields the shop owns of one item and
// nothing else: not its refs, not what HubRise holds, and not availability (a sold-out toggle never
// republishes the menu). LocalMenuHash covers every item and the settings that shape the payload.
// RemoteCatalogHash covers what HubRise holds, without the ids that change on every PUT.

func sumHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func hashValue(v any) string {
	return sumHex(marshalJSON(v))
}

// textsView is the language to {name, description} map of a hash.
func textsView(texts map[string]domain.LocalText) map[string][2]string {
	out := make(map[string][2]string, len(texts))
	for lang, t := range texts {
		if strings.TrimSpace(t.Name) == "" && strings.TrimSpace(t.Description) == "" {
			continue
		}
		out[lang] = [2]string{strings.TrimSpace(t.Name), strings.TrimSpace(t.Description)}
	}
	return out
}

func moneyView(d decimal.Decimal) string { return d.StringFixed(2) }

func categoryItemHash(c *domain.LocalCategory) string {
	return hashValue(map[string]any{"texts": textsView(c.Names), "order": c.Order})
}

func productItemHash(p *domain.LocalProduct) string {
	return hashValue(map[string]any{
		"texts": textsView(p.Texts), "price": moneyView(p.Price), "vat": p.VatCategory, "code": strings.TrimSpace(p.Code),
		"visible": p.Visible, "category": p.CategoryID.String(),
	})
}

func groupItemHash(g *domain.LocalGroup) string {
	return hashValue(map[string]any{"texts": textsView(g.Names), "min": g.Min, "max": g.Max, "sort": g.Sort})
}

func choiceItemHash(c *domain.LocalChoice) string {
	return hashValue(map[string]any{"texts": textsView(c.Names), "price": moneyView(c.PriceModifier), "sort": c.Sort})
}

// LocalMenuHash is the hash of the whole local menu as the publisher sees it, with the settings that
// change the payload (the language of the names, the price list, the images). It needs no HubRise
// data, so a periodic scan can compare it with the hash of the last publish at the cost of one local
// read. A deleted item changes it because its line disappears.
func LocalMenuHash(local *domain.LocalSnapshot, s PublishSettings) string {
	var lines []string
	if local != nil {
		for i := range local.Categories {
			c := &local.Categories[i]
			lines = append(lines, fmt.Sprintf("category:%s:%s", c.ID, categoryItemHash(c)))
		}
		for i := range local.Products {
			p := &local.Products[i]
			lines = append(lines, fmt.Sprintf("product:%s:%s", p.ID, productItemHash(p)))
		}
		for i := range local.Groups {
			g := &local.Groups[i]
			lines = append(lines, fmt.Sprintf("group:%s:%s:%s", g.ID, g.ProductID, groupItemHash(g)))
		}
		for i := range local.Choices {
			c := &local.Choices[i]
			lines = append(lines, fmt.Sprintf("choice:%s:%s:%s", c.ID, c.GroupID, choiceItemHash(c)))
		}
	}
	sort.Strings(lines)
	lines = append(lines, fmt.Sprintf("settings:%s|%s|%t", s.language(), s.VariantRef, s.Images))
	return sumHex([]byte(strings.Join(lines, "\n")))
}

// RemoteCatalogHash is the hash of a HubRise catalog's content, in a form that does not change when
// HubRise gives its elements new ids: ids are replaced by refs, the documentation's alternative
// spellings are folded and every id key is dropped. Unreadable JSON hashes as itself.
func RemoteCatalogHash(raw []byte) string {
	base, err := parseBaseCatalog(raw)
	if err != nil {
		return sumHex(raw)
	}
	return sumHex(canonicalJSON(base.data()))
}

// planHashOf is the identity of a plan for the preview and confirmation handshake: the sorted lines
// of what is added, updated and removed (the keys, not the fields, so a cosmetic change does not stale
// a preview but a different removal set does) and of the foreign items.
func planHashOf(changes []PublishChange, foreign []ForeignItem) string {
	var lines []string
	for _, c := range changes {
		// The images are uploaded after the confirmation and may fail: they never stale a preview.
		if c.Action == ActionPublishUpdate && slices.Equal(c.Fields, []string{FieldPubImage}) {
			continue
		}
		prefix := map[PublishAction]string{ActionPublishAdd: "A", ActionPublishUpdate: "U", ActionPublishRemove: "R"}[c.Action]
		lines = append(lines, fmt.Sprintf("%s:%s:%s", prefix, c.Entity, c.Key))
	}
	for _, f := range foreign {
		lines = append(lines, fmt.Sprintf("F:%s:%s", f.Entity, f.Ref))
	}
	sort.Strings(lines)
	return sumHex([]byte(strings.Join(slices.Compact(lines), "\n")))
}
