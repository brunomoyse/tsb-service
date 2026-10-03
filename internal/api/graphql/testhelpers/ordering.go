package testhelpers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"
)

// Seeding helpers for the ordering tests: products and categories with an explicit set of
// translations (the fixtures in fixtures.go always create the same three), choice groups, cached
// delivery addresses and customers.

// SeedCategory inserts a category with exactly the given translations (language -> name). An empty
// map creates a category without any translation row, an empty name a blank row.
func SeedCategory(t *testing.T, db *sqlx.DB, order int, names map[string]string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := db.ExecContext(t.Context(),
		`INSERT INTO product_categories (id, "order", slug) VALUES ($1, $2, $3)`,
		id, order, "cat-"+id.String()[:8])
	require.NoError(t, err, "insert category")
	for lang, name := range names {
		_, err := db.ExecContext(t.Context(),
			`INSERT INTO product_category_translations (id, product_category_id, language, name) VALUES ($1, $2, $3, $4)`,
			uuid.New(), id, lang, name)
		require.NoError(t, err, "insert category translation %s", lang)
	}
	return id
}

// ProductSpec describes a product to seed. The zero value of every flag is the ordinary product: on
// sale, discountable, food VAT.
type ProductSpec struct {
	CategoryID uuid.UUID
	Code       string
	// Price is a decimal literal ("12.50").
	Price string
	// Names maps language -> name; an empty map creates a product without any translation row.
	Names            map[string]string
	SoldOut          bool
	NotDiscountable  bool
	LunchOnly        bool
	VatCategory      string
	NotVisible       bool
	DescriptionLangs []string
}

// SeedProduct inserts a product and returns its id.
func SeedProduct(t *testing.T, db *sqlx.DB, spec ProductSpec) uuid.UUID {
	t.Helper()
	id := uuid.New()
	vat := spec.VatCategory
	if vat == "" {
		vat = "food"
	}
	code := spec.Code
	if code == "" {
		code = "P-" + id.String()[:8]
	}
	_, err := db.ExecContext(t.Context(), `
		INSERT INTO products (id, category_id, price, is_visible, is_available, code, slug, is_discountable, is_lunch_only, vat_category)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		id, spec.CategoryID, spec.Price, !spec.NotVisible, !spec.SoldOut, code,
		strings.ToLower(code)+"-"+id.String()[:8], !spec.NotDiscountable, spec.LunchOnly, vat)
	require.NoError(t, err, "insert product %s", code)
	for lang, name := range spec.Names {
		_, err := db.ExecContext(t.Context(),
			`INSERT INTO product_translations (id, product_id, language, name, description) VALUES ($1, $2, $3, $4, $5)`,
			uuid.New(), id, lang, name, "")
		require.NoError(t, err, "insert product translation %s", lang)
	}
	return id
}

// SeedChoiceGroup inserts a choice group with min/max selections and its translations (locale -> name).
func SeedChoiceGroup(t *testing.T, db *sqlx.DB, productID uuid.UUID, minSel, maxSel int, names map[string]string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := db.ExecContext(t.Context(),
		`INSERT INTO product_choice_groups (id, product_id, min_selections, max_selections) VALUES ($1, $2, $3, $4)`,
		id, productID, minSel, maxSel)
	require.NoError(t, err, "insert choice group")
	for locale, name := range names {
		_, err := db.ExecContext(t.Context(),
			`INSERT INTO product_choice_group_translations (product_choice_group_id, locale, name) VALUES ($1, $2, $3)`,
			id, locale, name)
		require.NoError(t, err, "insert choice group translation")
	}
	return id
}

// SeedChoice inserts a choice of a group with its price modifier ("0.50") and translations.
func SeedChoice(t *testing.T, db *sqlx.DB, productID, groupID uuid.UUID, modifier string, names map[string]string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := db.ExecContext(t.Context(),
		`INSERT INTO product_choices (id, product_id, choice_group_id, price_modifier) VALUES ($1, $2, $3, $4)`,
		id, productID, groupID, modifier)
	require.NoError(t, err, "insert choice")
	for locale, name := range names {
		_, err := db.ExecContext(t.Context(),
			`INSERT INTO product_choice_translations (product_choice_id, locale, name) VALUES ($1, $2, $3)`,
			id, locale, name)
		require.NoError(t, err, "insert choice translation")
	}
	return id
}

// SeedAddress puts a delivery address in the address cache, so resolving its place id never reaches
// Google. distanceMeters is the driving distance from the restaurant.
func SeedAddress(t *testing.T, db *sqlx.DB, placeID string, distanceMeters int, postcode string) {
	t.Helper()
	_, err := db.ExecContext(t.Context(), `
		INSERT INTO address_cache (place_id, formatted_address, lat, lng, street_name, house_number, postcode,
			municipality_name, distance_meters, duration_seconds)
		VALUES ($1, $2, 50.63, 5.57, 'Rue du Test', '1', $3, 'Liege', $4, 600)`,
		placeID, "Rue du Test 1, "+postcode+" Liege", postcode, distanceMeters)
	require.NoError(t, err, "insert address cache")
}

// SeedCustomer inserts a customer who gets no e-mail (a successful cash order sends one from a
// goroutine and the test environment has no mail client) and returns its id and an access token.
func SeedCustomer(t *testing.T, db *sqlx.DB, label string) (uuid.UUID, string) {
	t.Helper()
	id := uuid.New()
	_, err := db.ExecContext(t.Context(), `
		INSERT INTO users (id, first_name, last_name, email, zitadel_user_id, notify_order_updates)
		VALUES ($1, 'Test', $2, $3, $4, false)`,
		id, label, fmt.Sprintf("%s-%s@example.com", label, id.String()[:8]), id.String())
	require.NoError(t, err, "insert customer")
	token, err := GenerateTestAccessToken(id.String(), false)
	require.NoError(t, err)
	return id, token
}

// MollieStub stands in for api.mollie.com: it answers POST /v2/payments with a payment that has a
// checkout link and keeps what was asked, so tests can check the amount and lines sent to Mollie.
type MollieStub struct {
	Server *httptest.Server

	mu       sync.Mutex
	requests []MolliePaymentRequest
	failNext int
	seq      int
}

// MolliePaymentRequest is the part of a Create Payment call the tests look at.
type MolliePaymentRequest struct {
	Amount struct {
		Currency string `json:"currency"`
		Value    string `json:"value"`
	} `json:"amount"`
	Locale      string `json:"locale"`
	RedirectURL string `json:"redirectUrl"`
	Lines       []struct {
		Type        string `json:"type"`
		Description string `json:"description"`
		Quantity    int    `json:"quantity"`
		TotalAmount struct {
			Value string `json:"value"`
		} `json:"totalAmount"`
	} `json:"lines"`
}

// NewMollieStub starts the stub; it is closed with the test.
func NewMollieStub(t *testing.T) *MollieStub {
	t.Helper()
	stub := &MollieStub{}
	stub.Server = httptest.NewServer(http.HandlerFunc(stub.serve))
	t.Cleanup(stub.Server.Close)
	return stub
}

// BaseURL is the value for mollie.Client.BaseURL (with the trailing slash the client requires).
func (s *MollieStub) BaseURL() string { return s.Server.URL + "/" }

// FailNext makes the next n payment creations answer 422, as Mollie does for a refused payment.
func (s *MollieStub) FailNext(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failNext = n
}

// Requests are the payments asked for so far.
func (s *MollieStub) Requests() []MolliePaymentRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]MolliePaymentRequest(nil), s.requests...)
}

func (s *MollieStub) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/v2/payments" {
		http.NotFound(w, r)
		return
	}
	body, _ := io.ReadAll(r.Body)
	var req MolliePaymentRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	if s.failNext > 0 {
		s.failNext--
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"status":422,"title":"Unprocessable Entity","detail":"The amount is invalid","_links":{}}`))
		return
	}
	s.requests = append(s.requests, req)
	s.seq++
	id := fmt.Sprintf("tr_stub%06d", s.seq)
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"resource":  "payment",
		"id":        id,
		"status":    "open",
		"mode":      "test",
		"createdAt": time.Now().UTC().Format(time.RFC3339),
		"amount":    req.Amount,
		"_links": map[string]any{
			"checkout": map[string]string{"href": "https://www.mollie.com/checkout/select-method/" + id, "type": "text/html"},
		},
	})
}
