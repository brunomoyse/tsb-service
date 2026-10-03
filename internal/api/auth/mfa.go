package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"tsb-service/pkg/logging"
	"tsb-service/pkg/utils"
)

/*
 * Optional TOTP (authenticator app) second factor for staff.
 *
 * Zitadel stores the factor; the dashboard's custom login UI drives it through
 * the Session API. Zitadel does not apply login policies to the Session API, so
 * the enforcement lives here: FinalizeOIDCHandler refuses to finalize a login
 * for an admin client (the dashboard) when the user has TOTP enrolled and the
 * session has not passed the TOTP check. Customer clients are never affected.
 *
 * Enrollment is opt-in and self-service from the dashboard settings, limited to
 * admins so a customer cannot enroll a factor that tsb-core has no UI for.
 */

const (
	ErrMFARequired    = "mfa_required"
	ErrMFANotEnrolled = "mfa_not_enrolled"
	ErrForbidden      = "forbidden"

	authMethodTOTP = "AUTHENTICATION_METHOD_TYPE_TOTP"
)

// AuditFunc records a staff security action (MFA enrolled/removed). Set by
// main via SetAuditFunc; nil disables auditing.
type AuditFunc func(c *gin.Context, action string, success bool)

var auditFn AuditFunc

// SetAuditFunc registers the audit hook used by the MFA management handlers.
func SetAuditFunc(fn AuditFunc) { auditFn = fn }

func audit(c *gin.Context, action string, success bool) {
	if auditFn != nil {
		auditFn(c, action, success)
	}
}

// userHasTOTP reports whether the Zitadel user has a verified TOTP factor.
func userHasTOTP(userID string) (bool, error) {
	respBody, status, err := zitadelRequest("GET", "/v2/users/"+url.PathEscape(userID)+"/authentication_methods", nil)
	if err != nil {
		return false, fmt.Errorf("fetch authentication methods: %w", err)
	}
	if status != http.StatusOK {
		return false, fmt.Errorf("fetch authentication methods returned status %d: %s", status, parseZitadelError(respBody))
	}
	var resp struct {
		AuthMethodTypes []string `json:"authMethodTypes"`
	}
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return false, fmt.Errorf("parse authentication methods: %w", err)
	}
	return slices.Contains(resp.AuthMethodTypes, authMethodTOTP), nil
}

// sessionFactors is the subset of a Zitadel session needed for MFA checks
// and for profile completion.
type sessionFactors struct {
	UserID       string
	TOTPVerified bool
	// OTPEmailVerified is set once the emailed code was checked on the session.
	OTPEmailVerified bool
	// IntentVerified is set once an IdP (Google/Apple) intent was checked.
	IntentVerified bool
}

func fetchSessionFactors(sessionID string) (sessionFactors, error) {
	respBody, status, err := zitadelRequest("GET", "/v2/sessions/"+url.PathEscape(sessionID), nil)
	if err != nil {
		return sessionFactors{}, fmt.Errorf("fetch session: %w", err)
	}
	if status != http.StatusOK {
		return sessionFactors{}, fmt.Errorf("fetch session returned status %d", status)
	}
	var resp struct {
		Session struct {
			Factors struct {
				User struct {
					ID string `json:"id"`
				} `json:"user"`
				TOTP struct {
					VerifiedAt string `json:"verifiedAt"`
				} `json:"totp"`
				OTPEmail struct {
					VerifiedAt string `json:"verifiedAt"`
				} `json:"otpEmail"`
				Intent struct {
					VerifiedAt string `json:"verifiedAt"`
				} `json:"intent"`
			} `json:"factors"`
		} `json:"session"`
	}
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return sessionFactors{}, fmt.Errorf("parse session: %w", err)
	}
	if resp.Session.Factors.User.ID == "" {
		return sessionFactors{}, fmt.Errorf("session has no user")
	}
	return sessionFactors{
		UserID:           resp.Session.Factors.User.ID,
		TOTPVerified:     resp.Session.Factors.TOTP.VerifiedAt != "",
		OTPEmailVerified: resp.Session.Factors.OTPEmail.VerifiedAt != "",
		IntentVerified:   resp.Session.Factors.Intent.VerifiedAt != "",
	}, nil
}

// authRequestClientID returns the OIDC client an auth request was opened for.
func authRequestClientID(authRequestID string) (string, error) {
	respBody, status, err := zitadelRequest("GET", "/v2/oidc/auth_requests/"+url.PathEscape(authRequestID), nil)
	if err != nil {
		return "", fmt.Errorf("fetch auth request: %w", err)
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("fetch auth request returned status %d", status)
	}
	var resp struct {
		AuthRequest struct {
			ClientID string `json:"clientId"`
		} `json:"authRequest"`
	}
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return "", fmt.Errorf("parse auth request: %w", err)
	}
	return resp.AuthRequest.ClientID, nil
}

// checkStaffMFA decides whether a login may be finalized. It returns ("", nil)
// when allowed, ErrMFARequired when the user has TOTP but the session skipped
// it, or an error when Zitadel could not be consulted. Only admin clients are
// checked; for them a lookup failure fails closed.
func checkStaffMFA(authRequestID, sessionID string) (string, error) {
	if len(client.adminClients) == 0 {
		return "", nil
	}
	clientID, err := authRequestClientID(authRequestID)
	if err != nil {
		return "", err
	}
	if !client.adminClients[clientID] {
		return "", nil
	}
	factors, err := fetchSessionFactors(sessionID)
	if err != nil {
		return "", err
	}
	if factors.TOTPVerified {
		return "", nil
	}
	hasTOTP, err := userHasTOTP(factors.UserID)
	if err != nil {
		return "", err
	}
	if hasTOTP {
		return ErrMFARequired, nil
	}
	return "", nil
}

type verifyTotpBody struct {
	SessionID    string `json:"sessionId"`
	SessionToken string `json:"sessionToken"`
	Code         string `json:"code"`
}

// VerifyTotpHandler adds a TOTP check to a login session that already passed
// its first factor (email OTP or IdP).
//
// POST /auth/session/totp/verify { sessionId, sessionToken, code }
func VerifyTotpHandler(c *gin.Context) {
	log := logging.FromContext(c.Request.Context())

	var req verifyTotpBody
	if err := c.ShouldBindJSON(&req); err != nil || req.SessionID == "" || req.SessionToken == "" || req.Code == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "sessionId, sessionToken and code are required"})
		return
	}

	body := map[string]any{
		"sessionToken": req.SessionToken,
		"checks": map[string]any{
			"totp": map[string]any{"code": req.Code},
		},
	}
	respBody, status, err := zitadelRequest("PATCH", "/v2/sessions/"+url.PathEscape(req.SessionID), body)
	if err != nil {
		log.Error("zitadel totp session update failed", zap.Error(err))
		c.JSON(http.StatusBadGateway, gin.H{"error": "authentication service unavailable"})
		return
	}
	if status != http.StatusOK && status != http.StatusCreated {
		log.Warn("zitadel totp verify rejected",
			zap.Int("status", status),
			zap.String("message", parseZitadelError(respBody)))
		c.JSON(http.StatusUnauthorized, gin.H{"error": ErrInvalidCode})
		return
	}

	var zResp zitadelSessionResponse
	if err := json.Unmarshal(respBody, &zResp); err != nil {
		log.Error("invalid zitadel totp verify response", zap.Error(err))
		c.JSON(http.StatusBadGateway, gin.H{"error": "invalid response from auth service"})
		return
	}
	c.JSON(http.StatusOK, sessionResponse{SessionID: req.SessionID, SessionToken: zResp.SessionToken})
}

// staffZitadelUser returns the caller's Zitadel user ID when the request comes
// from an admin (Zitadel JWT). POS devices and customers are rejected.
func staffZitadelUser(c *gin.Context) (string, bool) {
	ctx := c.Request.Context()
	sub := utils.GetZitadelSub(ctx)
	if sub == "" || !utils.GetIsAdmin(ctx) {
		c.JSON(http.StatusForbidden, gin.H{"error": ErrForbidden})
		return "", false
	}
	return sub, true
}

// MFAStatusHandler reports which second factors the caller has enrolled.
//
// GET /auth/mfa
func MFAStatusHandler(c *gin.Context) {
	userID, ok := staffZitadelUser(c)
	if !ok {
		return
	}
	hasTOTP, err := userHasTOTP(userID)
	if err != nil {
		logging.FromContext(c.Request.Context()).Error("mfa status lookup failed", zap.Error(err))
		c.JSON(http.StatusBadGateway, gin.H{"error": "authentication service unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"totp": hasTOTP})
}

// StartTOTPHandler begins TOTP enrollment and returns the otpauth URI (for
// the QR code) and the secret (for manual entry). The factor only becomes
// active once ConfirmTOTPHandler verifies a code from the app. Calling it
// again replaces a pending, unverified secret.
//
// POST /auth/mfa/totp
func StartTOTPHandler(c *gin.Context) {
	log := logging.FromContext(c.Request.Context())
	userID, ok := staffZitadelUser(c)
	if !ok {
		return
	}

	respBody, status, err := zitadelAdminRequest("POST", "/v2/users/"+url.PathEscape(userID)+"/totp", map[string]any{})
	if err != nil {
		log.Error("zitadel totp register failed", zap.Error(err))
		c.JSON(http.StatusBadGateway, gin.H{"error": "authentication service unavailable"})
		return
	}
	if status != http.StatusOK && status != http.StatusCreated {
		log.Warn("zitadel totp register rejected",
			zap.Int("status", status),
			zap.String("message", parseZitadelError(respBody)))
		c.JSON(http.StatusConflict, gin.H{"error": "totp_register_failed"})
		return
	}

	var zResp struct {
		URI    string `json:"uri"`
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(respBody, &zResp); err != nil || zResp.URI == "" {
		log.Error("invalid zitadel totp register response", zap.Error(err))
		c.JSON(http.StatusBadGateway, gin.H{"error": "invalid response from auth service"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"uri": zResp.URI, "secret": zResp.Secret})
}

type totpCodeBody struct {
	Code string `json:"code"`
}

// ConfirmTOTPHandler activates a pending TOTP enrollment with a code from the
// authenticator app.
//
// POST /auth/mfa/totp/verify { code }
func ConfirmTOTPHandler(c *gin.Context) {
	log := logging.FromContext(c.Request.Context())
	userID, ok := staffZitadelUser(c)
	if !ok {
		return
	}
	var req totpCodeBody
	if err := c.ShouldBindJSON(&req); err != nil || req.Code == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "code is required"})
		return
	}

	respBody, status, err := zitadelAdminRequest("POST", "/v2/users/"+url.PathEscape(userID)+"/totp/verify", map[string]any{"code": req.Code})
	if err != nil {
		log.Error("zitadel totp verify failed", zap.Error(err))
		c.JSON(http.StatusBadGateway, gin.H{"error": "authentication service unavailable"})
		return
	}
	if status != http.StatusOK {
		log.Warn("zitadel totp enrollment verify rejected",
			zap.Int("status", status),
			zap.String("message", parseZitadelError(respBody)))
		audit(c, "mfa.totp.enable", false)
		// 422, not 401: the dashboard treats 401 as an expired session.
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": ErrInvalidCode})
		return
	}
	audit(c, "mfa.totp.enable", true)
	c.JSON(http.StatusOK, gin.H{"totp": true})
}

// RemoveTOTPHandler removes the caller's TOTP factor. It requires a current
// code so a stolen access token alone cannot strip the second factor.
//
// POST /auth/mfa/totp/remove { code }
func RemoveTOTPHandler(c *gin.Context) {
	log := logging.FromContext(c.Request.Context())
	userID, ok := staffZitadelUser(c)
	if !ok {
		return
	}
	var req totpCodeBody
	if err := c.ShouldBindJSON(&req); err != nil || req.Code == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "code is required"})
		return
	}

	hasTOTP, err := userHasTOTP(userID)
	if err != nil {
		log.Error("mfa status lookup failed", zap.Error(err))
		c.JSON(http.StatusBadGateway, gin.H{"error": "authentication service unavailable"})
		return
	}
	if !hasTOTP {
		c.JSON(http.StatusConflict, gin.H{"error": ErrMFANotEnrolled})
		return
	}

	valid, err := checkTOTPCode(userID, req.Code)
	if err != nil {
		log.Error("totp code check failed", zap.Error(err))
		c.JSON(http.StatusBadGateway, gin.H{"error": "authentication service unavailable"})
		return
	}
	if !valid {
		audit(c, "mfa.totp.disable", false)
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": ErrInvalidCode})
		return
	}

	respBody, status, err := zitadelAdminRequest("DELETE", "/v2/users/"+url.PathEscape(userID)+"/totp", nil)
	if err != nil {
		log.Error("zitadel totp remove failed", zap.Error(err))
		c.JSON(http.StatusBadGateway, gin.H{"error": "authentication service unavailable"})
		return
	}
	if status != http.StatusOK {
		log.Warn("zitadel totp remove rejected",
			zap.Int("status", status),
			zap.String("message", parseZitadelError(respBody)))
		c.JSON(http.StatusBadGateway, gin.H{"error": "authentication service unavailable"})
		return
	}
	audit(c, "mfa.totp.disable", true)
	c.JSON(http.StatusOK, gin.H{"totp": false})
}

// checkTOTPCode validates a TOTP code for a user. Zitadel has no standalone
// "verify code" call for an enrolled factor, so this opens a throwaway session
// with user + totp checks and deletes it straight after.
func checkTOTPCode(userID, code string) (bool, error) {
	body := map[string]any{
		"checks": map[string]any{
			"user": map[string]any{"userId": userID},
			"totp": map[string]any{"code": code},
		},
	}
	respBody, status, err := zitadelRequest("POST", "/v2/sessions", body)
	if err != nil {
		return false, err
	}
	if status != http.StatusOK && status != http.StatusCreated {
		zap.L().Debug("totp code check rejected", zap.Int("status", status), zap.String("message", parseZitadelError(respBody)))
		return false, nil
	}
	var sess zitadelSessionResponse
	if err := json.Unmarshal(respBody, &sess); err == nil && sess.SessionID != "" {
		if _, delStatus, delErr := zitadelRequest("DELETE", "/v2/sessions/"+url.PathEscape(sess.SessionID),
			map[string]any{"sessionToken": sess.SessionToken}); delErr != nil || delStatus != http.StatusOK {
			zap.L().Warn("failed to delete totp check session", zap.String("session_id", sess.SessionID), zap.Int("status", delStatus), zap.Error(delErr))
		}
	}
	return true, nil
}
