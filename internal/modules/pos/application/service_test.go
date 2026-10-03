package application

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"tsb-service/internal/modules/pos/domain"
)

type fakeDevices struct {
	domain.DeviceRepository
	devices map[uuid.UUID]*domain.Device
	lookups int
}

func (f *fakeDevices) FindByID(_ context.Context, id uuid.UUID) (*domain.Device, error) {
	f.lookups++
	d, ok := f.devices[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	cp := *d
	return &cp, nil
}

func (f *fakeDevices) TouchLastSeen(context.Context, uuid.UUID) error { return nil }

var testSecret = []byte("0123456789abcdef0123456789abcdef")

func newTestPOS(t *testing.T) (*Service, *fakeDevices, uuid.UUID, []byte) {
	t.Helper()
	deviceSecret := []byte("device-secret")
	sum := sha256.Sum256(deviceSecret)
	id := uuid.New()
	repo := &fakeDevices{devices: map[uuid.UUID]*domain.Device{
		id: {ID: id, DeviceSecretHash: hex.EncodeToString(sum[:])},
	}}
	return NewService(DefaultConfig(testSecret), repo), repo, id, sum[:]
}

func login(t *testing.T, s *Service, id uuid.UUID, key []byte) string {
	t.Helper()
	in := DeviceLoginInput{DeviceID: id, Timestamp: time.Now().UnixMilli(), Nonce: "nonce-1"}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(buildLoginHmacPayload(in)))
	in.HMAC = base64.StdEncoding.EncodeToString(mac.Sum(nil))
	tok, err := s.DeviceLogin(context.Background(), in)
	if err != nil {
		t.Fatalf("DeviceLogin: %v", err)
	}
	return tok.Token
}

func TestVerifyAccessToken_RevokedDeviceIsRejected(t *testing.T) {
	s, repo, id, key := newTestPOS(t)
	tok := login(t, s, id, key)

	if got, err := s.VerifyAccessToken(context.Background(), tok); err != nil || got != id {
		t.Fatalf("active device: got %v err %v", got, err)
	}

	now := time.Now()
	repo.devices[id].RevokedAt = &now

	// Within the cache TTL the earlier "active" answer is still served.
	if _, err := s.VerifyAccessToken(context.Background(), tok); err != nil {
		t.Fatalf("cached check: %v", err)
	}
	s.active.now = func() time.Time { return now.Add(deviceStatusTTL + time.Second) }

	if _, err := s.VerifyAccessToken(context.Background(), tok); !errors.Is(err, ErrDeviceRevoked) {
		t.Fatalf("revoked device: err = %v, want ErrDeviceRevoked", err)
	}
}

func TestVerifyAccessToken_CachesLookups(t *testing.T) {
	s, repo, id, key := newTestPOS(t)
	tok := login(t, s, id, key)
	before := repo.lookups
	for i := 0; i < 5; i++ {
		if _, err := s.VerifyAccessToken(context.Background(), tok); err != nil {
			t.Fatal(err)
		}
	}
	if n := repo.lookups - before; n != 1 {
		t.Fatalf("device lookups = %d, want 1", n)
	}
}

func TestVerifyAccessToken_UnknownDeviceAndMissingExp(t *testing.T) {
	s, _, _, _ := newTestPOS(t)
	sign := func(claims jwt.MapClaims) string {
		tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(testSecret)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}

	unknown := sign(jwt.MapClaims{"sub": uuid.NewString(), "iss": "tsb-pos", "exp": time.Now().Add(time.Hour).Unix()})
	if _, err := s.VerifyAccessToken(context.Background(), unknown); !errors.Is(err, ErrDeviceNotEnrolled) {
		t.Fatalf("unknown device: err = %v, want ErrDeviceNotEnrolled", err)
	}

	noExp := sign(jwt.MapClaims{"sub": uuid.NewString(), "iss": "tsb-pos"})
	if _, err := s.VerifyAccessToken(context.Background(), noExp); err == nil {
		t.Fatal("token without exp was accepted")
	}
}
