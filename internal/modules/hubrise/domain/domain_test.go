package domain_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"tsb-service/internal/modules/hubrise/domain"
)

func TestRedactedNeverLeaks(t *testing.T) {
	r := domain.Redacted("super-secret-token")
	for name, got := range map[string]string{
		"String": r.String(), "GoString": r.GoString(),
		"%s": fmt.Sprintf("%s", r), "%v": fmt.Sprintf("%v", r), "%+v": fmt.Sprintf("%+v", r),
		"%#v": fmt.Sprintf("%#v", r), "%q": fmt.Sprintf("%q", r), "%x": fmt.Sprintf("%x", r),
		"struct": fmt.Sprintf("%+v", struct{ Token domain.Redacted }{r}),
	} {
		if bytes.Contains([]byte(got), []byte("super-secret")) {
			t.Errorf("%s leaked the secret: %s", name, got)
		}
		if !bytes.Contains([]byte(got), []byte("[REDACTED]")) {
			t.Errorf("%s = %q, want the redaction marker", name, got)
		}
	}
	if r.Reveal() != "super-secret-token" {
		t.Error("Reveal must return the secret")
	}

	j, err := json.Marshal(map[string]domain.Redacted{"token": r})
	if err != nil || bytes.Contains(j, []byte("super-secret")) {
		t.Errorf("json = %s, %v", j, err)
	}
	txt, err := r.MarshalText()
	if err != nil || string(txt) != "[REDACTED]" {
		t.Errorf("MarshalText = %s, %v", txt, err)
	}
}

func TestRedactedInZapLogs(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	logger := zap.New(core)
	logger.Info("connecting", zap.Any("token", domain.Redacted("super-secret-token")), zap.Stringer("again", domain.Redacted("super-secret-token")))
	for _, e := range logs.All() {
		for _, f := range e.Context {
			if bytes.Contains([]byte(fmt.Sprint(f.String, f.Interface)), []byte("super-secret")) {
				t.Errorf("field %s leaked: %+v", f.Key, f)
			}
		}
	}
}

type fakeCodec struct{ err error }

func (c fakeCodec) Encrypt(p string) ([]byte, error) { return []byte("enc:" + p), nil }
func (c fakeCodec) Decrypt(b []byte) (string, error) {
	if c.err != nil {
		return "", c.err
	}
	return string(bytes.TrimPrefix(b, []byte("enc:"))), nil
}

func TestConnectionState(t *testing.T) {
	var nilConn *domain.Connection
	if nilConn.Connected() || nilConn.Active() {
		t.Error("a nil connection is neither connected nor active")
	}
	if _, err := nilConn.AccessToken(fakeCodec{}); !errors.Is(err, domain.ErrNotConnected) {
		t.Errorf("AccessToken on nil: %v", err)
	}

	c := &domain.Connection{Enabled: true}
	if c.Connected() || c.Active() {
		t.Error("a row without token is not connected")
	}
	if _, err := c.AccessToken(fakeCodec{}); !errors.Is(err, domain.ErrNotConnected) {
		t.Errorf("AccessToken without token: %v", err)
	}

	c.AccessTokenEnc = []byte("enc:tok")
	if !c.Connected() || !c.Active() {
		t.Error("a connected, enabled row is active")
	}
	tok, err := c.AccessToken(fakeCodec{})
	if err != nil || tok.Reveal() != "tok" {
		t.Errorf("AccessToken = %v, %v", tok, err)
	}
	if _, err := c.AccessToken(fakeCodec{err: errors.New("boom")}); err == nil {
		t.Error("a codec failure must surface")
	}

	c.Enabled = false
	if c.Active() {
		t.Error("disabled is not active")
	}
	c.Enabled = true
	now := time.Now()
	c.AuthErrorAt = &now
	if c.Active() {
		t.Error("an auth error pauses the integration")
	}
	c.AuthErrorAt, c.RelinkRequiredAt = nil, &now
	if c.Active() {
		t.Error("a pending relink pauses the integration")
	}
}

func TestChargeRefDefaults(t *testing.T) {
	c := &domain.Connection{}
	if c.DeliveryChargeRefOrDefault() != "DELIVERY" || c.PaymentFeeChargeRefOrDefault() != "PAYMENT_FEE" {
		t.Error("unset refs must fall back to the defaults")
	}
	empty, custom := "", "FEE-X"
	c.DeliveryChargeRef, c.PaymentFeeChargeRef = &empty, &custom
	if c.DeliveryChargeRefOrDefault() != "DELIVERY" {
		t.Error("an empty ref falls back to the default")
	}
	if c.PaymentFeeChargeRefOrDefault() != "FEE-X" {
		t.Error("a configured ref wins")
	}
}

func TestEntityTypeValid(t *testing.T) {
	for _, ty := range []domain.EntityType{domain.EntityCategory, domain.EntityProduct, domain.EntityChoiceGroup, domain.EntityChoice} {
		if !ty.Valid() {
			t.Errorf("%s must be valid", ty)
		}
	}
	if domain.EntityType("deal").Valid() {
		t.Error("deal is not an entity type")
	}
}

func TestCatalogLinkLocalID(t *testing.T) {
	id := uuid.New()
	for _, ty := range []domain.EntityType{domain.EntityCategory, domain.EntityProduct, domain.EntityChoiceGroup, domain.EntityChoice} {
		l := &domain.CatalogLink{EntityType: ty, HubriseKey: "k", HubriseRef: "r", Origin: domain.OriginImport}
		if l.LocalID() != uuid.Nil {
			t.Errorf("%s: an unset link has no local id", ty)
		}
		l.SetLocalID(id)
		if l.LocalID() != id {
			t.Errorf("%s: LocalID = %s, want %s", ty, l.LocalID(), id)
		}
		if err := l.Validate(); err != nil {
			t.Errorf("%s: Validate: %v", ty, err)
		}
		// SetLocalID clears the other ids.
		other := uuid.New()
		l.SetLocalID(other)
		n := 0
		for _, p := range []*uuid.UUID{l.CategoryID, l.ProductID, l.ChoiceGroupID, l.ChoiceID} {
			if p != nil {
				n++
			}
		}
		if n != 1 || l.LocalID() != other {
			t.Errorf("%s: SetLocalID left %d ids set", ty, n)
		}
	}
	unknown := &domain.CatalogLink{EntityType: "deal"}
	unknown.SetLocalID(id)
	if unknown.LocalID() != uuid.Nil {
		t.Error("an unknown entity type has no local id")
	}
}

func TestCatalogLinkValidate(t *testing.T) {
	id := uuid.New()
	valid := func() *domain.CatalogLink {
		return &domain.CatalogLink{EntityType: domain.EntityProduct, ProductID: &id, HubriseKey: "SKU1", HubriseRef: "SKU1", Origin: domain.OriginAdopted}
	}
	if err := valid().Validate(); err != nil {
		t.Fatalf("valid link: %v", err)
	}
	cases := map[string]func(*domain.CatalogLink){
		"bad type":        func(l *domain.CatalogLink) { l.EntityType = "deal" },
		"no key":          func(l *domain.CatalogLink) { l.HubriseKey = "" },
		"no ref":          func(l *domain.CatalogLink) { l.HubriseRef = "" },
		"no local id":     func(l *domain.CatalogLink) { l.ProductID = nil },
		"two local ids":   func(l *domain.CatalogLink) { l.CategoryID = &id },
		"bad origin":      func(l *domain.CatalogLink) { l.Origin = "guess" },
		"wrong id column": func(l *domain.CatalogLink) { l.ProductID, l.ChoiceID = nil, &id },
	}
	for name, mutate := range cases {
		l := valid()
		mutate(l)
		if err := l.Validate(); !errors.Is(err, domain.ErrInvalidLink) {
			t.Errorf("%s: err = %v, want ErrInvalidLink", name, err)
		}
	}
}

func TestNewOutboxItem(t *testing.T) {
	due := time.Now()
	oid := uuid.New()
	it := domain.NewOutboxItem(domain.OutboxOrderCreate, &oid, "", nil, due)
	if string(it.Payload) != "{}" || it.DedupeKey != nil || it.State != domain.OutboxPending || !it.NextAttemptAt.Equal(due) {
		t.Errorf("item = %+v", it)
	}
	it = domain.NewOutboxItem(domain.OutboxInventory, nil, "inv:sku:A", json.RawMessage(`{"a":1}`), due)
	if it.DedupeKey == nil || *it.DedupeKey != "inv:sku:A" || string(it.Payload) != `{"a":1}` || it.OrderID != nil {
		t.Errorf("item = %+v", it)
	}
}

func TestIsTerminalLocal(t *testing.T) {
	for _, s := range []string{domain.LocalPickedUp, domain.LocalDelivered, domain.LocalCancelled, domain.LocalFailed} {
		if !domain.IsTerminalLocal(s) {
			t.Errorf("%s ends an order", s)
		}
	}
	for _, s := range []string{domain.LocalPending, domain.LocalConfirmed, domain.LocalPreparing, domain.LocalAwaitingPickUp, domain.LocalOutForDelivery, "", "SOMETHING"} {
		if domain.IsTerminalLocal(s) {
			t.Errorf("%q does not end an order", s)
		}
	}
}

func TestCreatePayloadRoundTrips(t *testing.T) {
	in := domain.CreatePayload{
		Version: domain.PayloadVersion, Ref: "AB12", Total: decimal.RequireFromString("12.50"), UserID: uuid.New(),
		Items:   []domain.PayloadItem{{ProductID: uuid.New(), ProductName: "Sushi", Price: decimal.RequireFromString("6.25"), Quantity: 2}},
		Charges: []domain.PayloadAdjustment{{Kind: domain.AdjustmentDelivery, Name: "Livraison", Amount: decimal.RequireFromString("2.5")}},
	}
	raw, err := in.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var out domain.CreatePayload
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Ref != in.Ref || !out.Total.Equal(in.Total) || len(out.Items) != 1 || out.Items[0].Quantity != 2 ||
		!out.Charges[0].Amount.Equal(in.Charges[0].Amount) || out.UserID != in.UserID {
		t.Fatalf("round trip changed the payload: %+v", out)
	}
}

func TestInvalidSettingErrorNamesTheField(t *testing.T) {
	err := &domain.InvalidSettingError{Field: "catalogLanguage", Message: "must be one of fr, en"}
	if got, want := err.Error(), "hubrise: invalid setting catalogLanguage: must be one of fr, en"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	var target *domain.InvalidSettingError
	if !errors.As(fmt.Errorf("wrapped: %w", err), &target) || target.Field != "catalogLanguage" {
		t.Fatal("the error is found through wrapping")
	}
}
