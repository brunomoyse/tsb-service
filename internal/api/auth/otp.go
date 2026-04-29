package auth

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"tsb-service/pkg/email/scaleway"
	"tsb-service/pkg/logging"

	userDomain "tsb-service/internal/modules/user/domain"
)

// requestOtpBody is the frontend's request to start a passwordless login.
type requestOtpBody struct {
	LoginName string `json:"loginName"`
	Lang      string `json:"lang,omitempty"`
}

// verifyOtpBody is the frontend's request to verify an OTP code.
type verifyOtpBody struct {
	SessionID    string `json:"sessionId"`
	SessionToken string `json:"sessionToken"`
	Code         string `json:"code"`
}

// resendOtpBody is the frontend's request to ask Zitadel for a new code.
type resendOtpBody struct {
	SessionID    string `json:"sessionId"`
	SessionToken string `json:"sessionToken"`
	Lang         string `json:"lang,omitempty"`
}

// zitadelOtpSessionResponse mirrors the relevant fields of Zitadel's
// Session API response when a session is created or updated with an
// otpEmail challenge using returnCode.
type zitadelOtpSessionResponse struct {
	SessionID    string `json:"sessionId"`
	SessionToken string `json:"sessionToken"`
	Challenges   struct {
		OtpEmail string `json:"otpEmail"`
	} `json:"challenges"`
}

// RequestOtpHandler creates a Zitadel session with an otpEmail challenge,
// extracts the returned code, and emails it via Scaleway using our own
// templates.
//
// POST /auth/session/otp/request { loginName, lang? }
//
// Pattern B (identifier-first signup): unknown emails are not rejected.
// Instead the handler provisions a placeholder Zitadel user (givenName /
// familyName = "-", email pre-verified) and issues the OTP session as
// usual. This keeps the response shape identical for known and unknown
// emails (enumeration resistance) and lets the verify step ask the user
// for their real first/last name only after they prove email control.
func RequestOtpHandler(c *gin.Context) {
	log := logging.FromContext(c.Request.Context())

	var req requestOtpBody
	if err := c.ShouldBindJSON(&req); err != nil || req.LoginName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "loginName is required"})
		return
	}

	lang := req.Lang
	if lang == "" {
		lang = "fr"
	}

	// Resolve (or provision) the Zitadel user for this email. Unknown emails
	// get a placeholder account so the OTP session can be created uniformly.
	userID, err := findZitadelUserByEmail(req.LoginName)
	if err != nil {
		log.Debug("otp request for unknown email — creating placeholder", zap.String("email", req.LoginName))
		userID, err = createPlaceholderZitadelUser(req.LoginName)
		if err != nil {
			log.Warn("placeholder user creation failed", zap.Error(err), zap.String("email", req.LoginName))
			// Same enumeration-resistant empty response on failure: the caller
			// can't distinguish a real provisioning error from any other path
			// that returns an empty session shape.
			c.JSON(http.StatusOK, sessionResponse{})
			return
		}
	}

	// Lazy-enroll the OTP Email factor: existing accounts (and freshly
	// provisioned placeholders) don't have it configured by default, so
	// Zitadel rejects the otpEmail challenge with "Multifactor OTP isn't
	// ready" on the first attempt. Idempotent — already-enrolled users no-op.
	if err := ensureZitadelOtpEmail(userID); err != nil {
		log.Warn("zitadel otp email enrollment failed", zap.Error(err))
		// Fall through anyway: the session create will fail and we'll
		// return the same enumeration-resistant empty response.
	}

	body := map[string]any{
		"checks": map[string]any{
			"user": map[string]any{"loginName": req.LoginName},
		},
		"challenges": map[string]any{
			"otpEmail": map[string]any{
				"returnCode": map[string]any{},
			},
		},
	}

	respBody, status, err := zitadelRequest("POST", "/v2/sessions", body)
	if err != nil {
		log.Error("zitadel otp session create failed", zap.Error(err))
		c.JSON(http.StatusBadGateway, gin.H{"error": "authentication service unavailable"})
		return
	}

	if status != http.StatusCreated && status != http.StatusOK {
		log.Warn("zitadel otp session create rejected",
			zap.Int("status", status),
			zap.String("message", parseZitadelError(respBody)))
		// Generic 200 instead of a Zitadel-specific error: avoid leaking
		// account state through error codes.
		c.JSON(http.StatusOK, sessionResponse{})
		return
	}

	var zResp zitadelOtpSessionResponse
	if err := json.Unmarshal(respBody, &zResp); err != nil {
		log.Error("invalid zitadel otp session response", zap.Error(err))
		c.JSON(http.StatusBadGateway, gin.H{"error": "invalid response from auth service"})
		return
	}

	if zResp.Challenges.OtpEmail != "" && scaleway.IsInitialized() {
		// Best-effort profile fetch for the email salutation. Falls back to the
		// email address for placeholder accounts (givenName == "-") or when
		// Zitadel returns no profile.
		firstName := req.LoginName
		var lastName string
		if email, given, family, err := GetZitadelUserInfo(c.Request.Context(), userID); err == nil {
			if given != "" && given != placeholderProfileMarker {
				firstName = given
			}
			if family != placeholderProfileMarker {
				lastName = family
			}
			if email != "" {
				req.LoginName = email
			}
		}

		user := userDomain.User{
			FirstName: firstName,
			LastName:  lastName,
			Email:     req.LoginName,
		}
		if err := scaleway.SendLoginOtpEmail(user, lang, zResp.Challenges.OtpEmail); err != nil {
			log.Error("failed to send login otp email", zap.Error(err))
		}
	}

	c.JSON(http.StatusOK, sessionResponse{
		SessionID:    zResp.SessionID,
		SessionToken: zResp.SessionToken,
	})
}

// verifyOtpResponse extends sessionResponse with a flag telling the frontend
// whether the user still needs to fill in their first/last name before OIDC
// finalize. True when the OTP request created a placeholder account on the
// fly (Pattern B identifier-first signup).
type verifyOtpResponse struct {
	SessionID       string `json:"sessionId"`
	SessionToken    string `json:"sessionToken"`
	RequiresProfile bool   `json:"requiresProfile"`
}

// VerifyOtpHandler updates the Zitadel session with the user-supplied OTP
// code. On success, Zitadel issues a new sessionToken whose otpEmail check
// is fulfilled — this is the token used by /auth/finalize to complete the
// OIDC flow.
//
// POST /auth/session/otp/verify { sessionId, sessionToken, code }
//
// All Zitadel-side failures (wrong code, expired challenge, missing session)
// collapse to a single "invalid_code" response so the endpoint can't be
// turned into an enumeration oracle.
//
// On success the response also includes requiresProfile: true when the user
// is a fresh placeholder created during the OTP request — the frontend then
// renders the name-capture step before calling /auth/finalize.
func VerifyOtpHandler(c *gin.Context) {
	log := logging.FromContext(c.Request.Context())

	var req verifyOtpBody
	if err := c.ShouldBindJSON(&req); err != nil || req.SessionID == "" || req.SessionToken == "" || req.Code == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "sessionId, sessionToken and code are required"})
		return
	}

	// Serialize verifies for the same sessionID and short-circuit a duplicate
	// (sessionID, code) submit by returning the cached success. Without this
	// gate, a double-fire (form double-click, accidental retry, hydration
	// remount) consumes the OTP code on the first call and the second call
	// hits Zitadel with no live code → "Code not found" → user sees "expired".
	entry := verifyGate.acquire(req.SessionID)
	defer verifyGate.release(entry)
	if cached, ok := entry.hit(req.Code); ok {
		c.JSON(http.StatusOK, cached)
		return
	}

	body := map[string]any{
		"sessionToken": req.SessionToken,
		"checks": map[string]any{
			"otpEmail": map[string]any{
				"code": req.Code,
			},
		},
	}

	respBody, status, err := zitadelRequest("PATCH", "/v2/sessions/"+req.SessionID, body)
	if err != nil {
		log.Error("zitadel otp session update failed", zap.Error(err))
		c.JSON(http.StatusBadGateway, gin.H{"error": "authentication service unavailable"})
		return
	}

	if status != http.StatusOK && status != http.StatusCreated {
		log.Warn("zitadel otp verify rejected",
			zap.Int("status", status),
			zap.String("message", parseZitadelError(respBody)))
		c.JSON(http.StatusUnauthorized, gin.H{"error": ErrInvalidCode})
		return
	}

	var zResp zitadelOtpSessionResponse
	if err := json.Unmarshal(respBody, &zResp); err != nil {
		log.Error("invalid zitadel otp verify response", zap.Error(err))
		c.JSON(http.StatusBadGateway, gin.H{"error": "invalid response from auth service"})
		return
	}

	// Best-effort: figure out whether the session belongs to a placeholder
	// user that still needs first/last name. If either lookup fails the user
	// can still log in — they keep the placeholder profile and can edit it
	// from /me later. We don't want this check to block authentication.
	var requiresProfile bool
	if userID, err := lookupSessionUserID(req.SessionID); err == nil {
		if needs, err := userNeedsProfileCompletion(userID); err == nil {
			requiresProfile = needs
		}
	}

	// Zitadel's PATCH /v2/sessions response only includes sessionToken;
	// the sessionId in the URL is what the client must continue to use.
	resp := verifyOtpResponse{
		SessionID:       req.SessionID,
		SessionToken:    zResp.SessionToken,
		RequiresProfile: requiresProfile,
	}
	verifyGate.cache(entry, req.Code, resp)
	c.JSON(http.StatusOK, resp)
}

// ResendOtpHandler asks Zitadel to issue a fresh otpEmail code on the
// existing session and emails it via Scaleway. The previous code is
// invalidated by Zitadel.
//
// POST /auth/session/otp/resend { sessionId, sessionToken, lang? }
func ResendOtpHandler(c *gin.Context) {
	log := logging.FromContext(c.Request.Context())

	var req resendOtpBody
	if err := c.ShouldBindJSON(&req); err != nil || req.SessionID == "" || req.SessionToken == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "sessionId and sessionToken are required"})
		return
	}

	lang := req.Lang
	if lang == "" {
		lang = "fr"
	}

	body := map[string]any{
		"sessionToken": req.SessionToken,
		"challenges": map[string]any{
			"otpEmail": map[string]any{
				"returnCode": map[string]any{},
			},
		},
	}

	respBody, status, err := zitadelRequest("PATCH", "/v2/sessions/"+req.SessionID, body)
	if err != nil {
		log.Error("zitadel otp resend failed", zap.Error(err))
		c.JSON(http.StatusBadGateway, gin.H{"error": "authentication service unavailable"})
		return
	}

	if status != http.StatusOK && status != http.StatusCreated {
		log.Warn("zitadel otp resend rejected",
			zap.Int("status", status),
			zap.String("message", parseZitadelError(respBody)))
		// Treat any failure as a generic success to avoid leaking session
		// state — the user can request a fresh login from step 1.
		c.JSON(http.StatusOK, gin.H{"success": true})
		return
	}

	var zResp zitadelOtpSessionResponse
	if err := json.Unmarshal(respBody, &zResp); err != nil {
		log.Error("invalid zitadel otp resend response", zap.Error(err))
		c.JSON(http.StatusOK, gin.H{"success": true})
		return
	}

	if zResp.Challenges.OtpEmail != "" && scaleway.IsInitialized() {
		// We don't have the loginName on a resend — Zitadel's session
		// response doesn't echo it back — so look it up by sessionId. The
		// cheapest path is to re-fetch the session itself.
		loginName, firstName := lookupSessionUser(req.SessionID)
		if loginName != "" {
			user := userDomain.User{
				FirstName: firstName,
				Email:     loginName,
			}
			if err := scaleway.SendLoginOtpEmail(user, lang, zResp.Challenges.OtpEmail); err != nil {
				log.Error("failed to send login otp email", zap.Error(err))
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{"success": true})
}

// lookupSessionUser fetches a Zitadel session and returns the associated
// user's loginName and first name. Used by ResendOtpHandler to recover the
// destination email address when the client only sends sessionId.
//
// Returns empty strings on any failure — callers should treat that as a
// silent skip rather than an error.
func lookupSessionUser(sessionID string) (loginName string, firstName string) {
	respBody, status, err := zitadelRequest("GET", "/v2/sessions/"+sessionID, nil)
	if err != nil || status != http.StatusOK {
		return "", ""
	}
	var sessResp struct {
		Session struct {
			Factors struct {
				User struct {
					LoginName   string `json:"loginName"`
					DisplayName string `json:"displayName"`
				} `json:"user"`
			} `json:"factors"`
		} `json:"session"`
	}
	if json.Unmarshal(respBody, &sessResp) != nil {
		return "", ""
	}
	return sessResp.Session.Factors.User.LoginName, sessResp.Session.Factors.User.DisplayName
}
