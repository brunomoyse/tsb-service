package upstream_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"tsb-service/internal/mcp/fakeupstream"
	"tsb-service/internal/mcp/upstream"
)

type failingTokens struct{}

func (failingTokens) Token() (*oauth2.Token, error) {
	return nil, errors.New("zitadel is down: client secret rejected")
}

func clientFor(t *testing.T, h http.HandlerFunc, logs io.Writer) *upstream.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return upstream.New(srv.URL, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "x"}), slog.New(slog.NewTextHandler(logs, nil)))
}

func asUpstream(t *testing.T, err error, kind upstream.ErrorKind) *upstream.Error {
	t.Helper()
	var ue *upstream.Error
	if !errors.As(err, &ue) || ue.Kind != kind {
		t.Fatalf("want upstream error of kind %d, got %T %v", kind, err, err)
	}
	return ue
}

func TestSendFailureModes(t *testing.T) {
	ctx := t.Context()
	tests := []struct {
		name     string
		handler  http.HandlerFunc
		kind     upstream.ErrorKind
		wantMsg  string
		wantLogs string
	}{
		{"401", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "token expired for user bob", 401) }, upstream.KindUnauthorized, "not allowed", "token expired for user bob"},
		{"403", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "nope", 403) }, upstream.KindUnauthorized, "not allowed", "nope"},
		{"500", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "pq: connection refused 10.1.2.3", 500) }, upstream.KindUnavailable, "had a problem", "10.1.2.3"},
		{"503", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "busy", 503) }, upstream.KindUnavailable, "had a problem", "busy"},
		{"not json", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "<html>gateway</html>") }, upstream.KindUnavailable, "unexpected answer", "gateway"},
		{"wrong shape", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"data":{"products":"not a list"}}`)
		}, upstream.KindUnavailable, "unexpected answer", ""},
		{"cut body", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "100")
			_, _ = io.WriteString(w, `{"data"`)
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		}, upstream.KindUnavailable, "not reachable", "unexpected EOF"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			c := clientFor(t, tt.handler, &logs)
			_, err := c.Products(ctx)
			ue := asUpstream(t, err, tt.kind)
			if !strings.Contains(ue.Message, tt.wantMsg) {
				t.Errorf("message = %q, want %q", ue.Message, tt.wantMsg)
			}
			// The owner never sees the backend's own words; they are only logged.
			for _, leak := range []string{"bob", "10.1.2.3", "gateway", "pq:"} {
				if strings.Contains(ue.Message, leak) {
					t.Errorf("message leaks %q: %q", leak, ue.Message)
				}
			}
			if tt.wantLogs != "" && !strings.Contains(logs.String(), tt.wantLogs) {
				t.Errorf("the detail must be logged: %q", logs.String())
			}
		})
	}
}

func TestTokenFailureIsUnauthorized(t *testing.T) {
	var logs bytes.Buffer
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	defer srv.Close()
	c := upstream.New(srv.URL, failingTokens{}, slog.New(slog.NewTextHandler(&logs, nil)))
	_, err := c.Products(t.Context())
	ue := asUpstream(t, err, upstream.KindUnauthorized)
	if strings.Contains(ue.Message, "secret") || !strings.Contains(ue.Message, "could not sign in") {
		t.Errorf("message = %q", ue.Message)
	}
	if hits != 0 {
		t.Error("no request may be sent without a token")
	}
	if !strings.Contains(logs.String(), "client secret rejected") {
		t.Errorf("logs: %q", logs.String())
	}
	if err := c.UpdateOrderingEnabled(t.Context(), true); err == nil {
		t.Error("writes need a token too")
	}
	err = c.Upload(t.Context(), "McpUpdateProduct", nil, "variables.input.image", "a.png", "image/png", []byte("x"), nil)
	asUpstream(t, err, upstream.KindUnauthorized)
}

func TestMapErrors(t *testing.T) {
	tests := []struct {
		name    string
		code    string
		message string
		kind    upstream.ErrorKind
		msg     string
	}{
		{"unauthenticated", "UNAUTHENTICATED", "x", upstream.KindUnauthorized, "not allowed"},
		{"forbidden code", "FORBIDDEN", "x", upstream.KindUnauthorized, "not allowed"},
		{"unauthorized text", "", "User is Unauthorized", upstream.KindUnauthorized, "not allowed"},
		{"forbidden text", "", "forbidden resource", upstream.KindUnauthorized, "not allowed"},
		{"access denied text", "", "Access Denied", upstream.KindUnauthorized, "not allowed"},
		{"not found code", "NOT_FOUND", "x", upstream.KindNotFound, "no longer exists"},
		{"not found text", "", "product not found", upstream.KindNotFound, "no longer exists"},
		{"no rows text", "", "sql: no rows in result set", upstream.KindNotFound, "no longer exists"},
		{"custom code", "BAD_USER_INPUT", "price must be positive", upstream.KindRejected, "refused the change (BAD_USER_INPUT)"},
		{"internal code", "INTERNAL_SERVER_ERROR", "pq: boom", upstream.KindRejected, "refused the request."},
		{"validation code", "GRAPHQL_VALIDATION_FAILED", "unknown field", upstream.KindRejected, "refused the request."},
		{"parse code", "GRAPHQL_PARSE_FAILED", "syntax", upstream.KindRejected, "refused the request."},
		{"no code", "", "something odd", upstream.KindRejected, "refused the request."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := clientFor(t, func(w http.ResponseWriter, _ *http.Request) {
				ext := map[string]any{}
				if tt.code != "" {
					ext["code"] = tt.code
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]any{{"message": tt.message, "extensions": ext}, {"message": "second"}}})
			}, io.Discard)
			_, err := c.Coupons(t.Context())
			ue := asUpstream(t, err, tt.kind)
			if !strings.Contains(ue.Message, tt.msg) || ue.Code != tt.code {
				t.Errorf("error = %+v, want message containing %q", ue, tt.msg)
			}
			if strings.Contains(ue.Message, tt.message) && tt.message != "x" {
				t.Errorf("the backend's message must not reach the owner: %q", ue.Message)
			}
			if upstream.IsNotFound(err) != (tt.kind == upstream.KindNotFound) {
				t.Errorf("IsNotFound = %v", upstream.IsNotFound(err))
			}
		})
	}
}

func TestIsNotFoundOnWrappedAndForeignErrors(t *testing.T) {
	nf := &upstream.Error{Kind: upstream.KindNotFound}
	if !upstream.IsNotFound(nf) || !upstream.IsNotFound(errors.Join(errors.New("x"), nf)) {
		t.Error("IsNotFound must see wrapped errors")
	}
	if upstream.IsNotFound(nil) || upstream.IsNotFound(errors.New("not found")) || upstream.IsNotFound(&upstream.Error{Kind: upstream.KindRejected}) {
		t.Error("only upstream not-found errors count")
	}
}

func TestLongBodiesAreTruncatedInLogs(t *testing.T) {
	var logs bytes.Buffer
	c := clientFor(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
		_, _ = io.WriteString(w, strings.Repeat("A", 2000))
	}, &logs)
	_, _ = c.Products(t.Context())
	if got := strings.Count(logs.String(), "A"); got != 512 || !strings.Contains(logs.String(), "…") {
		t.Errorf("logged %d bytes of the body", got)
	}
}

func TestInvalidBaseURLIsAnUnreachableSystem(t *testing.T) {
	c := upstream.New("http://bad host", oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "x"}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := c.Products(t.Context()); err == nil || !strings.Contains(err.Error(), "build request") {
		t.Errorf("err = %v", err)
	}
}

func TestUnknownOperationsAreRefused(t *testing.T) {
	var hits int
	c := clientFor(t, func(http.ResponseWriter, *http.Request) { hits++ }, io.Discard)
	if err := c.Do(t.Context(), "createOrder", nil, nil); err == nil || !strings.Contains(err.Error(), `unknown operation "createOrder"`) {
		t.Errorf("Do: %v", err)
	}
	if err := c.Upload(t.Context(), "UpdateOrder", nil, "variables.x", "a", "image/png", nil, nil); err == nil || !strings.Contains(err.Error(), "unknown operation") {
		t.Errorf("Upload: %v", err)
	}
	if hits != 0 {
		t.Error("an unknown operation must never be sent")
	}
}

func TestEncodeFailuresNeverReachTheNetwork(t *testing.T) {
	var hits int
	c := clientFor(t, func(http.ResponseWriter, *http.Request) { hits++ }, io.Discard)
	bad := map[string]any{"x": make(chan int)}
	if err := c.Do(t.Context(), "McpProducts", bad, nil); err == nil || !strings.Contains(err.Error(), "encode McpProducts") {
		t.Errorf("Do: %v", err)
	}
	if err := c.Upload(t.Context(), "McpUpdateProduct", bad, "variables.input.image", "a.png", "image/png", nil, nil); err == nil || !strings.Contains(err.Error(), "encode McpUpdateProduct") {
		t.Errorf("Upload: %v", err)
	}
	// A cancelled context is an unreachable system, not a crash.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := c.Products(ctx)
	asUpstream(t, err, upstream.KindUnavailable)
	if hits != 0 {
		t.Errorf("hits = %d", hits)
	}
}

func TestRequestHeadersAndBody(t *testing.T) {
	var gotAuth, gotCT, gotLang, gotAccept string
	var body map[string]any
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotCT, gotLang, gotAccept = r.Header.Get("Authorization"), r.Header.Get("Content-Type"), r.Header.Get("Accept-Language"), r.Header.Get("Accept")
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = io.WriteString(w, `{"data":{"restaurantConfig":{"orderingEnabled":true,"preparationMinutes":25,"openingHours":null,"orderingHours":null,"updatedAt":"2026-10-01T08:00:00Z"}}}`)
	}, io.Discard)
	cfg, err := c.RestaurantConfig(t.Context())
	if err != nil || !cfg.OrderingEnabled || cfg.PreparationMinutes != 25 {
		t.Fatal(cfg, err)
	}
	if gotAuth != "Bearer x" || gotCT != "application/json" || gotLang != "fr" || gotAccept != "application/json" {
		t.Errorf("headers: %q %q %q %q", gotAuth, gotCT, gotLang, gotAccept)
	}
	if body["operationName"] != "McpRestaurantConfig" || !strings.Contains(body["query"].(string), "query McpRestaurantConfig") {
		t.Errorf("body: %v", body)
	}
	if _, ok := body["variables"]; ok {
		t.Error("no variables must be omitted")
	}
	// The base URL may end with a slash.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/graphql" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"data":{"products":[]}}`)
	}))
	defer srv.Close()
	c2 := upstream.New(srv.URL+"/api/v1//", oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "x"}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := c2.Products(t.Context()); err != nil {
		t.Error(err)
	}
}

func TestMissingEntitiesAreNotFound(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":{"product":null,"coupon":null,"order":null}}`)
	}, io.Discard)
	ctx := t.Context()
	if _, err := c.Product(ctx, "x"); !upstream.IsNotFound(err) {
		t.Errorf("Product: %v", err)
	}
	if _, err := c.Coupon(ctx, "x"); !upstream.IsNotFound(err) {
		t.Errorf("Coupon: %v", err)
	}
	if _, err := c.Order(ctx, "x"); !upstream.IsNotFound(err) {
		t.Errorf("Order: %v", err)
	}
}

// Every client method reports an upstream failure and returns no data with it.
func TestEveryMethodSurfacesUpstreamFailures(t *testing.T) {
	ctx := t.Context()
	day := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	calls := map[string]func(c *upstream.Client) error{
		"McpProducts":          func(c *upstream.Client) error { _, err := c.Products(ctx); return err },
		"McpProduct":           func(c *upstream.Client) error { _, err := c.Product(ctx, "p-maki-box"); return err },
		"McpCategories":        func(c *upstream.Client) error { _, err := c.Categories(ctx); return err },
		"McpRestaurantConfig":  func(c *upstream.Client) error { _, err := c.RestaurantConfig(ctx); return err },
		"McpScheduleOverrides": func(c *upstream.Client) error { _, err := c.ScheduleOverrides(ctx, day, day); return err },
		"McpCoupons":           func(c *upstream.Client) error { _, err := c.Coupons(ctx); return err },
		"McpCoupon":            func(c *upstream.Client) error { _, err := c.Coupon(ctx, "cp-old"); return err },
		"McpOrders":            func(c *upstream.Client) error { _, err := c.Orders(ctx); return err },
		"McpOrder":             func(c *upstream.Client) error { _, err := c.Order(ctx, "o-1"); return err },
		"McpOrderHistory": func(c *upstream.Client) error {
			_, err := c.OrderHistory(ctx, upstream.OrderHistoryInput{})
			return err
		},
		"McpCustomerStats": func(c *upstream.Client) error {
			_, err := c.CustomerStats(ctx, upstream.CustomerStatsInput{})
			return err
		},
		"McpUpdateProduct": func(c *upstream.Client) error {
			_, err := c.UpdateProduct(ctx, "p-maki-box", upstream.UpdateProductInput{})
			return err
		},
		"McpCreateProduct": func(c *upstream.Client) error {
			_, err := c.CreateProduct(ctx, upstream.CreateProductInput{CategoryID: "cat-box", Translations: []upstream.Translation{{Language: "fr", Name: "X"}}})
			return err
		},
		"McpCreateChoiceGroup": func(c *upstream.Client) error {
			_, err := c.CreateChoiceGroup(ctx, upstream.ChoiceGroupInput{ProductID: "p-maki-box"})
			return err
		},
		"McpUpdateChoiceGroup": func(c *upstream.Client) error {
			_, err := c.UpdateChoiceGroup(ctx, "g-sauce", upstream.ChoiceGroupInput{})
			return err
		},
		"McpDeleteChoiceGroup": func(c *upstream.Client) error { return c.DeleteChoiceGroup(ctx, "g-sauce") },
		"McpCreateChoice": func(c *upstream.Client) error {
			_, err := c.CreateChoice(ctx, upstream.ChoiceInput{ChoiceGroupID: "g-sauce"})
			return err
		},
		"McpUpdateChoice": func(c *upstream.Client) error {
			_, err := c.UpdateChoice(ctx, "c-soja", upstream.ChoiceInput{})
			return err
		},
		"McpDeleteChoice":          func(c *upstream.Client) error { return c.DeleteChoice(ctx, "c-soja") },
		"McpUpdateOrderingEnabled": func(c *upstream.Client) error { return c.UpdateOrderingEnabled(ctx, false) },
		"McpUpdateOpeningHours": func(c *upstream.Client) error {
			return c.UpdateOpeningHours(ctx, upstream.Week{"monday": {Open: "10:00", Close: "11:00"}})
		},
		"McpUpdateOrderingHours": func(c *upstream.Client) error {
			return c.UpdateOrderingHours(ctx, upstream.Week{"monday": {Open: "10:00", Close: "11:00"}})
		},
		"McpUpdatePreparationMinutes": func(c *upstream.Client) error { return c.UpdatePreparationMinutes(ctx, 20) },
		"McpUpsertScheduleOverride": func(c *upstream.Client) error {
			return c.UpsertScheduleOverride(ctx, upstream.ScheduleOverrideInput{Date: day, Closed: true})
		},
		"McpDeleteScheduleOverride": func(c *upstream.Client) error { return c.DeleteScheduleOverride(ctx, day) },
		"McpCreateCoupon":           func(c *upstream.Client) error { _, err := c.CreateCoupon(ctx, upstream.CouponInput{}); return err },
		"McpUpdateCoupon": func(c *upstream.Client) error {
			_, err := c.UpdateCoupon(ctx, "cp-old", upstream.CouponInput{})
			return err
		},
	}
	for op, call := range calls {
		t.Run(op, func(t *testing.T) {
			f := fakeupstream.New()
			defer f.Close()
			c := newClient(t, f)
			if err := call(c); err != nil {
				t.Fatalf("%s must work against the fake: %v", op, err)
			}
			f.Lock()
			f.FailOps[op] = "BOOM"
			f.Unlock()
			err := call(c)
			asUpstream(t, err, upstream.KindRejected)
		})
	}
	// Writes that return data return nothing on failure.
	f := fakeupstream.New()
	defer f.Close()
	c := newClient(t, f)
	f.Lock()
	f.FailOps["McpUpdateProduct"] = "BOOM"
	f.Unlock()
	if p, err := c.UpdateProduct(ctx, "p-maki-box", upstream.UpdateProductInput{}); p != nil || err == nil {
		t.Errorf("UpdateProduct returned %v, %v", p, err)
	}
	if p, err := c.UpdateProductImage(ctx, "p-maki-box", false, "a.png", "image/png", []byte("x")); p != nil || err == nil {
		t.Errorf("UpdateProductImage returned %v, %v", p, err)
	}
}

func TestUpdatesNeverSendCreationIDs(t *testing.T) {
	f := fakeupstream.New()
	defer f.Close()
	c := newClient(t, f)
	ctx := t.Context()
	// The ids of the parent are part of the URL-like arguments of an update,
	// not of its input: they are stripped.
	if _, err := c.UpdateChoiceGroup(ctx, "g-sauce", upstream.ChoiceGroupInput{ProductID: "p-other", MaxSelections: new(3)}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.UpdateChoice(ctx, "c-soja", upstream.ChoiceInput{ProductID: "p-other", ChoiceGroupID: "g-other", SortOrder: new(2)}); err != nil {
		t.Fatal(err)
	}
	f.Lock()
	defer f.Unlock()
	for _, call := range f.Calls {
		if call.Op != "McpUpdateChoiceGroup" && call.Op != "McpUpdateChoice" {
			continue
		}
		in := call.Vars["input"].(map[string]any)
		if in["productId"] != nil || in["choiceGroupId"] != nil {
			t.Errorf("%s input carries parent ids: %v", call.Op, in)
		}
	}
}

func TestParseWeek(t *testing.T) {
	if w, err := upstream.ParseWeek(nil); w != nil || err != nil {
		t.Errorf("empty: %v %v", w, err)
	}
	if w, err := upstream.ParseWeek(json.RawMessage("null")); w != nil || err != nil {
		t.Errorf("null: %v %v", w, err)
	}
	// Days without opening time are closed days and are dropped.
	w, err := upstream.ParseWeek(json.RawMessage(`{"monday":null,"tuesday":{"open":"","close":""},"wednesday":{"open":"11:00","close":"14:00"}}`))
	if err != nil || len(w) != 1 || w["wednesday"].Close != "14:00" {
		t.Errorf("week: %v %v", w, err)
	}
	if _, err := upstream.ParseWeek(json.RawMessage(`[1,2]`)); err == nil {
		t.Error("a list is not a week")
	}
	if _, err := upstream.ParseWeek(json.RawMessage(`{"monday":`)); err == nil {
		t.Error("truncated JSON")
	}
}

func TestInitialKeepsOnlyTheFirstLetter(t *testing.T) {
	var out struct {
		Customer struct {
			LastName upstream.Initial `json:"lastName"`
		} `json:"customer"`
	}
	for in, want := range map[string]string{`"Dupont"`: "D", `"  dupont"`: "D", `"élodie"`: "É", `""`: "", `"   "`: "", `null`: "", `"李"`: "李"} {
		if err := json.Unmarshal([]byte(`{"customer":{"lastName":`+in+`}}`), &out); err != nil || string(out.Customer.LastName) != want {
			t.Errorf("%s -> %q, %v; want %q", in, out.Customer.LastName, err, want)
		}
	}
	if err := json.Unmarshal([]byte(`{"customer":{"lastName":42}}`), &out); err == nil {
		t.Error("a number is not a name")
	}
	// Unmarshalling again resets a previous value.
	out.Customer.LastName = "X"
	_ = json.Unmarshal([]byte(`{"customer":{"lastName":null}}`), &out)
	if out.Customer.LastName != "" {
		t.Errorf("stale initial: %q", out.Customer.LastName)
	}
}

func TestName(t *testing.T) {
	tests := []struct {
		first string
		last  upstream.Initial
		want  string
	}{{"Marie", "D", "Marie D."}, {" Marie ", "", "Marie"}, {"", "D", "D."}, {"  ", "D", "D."}, {"", "", ""}}
	for _, tt := range tests {
		if got := upstream.Name(tt.first, tt.last); got != tt.want {
			t.Errorf("Name(%q, %q) = %q, want %q", tt.first, tt.last, got, tt.want)
		}
	}
}

func TestProductNameInAndDateKey(t *testing.T) {
	p := &upstream.Product{Name: "Maki", Translations: []upstream.Translation{{Language: "zh", Name: "卷"}, {Language: "en", Name: ""}}}
	if p.NameIn("zh") != "卷" || p.NameIn("en") != "Maki" || p.NameIn("nl") != "Maki" {
		t.Error("NameIn falls back to the base name when a translation is missing or empty")
	}
	o := upstream.ScheduleOverride{Date: time.Date(2026, 12, 25, 23, 30, 0, 0, time.FixedZone("x", -5*3600))}
	if o.DateKey() != "2026-12-26" {
		t.Errorf("DateKey = %s (the UTC date is the stored DATE)", o.DateKey())
	}
	if d, err := upstream.OverrideDate("2026-12-25"); err != nil || d.Hour() != 0 || d.Location() != time.UTC {
		t.Errorf("OverrideDate: %v %v", d, err)
	}
	if _, err := upstream.OverrideDate("25/12/2026"); err == nil {
		t.Error("bad date")
	}
}

func TestScopesAndTokenFailure(t *testing.T) {
	sa := upstream.ServiceAccount{Issuer: "https://auth.example.com", ProjectID: "123"}
	want := []string{"openid", "urn:zitadel:iam:org:project:id:123:aud", "urn:zitadel:iam:org:projects:roles"}
	if got := sa.Scopes(); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("scopes = %v", got)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, `{"error":"invalid_client"}`, 401) }))
	defer srv.Close()
	bad := upstream.ServiceAccount{Issuer: srv.URL, ClientID: "a", ClientSecret: "b", ProjectID: "1"}
	if _, err := bad.TokenSource(t.Context()).Token(); err == nil {
		t.Error("a rejected client must not produce a token")
	}
	// An issuer that is not a URL still builds a source (the internal URL is used as-is).
	odd := upstream.ServiceAccount{Issuer: "://bad", InternalURL: srv.URL, ClientID: "a", ClientSecret: "b", ProjectID: "1"}
	if _, err := odd.TokenSource(t.Context()).Token(); err == nil {
		t.Error("401 expected")
	}
}
