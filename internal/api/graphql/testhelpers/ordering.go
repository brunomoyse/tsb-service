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
	"github.com/shopspring/decimal"
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

// MollieStub stands in for api.mollie.com. It answers POST /v2/payments with a cancelable payment
// that has a checkout link, GET /v2/payments/{id} with the payment as it currently stands (open until
// MarkPaid, with what was refunded so far), POST /v2/payments/{id}/refunds with a refund of the amount
// asked for (refused beyond what is refundable) and DELETE /v2/payments/{id} with a canceled payment
// (refused unless the payment is open). It keeps every call and its body, so tests
// can check the amount and lines sent to Mollie, how often Mollie was called and with what amount a
// refund was asked. Failures can be switched on per kind of call.
type MollieStub struct {
	Server *httptest.Server

	mu       sync.Mutex
	requests []MolliePaymentRequest
	calls    []string // "METHOD /path" of every call
	bodies   []string // request body of calls[i]
	failNext int
	failRef  bool
	failCanc bool
	failPay  bool
	failGet  bool
	getDelay time.Duration
	refDelay time.Duration
	seq      int
	payments map[string]*stubPayment
}

// stubPayment is what the stub remembers of a payment it created.
type stubPayment struct {
	amount   decimal.Decimal
	status   string
	refunded decimal.Decimal
	// locked: Mollie no longer lets the payment be cancelled (the customer is in the middle of paying).
	locked bool
	// cap is how much may be refunded in total (the payment amount unless raised: Mollie's
	// amountRemaining "may be higher than the payment amount", e.g. to reimburse a return shipment).
	cap decimal.Decimal
	// noRefundInfo: a paid payment without amountRefunded / amountRemaining, as for payment methods
	// that cannot be refunded through the API (vouchers, gift cards). Refund requests are refused.
	noRefundInfo bool
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
	stub := &MollieStub{payments: map[string]*stubPayment{}}
	stub.Server = httptest.NewServer(http.HandlerFunc(stub.serve))
	t.Cleanup(stub.Server.Close)
	return stub
}

// BaseURL is the value for mollie.Client.BaseURL (with the trailing slash the client requires).
func (s *MollieStub) BaseURL() string { return s.Server.URL + "/" }

// MarkPaid makes the payment paid at Mollie, as the customer's checkout does.
func (s *MollieStub) MarkPaid(paymentID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.payments[paymentID].status = "paid"
}

// LockPayment makes an open payment not cancelable any more, as when the customer is paying right now.
func (s *MollieStub) LockPayment(paymentID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.payments[paymentID].locked = true
}

// SetRefunded sets how much of a payment Mollie considers refunded, as a refund made by staff in the
// Mollie dashboard would (this service is not told about it).
func (s *MollieStub) SetRefunded(paymentID, amount string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.payments[paymentID].refunded = decimal.RequireFromString(amount)
}

// SetRefundCap raises (or lowers) how much may be refunded in total for a payment, and what Mollie
// reports as amountRemaining accordingly.
func (s *MollieStub) SetRefundCap(paymentID, total string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.payments[paymentID].cap = decimal.RequireFromString(total)
}

// SetNotRefundable makes a paid payment one Mollie cannot refund: it reports neither amountRefunded
// nor amountRemaining and refuses refund requests.
func (s *MollieStub) SetNotRefundable(paymentID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.payments[paymentID].noRefundInfo = true
}

// SetRefundDelay makes every refund request take that long to be answered (the refund is only
// recorded once the answer is out), which keeps a cancellation busy while something else happens.
func (s *MollieStub) SetRefundDelay(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refDelay = d
}

// SetLookupDelay makes every GET of a payment take that long, which widens the window in which two
// concurrent callers can both read the payment before either has refunded it.
func (s *MollieStub) SetLookupDelay(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getDelay = d
}

// SetFailLookup makes every GET of a payment answer 500 (until switched off again).
func (s *MollieStub) SetFailLookup(fail bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failGet = fail
}

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

// SetFail makes every payment creation, refund or cancellation (until switched off again) answer
// 422, as Mollie does for a refused call.
func (s *MollieStub) SetFail(pay, refund, cancel bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failPay, s.failRef, s.failCanc = pay, refund, cancel
}

// CallsMatching lists the "METHOD /path" of the calls made so far that start with prefix.
func (s *MollieStub) CallsMatching(prefix string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, c := range s.calls {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

// BodiesMatching lists the request bodies of the calls whose "METHOD /path" starts with prefix.
func (s *MollieStub) BodiesMatching(prefix string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for i, c := range s.calls {
		if strings.HasPrefix(c, prefix) {
			out = append(out, s.bodies[i])
		}
	}
	return out
}

// RefundAmounts lists the amount.value asked for by each refund request of a payment.
func (s *MollieStub) RefundAmounts(t *testing.T, paymentID string) []string {
	t.Helper()
	var out []string
	for _, b := range s.BodiesMatching("POST /v2/payments/" + paymentID + "/refunds") {
		var req struct {
			Amount struct{ Value string } `json:"amount"`
		}
		require.NoError(t, json.Unmarshal([]byte(b), &req), b)
		out = append(out, req.Amount.Value)
	}
	return out
}

func (s *MollieStub) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.calls = append(s.calls, r.Method+" "+r.URL.Path)
	s.bodies = append(s.bodies, string(body))
	failPay, failRef, failCanc, failGet, getDelay, refDelay := s.failPay, s.failRef, s.failCanc, s.failGet, s.getDelay, s.refDelay
	s.seq++
	seq := s.seq
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/hal+json")
	refuse := func(detail string) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"status":422,"title":"Unprocessable Entity","detail":"` + detail + `","_links":{}}`))
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v2/payments":
		var req MolliePaymentRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		fail := failPay
		if s.failNext > 0 {
			s.failNext--
			fail = true
		}
		if !fail {
			s.requests = append(s.requests, req)
		}
		s.mu.Unlock()
		if fail {
			refuse("The amount is invalid")
			return
		}
		id := fmt.Sprintf("tr_stub%06d", seq)
		s.mu.Lock()
		amount := decimal.RequireFromString(req.Amount.Value)
		s.payments[id] = &stubPayment{amount: amount, cap: amount, status: "open"}
		s.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"resource": "payment", "id": id, "status": "open", "mode": "test", "isCancelable": true,
			"createdAt": time.Now().UTC().Format(time.RFC3339),
			"amount":    req.Amount,
			"_links": map[string]any{
				"checkout": map[string]string{"href": "https://www.mollie.com/checkout/select-method/" + id, "type": "text/html"},
			},
		})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v2/payments/"):
		if failGet {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"status":500,"title":"Internal Server Error","detail":"boom","_links":{}}`))
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/v2/payments/")
		s.mu.Lock()
		p, ok := s.payments[id]
		var snapshot stubPayment
		if ok {
			snapshot = *p
		}
		s.mu.Unlock()
		time.Sleep(getDelay) // the state above is what this caller gets, however late the answer arrives
		if !ok {
			http.NotFound(w, r)
			return
		}
		eur := func(d decimal.Decimal) map[string]string {
			return map[string]string{"currency": "EUR", "value": d.StringFixed(2)}
		}
		out := map[string]any{
			"resource": "payment", "id": id, "status": snapshot.status, "mode": "test",
			"isCancelable": snapshot.status == "open" && !snapshot.locked, "amount": eur(snapshot.amount),
		}
		if snapshot.status == "paid" && !snapshot.noRefundInfo {
			out["amountRefunded"] = eur(snapshot.refunded)
			out["amountRemaining"] = eur(snapshot.cap.Sub(snapshot.refunded))
		}
		_ = json.NewEncoder(w).Encode(out)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/refunds"):
		time.Sleep(refDelay)
		if failRef {
			refuse("refused")
			return
		}
		var req struct {
			Amount map[string]string `json:"amount"`
		}
		_ = json.Unmarshal(body, &req)
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v2/payments/"), "/refunds")
		s.mu.Lock()
		p, known := s.payments[id]
		over := false
		if known {
			asked, err := decimal.NewFromString(req.Amount["value"])
			if err != nil || p.status != "paid" || p.noRefundInfo || p.refunded.Add(asked).GreaterThan(p.cap) {
				over = true
			} else {
				p.refunded = p.refunded.Add(asked)
			}
		}
		s.mu.Unlock()
		if over {
			refuse("The amount is higher than the refundable amount")
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"resource": "refund", "id": fmt.Sprintf("re_stub%06d", seq), "amount": req.Amount, "status": "pending"})
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v2/payments/"):
		if failCanc {
			refuse("refused")
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/v2/payments/")
		s.mu.Lock()
		p, known := s.payments[id]
		notOpen := known && (p.status != "open" || p.locked)
		if known && !notOpen {
			p.status = "canceled"
		}
		s.mu.Unlock()
		if notOpen {
			refuse("The payment cannot be canceled")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"resource": "payment", "id": id, "status": "canceled"})
	default:
		http.NotFound(w, r)
	}
}
