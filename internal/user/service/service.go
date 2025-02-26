// internal/user/service/service.go
package service

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"time"

	"tsb-service/internal/user"
	"tsb-service/internal/user/repository"

	"github.com/golang-jwt/jwt/v4"
	"golang.org/x/crypto/argon2"
)

type UserService interface {
	SignUp(u user.UserRegister) (user.UserResponse, error)
	SignIn(u user.UserLogin) (user.User, string, string, error)
	GetUserByID(id string) (user.User, error)
	HandleGoogleUser(googleUser user.GoogleUser) (*user.User, error)
	GenerateTokens(userID string) (string, string, error)
	RefreshToken(refreshToken string) (string, *user.UserResponse, error)
}

type userService struct {
	repo repository.UserRepository
}

func NewUserService(r repository.UserRepository) UserService {
	return &userService{repo: r}
}

func (s *userService) SignUp(u user.UserRegister) (user.UserResponse, error) {
	salt, err := generateSalt()
	if err != nil {
		return user.UserResponse{}, err
	}
	hashedPassword := hashPassword(u.Password, salt)

	newUserID, err := s.repo.CreateUser(u, hashedPassword, salt)
	if err != nil {
		return user.UserResponse{}, err
	}

	return user.UserResponse{
		ID:    newUserID,
		Name:  u.Name,
		Email: u.Email,
	}, nil
}

func (s *userService) SignIn(u user.UserLogin) (user.User, string, string, error) {
	dbUser, err := s.repo.GetUserByEmail(u.Email)
	if err != nil {
		return dbUser, "", "", err
	}

	hashedInput := hashPassword(u.Password, *dbUser.Salt)
	if hashedInput != *dbUser.PasswordHash {
		return dbUser, "", "", fmt.Errorf("invalid password")
	}

	accessToken, refreshToken, err := generateJWT(dbUser.ID.String())
	if err != nil {
		return dbUser, "", "", err
	}

	return dbUser, accessToken, refreshToken, nil
}

func (s *userService) HandleGoogleUser(googleUser user.GoogleUser) (*user.User, error) {
	dbUser, err := s.repo.UpdateGoogleID(googleUser)
	if err != nil {
		return nil, fmt.Errorf("failed to handle Google user: %v", err)
	}
	return dbUser, nil
}

func (s *userService) GenerateTokens(userID string) (string, string, error) {
	return generateJWT(userID)
}

func (s *userService) RefreshToken(refreshToken string) (string, *user.UserResponse, error) {
	claims := &jwt.RegisteredClaims{}
	token, err := jwt.ParseWithClaims(refreshToken, claims, func(token *jwt.Token) (interface{}, error) {
		return []byte(os.Getenv("JWT_SECRET")), nil
	})

	if err != nil || !token.Valid {
		return "", nil, fmt.Errorf("invalid or expired refresh token")
	}

	userID := claims.Subject
	dbUser, err := s.repo.GetUserByID(userID)
	if err != nil {
		return "", nil, fmt.Errorf("failed to fetch user: %v", err)
	}

	newAccessToken, _, err := generateJWT(userID)
	if err != nil {
		return "", nil, fmt.Errorf("failed to generate new access token")
	}

	return newAccessToken, &user.UserResponse{
		ID:    dbUser.ID,
		Name:  dbUser.Name,
		Email: dbUser.Email,
	}, nil
}

func (s *userService) GetUserByID(id string) (user.User, error) {
	return s.repo.GetUserByID(id)
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

func generateJWT(userId string) (string, string, error) {
	if os.Getenv("JWT_SECRET") == "" {
		return "", "", fmt.Errorf("JWT_SECRET is not set")
	}

	secretKey := []byte(os.Getenv("JWT_SECRET"))
	accessTokenExpiration := time.Now().Add(15 * time.Minute)
	refreshTokenExpiration := time.Now().Add(7 * 24 * time.Hour)

	accessClaims := jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(accessTokenExpiration),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
		Subject:   userId,
	}

	refreshClaims := jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(refreshTokenExpiration),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
		Subject:   userId,
	}

	accessToken := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims)
	accessTokenString, err := accessToken.SignedString(secretKey)
	if err != nil {
		return "", "", fmt.Errorf("failed to sign access token: %v", err)
	}

	refreshToken := jwt.NewWithClaims(jwt.SigningMethodHS256, refreshClaims)
	refreshTokenString, err := refreshToken.SignedString(secretKey)
	if err != nil {
		return "", "", fmt.Errorf("failed to sign refresh token: %v", err)
	}

	return accessTokenString, refreshTokenString, nil
}
