package application

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"tsb-service/internal/modules/pos/domain"
)

// ---- errors surfaced to the handler

var (
	ErrDeviceNotEnrolled = errors.New("device not enrolled")
	ErrDeviceRevoked     = errors.New("device revoked")
	ErrStaleRequest      = errors.New("request timestamp out of bounds")
	ErrInvalidHMAC       = errors.New("hmac signature mismatch")
)

// Config holds runtime configuration for the POS auth service.
type Config struct {
	JWTSecret      []byte        // HS256 signing key; env POS_JWT_SECRET
	AccessTokenTTL time.Duration // default 8h
	HMACSkew       time.Duration // default 60s
}

func DefaultConfig(secret []byte) Config {
	return Config{
		JWTSecret:      secret,
		AccessTokenTTL: 8 * time.Hour,
		HMACSkew:       60 * time.Second,
	}
}

type Service struct {
	cfg     Config
	devices domain.DeviceRepository
}

func NewService(cfg Config, d domain.DeviceRepository) *Service {
	return &Service{cfg: cfg, devices: d}
}

// ---- device login

// DeviceLoginInput is the HMAC-signed request the device sends on app start (or
// when its access token approaches expiry).
type DeviceLoginInput struct {
	DeviceID  uuid.UUID
	Timestamp int64 // epoch ms
	Nonce     string
	HMAC      string // base64
}

type AccessToken struct {
	Token     string
	ExpiresIn int64 // seconds
	DeviceID  uuid.UUID
}

// DeviceLogin verifies the HMAC and issues an HS256 access token whose only
// claim of consequence is `deviceId`. Middleware grants admin scope to any
// valid POS token, so callers can hit the same GraphQL surface as a Zitadel
// admin once the token is set.
func (s *Service) DeviceLogin(ctx context.Context, in DeviceLoginInput) (*AccessToken, error) {
	device, err := s.verifyDeviceRequest(ctx, in.DeviceID, in.Timestamp, in.Nonce, in.HMAC, buildLoginHmacPayload(in))
	if err != nil {
		return nil, err
	}
	_ = s.devices.TouchLastSeen(ctx, device.ID)
	return s.issueAccessToken(device.ID)
}

// ---- FCM token management

// FCMTokenInput carries the FCM registration token and HMAC proof from the device.
type FCMTokenInput struct {
	DeviceID  uuid.UUID
	FCMToken  string
	Timestamp int64
	Nonce     string
	HMAC      string
}

// UpdateDeviceFCMToken validates the HMAC and stores the FCM token for a device.
func (s *Service) UpdateDeviceFCMToken(ctx context.Context, in FCMTokenInput) error {
	payload := fmt.Sprintf("%s|%s|%d|%s", in.DeviceID, in.FCMToken, in.Timestamp, in.Nonce)
	if _, err := s.verifyDeviceRequest(ctx, in.DeviceID, in.Timestamp, in.Nonce, in.HMAC, payload); err != nil {
		return err
	}
	return s.devices.UpdateFCMToken(ctx, in.DeviceID, in.FCMToken)
}

// GetActiveFCMTokens returns FCM tokens for all non-revoked POS devices.
func (s *Service) GetActiveFCMTokens(ctx context.Context) ([]string, error) {
	return s.devices.FindActiveFCMTokens(ctx)
}

// ---- shared helpers

func (s *Service) verifyDeviceRequest(
	ctx context.Context,
	deviceID uuid.UUID, timestamp int64, nonce, hmacB64, payload string,
) (*domain.Device, error) {
	device, err := s.devices.FindByID(ctx, deviceID)
	if err != nil {
		return nil, ErrDeviceNotEnrolled
	}
	if device.RevokedAt != nil {
		return nil, ErrDeviceRevoked
	}
	now := time.Now().UnixMilli()
	if absInt64(now-timestamp) > s.cfg.HMACSkew.Milliseconds() {
		return nil, ErrStaleRequest
	}
	if !verifyHMAC(device.DeviceSecretHash, payload, hmacB64) {
		return nil, ErrInvalidHMAC
	}
	_ = nonce // covered by HMAC; rate limiter blocks repeated submissions
	return device, nil
}

func (s *Service) issueAccessToken(deviceID uuid.UUID) (*AccessToken, error) {
	now := time.Now()
	exp := now.Add(s.cfg.AccessTokenTTL)
	claims := jwt.MapClaims{
		"sub":      deviceID.String(),
		"iat":      now.Unix(),
		"exp":      exp.Unix(),
		"iss":      "tsb-pos",
		"deviceId": deviceID.String(),
		"typ":      "access",
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.cfg.JWTSecret)
	if err != nil {
		return nil, err
	}
	return &AccessToken{
		Token:     token,
		ExpiresIn: int64(s.cfg.AccessTokenTTL.Seconds()),
		DeviceID:  deviceID,
	}, nil
}

// VerifyAccessToken validates a POS-issued HS256 token and returns the device
// UUID. Used by the HTTP/WS middleware to accept POS tokens alongside Zitadel
// JWTs. POS tokens always confer admin scope at the middleware level.
func (s *Service) VerifyAccessToken(tokenStr string) (uuid.UUID, error) {
	parsed, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return s.cfg.JWTSecret, nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || !parsed.Valid {
		return uuid.Nil, err
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return uuid.Nil, errors.New("invalid claims")
	}
	if iss, _ := claims["iss"].(string); iss != "tsb-pos" {
		return uuid.Nil, errors.New("bad issuer")
	}
	sub, _ := claims["sub"].(string)
	id, err := uuid.Parse(sub)
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// AccessTokenExpiry reads the exp claim from a POS token without re-verifying
// the signature. Returns the zero time if the claim is missing or the token
// can't be parsed. Callers must verify the signature via VerifyAccessToken
// before trusting the returned value.
func (s *Service) AccessTokenExpiry(tokenStr string) time.Time {
	parser := jwt.NewParser(jwt.WithValidMethods([]string{"HS256"}))
	claims := jwt.MapClaims{}
	if _, _, err := parser.ParseUnverified(tokenStr, claims); err != nil {
		return time.Time{}
	}
	exp, err := claims.GetExpirationTime()
	if err != nil || exp == nil {
		return time.Time{}
	}
	return exp.UTC()
}

// ---- utilities

func verifyHMAC(secretHashHex, payload, sigB64 string) bool {
	// The server only stores the SHA-256 of the secret. Both sides use the hash
	// itself as the HMAC key — see internal docs for rationale.
	key, err := hex.DecodeString(secretHashHex)
	if err != nil {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return false
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte(payload))
	return hmac.Equal(m.Sum(nil), sig)
}

func buildLoginHmacPayload(in DeviceLoginInput) string {
	return fmt.Sprintf("%s|%d|%s", in.DeviceID, in.Timestamp, in.Nonce)
}

func absInt64(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}
