package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	net_mail "net/mail"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"golang.org/x/crypto/argon2"

	"tsb-service/internal/modules/user/domain"
	"tsb-service/pkg/types"
	"tsb-service/pkg/utils"
	es "tsb-service/pkg/email/scaleway"
)

type UserService interface {
	CreateUser(ctx context.Context, firstName string, lastName string, email string, phoneNumber *string, addressID *string, password *string, googleID *string) (*domain.User, error)
	UpdateMe(ctx context.Context, userID string, firstName *string, lastName *string, email *string, phoneNumber *string, addressID *string) (*domain.User, error)
	Login(ctx context.Context, email string, password string, jwtToken string) (*domain.User, *string, *string, error)
	GetUserByID(ctx context.Context, id string) (*domain.User, error)
	GetUserByEmail(ctx context.Context, email string) (*domain.User, error)
	GetUserByGoogleID(ctx context.Context, googleID string) (*domain.User, error)
	GenerateTokens(ctx context.Context, user domain.User, jwtToken string) (string, string, error)
	RefreshToken(ctx context.Context, oldRefreshToken string, jwtSecret string) (string, string, *domain.User, error)
	UpdateGoogleID(ctx context.Context, userID string, googleID string) (*domain.User, error)
	UpdateUserPassword(ctx context.Context, userID string, password string, salt string) (*domain.User, error)
	UpdateEmailVerifiedAt(ctx context.Context, userID string) (*domain.User, error)
	InvalidateRefreshToken(ctx context.Context, refreshToken string) error
	VerifyUserEmail(ctx context.Context, userID string) error
	RequestPasswordReset(ctx context.Context, email string) error
	ResetPassword(ctx context.Context, token string, newPassword string) error
	RequestDeletion(ctx context.Context, userID string) (*domain.User, error)
	CancelDeletionRequest(ctx context.Context, userID string) (*domain.User, error)

	ResendVerificationEmail(ctx context.Context, userID string) error
	BatchGetUsersByOrderIDs(ctx context.Context, orderIDs []string) (map[string][]*domain.User, error)
}

type userService struct {
	repo domain.UserRepository
}

func NewUserService(repo domain.UserRepository) UserService {
	return &userService{
		repo: repo,
	}
}

func (s *userService) CreateUser(ctx context.Context, firstName string, lastName string, email string, phoneNumber *string, addressID *string, password *string, googleID *string) (*domain.User, error) {
	// Validate email format
	if _, err := net_mail.ParseAddress(email); err != nil {
		return nil, fmt.Errorf("invalid email format")
	}

	// Ensure at least one credential is provided.
	if password == nil && googleID == nil {
		return nil, fmt.Errorf("password or googleID must be provided")
	}

	if password != nil {
		if err := validatePasswordStrength(*password); err != nil {
			return nil, err
		}

		// Email/password flow.
		salt, err := generateSalt()
		if err != nil {
			return nil, fmt.Errorf("failed to generate salt: %w", err)
		}
		hashedPassword, err := hashPassword(*password, salt)
		if err != nil {
			return nil, fmt.Errorf("failed to hash password: %w", err)
		}

		// Try to find an existing user by email.
		user, err := s.repo.FindByEmail(ctx, email)
		if err == nil {
			// User already exists.
			if user.PasswordHash != nil {
				// Already has a password — true duplicate registration attempt.
				return nil, fmt.Errorf("user with email %s already exists", user.Email)
			}
			// Google-first user: link account by setting password and auto-verify email.
			updatedUser, err := s.repo.UpdateUserPassword(ctx, user.ID.String(), hashedPassword, salt)
			if err != nil {
				return nil, fmt.Errorf("failed to update user password: %w", err)
			}
			// Auto-verify email (Google already verified it).
			if updatedUser.EmailVerifiedAt == nil {
				updatedUser, err = s.repo.UpdateEmailVerifiedAt(ctx, updatedUser.ID.String())
				if err != nil {
					return nil, fmt.Errorf("failed to verify email: %w", err)
				}
			}
			return updatedUser, nil
		}
		// If the error is something other than "user not found", return it.
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("error checking existing user: %w", err)
		}

		// 1. User does not exist; create a new user.
		newUser := domain.NewUser(firstName, lastName, email, phoneNumber, addressID, &hashedPassword, &salt)
		id, err := s.repo.Save(ctx, &newUser)
		if err != nil {
			return nil, fmt.Errorf("failed to create user: %w", err)
		}
		newUser.ID = id

		// 2. Build verification URL.
		apiBaseUrl := os.Getenv("API_BASE_URL")
		jwtSecret := os.Getenv("JWT_SECRET")
		verificationToken, _ := generateEmailVerificationJWT(newUser, jwtSecret)
		verificationURL := fmt.Sprintf("%s/verify?token=%s", apiBaseUrl, verificationToken)

		// 3. Send verification email asynchronously in a goroutine.
		go func() {
			err = es.SendVerificationEmail(newUser, utils.GetLang(ctx), verificationURL)
			if err != nil {
				zap.L().Error("failed to send verification email", zap.String("user_id", newUser.ID.String()), zap.Error(err))
			}
		}()

		// 4. Return immediately.
		return &newUser, nil
	}

	// Google flow: check if user already exists.
	existingUser, err := s.repo.FindByEmail(ctx, email)
	if err == nil {
		// User with this email already exists — link Google ID if not set.
		if existingUser.GoogleID == nil {
			updatedUser, err := s.repo.UpdateGoogleID(ctx, existingUser.ID.String(), *googleID)
			if err != nil {
				return nil, fmt.Errorf("failed to link Google account: %w", err)
			}
			return updatedUser, nil
		}
		// Already has Google ID — return the existing user.
		return existingUser, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("error checking existing user: %w", err)
	}

	// Brand new Google user.
	newUser := domain.NewGoogleUser(firstName, lastName, email, *googleID)
	id, err := s.repo.Save(ctx, &newUser)
	if err != nil {
		return nil, fmt.Errorf("failed to create user: %w", err)
	}
	newUser.ID = id
	return &newUser, nil
}

func (s *userService) Login(ctx context.Context, email string, password string, jwtSecret string) (*domain.User, *string, *string, error) {
	user, err := s.repo.FindByEmail(ctx, email)
	if err != nil {
		return nil, nil, nil, err
	}

	if (user.PasswordHash == nil || user.Salt == nil) && user.GoogleID != nil {
		return nil, nil, nil, fmt.Errorf("user account was created via google")
	}

	if user.EmailVerifiedAt == nil {
		return nil, nil, nil, fmt.Errorf("email not verified")
	}

	hashedPasswordRequest, err := hashPassword(password, *user.Salt)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to hash password: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(hashedPasswordRequest), []byte(*user.PasswordHash)) != 1 {
		return nil, nil, nil, fmt.Errorf("invalid password")
	}

	accessToken, refreshToken, err := generateTokens(*user, jwtSecret)
	if err != nil {
		return nil, nil, nil, err
	}

	// Store refresh token hash
	tokenHash := hashToken(refreshToken)
	expiresAt := time.Now().Add(7 * 24 * time.Hour).Unix()
	if err := s.repo.StoreRefreshToken(ctx, user.ID, tokenHash, expiresAt); err != nil {
		return nil, nil, nil, fmt.Errorf("failed to store refresh token: %w", err)
	}

	return user, &accessToken, &refreshToken, nil
}

func (s *userService) InvalidateRefreshToken(ctx context.Context, refreshToken string) error {
	if refreshToken == "" {
		return fmt.Errorf("refresh token is empty")
	}

	tokenHash := hashToken(refreshToken)
	if err := s.repo.InvalidateRefreshToken(ctx, tokenHash); err != nil {
		return fmt.Errorf("failed to invalidate refresh token: %w", err)
	}

	return nil
}

func (s *userService) GetUserByID(ctx context.Context, id string) (*domain.User, error) {
	return s.repo.FindByID(ctx, id)
}

func (s *userService) GetUserByEmail(ctx context.Context, email string) (*domain.User, error) {
	return s.repo.FindByEmail(ctx, email)
}

func (s *userService) GetUserByGoogleID(ctx context.Context, googleID string) (*domain.User, error) {
	return s.repo.FindByGoogleID(ctx, googleID)
}

func (s *userService) GenerateTokens(ctx context.Context, user domain.User, jwtSecret string) (string, string, error) {
	accessToken, refreshToken, err := generateTokens(user, jwtSecret)
	if err != nil {
		return "", "", err
	}

	// Store refresh token hash
	tokenHash := hashToken(refreshToken)
	expiresAt := time.Now().Add(7 * 24 * time.Hour).Unix()
	if err := s.repo.StoreRefreshToken(ctx, user.ID, tokenHash, expiresAt); err != nil {
		return "", "", fmt.Errorf("failed to store refresh token: %w", err)
	}

	return accessToken, refreshToken, nil
}

func (s *userService) UpdateGoogleID(ctx context.Context, userID string, googleID string) (*domain.User, error) {
	return s.repo.UpdateGoogleID(ctx, userID, googleID)
}

func (s *userService) UpdateMe(ctx context.Context, userID string, firstName *string, lastName *string, email *string, phoneNumber *string, addressID *string) (*domain.User, error) {
	user, err := s.repo.FindByID(ctx, userID)
	if err != nil {
		return nil, err
	}

	if firstName != nil {
		user.FirstName = *firstName
	}
	if lastName != nil {
		user.LastName = *lastName
	}
	if email != nil {
		user.Email = *email
	}
	if phoneNumber != nil {
		user.PhoneNumber = phoneNumber
	}
	if addressID != nil {
		user.AddressID = addressID
	}

	return s.repo.UpdateUser(ctx, user)
}

func (s *userService) UpdateUserPassword(ctx context.Context, userID string, password string, salt string) (*domain.User, error) {
	return s.repo.UpdateUserPassword(ctx, userID, password, salt)
}

func (s *userService) UpdateEmailVerifiedAt(ctx context.Context, userID string) (*domain.User, error) {
	return s.repo.UpdateEmailVerifiedAt(ctx, userID)
}

func (s *userService) RefreshToken(
	ctx context.Context,
	oldRefreshToken string,
	jwtSecret string,
) (string, string, *domain.User, error) {
	// 1. Validate refresh token JWT
	claims, err := s.validateRefreshToken(oldRefreshToken, jwtSecret)
	if err != nil {
		return "", "", nil, err
	}

	// 2. Check token revocation in database
	tokenHash := hashToken(oldRefreshToken)
	isValid, err := s.repo.IsRefreshTokenValid(ctx, tokenHash)
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to validate token: %w", err)
	}
	if !isValid {
		return "", "", nil, fmt.Errorf("token revoked or expired")
	}

	// 3. Get user
	user, err := s.repo.FindByID(ctx, claims.Subject)
	if err != nil {
		return "", "", nil, fmt.Errorf("user not found")
	}

	// 4. Generate new tokens
	accessToken, refreshToken, err := generateTokens(*user, jwtSecret)
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to generate tokens")
	}

	// 5. Revoke old refresh token
	if err := s.repo.InvalidateRefreshToken(ctx, tokenHash); err != nil {
		return "", "", nil, fmt.Errorf("failed to revoke old refresh token: %w", err)
	}

	// 6. Store new refresh token
	newTokenHash := hashToken(refreshToken)
	expiresAt := time.Now().Add(7 * 24 * time.Hour).Unix()
	if err := s.repo.StoreRefreshToken(ctx, user.ID, newTokenHash, expiresAt); err != nil {
		return "", "", nil, fmt.Errorf("failed to store new refresh token: %w", err)
	}

	return accessToken, refreshToken, user, nil
}

func (s *userService) VerifyUserEmail(ctx context.Context, userID string) error {
	user, err := s.repo.FindByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("user not found")
	}

	if user.EmailVerifiedAt != nil {
		return fmt.Errorf("email already verified")
	}

	currentTime := time.Now()

	user.EmailVerifiedAt = &currentTime

	// 1. Update email verified at
	user, err = s.repo.UpdateUser(ctx, user)
	if err != nil {
		return fmt.Errorf("failed to update email verified at: %w", err)
	}

	// 2. Send welcome email
	go func() {
		err = es.SendWelcomeEmail(*user, utils.GetLang(ctx), os.Getenv("APP_BASE_URL")+"/menu")
		if err != nil {
			zap.L().Error("failed to send welcome email", zap.String("user_id", user.ID.String()), zap.Error(err))
		}
	}()

	return nil
}

// Token validation
func (s *userService) validateRefreshToken(tokenString, secret string) (*types.JwtClaims, error) {
	claims := &types.JwtClaims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return []byte(secret), nil
	})

	if err != nil || !token.Valid || claims.Type != "refresh" {
		return nil, fmt.Errorf("invalid token")
	}

	return claims, nil
}

func (s *userService) RequestDeletion(ctx context.Context, userID string) (*domain.User, error) {
	user, err := s.repo.RequestDeletion(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to request deletion: %w", err)
	}

	// Send admin notification email asynchronously
	go func() {
		err := es.SendDeletionRequestEmail(*user)
		if err != nil {
			zap.L().Error("failed to send deletion request email", zap.String("user_id", user.ID.String()), zap.Error(err))
		}
	}()

	return user, nil
}

func (s *userService) CancelDeletionRequest(ctx context.Context, userID string) (*domain.User, error) {
	user, err := s.repo.CancelDeletionRequest(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to cancel deletion request: %w", err)
	}
	return user, nil
}

func (s *userService) ResendVerificationEmail(ctx context.Context, userID string) error {
	user, err := s.repo.FindByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("user not found")
	}

	if user.EmailVerifiedAt != nil {
		return fmt.Errorf("email already verified")
	}

	apiBaseUrl := os.Getenv("API_BASE_URL")
	jwtSecret := os.Getenv("JWT_SECRET")
	verificationToken, err := generateEmailVerificationJWT(*user, jwtSecret)
	if err != nil {
		return fmt.Errorf("failed to generate verification token: %w", err)
	}
	verificationURL := fmt.Sprintf("%s/verify?token=%s", apiBaseUrl, verificationToken)

	go func() {
		err := es.SendVerificationEmail(*user, utils.GetLang(ctx), verificationURL)
		if err != nil {
			zap.L().Error("failed to send verification email", zap.String("user_id", user.ID.String()), zap.Error(err))
		}
	}()

	return nil
}

func (s *userService) BatchGetUsersByOrderIDs(ctx context.Context, orderIDs []string) (map[string][]*domain.User, error) {
	return s.repo.BatchGetUsersByOrderIDs(ctx, orderIDs)
}

func validatePasswordStrength(password string) error {
	if len(password) < 8 {
		return fmt.Errorf("password must be at least 8 characters long")
	}
	var hasUpper, hasLower, hasDigit, hasSpecial bool
	for _, ch := range password {
		switch {
		case ch >= 'A' && ch <= 'Z':
			hasUpper = true
		case ch >= 'a' && ch <= 'z':
			hasLower = true
		case ch >= '0' && ch <= '9':
			hasDigit = true
		case strings.ContainsRune(`!@#$%^&*(),.?":{}|<>`, ch):
			hasSpecial = true
		}
	}
	if !hasUpper {
		return fmt.Errorf("password must contain at least one uppercase letter")
	}
	if !hasLower {
		return fmt.Errorf("password must contain at least one lowercase letter")
	}
	if !hasDigit {
		return fmt.Errorf("password must contain at least one digit")
	}
	if !hasSpecial {
		return fmt.Errorf("password must contain at least one special character (!@#$%%^&*(),.?\":{}|<>)")
	}
	return nil
}

func (s *userService) RequestPasswordReset(ctx context.Context, email string) error {
	user, err := s.repo.FindByEmail(ctx, email)
	if err != nil {
		// Silently return nil to prevent email enumeration
		return nil
	}

	// Only allow password reset for users with email/password auth
	if user.PasswordHash == nil {
		return nil
	}

	jwtSecret := os.Getenv("JWT_SECRET")
	token, err := generatePasswordResetJWT(*user, jwtSecret)
	if err != nil {
		return fmt.Errorf("failed to generate password reset token: %w", err)
	}

	appBaseURL := os.Getenv("APP_BASE_URL")
	resetURL := fmt.Sprintf("%s/reset-password?token=%s", appBaseURL, token)

	go func() {
		err := es.SendPasswordResetEmail(*user, utils.GetLang(ctx), resetURL)
		if err != nil {
			zap.L().Error("failed to send password reset email", zap.String("user_id", user.ID.String()), zap.Error(err))
		}
	}()

	return nil
}

func (s *userService) ResetPassword(ctx context.Context, token string, newPassword string) error {
	if err := validatePasswordStrength(newPassword); err != nil {
		return err
	}

	jwtSecret := os.Getenv("JWT_SECRET")

	// Parse and validate the token
	parsedToken, err := jwt.Parse(token, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return []byte(jwtSecret), nil
	})
	if err != nil {
		return fmt.Errorf("invalid or expired token")
	}

	claims, ok := parsedToken.Claims.(jwt.MapClaims)
	if !ok || !parsedToken.Valid {
		return fmt.Errorf("invalid token claims")
	}

	// Verify purpose
	if claims["purpose"] != "password_reset" {
		return fmt.Errorf("invalid token purpose")
	}

	userID, ok := claims["sub"].(string)
	if !ok || userID == "" {
		return fmt.Errorf("invalid token subject")
	}

	// Hash new password
	salt, err := generateSalt()
	if err != nil {
		return fmt.Errorf("failed to generate salt: %w", err)
	}
	hashedPassword, err := hashPassword(newPassword, salt)
	if err != nil {
		return fmt.Errorf("failed to hash password: %w", err)
	}

	// Update password in DB
	_, err = s.repo.UpdateUserPassword(ctx, userID, hashedPassword, salt)
	if err != nil {
		return fmt.Errorf("failed to update password: %w", err)
	}

	// Invalidate all refresh tokens for this user
	if err := s.repo.InvalidateAllRefreshTokens(ctx, userID); err != nil {
		zap.L().Error("failed to invalidate refresh tokens after password reset", zap.String("user_id", userID), zap.Error(err))
	}

	return nil
}

func generatePasswordResetJWT(user domain.User, jwtSecret string) (string, error) {
	expirationTime := time.Now().Add(1 * time.Hour)

	claims := jwt.MapClaims{
		"sub":     user.ID.String(),
		"email":   user.Email,
		"purpose": "password_reset",
		"iat":     time.Now().Unix(),
		"exp":     expirationTime.Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	tokenString, err := token.SignedString([]byte(jwtSecret))
	if err != nil {
		return "", err
	}
	return tokenString, nil
}

func generateSalt() (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("failed to generate salt: %v", err)
	}
	return base64.StdEncoding.EncodeToString(salt), nil
}

func hashPassword(password string, salt string) (string, error) {
	saltBytes, err := base64.StdEncoding.DecodeString(salt)
	if err != nil {
		return "", fmt.Errorf("failed to decode salt: %w", err)
	}
	hashedPassword := argon2.IDKey([]byte(password), saltBytes, 3, 64*1024, 4, 32)
	return base64.StdEncoding.EncodeToString(hashedPassword), nil
}

func generateTokens(user domain.User, jwtSecret string) (string, string, error) {
	// Access Token (15m)
	accessClaims := types.JwtClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   user.ID.String(),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(15 * time.Minute)),
		},
		Type:    "access",
		ID:      uuid.NewString(),
		IsAdmin: user.IsAdmin,
	}
	at := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims)
	accessTokenString, err := at.SignedString([]byte(jwtSecret))
	if err != nil {
		return "", "", fmt.Errorf("failed to sign access token: %w", err)
	}

	// Refresh Token (7d)
	refreshClaims := types.JwtClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   user.ID.String(),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(7 * 24 * time.Hour)),
		},
		Type:    "refresh",
		ID:      uuid.NewString(),
		IsAdmin: user.IsAdmin,
	}
	rt := jwt.NewWithClaims(jwt.SigningMethodHS256, refreshClaims)
	refreshTokenString, err := rt.SignedString([]byte(jwtSecret))
	if err != nil {
		return "", "", fmt.Errorf("failed to sign refresh token: %w", err)
	}

	return accessTokenString, refreshTokenString, nil
}

// GenerateEmailVerificationJWT generates a JWT token for email verification.
func generateEmailVerificationJWT(user domain.User, jwtSecret string) (string, error) {
	// Define token expiration; for example, 24 hours.
	expirationTime := time.Now().Add(24 * time.Hour)

	// Create a new token object, specifying signing method and the claims.
	claims := jwt.MapClaims{
		"sub":     user.ID,               // subject
		"purpose": "email_verification",  // optional: to distinguish token usage
		"iat":     time.Now().Unix(),     // issued at
		"exp":     expirationTime.Unix(), // expiration time
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	// Sign the token with the provided secret key.
	tokenString, err := token.SignedString([]byte(jwtSecret))
	if err != nil {
		return "", err
	}
	return tokenString, nil
}

func hashToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}
