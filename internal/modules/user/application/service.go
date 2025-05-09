package application

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"log"
	"os"
	"time"
	"tsb-service/internal/modules/user/domain"
	"tsb-service/pkg/utils"
	es "tsb-service/services/email/scaleway"

	"github.com/golang-jwt/jwt/v4"
	"golang.org/x/crypto/argon2"
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
	// Ensure at least one credential is provided.
	if password == nil && googleID == nil {
		return nil, fmt.Errorf("password or googleID must be provided")
	}

	// Check if user already exists
	if user, err := s.repo.FindByEmail(ctx, email); err == nil {
		return nil, fmt.Errorf("user with email %s already exists", user.Email)
	}

	if password != nil {
		// Email/password flow.
		salt, err := generateSalt()
		if err != nil {
			return nil, fmt.Errorf("failed to generate salt: %w", err)
		}
		hashedPassword := hashPassword(*password, salt)

		// Try to find an existing user by email.
		user, err := s.repo.FindByEmail(ctx, email)
		if err == nil {
			// User already exists; update password.
			updatedUser, err := s.repo.UpdateUserPassword(ctx, user.ID.String(), hashedPassword, salt)
			if err != nil {
				return nil, fmt.Errorf("failed to update user password: %w", err)
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
				log.Printf("failed to send verification email: %v", err)
			}
		}()

		// 4. Return immediately.
		return &newUser, nil
	}

	// Google flow.
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

	hashedPasswordRequest := hashPassword(password, *user.Salt)
	if hashedPasswordRequest != *user.PasswordHash {
		return nil, nil, nil, fmt.Errorf("invalid password")
	}

	accessToken, refreshToken, err := generateTokens(*user, jwtSecret)
	if err != nil {
		return nil, nil, nil, err
	}

	return user, &accessToken, &refreshToken, nil
}

func (s *userService) InvalidateRefreshToken(ctx context.Context, refreshToken string) error {
	if refreshToken == "" {
		return fmt.Errorf("refresh token is empty")
	}

	if err := s.repo.InvalidateRefreshToken(ctx, refreshToken); err != nil {
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
	return generateTokens(user, jwtSecret)
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
	// 1. Validate refresh token
	claims, err := s.validateRefreshToken(oldRefreshToken, jwtSecret)
	if err != nil {
		return "", "", nil, err
	}

	// @TODO: 2. Check token revocation
	//if s.isTokenRevoked(claims.ID) {
	//	return "", "", nil, fmt.Errorf("token revoked")
	//}

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

	// @TODO: 5. Revoke old refresh token
	// s.revokeToken(claims.ID)

	// @TODO: 6. Store new refresh token
	//s.storeRefreshToken(refreshToken)

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
			log.Printf("failed to send welcome email: %v", err)
		}
	}()

	return nil
}

// Token validation
func (s *userService) validateRefreshToken(tokenString, secret string) (*domain.JwtClaims, error) {
	claims := &domain.JwtClaims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (interface{}, error) {
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

func (s *userService) BatchGetUsersByOrderIDs(ctx context.Context, orderIDs []string) (map[string][]*domain.User, error) {
	return s.repo.BatchGetUsersByOrderIDs(ctx, orderIDs)
}

func generateSalt() (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("failed to generate salt: %v", err)
	}
	return base64.StdEncoding.EncodeToString(salt), nil
}

func hashPassword(password string, salt string) string {
	saltBytes, _ := base64.StdEncoding.DecodeString(salt)
	hashedPassword := argon2.IDKey([]byte(password), saltBytes, 1, 64*1024, 4, 32)
	return base64.StdEncoding.EncodeToString(hashedPassword)
}

func generateTokens(user domain.User, jwtSecret string) (string, string, error) {
	// build the base RegisteredClaims
	baseRC := jwt.RegisteredClaims{
		Subject:   user.ID.String(),
		ExpiresAt: jwt.NewNumericDate(time.Now()), // we'll override per-token
	}

	// if the user is an admin, include "admin" in the Audience
	if user.IsAdmin {
		baseRC.Audience = jwt.ClaimStrings{"admin"}
	}

	// Access Token (15m)
	accessRC := baseRC
	accessRC.ExpiresAt = jwt.NewNumericDate(time.Now().Add(15 * time.Minute))
	accessClaims := domain.JwtClaims{
		RegisteredClaims: accessRC,
		Type:             "access",
		ID:               uuid.NewString(),
	}
	at := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims)
	accessTokenString, err := at.SignedString([]byte(jwtSecret))
	if err != nil {
		return "", "", fmt.Errorf("failed to sign access token: %w", err)
	}

	// Refresh Token (7d)
	refreshRC := baseRC
	refreshRC.ExpiresAt = jwt.NewNumericDate(time.Now().Add(7 * 24 * time.Hour))
	refreshClaims := domain.JwtClaims{
		RegisteredClaims: refreshRC,
		Type:             "refresh",
		ID:               uuid.NewString(),
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

// @TODO
//func (s *userService) storeRefreshToken(token string) error {
//	claims, _ := parseTokenWithoutValidation(token) // Extract claims
//	return s.repo.StoreToken(claims.ID, claims.Subject, claims.ExpiresAt)
//}
//
//func (s *userService) isTokenRevoked(tokenID string) bool {
//	return s.repo.IsTokenRevoked(tokenID)
//}
//
//func (s *userService) revokeToken(tokenID string) {
//	s.repo.RevokeToken(tokenID)
//}
