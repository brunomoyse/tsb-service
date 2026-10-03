package upstream_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/oauth2"

	"tsb-service/internal/mcp/fakeupstream"
	"tsb-service/internal/mcp/upstream"
)

func newClient(t *testing.T, f *fakeupstream.Server) *upstream.Client {
	t.Helper()
	sa := upstream.ServiceAccount{Issuer: f.URL, ClientID: "mcp-client", ClientSecret: "mcp-secret", ProjectID: "123"}
	return upstream.New(f.URL, sa.TokenSource(context.Background()), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// TestNoOrderWrites guarantees the MCP server can never send an order write
// (or any account/device mutation) upstream: Client.Do only accepts documents
// from this registry.
func TestNoOrderWrites(t *testing.T) {
	forbidden := []string{
		"createOrder", "updateOrder", "updatePaymentStatus",
		"registerDeviceToken", "unregisterDeviceToken", "registerLiveActivityToken", "updateMyOrdersLanguage",
		"updateMe", "deleteMe",
	}
	for name, doc := range upstream.Documents {
		for _, f := range forbidden {
			if regexp.MustCompile(`\b` + f + `\s*\(`).MatchString(doc) {
				t.Errorf("document %s calls forbidden mutation %s", name, f)
			}
		}
		if !strings.HasPrefix(name, "Mcp") {
			t.Errorf("document %s must be named Mcp*", name)
		}
		if !strings.Contains(doc, " "+name) {
			t.Errorf("document %s does not declare operation %s", name, name)
		}
	}
}

func TestTokenSourceAndCall(t *testing.T) {
	f := fakeupstream.New()
	defer f.Close()
	c := newClient(t, f)

	ps, err := c.Products(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) == 0 || ps[0].Translations[2].Name == "" {
		t.Fatalf("unexpected products: %+v", ps)
	}
	if _, err := c.Products(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.TokenRequests != 1 {
		t.Errorf("token should be cached, got %d token requests", f.TokenRequests)
	}
}

func TestInternalURLKeepsPublicHost(t *testing.T) {
	var gotHost string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"x","token_type":"Bearer","expires_in":60}`)
	}))
	defer srv.Close()
	sa := upstream.ServiceAccount{Issuer: "https://auth.example.com", InternalURL: srv.URL, ClientID: "a", ClientSecret: "b", ProjectID: "1"}
	if _, err := sa.TokenSource(context.Background()).Token(); err != nil {
		t.Fatal(err)
	}
	if gotHost != "auth.example.com" {
		t.Errorf("Host = %q, want auth.example.com", gotHost)
	}
}

func TestErrorMapping(t *testing.T) {
	f := fakeupstream.New()
	defer f.Close()
	c := newClient(t, f)
	ctx := context.Background()

	_, err := c.Product(ctx, "missing")
	if !upstream.IsNotFound(err) {
		t.Errorf("want not found, got %v", err)
	}

	f.FailOps["McpCoupons"] = "UNAUTHENTICATED"
	_, err = c.Coupons(ctx)
	var ue *upstream.Error
	if !errors.As(err, &ue) || ue.Kind != upstream.KindUnauthorized {
		t.Fatalf("want unauthorized, got %v", err)
	}
	if strings.Contains(err.Error(), "injected") || strings.Contains(err.Error(), fakeupstream.Token) {
		t.Errorf("error leaks upstream detail: %q", err)
	}

	bad := upstream.New(f.URL, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "wrong"}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	_, err = bad.Products(ctx)
	if !errors.As(err, &ue) || ue.Kind != upstream.KindUnauthorized {
		t.Fatalf("want unauthorized on 401, got %v", err)
	}

	down := upstream.New("http://127.0.0.1:1", oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "x"}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	_, err = down.Products(ctx)
	if !errors.As(err, &ue) || ue.Kind != upstream.KindUnavailable {
		t.Fatalf("want unavailable, got %v", err)
	}
}

func TestImageUploadIsMultipart(t *testing.T) {
	f := fakeupstream.New()
	defer f.Close()
	c := newClient(t, f)
	if _, err := c.UpdateProductImage(context.Background(), "p-maki-saumon", true, "maki.png", "image/png", []byte("PNGDATA")); err != nil {
		t.Fatal(err)
	}
	if string(f.Images["p-maki-saumon"]) != "PNGDATA" {
		t.Errorf("image not received")
	}
}
