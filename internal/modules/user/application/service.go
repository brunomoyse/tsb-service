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
	emailService "tsb-service/templates/email"

	"github.com/golang-jwt/jwt/v4"
	"golang.org/x/crypto/argon2"
)

type UserService interface {
	CreateUser(ctx context.Context, name string, email string, phoneNumber *string, address *string, password *string, googleID *string) (*domain.User, error)
	UpdateMe(ctx context.Context, userID string, name *string, email *string, phoneNumber *string, address *string) (*domain.User, error)
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
}

type userService struct {
	repo domain.UserRepository
}

func NewUserService(repo domain.UserRepository) UserService {
	return &userService{
		repo: repo,
	}
}

func (s *userService) CreateUser(ctx context.Context, name string, email string, phoneNumber *string, address *string, password *string, googleID *string) (*domain.User, error) {
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
		newUser := domain.NewUser(name, email, phoneNumber, address, &hashedPassword, &salt)
		id, err := s.repo.Save(ctx, &newUser)
		if err != nil {
			return nil, fmt.Errorf("failed to create user: %w", err)
		}
		newUser.ID = id

		// 2. Build verification URL or token. @TODO: Implement VerificationToken generation.
		appBaseUrl := os.Getenv("APP_BASE_URL")
		verificationURL := fmt.Sprintf("%s/verify?token=%s", appBaseUrl, "newUser.VerificationToken")

		// 3. Send verification email asynchronously in a goroutine.
		go func() {
			// Create a new background context for the asynchronous work.
			bgCtx := context.Background()
			bgCtx = utils.SetLang(bgCtx, utils.GetLang(ctx))
			es, err := emailService.NewEmailService(bgCtx)
			if err != nil {
				log.Printf("failed to initialize email service: %v", err)
			}
			err = es.SendVerificationEmail(bgCtx, newUser.Email, newUser.Name, verificationURL)
			if err != nil {
			}
		}()

		// 4. Return immediately.
		return &newUser, nil
	}

	// Google flow.
	newUser := domain.NewGoogleUser(name, email, *googleID)
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

func (s *userService) UpdateMe(ctx context.Context, userID string, name *string, email *string, phoneNumber *string, address *string) (*domain.User, error) {
	user, err := s.repo.FindByID(ctx, userID)
	if err != nil {
		return nil, err
	}

	if name != nil {
		user.Name = *name
	}
	if email != nil {
		user.Email = *email
	}
	if phoneNumber != nil {
		user.PhoneNumber = phoneNumber
	}
	if address != nil {
		user.Address = address
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
	// Access Token
	accessClaims := domain.JwtClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(15 * time.Minute)),
			Subject:   user.ID.String(),
		},
		Type: "access",
		ID:   uuid.NewString(),
	}

	// Refresh Token
	refreshClaims := domain.JwtClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(7 * 24 * time.Hour)),
			Subject:   user.ID.String(),
		},
		Type: "refresh",
		ID:   uuid.NewString(),
	}

	accessToken := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims)
	accessTokenString, err := accessToken.SignedString([]byte(jwtSecret))
	if err != nil {
		return "", "", fmt.Errorf("failed to sign access token: %w", err)
	}

	refreshToken := jwt.NewWithClaims(jwt.SigningMethodHS256, refreshClaims)
	refreshTokenString, err := refreshToken.SignedString([]byte(jwtSecret))
	if err != nil {
		return "", "", fmt.Errorf("failed to sign refresh token: %w", err)
	}

	return accessTokenString, refreshTokenString, nil
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
