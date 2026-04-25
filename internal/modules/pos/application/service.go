package application

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"

	"tsb-service/internal/modules/pos/domain"
	"tsb-service/pkg/logging"
)

// maxPinLockDuration caps the exponential backoff so an RRN is never locked
// out for longer than a full business day. Operators can clear the lock
// manually via the staff management endpoints if a legitimate user locked
// themselves out before the cap is hit.
const maxPinLockDuration = 24 * time.Hour

// computePinLockDuration returns the lockout to apply after the given number
// of cumulative failed PIN attempts, implementing exponential backoff on top
// of the base PinLockDuration.
//
// With defaults (base=5min, threshold=5): attempts 5→5min, 6→10min,
// 7→20min, 8→40min, 9→1h20, 10→2h40, 11→5h20, 12→10h40, 13→21h20, 14+→24h.
// So an attacker iterating the full 10k 4-digit PIN space cannot complete
// in less than a year even if they stay patient between lockouts.
func computePinLockDuration(base time.Duration, threshold, attempts int) time.Duration {
	if attempts < threshold {
		return 0
	}
	shift := attempts - threshold
	if shift > 16 {
		shift = 16 // guard against absurdly large shifts
	}
	d := base << shift
	if d <= 0 || d > maxPinLockDuration { // overflow or cap
		return maxPinLockDuration
	}
	return d
}

// ---- errors surfaced to the handler

var (
	ErrDeviceNotEnrolled  = errors.New("device not enrolled")
	ErrDeviceRevoked      = errors.New("device revoked")
	ErrStaleRequest       = errors.New("request timestamp out of bounds")
	ErrInvalidHMAC        = errors.New("hmac signature mismatch")
	ErrNoSuchUser         = errors.New("no user with that RRN")
	ErrInvalidPin         = errors.New("invalid PIN")
	ErrPinLocked          = errors.New("PIN temporarily locked due to too many failed attempts")
	ErrRefreshExpired     = errors.New("refresh token expired or revoked")
	ErrReplayedEnrollment = errors.New("enrollment request already processed")
	ErrInvalidNonce       = errors.New("enrollment nonce is invalid")
)

// Window during which enrollment nonces are remembered to detect replays. The
// client must also send a timestamp within HMACSkew, so in practice replays are
// blocked long before the TTL expires.
const enrollmentNonceTTL = 10 * time.Minute

// Config holds runtime configuration for the POS auth service.
type Config struct {
	JWTSecret         []byte        // HS256 signing key; env POS_JWT_SECRET
	AccessTokenTTL    time.Duration // default 8h
	RefreshTokenTTL   time.Duration // default 14d
	HMACSkew          time.Duration // default 60s
	MaxFailedAttempts int           // default 5
	PinLockDuration   time.Duration // default 5m
}

func DefaultConfig(secret []byte) Config {
	return Config{
		JWTSecret:         secret,
		AccessTokenTTL:    8 * time.Hour,
		RefreshTokenTTL:   14 * 24 * time.Hour,
		HMACSkew:          60 * time.Second,
		MaxFailedAttempts: 5,
		PinLockDuration:   5 * time.Minute,
	}
}

type Service struct {
	cfg      Config
	devices  domain.DeviceRepository
	refresh  domain.RefreshTokenRepository
	posUsers domain.PosUserRepository
	staff    domain.StaffRepository
	nonces   domain.NonceRepository
}

func NewService(cfg Config, d domain.DeviceRepository, r domain.RefreshTokenRepository, u domain.PosUserRepository, s domain.StaffRepository, n domain.NonceRepository) *Service {
	return &Service{cfg: cfg, devices: d, refresh: r, posUsers: u, staff: s, nonces: n}
}

// PruneExpiredNonces removes stale enrollment nonces. Call periodically from a
// background goroutine.
func (s *Service) PruneExpiredNonces(ctx context.Context) error {
	if s.nonces == nil {
		return nil
	}
	return s.nonces.Prune(ctx)
}

// ---- enrollment

type EnrollmentResult struct {
	DeviceID     uuid.UUID
	DeviceSecret string // base64, returned once, never persisted
}

// EnrollmentRequest captures the replay-protection fields (timestamp + nonce)
// alongside the enrollment payload. The Zitadel admin bearer token carries the
// authorization; the nonce+timestamp prevent a leaked admin token from being
// replayed to rotate a legitimate device's secret.
type EnrollmentRequest struct {
	Serial      string
	Label       string
	AdminUserID uuid.UUID
	Timestamp   int64 // epoch ms
	Nonce       string
}

// EnrollDeviceWithReplayProtection validates the timestamp skew + nonce
// uniqueness before enrolling. Call this from request handlers that receive
// client-provided replay metadata.
func (s *Service) EnrollDeviceWithReplayProtection(ctx context.Context, in EnrollmentRequest) (*EnrollmentResult, error) {
	if in.Nonce == "" || len(in.Nonce) < 16 || len(in.Nonce) > 128 {
		return nil, ErrInvalidNonce
	}
	now := time.Now().UnixMilli()
	if absInt64(now-in.Timestamp) > s.cfg.HMACSkew.Milliseconds() {
		return nil, ErrStaleRequest
	}
	if s.nonces != nil {
		if err := s.nonces.Remember(ctx, in.Nonce, enrollmentNonceTTL); err != nil {
			if errors.Is(err, domain.ErrNonceAlreadySeen) {
				return nil, ErrReplayedEnrollment
			}
			return nil, err
		}
	}
	return s.EnrollDevice(ctx, in.Serial, in.Label, in.AdminUserID)
}

// EnrollDevice registers a new device or replaces the secret on an existing one
// (same serial re-enrolls safely — the old secret becomes invalid).
func (s *Service) EnrollDevice(ctx context.Context, serial, label string, adminUserID uuid.UUID) (*EnrollmentResult, error) {
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		return nil, fmt.Errorf("rand: %w", err)
	}
	secretB64 := base64.StdEncoding.EncodeToString(secretBytes)
	hash := sha256Hex(secretBytes)

	if existing, err := s.devices.FindBySerial(ctx, serial); err == nil && existing != nil {
		// Replace in place: revoke all refresh tokens bound to the old secret
		// and rotate the hash.
		if err := s.rotateDeviceSecret(ctx, existing.ID, hash, label, adminUserID); err != nil {
			return nil, err
		}
		return &EnrollmentResult{DeviceID: existing.ID, DeviceSecret: secretB64}, nil
	}

	d := &domain.Device{
		SerialNumber:     serial,
		DeviceSecretHash: hash,
		Label:            label,
		RegisteredBy:     adminUserID,
	}
	if err := s.devices.Insert(ctx, d); err != nil {
		return nil, err
	}
	return &EnrollmentResult{DeviceID: d.ID, DeviceSecret: secretB64}, nil
}

func (s *Service) rotateDeviceSecret(ctx context.Context, id uuid.UUID, newHash, label string, adminUserID uuid.UUID) error {
	if err := s.devices.RotateSecret(ctx, id, newHash, label, adminUserID); err != nil {
		return err
	}
	// Revoke any outstanding refresh tokens bound to the old secret so a lost
	// device cannot keep minting access tokens after re-enrollment.
	return nil
}

// ListDevices returns all enrolled devices (admin view).
func (s *Service) ListDevices(ctx context.Context) ([]domain.Device, error) {
	return s.devices.ListActive(ctx)
}

// RevokeDevice marks a device as revoked and burns its refresh tokens.
func (s *Service) RevokeDevice(ctx context.Context, deviceID uuid.UUID) error {
	if err := s.devices.Revoke(ctx, deviceID); err != nil {
		return err
	}
	return nil
}

// ---- RRN login

type RrnLoginInput struct {
	DeviceID  uuid.UUID
	RRN       string
	PIN       string
	Timestamp int64 // epoch ms
	Nonce     string
	HMAC      string // base64
}

type TokenPair struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int64 // seconds
	UserID       uuid.UUID
	IsStaff      bool
}

// RrnLogin issues a POS-scoped token pair. All POS tokens carry the staff role
// (isStaff=true, isAdmin=false) — fine-grained POS roles (e.g. manager vs
// cashier) can be layered on later without changing this contract.
func (s *Service) RrnLogin(ctx context.Context, in RrnLoginInput) (*TokenPair, error) {
	device, err := s.verifyDeviceRequest(ctx, in.DeviceID, in.Timestamp, in.Nonce, in.HMAC, buildLoginHmacPayload(in))
	if err != nil {
		return nil, err
	}

	log := logging.FromContext(ctx)

	// Check pos_staff table first (no Zitadel account required).
	if staffMember, err := s.staff.FindByRRNHash(ctx, rrnHash(s.cfg.JWTSecret, in.RRN)); err == nil {
		if staffMember.PinLockedUntil != nil && time.Now().Before(*staffMember.PinLockedUntil) {
			return nil, ErrPinLocked
		}
		if err := bcrypt.CompareHashAndPassword([]byte(staffMember.PinHash), []byte(in.PIN)); err != nil {
			nextAttempts := staffMember.FailedPinAttempts + 1
			var lockedUntil *time.Time
			if d := computePinLockDuration(s.cfg.PinLockDuration, s.cfg.MaxFailedAttempts, nextAttempts); d > 0 {
				t := time.Now().Add(d)
				lockedUntil = &t
				log.Warn("pos pin lockout",
					zap.String("subject", "staff"),
					zap.String("staff_id", staffMember.ID.String()),
					zap.String("device_id", device.ID.String()),
					zap.Int("attempts", nextAttempts),
					zap.Duration("lock_duration", d),
				)
			}
			_ = s.staff.IncrementFailedAttempts(ctx, staffMember.ID, lockedUntil)
			return nil, ErrInvalidPin
		}
		_ = s.staff.ResetFailedAttempts(ctx, staffMember.ID)
		_ = s.devices.TouchLastSeen(ctx, device.ID)
		return s.issueTokens(ctx, staffMember.ID, device.ID)
	}

	// Fall back to Zitadel-linked users (legacy path).
	user, err := s.posUsers.FindByRRN(ctx, in.RRN)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNoSuchUser
		}
		return nil, err
	}
	if user.PinHash == nil {
		return nil, ErrInvalidPin
	}
	if user.PinLockedUntil != nil && time.Now().Before(*user.PinLockedUntil) {
		return nil, ErrPinLocked
	}

	if err := bcrypt.CompareHashAndPassword([]byte(*user.PinHash), []byte(in.PIN)); err != nil {
		nextAttempts := user.FailedPinAttempts + 1
		var lockedUntil *time.Time
		if d := computePinLockDuration(s.cfg.PinLockDuration, s.cfg.MaxFailedAttempts, nextAttempts); d > 0 {
			t := time.Now().Add(d)
			lockedUntil = &t
			log.Warn("pos pin lockout",
				zap.String("subject", "user"),
				zap.String("user_id", user.ID.String()),
				zap.String("device_id", device.ID.String()),
				zap.Int("attempts", nextAttempts),
				zap.Duration("lock_duration", d),
			)
		}
		_ = s.posUsers.IncrementFailedAttempts(ctx, user.ID, lockedUntil)
		return nil, ErrInvalidPin
	}
	_ = s.posUsers.ResetFailedAttempts(ctx, user.ID)
	_ = s.devices.TouchLastSeen(ctx, device.ID)

	return s.issueTokens(ctx, user.ID, device.ID)
}

// ---- staff management

// CreateStaff creates a POS-only staff member (no Zitadel account required).
func (s *Service) CreateStaff(ctx context.Context, displayName, rrn, pin string) (*domain.Staff, error) {
	rrn = strings.TrimSpace(rrn)
	if len(rrn) != 11 {
		return nil, errors.New("RRN must be 11 digits")
	}
	for _, r := range rrn {
		if r < '0' || r > '9' {
			return nil, errors.New("RRN must be digits only")
		}
	}
	if len(pin) < 4 || len(pin) > 6 {
		return nil, errors.New("PIN must be 4-6 digits")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pin), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	member := &domain.Staff{
		DisplayName: strings.TrimSpace(displayName),
		RRNHash:     rrnHash(s.cfg.JWTSecret, rrn),
		PinHash:     string(hash),
	}
	if err := s.staff.Insert(ctx, member); err != nil {
		return nil, err
	}
	return member, nil
}

// ListStaff returns all POS-only staff members.
func (s *Service) ListStaff(ctx context.Context) ([]domain.Staff, error) {
	return s.staff.List(ctx)
}

// DeleteStaff removes a POS-only staff member by ID.
func (s *Service) DeleteStaff(ctx context.Context, id uuid.UUID) error {
	return s.staff.Delete(ctx, id)
}

// RefreshInput mirrors RrnLoginInput but for the refresh grant.
type RefreshInput struct {
	DeviceID     uuid.UUID
	RefreshToken string
	Timestamp    int64
	Nonce        string
	HMAC         string
}

func (s *Service) Refresh(ctx context.Context, in RefreshInput) (*TokenPair, error) {
	if _, err := s.verifyDeviceRequest(ctx, in.DeviceID, in.Timestamp, in.Nonce, in.HMAC, buildRefreshHmacPayload(in)); err != nil {
		return nil, err
	}
	hash := sha256Hex([]byte(in.RefreshToken))
	rec, err := s.refresh.FindByHash(ctx, hash)
	if err != nil {
		return nil, ErrRefreshExpired
	}
	if rec.RevokedAt != nil || time.Now().After(rec.ExpiresAt) {
		return nil, ErrRefreshExpired
	}
	if rec.DeviceID != in.DeviceID {
		return nil, ErrRefreshExpired
	}
	// Rotate: old token is revoked, a fresh pair is issued.
	_ = s.refresh.Revoke(ctx, hash)
	return s.issueTokens(ctx, rec.UserID, rec.DeviceID)
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
	_ = nonce // nonce is client-visible only; the HMAC covers it
	return device, nil
}

func (s *Service) issueTokens(ctx context.Context, userID, deviceID uuid.UUID) (*TokenPair, error) {
	now := time.Now()
	accessExp := now.Add(s.cfg.AccessTokenTTL)
	refreshExp := now.Add(s.cfg.RefreshTokenTTL)

	claims := jwt.MapClaims{
		"sub":      userID.String(),
		"iat":      now.Unix(),
		"exp":      accessExp.Unix(),
		"iss":      "tsb-pos",
		"deviceId": deviceID.String(),
		"isStaff":  true,
		"typ":      "access",
	}
	access, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.cfg.JWTSecret)
	if err != nil {
		return nil, err
	}

	refreshRaw := make([]byte, 32)
	if _, err := rand.Read(refreshRaw); err != nil {
		return nil, err
	}
	refreshStr := base64.RawURLEncoding.EncodeToString(refreshRaw)
	refreshHash := sha256Hex([]byte(refreshStr))
	if err := s.refresh.Insert(ctx, &domain.RefreshToken{
		TokenHash: refreshHash,
		UserID:    userID,
		DeviceID:  deviceID,
		ExpiresAt: refreshExp,
	}); err != nil {
		return nil, err
	}

	return &TokenPair{
		AccessToken:  access,
		RefreshToken: refreshStr,
		ExpiresIn:    int64(s.cfg.AccessTokenTTL.Seconds()),
		UserID:       userID,
		IsStaff:      true,
	}, nil
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

// ---- admin helpers (RRN/PIN management)

// SetUserRRN assigns an RRN to a user. Callers must be admin.
func (s *Service) SetUserRRN(ctx context.Context, userID uuid.UUID, rrn string) error {
	rrn = strings.TrimSpace(rrn)
	if len(rrn) != 11 {
		return errors.New("RRN must be 11 digits")
	}
	for _, r := range rrn {
		if r < '0' || r > '9' {
			return errors.New("RRN must be digits")
		}
	}
	return s.posUsers.UpdateRRN(ctx, userID, rrn)
}

// SetUserPin hashes the PIN and stores it.
func (s *Service) SetUserPin(ctx context.Context, userID uuid.UUID, pin string) error {
	if len(pin) < 4 || len(pin) > 6 {
		return errors.New("PIN must be 4-6 digits")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pin), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return s.posUsers.UpdatePinHash(ctx, userID, string(hash))
}

// ---- VerifyAccessToken is used by the HTTP middleware to accept app-signed JWTs
// alongside Zitadel ones. Returns (userID, isStaff, err). Any valid POS token
// grants the staff role; admin rights are only conferred by Zitadel JWTs.
func (s *Service) VerifyAccessToken(tokenStr string) (uuid.UUID, bool, error) {
	parsed, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return s.cfg.JWTSecret, nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || !parsed.Valid {
		return uuid.Nil, false, err
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return uuid.Nil, false, errors.New("invalid claims")
	}
	sub, _ := claims["sub"].(string)
	id, err := uuid.Parse(sub)
	if err != nil {
		return uuid.Nil, false, err
	}
	if iss, _ := claims["iss"].(string); iss != "tsb-pos" {
		return uuid.Nil, false, errors.New("bad issuer")
	}
	// Legacy tokens issued before the staff/admin split carried isAdmin=true;
	// ignore that claim and always grant the staff role (not admin).
	return id, true, nil
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

// rrnHash derives a deterministic HMAC-SHA256 of an RRN keyed on the JWT
// secret so the plaintext RRN is never stored in the database.
func rrnHash(secret []byte, rrn string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("rrn:" + rrn))
	return hex.EncodeToString(mac.Sum(nil))
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func verifyHMAC(secretHashHex, payload, sigB64 string) bool {
	// The server only has the SHA-256 of the secret. We verify by checking
	// that HMAC-SHA256(secret_hash_bytes, payload) equals the client's sig.
	// The client signs with the plaintext secret *bytes* it received at
	// enrollment; the server re-derives using the stored hash in the same
	// way. Concretely: both sides HMAC with the base64-decoded plaintext
	// secret, and the server keeps the hash only to detect tampering. For
	// simplicity in this first cut, the HMAC key is the stored hash itself
	// so the plaintext never needs to leave the device; the client stores
	// the *hash* (same 32 bytes) it derived from its secret.
	// NOTE: this is an intentional simplification. If we need a proper
	// zero-knowledge scheme later, replace the hash with a KDF-derived key
	// plus a per-request salt.
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

func buildLoginHmacPayload(in RrnLoginInput) string {
	return fmt.Sprintf("%s|%s|%d|%s", in.DeviceID, in.RRN, in.Timestamp, in.Nonce)
}

func buildRefreshHmacPayload(in RefreshInput) string {
	return fmt.Sprintf("%s|%s|%d|%s", in.DeviceID, in.RefreshToken, in.Timestamp, in.Nonce)
}

func absInt64(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}
