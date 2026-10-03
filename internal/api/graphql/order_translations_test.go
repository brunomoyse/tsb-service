package graphql_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/testhelpers"
)

// Ordering must never depend on the request language having a translation. Production once failed
// every Dutch order because the product queries filtered translations in WHERE (no category has an
// "nl" translation), so each product came back "not found". These tests run the whole ordering
// surface against every language x translation shape through the real resolvers and a real
// Postgres: createOrder, quoteOrder and every way an order is read back.

// translations maps language -> name.
type translations map[string]string

// fallbackChain mirrors productDomain.translationFallbackOrder: the request language, then fr, en,
// nl, zh. It is restated here on purpose, so a change to the production chain has to change a test.
var fallbackChain = map[string][]string{
	"fr": {"fr", "en", "nl", "zh"},
	"en": {"en", "fr", "nl", "zh"},
	"nl": {"nl", "fr", "en", "zh"},
	"zh": {"zh", "fr", "en", "nl"},
}

// expectedName is the name a customer sees: the first non-blank translation along the chain.
func expectedName(names translations, lang string) string {
	for _, l := range fallbackChain[lang] {
		if names[l] != "" {
			return names[l]
		}
	}
	return ""
}

// requestLanguage is an Accept-Language header and the language the middleware resolves it to.
type requestLanguage struct {
	name      string
	header    string
	effective string
}

var requestLanguages = []requestLanguage{
	{"fr", "fr", "fr"},
	{"en", "en", "en"},
	{"nl", "nl", "nl"},
	{"zh", "zh", "zh"},
	{"regional nl-BE with weights", "nl-BE,nl;q=0.9,en;q=0.8", "nl"},
	{"regional zh-CN", "zh-CN,zh;q=0.9", "zh"},
	{"unsupported de falls back to en", "de", "en"},
	{"unsupported de-DE with weights", "de-DE,de;q=0.9", "en"},
	{"missing header", "", "en"},
}

// shape builds a translation set where each name says which language it is in.
func shape(label string, langs ...string) translations {
	out := translations{}
	for _, l := range langs {
		out[l] = fmt.Sprintf("%s [%s]", label, l)
	}
	return out
}

// translationShapes are the translation sets a product or a category can have in production:
// complete, without nl (every category today), partial, single-language and with a blank row.
func translationShapes(prefix string) []struct {
	name  string
	names translations
} {
	blankNL := shape(prefix+" blank-nl", "fr", "en")
	blankNL["nl"] = ""
	blankFRNL := shape(prefix+" blank-fr", "en", "zh")
	blankFRNL["fr"] = ""
	return []struct {
		name  string
		names translations
	}{
		{"full", shape(prefix+" full", "fr", "en", "nl", "zh")},
		{"no nl", shape(prefix+" no-nl", "fr", "en", "zh")},
		{"fr only", shape(prefix+" fr-only", "fr")},
		{"en only", shape(prefix+" en-only", "en")},
		{"nl only", shape(prefix+" nl-only", "nl")},
		{"zh only", shape(prefix+" zh-only", "zh")},
		{"blank nl row", blankNL},
		{"blank fr row", blankFRNL},
	}
}

type seededProduct struct {
	id       uuid.UUID
	code     string
	label    string
	names    translations
	category translations
}

// orderChunkSize keeps an order under the 50-line cap.
const orderChunkSize = 32

func setupOrderingEnv(t *testing.T) (*TestContext, *testhelpers.MollieStub) {
	t.Helper()
	t.Setenv("APP_BASE_URL", "https://shop.example.test")
	t.Setenv("MOLLIE_WEBHOOK_URL", "https://api.example.test/api/v1/payments/webhook")
	stub := testhelpers.NewMollieStub(t)
	return setupTestContextWith(t, testContextOptions{MollieBaseURL: stub.BaseURL()}), stub
}

func TestOrderTranslationFallback(t *testing.T) {
	tc, stub := setupOrderingEnv(t)
	db := tc.DB.DB

	// Every product shape in every category shape.
	var all []seededProduct
	order := 10
	for _, cat := range translationShapes("Cat") {
		catID := testhelpers.SeedCategory(t, db, order, cat.names)
		order++
		for _, prod := range translationShapes("Prod") {
			code := fmt.Sprintf("T%02d", len(all))
			id := testhelpers.SeedProduct(t, db, testhelpers.ProductSpec{
				CategoryID: catID, Code: code, Price: "4.00", Names: prod.names,
			})
			all = append(all, seededProduct{
				id: id, code: code, label: cat.name + " category / " + prod.name + " product",
				names: prod.names, category: cat.names,
			})
		}
	}
	require.Greater(t, len(all), orderChunkSize)

	customerID, token := testhelpers.SeedCustomer(t, db, "translations")
	admin := adminToken(t, tc)

	t.Run("a single product orders in every language, whatever it is translated into", func(t *testing.T) {
		// The production incident: one product, a category without nl, an nl customer. Every
		// product shape in the category shapes that matter (what production has, and the extremes).
		var singles []seededProduct
		for _, p := range all {
			if strings.HasPrefix(p.label, "no nl category") || strings.HasPrefix(p.label, "full category") ||
				strings.HasPrefix(p.label, "fr only category") || strings.HasPrefix(p.label, "nl only category") {
				singles = append(singles, p)
			}
		}
		require.Len(t, singles, 32)
		for _, lang := range requestLanguages {
			for _, p := range singles {
				items := lines(p.id, 1)
				input := createOrderInput("PICKUP", items, nil)

				quote := quoteAs(t, tc, token, lang.header, quoteInput("PICKUP", items, nil))
				require.Empty(t, quoteCodes(quote), "quote [%s] %s", lang.name, p.label)
				require.Equal(t, "4.00", quote.Total, "quote [%s] %s", lang.name, p.label)

				got, orderErr := createOrderAs(t, tc, token, lang.header, input)
				require.Nil(t, orderErr, "[%s] %s: %+v", lang.name, p.label, orderErr)
				require.Len(t, got.Items, 1, "[%s] %s: the line vanished", lang.name, p.label)
				assert.Equal(t, p.id.String(), got.Items[0].ProductID)
				assert.Equal(t, expectedName(p.names, lang.effective), got.Items[0].Product.Name, "[%s] %s", lang.name, p.label)
				assert.Equal(t, expectedName(p.category, lang.effective), got.Items[0].Product.Category.Name, "[%s] %s", lang.name, p.label)
				assert.Equal(t, lang.effective, loadStoredOrder(t, tc, got.ID).Language)
			}
		}
	})

	t.Run("a mixed basket orders, quotes the same and reads back in every language", func(t *testing.T) {
		for _, lang := range requestLanguages {
			for start := 0; start < len(all); start += orderChunkSize {
				chunk := all[start:min(start+orderChunkSize, len(all))]
				var pairs []any
				for _, p := range chunk {
					pairs = append(pairs, p.id, 1)
				}
				items := lines(pairs...)

				quote := quoteAs(t, tc, token, lang.header, quoteInput("PICKUP", items, nil))
				require.Empty(t, quoteCodes(quote), "quote [%s]", lang.name)
				require.Len(t, quote.Lines, len(chunk))

				created := mustCreateOrder(t, tc, token, lang.header, createOrderInput("PICKUP", items, nil))
				require.Len(t, created.Items, len(chunk), "[%s] createOrder lost lines", lang.name)
				stored := loadStoredOrder(t, tc, created.ID)
				assert.Equal(t, quote.Total, stored.TotalPrice, "[%s] quote and stored total", lang.name)
				assert.Equal(t, lang.effective, stored.Language)
				assert.Equal(t, len(chunk), countRows(t, tc, `SELECT count(*) FROM order_product WHERE order_id = $1`, created.ID))

				// Names as createOrder returned them, then from every way of reading the order back.
				assertItemNames(t, created.Items, chunk, lang.effective, "createOrder ["+lang.name+"]")
				for _, read := range []string{"fr", "en", "nl", "zh", "de", ""} {
					effective := "en"
					for _, rl := range requestLanguages {
						if rl.header == read {
							effective = rl.effective
						}
					}
					for _, path := range orderReadPaths(customerID.String()) {
						items := readOrderItems(t, tc, path, token, admin, read, created.ID)
						require.Len(t, items, len(chunk), "%s read in %q lost lines of an order made in [%s]", path.name, read, lang.name)
						assertItemNames(t, items, chunk, effective, fmt.Sprintf("%s read in %q", path.name, read))
					}
				}
			}
		}
	})

	t.Run("online payment sends the fallback names to Mollie", func(t *testing.T) {
		pick := all[:12] // spans the category shapes with their different product shapes
		var pairs []any
		for _, p := range pick {
			pairs = append(pairs, p.id, 1)
		}
		items := lines(pairs...)
		locales := map[string]string{"fr": "fr_BE", "en": "en_US", "nl": "nl_BE", "zh": "zh_CN"}

		for _, lang := range requestLanguages {
			before := len(stub.Requests())
			got := mustCreateOrder(t, tc, token, lang.header, createOrderInput("PICKUP", items, map[string]any{"isOnlinePayment": true}))
			require.NotNil(t, got.Payment, "[%s] an online order returns its payment", lang.name)
			assert.Equal(t, "open", got.Payment.Status)
			assert.Contains(t, string(got.Payment.Links), "checkout", "[%s] the payment links hold the checkout url", lang.name)

			requests := stub.Requests()
			require.Len(t, requests, before+1, "[%s] exactly one Mollie payment", lang.name)
			sent := requests[before]
			assert.Equal(t, locales[lang.effective], sent.Locale, "[%s]", lang.name)

			stored := loadStoredOrder(t, tc, got.ID)
			assert.Equal(t, stored.TotalPrice, sent.Amount.Value, "[%s] Mollie is asked for the stored total", lang.name)
			sum := decimal.Zero
			descriptions := map[string]bool{}
			for _, l := range sent.Lines {
				v, err := decimal.NewFromString(l.TotalAmount.Value)
				require.NoError(t, err)
				sum = sum.Add(v)
				descriptions[l.Description] = true
			}
			assert.Equal(t, sent.Amount.Value, sum.StringFixed(2), "[%s] Mollie rejects lines that do not add up to the amount", lang.name)
			for _, p := range pick {
				want := fmt.Sprintf("%s ‒ %s %s", p.code, expectedName(p.category, lang.effective), expectedName(p.names, lang.effective))
				assert.True(t, descriptions[want], "[%s] Mollie line %q missing in %v", lang.name, want, descriptions)
			}
		}
	})

	t.Run("a product or category without any translation row still orders", func(t *testing.T) {
		catFull := testhelpers.SeedCategory(t, db, 90, shape("Plain", "fr", "en", "nl", "zh"))
		catNone := testhelpers.SeedCategory(t, db, 91, translations{})
		bare := testhelpers.SeedProduct(t, db, testhelpers.ProductSpec{CategoryID: catFull, Code: "BARE", Price: "4.00", Names: translations{}})
		inBare := testhelpers.SeedProduct(t, db, testhelpers.ProductSpec{CategoryID: catNone, Code: "INBARE", Price: "4.00", Names: shape("In bare", "fr")})
		for _, lang := range requestLanguages {
			items := lines(bare, 1, inBare, 1)
			quote := quoteAs(t, tc, token, lang.header, quoteInput("PICKUP", items, nil))
			require.Empty(t, quoteCodes(quote), "quote [%s]", lang.name)
			resp := gqlAs(t, tc, token, lang.header, `mutation ($input: CreateOrderInput!) { createOrder(input: $input) { id totalPrice } }`,
				map[string]any{"input": createOrderInput("PICKUP", items, nil)})
			require.Empty(t, resp.Errors, "[%s] %+v", lang.name, resp.Errors)
		}
	})

	t.Run("a product or category without any translation row reads back", func(t *testing.T) {
		catNone := testhelpers.SeedCategory(t, db, 92, translations{})
		bare := testhelpers.SeedProduct(t, db, testhelpers.ProductSpec{CategoryID: catNone, Code: "BARE2", Price: "4.00", Names: translations{}})
		got, orderErr := createOrderAs(t, tc, token, "nl", createOrderInput("PICKUP", lines(bare, 1), nil))
		require.Nil(t, orderErr, "%+v", orderErr)
		require.Len(t, got.Items, 1)
	})
}

// orderReadPath is one way a client reads an order back.
type orderReadPath struct {
	name  string
	admin bool
	query string
	vars  func(orderID string) map[string]any
	// pick returns the order's items from the response data.
	pick func(t *testing.T, data json.RawMessage, orderID string) []orderItemView
}

func orderReadPaths(customerID string) []orderReadPath {
	itemsOf := func(t *testing.T, raw json.RawMessage) []orderItemView {
		t.Helper()
		var order struct {
			ID    string          `json:"id"`
			Items []orderItemView `json:"items"`
		}
		require.NoError(t, json.Unmarshal(raw, &order))
		return order.Items
	}
	// fromList finds the order in a list: a missing order is as bad as a missing line.
	fromList := func(t *testing.T, raw json.RawMessage, orderID string) []orderItemView {
		t.Helper()
		var orders []struct {
			ID    string          `json:"id"`
			Items []orderItemView `json:"items"`
		}
		require.NoError(t, json.Unmarshal(raw, &orders))
		for _, o := range orders {
			if o.ID == orderID {
				return o.Items
			}
		}
		require.Failf(t, "order missing from the list", "order %s in %d orders", orderID, len(orders))
		return nil
	}
	field := func(name string, raw json.RawMessage) json.RawMessage {
		var m map[string]json.RawMessage
		_ = json.Unmarshal(raw, &m)
		return m[name]
	}
	return []orderReadPath{
		{
			name:  "myOrder",
			query: `query ($id: ID!) { myOrder(id: $id) { id ` + orderItemsFragment + ` } }`,
			vars:  func(id string) map[string]any { return map[string]any{"id": id} },
			pick: func(t *testing.T, data json.RawMessage, _ string) []orderItemView {
				return itemsOf(t, field("myOrder", data))
			},
		},
		{
			name:  "myOrders",
			query: `query { myOrders(first: 100) { id ` + orderItemsFragment + ` } }`,
			vars:  func(string) map[string]any { return nil },
			pick: func(t *testing.T, data json.RawMessage, id string) []orderItemView {
				return fromList(t, field("myOrders", data), id)
			},
		},
		{
			name: "order (staff)", admin: true,
			query: `query ($id: ID!) { order(id: $id) { id ` + orderItemsFragment + ` } }`,
			vars:  func(id string) map[string]any { return map[string]any{"id": id} },
			pick: func(t *testing.T, data json.RawMessage, _ string) []orderItemView {
				return itemsOf(t, field("order", data))
			},
		},
		{
			name: "orders (staff list)", admin: true,
			query: `query { orders { id ` + orderItemsFragment + ` } }`,
			vars:  func(string) map[string]any { return nil },
			pick: func(t *testing.T, data json.RawMessage, id string) []orderItemView {
				return fromList(t, field("orders", data), id)
			},
		},
		{
			name: "customerOrders (admin)", admin: true,
			query: `query ($u: ID!) { customerOrders(userId: $u, first: 100) { id ` + orderItemsFragment + ` } }`,
			vars:  func(string) map[string]any { return map[string]any{"u": customerID} },
			pick: func(t *testing.T, data json.RawMessage, id string) []orderItemView {
				return fromList(t, field("customerOrders", data), id)
			},
		},
		{
			name: "orderHistory (admin)", admin: true,
			query: `query { orderHistory(input: { first: 100 }) { orders { id ` + orderItemsFragment + ` } } }`,
			vars:  func(string) map[string]any { return nil },
			pick: func(t *testing.T, data json.RawMessage, id string) []orderItemView {
				return fromList(t, field("orders", field("orderHistory", data)), id)
			},
		},
	}
}

func readOrderItems(t *testing.T, tc *TestContext, path orderReadPath, customer, admin, lang, orderID string) []orderItemView {
	t.Helper()
	token := customer
	if path.admin {
		token = admin
	}
	resp := gqlAs(t, tc, token, lang, path.query, path.vars(orderID))
	require.Empty(t, resp.Errors, "%s read in %q: %+v", path.name, lang, resp.Errors)
	return path.pick(t, resp.Data, orderID)
}

// assertItemNames checks every seeded product of the chunk is among the items with the names the
// fallback chain gives.
func assertItemNames(t *testing.T, items []orderItemView, chunk []seededProduct, lang, where string) {
	t.Helper()
	byID := map[string]orderItemView{}
	for _, item := range items {
		byID[item.ProductID] = item
	}
	for _, p := range chunk {
		item, ok := byID[p.id.String()]
		if !assert.True(t, ok, "%s: %s is missing", where, p.label) {
			continue
		}
		assert.Equal(t, expectedName(p.names, lang), item.Product.Name, "%s: %s product name", where, p.label)
		assert.Equal(t, expectedName(p.category, lang), item.Product.Category.Name, "%s: %s category name", where, p.label)
	}
}
