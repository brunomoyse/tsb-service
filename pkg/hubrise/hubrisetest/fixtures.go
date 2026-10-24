package hubrisetest

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"tsb-service/pkg/hubrise"
)

// The fixtures are the exchanges with HubRise recorded by the moyun-pos integration (scenarios/,
// scrubbed, one JSON file per request) and the samples of the HubRise documentation (docs/). A
// contract test decodes every one of them into the types of pkg/hubrise.
//
//go:embed fixtures
var fixtureFS embed.FS

// Fixture returns the content of a fixture file, named relative to the fixtures directory
// ("docs/order_retrieve_doc.json"). It panics on a name that does not exist: fixture names are
// constants in tests.
func Fixture(name string) []byte {
	b, err := fixtureFS.ReadFile(path.Join("fixtures", name))
	if err != nil {
		panic(fmt.Sprintf("hubrisetest: no fixture %q", name))
	}
	return b
}

// FixtureNames lists every fixture file, sorted.
func FixtureNames() []string {
	var names []string
	_ = fs.WalkDir(fixtureFS, "fixtures", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".json") {
			names = append(names, strings.TrimPrefix(p, "fixtures/"))
		}
		return nil
	})
	sort.Strings(names)
	return names
}

// Exchange is one recorded HubRise request and its response.
type Exchange struct {
	Method   string          `json:"method"`
	Path     string          `json:"path"`
	Request  json.RawMessage `json:"request"`
	Response json.RawMessage `json:"response"`
	Status   int             `json:"status"`
}

// Scenario returns the exchanges of a recorded scenario ("prepaid_cancel_refund"), in the order
// they were recorded.
func Scenario(name string) []Exchange {
	entries, err := fixtureFS.ReadDir(path.Join("fixtures/scenarios", name))
	if err != nil {
		panic(fmt.Sprintf("hubrisetest: no scenario %q", name))
	}
	var out []Exchange
	for _, e := range entries { // ReadDir sorts by file name, which starts with the sequence number
		var x Exchange
		if err := json.Unmarshal(Fixture(path.Join("scenarios", name, e.Name())), &x); err != nil {
			panic(fmt.Sprintf("hubrisetest: scenario %q file %q: %v", name, e.Name(), err))
		}
		out = append(out, x)
	}
	return out
}

// SeedCatalog is the menu a new mock starts with: the catalog the moyun-pos integration uploaded
// in its recorded scenarios (makis, soups, drinks, SKUs with refs, sauce and extras option lists,
// tax rates per service type, a delivery charge).
func SeedCatalog() hubrise.CatalogData {
	for _, x := range Scenario("catalog_mirror") {
		if x.Method != "PUT" {
			continue
		}
		var body struct {
			Data hubrise.CatalogData `json:"data"`
		}
		if err := json.Unmarshal(x.Request, &body); err != nil {
			panic(fmt.Sprintf("hubrisetest: seed catalog: %v", err))
		}
		return body.Data
	}
	panic("hubrisetest: the catalog_mirror scenario has no PUT")
}
