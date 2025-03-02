package application

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
	"tsb-service/internal/modules/user/domain"

	"github.com/golang-jwt/jwt/v4"
	"golang.org/x/crypto/argon2"
)

type UserService interface {
	CreateUser(ctx context.Context, name string, email string, phone_number *string, address *string, password *string, googleID *string) (*domain.User, error)
	Login(ctx context.Context, email string, password string, jwtToken string) (*domain.User, *string, *string, error)
	GetUserByID(ctx context.Context, id string) (*domain.User, error)
	GetUserByEmail(ctx context.Context, email string) (*domain.User, error)
	GetUserByGoogleID(ctx context.Context, googleID string) (*domain.User, error)
	GenerateTokens(ctx context.Context, userID string, jwtToken string) (string, string, error)
	RefreshToken(ctx context.Context, refreshToken string, jwtSecret string) (*domain.User, *string, error)
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

func (s *userService) CreateUser(ctx context.Context, name string, email string, phone_number *string, address *string, password *string, googleID *string) (*domain.User, error) {
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

		// User does not exist; create a new user.
		newUser := domain.NewUser(name, email, phone_number, address, &hashedPassword, &salt)
		id, err := s.repo.Save(ctx, &newUser)
		if err != nil {
			return nil, fmt.Errorf("failed to create user: %w", err)
		}
		newUser.ID = id
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

	accessToken, refreshToken, err := generateJWT(user.ID.String(), jwtSecret)
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

func (s *userService) GenerateTokens(ctx context.Context, userID string, jwtSecret string) (string, string, error) {
	return generateJWT(userID, jwtSecret)
}

func (s *userService) UpdateGoogleID(ctx context.Context, userID string, googleID string) (*domain.User, error) {
	return s.repo.UpdateGoogleID(ctx, userID, googleID)
}

func (s *userService) UpdateUserPassword(ctx context.Context, userID string, password string, salt string) (*domain.User, error) {
	return s.repo.UpdateUserPassword(ctx, userID, password, salt)
}

func (s *userService) UpdateEmailVerifiedAt(ctx context.Context, userID string) (*domain.User, error) {
	return s.repo.UpdateEmailVerifiedAt(ctx, userID)
}

func (s *userService) RefreshToken(ctx context.Context, refreshToken string, jwtSecret string) (*domain.User, *string, error) {
	claims := &jwt.RegisteredClaims{}
	token, err := jwt.ParseWithClaims(refreshToken, claims, func(token *jwt.Token) (any, error) {
		return []byte(jwtSecret), nil
	})
	if err != nil || !token.Valid {
		return nil, nil, fmt.Errorf("invalid or expired refresh token")
	}

	userID := claims.Subject
	user, err := s.repo.FindByID(ctx, userID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to fetch user: %w", err)
	}

	// Optionally, update generateJWT to use jwtSecret as needed.
	newAccessToken, _, err := generateJWT(userID, jwtSecret)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate new access token: %w", err)
	}

	return user, &newAccessToken, nil
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

func generateJWT(userID string, jwtSecret string) (string, string, error) {
	accessTokenExpiration := time.Now().Add(15 * time.Minute)
	refreshTokenExpiration := time.Now().Add(7 * 24 * time.Hour)

	accessClaims := jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(accessTokenExpiration),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
		Subject:   userID,
	}

	refreshClaims := jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(refreshTokenExpiration),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
		Subject:   userID,
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
