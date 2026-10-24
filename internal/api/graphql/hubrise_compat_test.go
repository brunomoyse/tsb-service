package graphql_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"golang.org/x/time/rate"

	gql "tsb-service/internal/api/graphql"
	"tsb-service/internal/api/graphql/resolver"
	"tsb-service/internal/api/graphql/testhelpers"
	addressDomain "tsb-service/internal/modules/address/domain"
	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/modules/hubrise/infrastructure"
	hubtest "tsb-service/internal/modules/hubrise/testhelpers"
	"tsb-service/internal/shared/middleware"
)

// The HubRise integration is additive: a client written before it existed (the customer web app, the
// mobile app, the dashboard, the POS handheld) must see exactly what it saw. testdata/clients holds
// the query documents those clients send, as they were when the integration was added (none of them
// mentions a HubRise field). They are replayed here against the current schema on an install without
// HubRise and on one with it connected and everything switched on, as an anonymous caller, a customer
// and an admin: the data and the error codes must be the same.

type compatDocument struct {
	File      string         `json:"file"`
	Source    string         `json:"source"`
	Variables map[string]any `json:"variables"`
	query     string
}

func loadCompatDocuments(t *testing.T) []compatDocument {
	t.Helper()
	root := filepath.Join("testdata", "clients")
	raw, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	require.NoError(t, err)
	var docs []compatDocument
	require.NoError(t, json.Unmarshal(raw, &docs))
	for i := range docs {
		q, err := os.ReadFile(filepath.Join(root, docs[i].File))
		require.NoError(t, err)
		docs[i].query = string(q)
	}
	return docs
}

// withPlaceholders resolves {{name}} in the variables of a document.
func withPlaceholders(t *testing.T, vars map[string]any, values map[string]string) map[string]any {
	t.Helper()
	if vars == nil {
		return nil
	}
	raw, err := json.Marshal(vars)
	require.NoError(t, err)
	text := string(raw)
	for name, v := range values {
		text = strings.ReplaceAll(text, "{{"+name+"}}", v)
	}
	require.NotContains(t, text, "{{", "an unresolved placeholder")
	var out map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &out))
	return out
}

func compatSchema() *ast.Schema {
	return gql.NewExecutableSchema(gql.Config{Resolvers: &resolver.Resolver{}}).Schema()
}

func TestTheRecordedClientDocumentsAreStillValidAgainstTheSchema(t *testing.T) {
	docs := loadCompatDocuments(t)
	require.GreaterOrEqual(t, len(docs), 30, "the four clients are covered")
	clients := map[string]int{}
	schema := compatSchema()
	for _, d := range docs {
		clients[strings.Split(d.File, "/")[0]]++
		_, err := gqlparser.LoadQueryWithRules(schema, d.query, nil)
		assert.Empty(t, err, d.File)
		assert.NotContains(t, strings.ToLower(d.query), "hubrise", "%s: the fixtures are the documents of before the integration", d.File)
	}
	assert.Len(t, clients, 4, "tsb-core, tsb-dashboard, tsb-mobile and tsb-pos-android: %v", clients)
}

func TestClientQueriesAnswerTheSameWithAndWithoutHubRise(t *testing.T) {
	// The integration with HubRise owning the menu (catalog sync) and with the dashboard owning it
	// (catalog publishing): neither may change what a client written before it existed sees.
	t.Run("catalog sync", func(t *testing.T) {
		replayClientDocuments(t, hubtest.WithFeatures(true, true, true, true, false))
	})
	t.Run("catalog publishing", func(t *testing.T) {
		replayClientDocuments(t, hubtest.WithFeatures(true, true, true, false, false), hubtest.WithPublishCatalog())
	})
}

func replayClientDocuments(t *testing.T, seed ...hubtest.SeedOption) {
	t.Helper()
	docs := loadCompatDocuments(t)
	r := newHubriseAdminRig(t, true, seed...)
	r.Resolver.PublicQueryLimiter = middleware.NewRateLimiter(rate.Every(time.Millisecond), 10000)
	r.Resolver.CouponValidateLimiter = middleware.NewRateLimiter(rate.Every(time.Millisecond), 10000)

	street, house, post, city := "Rue du Pont", "1", "4000", "Liège"
	r.Google.place = &addressDomain.AddressCache{PlaceID: "place-1", FormattedAddress: "Rue du Pont 1, 4000 Liège", Lat: 50.6, Lng: 5.5,
		StreetName: &street, HouseNumber: &house, Postcode: &post, MunicipalityName: &city, CountryCode: "BE"}

	// An install with something to read: a customer with an order, a managed product, a backlog.
	customer, custTok := seedCompatCustomer(t, r)
	placed := mustCreateOrder(t, r.TestContext, custTok, "en", map[string]any{
		"orderType": "PICKUP", "isOnlinePayment": false, "items": lines(r.Fixtures.SalmonSushi.ID, 2)})
	r.exec(`INSERT INTO hubrise_catalog_links (entity_type, product_id, hubrise_key, hubrise_ref, origin)
		VALUES ('product', $1, 'MAKI-SAUMON-6', 'MAKI-SAUMON-6', 'import')`, r.Fixtures.SalmonSushi.ID)
	r.exec(`INSERT INTO hubrise_outbox (kind, dedupe_key, payload, state) VALUES ('inventory', 'k', '{}', 'parked')`)

	values := map[string]string{
		"salmon": r.Fixtures.SalmonSushi.ID.String(), "order": placed.ID, "customer": customer,
	}
	callers := []struct{ name, token string }{{"anonymous", ""}, {"customer", custTok}, {"admin", r.adminTok}}

	standalone := func() { r.Resolver.HubriseAdmin, r.Resolver.Hubrise = nil, nil }
	links := infrastructure.NewCatalogLinkRepository(r.DB.DB)
	enabled := func() {
		r.Resolver.HubriseAdmin = r.admin
		r.Resolver.Hubrise = resolver.CatalogGuardHooks{Guard: &application.Guard{Connections: r.mod.Connections, Links: links}}
	}

	answered := map[string]bool{}
	for _, d := range docs {
		vars := withPlaceholders(t, d.Variables, values)
		for _, c := range callers {
			standalone()
			without := r.ask(c.token, d.query, vars)
			enabled()
			with := r.ask(c.token, d.query, vars)

			assert.JSONEq(t, string(without.Data), string(with.Data), "%s as %s: data", d.File, c.name)
			assert.Equal(t, errorSummary(without), errorSummary(with), "%s as %s: errors", d.File, c.name)
			if len(with.Errors) == 0 {
				answered[d.File] = true
			}
		}
	}
	standalone()

	for _, d := range docs {
		assert.True(t, answered[d.File], "%s never answered without an error: the replay would prove nothing", d.File)
	}
}

// errorSummary is what a client can tell from the errors of a response: codes, paths and messages.
// Sorted, because sibling fields resolve concurrently and their errors come in any order.
func errorSummary(resp graphqlResponseWithExtensions) []string {
	var out []string
	for _, e := range resp.Errors {
		code, _ := e.Extensions["code"].(string)
		out = append(out, code+"|"+strings.Join(e.Path, ".")+"|"+e.Message)
	}
	slices.Sort(out)
	return out
}

func seedCompatCustomer(t *testing.T, r *hubriseAdminRig) (id, token string) {
	t.Helper()
	uid, tok := testhelpers.SeedCustomer(t, r.DB.DB, "compat")
	return uid.String(), tok
}
