package application

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/pos/domain"
)

// memDevices is a complete in-memory domain.DeviceRepository.
type memDevices struct {
	devices   map[uuid.UUID]*domain.Device
	lookups   int
	findErr   error
	touched   []uuid.UUID
	touchErr  error
	fcm       map[uuid.UUID]string
	fcmErr    error
	activeFCM []string
	activeErr error
}

func (m *memDevices) FindByID(_ context.Context, id uuid.UUID) (*domain.Device, error) {
	m.lookups++
	if m.findErr != nil {
		return nil, m.findErr
	}
	d, ok := m.devices[id]
	if !ok {
		return nil, errNoRows
	}
	cp := *d
	return &cp, nil
}

func (m *memDevices) TouchLastSeen(_ context.Context, id uuid.UUID) error {
	m.touched = append(m.touched, id)
	return m.touchErr
}

func (m *memDevices) UpdateFCMToken(_ context.Context, id uuid.UUID, token string) error {
	if m.fcmErr != nil {
		return m.fcmErr
	}
	m.fcm[id] = token
	return nil
}

func (m *memDevices) FindActiveFCMTokens(context.Context) ([]string, error) {
	return m.activeFCM, m.activeErr
}

var errNoRows = fmt.Errorf("pos device: %w", sql.ErrNoRows) // what the sqlx repository answers for an unknown id

type deviceEnv struct {
	svc  *Service
	repo *memDevices
	id   uuid.UUID
	key  []byte // HMAC key = SHA-256 of the device secret
}

func newDeviceEnv(t *testing.T) *deviceEnv {
	t.Helper()
	sum := sha256.Sum256([]byte("device-secret"))
	id := uuid.New()
	repo := &memDevices{
		devices: map[uuid.UUID]*domain.Device{id: {ID: id, DeviceSecretHash: hex.EncodeToString(sum[:])}},
		fcm:     map[uuid.UUID]string{},
	}
	return &deviceEnv{svc: NewService(DefaultConfig(testSecret), repo), repo: repo, id: id, key: sum[:]}
}

func sign(key []byte, payload string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(payload))
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

func (e *deviceEnv) loginInput(ts int64) DeviceLoginInput {
	in := DeviceLoginInput{DeviceID: e.id, Timestamp: ts, Nonce: "nonce-0123456789ab"}
	in.HMAC = sign(e.key, buildLoginHmacPayload(in))
	return in
}

func (e *deviceEnv) fcmInput(ts int64, token string) FCMTokenInput {
	in := FCMTokenInput{DeviceID: e.id, FCMToken: token, Timestamp: ts, Nonce: "n-1"}
	in.HMAC = sign(e.key, fmt.Sprintf("%s|%s|%d|%s", in.DeviceID, in.FCMToken, in.Timestamp, in.Nonce))
	return in
}

func TestDeviceLogin(t *testing.T) {
	now := func() int64 { return time.Now().UnixMilli() }

	t.Run("a valid proof yields an 8h token for the device and records the visit", func(t *testing.T) {
		e := newDeviceEnv(t)
		tok, err := e.svc.DeviceLogin(t.Context(), e.loginInput(now()))
		require.NoError(t, err)
		assert.Equal(t, e.id, tok.DeviceID)
		assert.Equal(t, int64(8*3600), tok.ExpiresIn)
		assert.Equal(t, []uuid.UUID{e.id}, e.repo.touched)

		got, err := e.svc.VerifyAccessToken(t.Context(), tok.Token)
		require.NoError(t, err)
		assert.Equal(t, e.id, got)
		assert.WithinDuration(t, time.Now().Add(8*time.Hour), e.svc.AccessTokenExpiry(tok.Token), 5*time.Second)
	})

	t.Run("a failure to record last seen does not block login", func(t *testing.T) {
		e := newDeviceEnv(t)
		e.repo.touchErr = errors.New("db down")
		_, err := e.svc.DeviceLogin(t.Context(), e.loginInput(now()))
		require.NoError(t, err)
	})

	t.Run("unknown device", func(t *testing.T) {
		e := newDeviceEnv(t)
		in := e.loginInput(now())
		in.DeviceID = uuid.New()
		_, err := e.svc.DeviceLogin(t.Context(), in)
		assert.ErrorIs(t, err, ErrDeviceNotEnrolled)
		assert.Empty(t, e.repo.touched)
	})

	// A database outage is not "not enrolled": the handheld could not tell it from having lost its
	// enrolment (403) and may wipe its credentials on the first outage. It is a server fault.
	t.Run("a repository error is a server fault, not 'not enrolled'", func(t *testing.T) {
		e := newDeviceEnv(t)
		e.repo.findErr = errors.New("db down")
		_, err := e.svc.DeviceLogin(t.Context(), e.loginInput(now()))
		require.ErrorContains(t, err, "load pos device: db down")
		assert.NotErrorIs(t, err, ErrDeviceNotEnrolled)
		assert.Empty(t, e.repo.touched)

		// The same for the other calls that authenticate the device.
		in := FCMTokenInput{DeviceID: e.id, FCMToken: "t", Timestamp: now(), Nonce: "n"}
		err = e.svc.UpdateDeviceFCMToken(t.Context(), in)
		require.ErrorContains(t, err, "load pos device")
		assert.NotErrorIs(t, err, ErrDeviceNotEnrolled)
	})

	t.Run("revoked device", func(t *testing.T) {
		e := newDeviceEnv(t)
		at := time.Now()
		e.repo.devices[e.id].RevokedAt = &at
		_, err := e.svc.DeviceLogin(t.Context(), e.loginInput(now()))
		assert.ErrorIs(t, err, ErrDeviceRevoked)
	})

	t.Run("timestamps outside the skew window, past or future, are stale", func(t *testing.T) {
		e := newDeviceEnv(t)
		for _, skew := range []time.Duration{-2 * time.Minute, 2 * time.Minute} {
			_, err := e.svc.DeviceLogin(t.Context(), e.loginInput(time.Now().Add(skew).UnixMilli()))
			assert.ErrorIs(t, err, ErrStaleRequest, skew.String())
		}
		_, err := e.svc.DeviceLogin(t.Context(), e.loginInput(time.Now().Add(-30*time.Second).UnixMilli()))
		assert.NoError(t, err, "within 60s is accepted")
	})

	// KNOWN LIMITATION (documented in verifyDeviceRequest, "_ = nonce"): the nonce is only part of the
	// HMAC, it is never remembered. A captured login request replays successfully until its timestamp
	// leaves the 60 s window; the rate limiter is the only mitigation. If replay protection is ever
	// added (remember nonces for the window), this test must flip to expect ErrReplay or similar.
	t.Run("a captured login request can be replayed inside the skew window", func(t *testing.T) {
		e := newDeviceEnv(t)
		in := e.loginInput(now())
		first, err := e.svc.DeviceLogin(t.Context(), in)
		require.NoError(t, err)
		second, err := e.svc.DeviceLogin(t.Context(), in)
		require.NoError(t, err, "the same nonce, timestamp and HMAC are accepted a second time")
		assert.Equal(t, first.DeviceID, second.DeviceID)
		assert.Len(t, e.repo.touched, 2, "and recorded as two visits")
	})

	t.Run("a signature made with another key, a tampered nonce or garbage is rejected", func(t *testing.T) {
		e := newDeviceEnv(t)
		in := e.loginInput(now())
		in.HMAC = sign([]byte("wrong-key"), buildLoginHmacPayload(in))
		_, err := e.svc.DeviceLogin(t.Context(), in)
		assert.ErrorIs(t, err, ErrInvalidHMAC)

		in = e.loginInput(now())
		in.Nonce = "tampered-nonce-xxxxx"
		_, err = e.svc.DeviceLogin(t.Context(), in)
		assert.ErrorIs(t, err, ErrInvalidHMAC)

		in = e.loginInput(now())
		in.HMAC = "!!!not base64!!!"
		_, err = e.svc.DeviceLogin(t.Context(), in)
		assert.ErrorIs(t, err, ErrInvalidHMAC)
		assert.Empty(t, e.repo.touched)
	})

	t.Run("a device whose stored secret hash is not hex can never log in", func(t *testing.T) {
		e := newDeviceEnv(t)
		e.repo.devices[e.id].DeviceSecretHash = "zz-not-hex"
		_, err := e.svc.DeviceLogin(t.Context(), e.loginInput(now()))
		assert.ErrorIs(t, err, ErrInvalidHMAC)
	})
}

func TestUpdateDeviceFCMToken(t *testing.T) {
	now := func() int64 { return time.Now().UnixMilli() }

	t.Run("stores the token of an authenticated device", func(t *testing.T) {
		e := newDeviceEnv(t)
		require.NoError(t, e.svc.UpdateDeviceFCMToken(t.Context(), e.fcmInput(now(), "fcm-abc")))
		assert.Equal(t, "fcm-abc", e.repo.fcm[e.id])
	})

	t.Run("the signature covers the token", func(t *testing.T) {
		e := newDeviceEnv(t)
		in := e.fcmInput(now(), "fcm-abc")
		in.FCMToken = "fcm-attacker"
		assert.ErrorIs(t, e.svc.UpdateDeviceFCMToken(t.Context(), in), ErrInvalidHMAC)
		assert.Empty(t, e.repo.fcm)
	})

	t.Run("stale, revoked and unknown devices are refused", func(t *testing.T) {
		e := newDeviceEnv(t)
		assert.ErrorIs(t, e.svc.UpdateDeviceFCMToken(t.Context(), e.fcmInput(now()-int64(time.Hour/time.Millisecond), "t")), ErrStaleRequest)
		in := e.fcmInput(now(), "t")
		in.DeviceID = uuid.New()
		assert.ErrorIs(t, e.svc.UpdateDeviceFCMToken(t.Context(), in), ErrDeviceNotEnrolled)
		at := time.Now()
		e.repo.devices[e.id].RevokedAt = &at
		assert.ErrorIs(t, e.svc.UpdateDeviceFCMToken(t.Context(), e.fcmInput(now(), "t")), ErrDeviceRevoked)
		assert.Empty(t, e.repo.fcm)
	})

	t.Run("a storage failure is returned", func(t *testing.T) {
		e := newDeviceEnv(t)
		boom := errors.New("db down")
		e.repo.fcmErr = boom
		assert.ErrorIs(t, e.svc.UpdateDeviceFCMToken(t.Context(), e.fcmInput(now(), "t")), boom)
	})
}

func TestGetActiveFCMTokens(t *testing.T) {
	e := newDeviceEnv(t)
	e.repo.activeFCM = []string{"a", "b"}
	got, err := e.svc.GetActiveFCMTokens(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, got)

	boom := errors.New("db down")
	e.repo.activeErr = boom
	_, err = e.svc.GetActiveFCMTokens(t.Context())
	assert.ErrorIs(t, err, boom)
}

func TestVerifyAccessToken_Rejections(t *testing.T) {
	e := newDeviceEnv(t)
	valid := func() jwt.MapClaims {
		return jwt.MapClaims{"sub": e.id.String(), "iss": "tsb-pos", "exp": time.Now().Add(time.Hour).Unix()}
	}
	mint := func(method jwt.SigningMethod, claims jwt.MapClaims, key any) string {
		s, err := jwt.NewWithClaims(method, claims).SignedString(key)
		require.NoError(t, err)
		return s
	}

	t.Run("accepts a well-formed token", func(t *testing.T) {
		got, err := e.svc.VerifyAccessToken(t.Context(), mint(jwt.SigningMethodHS256, valid(), testSecret))
		require.NoError(t, err)
		assert.Equal(t, e.id, got)
	})

	rejects := map[string]string{
		"garbage":            "not-a-jwt",
		"wrong secret":       mint(jwt.SigningMethodHS256, valid(), []byte("another-secret-another-secret-00")),
		"wrong HMAC variant": mint(jwt.SigningMethodHS512, valid(), testSecret),
		"unsigned (alg none)": func() string {
			return mint(jwt.SigningMethodNone, valid(), jwt.UnsafeAllowNoneSignatureType)
		}(),
		"wrong issuer": func() string {
			c := valid()
			c["iss"] = "zitadel"
			return mint(jwt.SigningMethodHS256, c, testSecret)
		}(),
		"expired": func() string {
			c := valid()
			c["exp"] = time.Now().Add(-time.Minute).Unix()
			return mint(jwt.SigningMethodHS256, c, testSecret)
		}(),
		"subject is not a uuid": func() string {
			c := valid()
			c["sub"] = "device-1"
			return mint(jwt.SigningMethodHS256, c, testSecret)
		}(),
		"no subject": func() string {
			c := valid()
			delete(c, "sub")
			return mint(jwt.SigningMethodHS256, c, testSecret)
		}(),
	}
	for name, tok := range rejects {
		t.Run("rejects "+name, func(t *testing.T) {
			got, err := e.svc.VerifyAccessToken(t.Context(), tok)
			require.Error(t, err)
			assert.Equal(t, uuid.Nil, got)
		})
	}
}

func TestVerifyAccessToken_DeviceLookup(t *testing.T) {
	mint := func(e *deviceEnv) string {
		s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": e.id.String(), "iss": "tsb-pos", "exp": time.Now().Add(time.Hour).Unix()}).SignedString(testSecret)
		require.NoError(t, err)
		return s
	}

	t.Run("a lookup failure is reported and not cached", func(t *testing.T) {
		e := newDeviceEnv(t)
		tok := mint(e)
		e.repo.findErr = errors.New("db down")
		_, err := e.svc.VerifyAccessToken(t.Context(), tok)
		require.ErrorContains(t, err, "load pos device")

		e.repo.findErr = nil
		got, err := e.svc.VerifyAccessToken(t.Context(), tok)
		require.NoError(t, err, "the outage must not poison the cache")
		assert.Equal(t, e.id, got)
	})

	t.Run("a repository answering nil, nil means not enrolled", func(t *testing.T) {
		e := newDeviceEnv(t)
		e.svc.devices = nilDevice{}
		_, err := e.svc.VerifyAccessToken(t.Context(), mint(e))
		assert.ErrorIs(t, err, ErrDeviceNotEnrolled)
	})

	t.Run("a revocation is cached for the TTL, then noticed", func(t *testing.T) {
		e := newDeviceEnv(t)
		tok := mint(e)
		at := time.Now()
		e.repo.devices[e.id].RevokedAt = &at
		_, err := e.svc.VerifyAccessToken(t.Context(), tok)
		assert.ErrorIs(t, err, ErrDeviceRevoked)
		lookups := e.repo.lookups
		_, err = e.svc.VerifyAccessToken(t.Context(), tok)
		assert.ErrorIs(t, err, ErrDeviceRevoked)
		assert.Equal(t, lookups, e.repo.lookups, "second check served from the cache")
	})
}

type nilDevice struct{ domain.DeviceRepository }

func (nilDevice) FindByID(context.Context, uuid.UUID) (*domain.Device, error) { return nil, nil }

func TestAccessTokenExpiry(t *testing.T) {
	e := newDeviceEnv(t)
	exp := time.Now().Add(3 * time.Hour).Truncate(time.Second)
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"exp": exp.Unix()}).SignedString(testSecret)
	require.NoError(t, err)
	assert.True(t, exp.Equal(e.svc.AccessTokenExpiry(tok)))

	noExp, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": "x"}).SignedString(testSecret)
	require.NoError(t, err)
	assert.True(t, e.svc.AccessTokenExpiry(noExp).IsZero())
	assert.True(t, e.svc.AccessTokenExpiry("garbage").IsZero())
}

func TestDeviceStatusCache_DropsExpiredEntriesOnWrite(t *testing.T) {
	c := newDeviceStatusCache(time.Minute)
	clock := time.Now()
	c.now = func() time.Time { return clock }
	a, b := uuid.New(), uuid.New()

	c.put(a, nil)
	found, status := c.get(a)
	assert.True(t, found)
	assert.NoError(t, status)

	clock = clock.Add(2 * time.Minute)
	found, _ = c.get(a)
	assert.False(t, found, "expired entries are not served")

	c.put(b, ErrDeviceRevoked)
	assert.Len(t, c.entries, 1, "writing evicts the expired entry")
	found, status = c.get(b)
	assert.True(t, found)
	assert.ErrorIs(t, status, ErrDeviceRevoked)
}
