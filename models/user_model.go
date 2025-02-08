package models

import (
	"crypto/rand"
	"os"

	"encoding/base64"
	"fmt"
	"time"
	"tsb-service/config"

	"github.com/golang-jwt/jwt/v4"
	"github.com/google/uuid"
	"golang.org/x/crypto/argon2"
)

type User struct {
	ID              uuid.UUID  `json:"id"`
	CreatedAt       string     `json:"createdAt"`
	UpdatedAt       string     `json:"updatedAt"`
	Name            string     `json:"name"`
	Email           string     `json:"email"`
	EmailVerifiedAt *time.Time `json:"emailVerifiedAt"`
	PasswordHash    string     `json:"passwordHash"`
	Salt            string     `json:"salt"`
	RememberToken   *string    `json:"rememberToken"`
}

type UserLogin struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type UserRegister struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

type UserResponse struct {
	ID    uuid.UUID `json:"id"`
	Name  string    `json:"name"`
	Email string    `json:"email"`
}

type RefreshTokenRequest struct {
	RefreshToken string `json:"refreshToken"`
}

type TokenResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
}

// Claims is the struct for JWT claims
type Claims struct {
	jwt.RegisteredClaims
}

// GenerateSalt generates a random salt for password hashing
func GenerateSalt() (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("failed to generate salt: %v", err)
	}
	return base64.StdEncoding.EncodeToString(salt), nil
}

// HashPassword uses Argon2 to hash the password with the given salt
func HashPassword(password string, salt string) string {
	saltBytes, _ := base64.StdEncoding.DecodeString(salt)

	// Use Argon2id (the recommended version)
	hashedPassword := argon2.IDKey([]byte(password), saltBytes, 1, 64*1024, 4, 32)

	// Encode the hashed password to base64 for storage in the database
	return base64.StdEncoding.EncodeToString(hashedPassword)
}

// GenerateJWT generates an access token and a refresh token
func GenerateJWT(userId string) (string, string, error) {
	// If the secret key is empty, err
	if os.Getenv("JWT_SECRET") == "" {
		return "", "", fmt.Errorf("JWT_SECRET is not set")
	}

	secretKey := []byte(os.Getenv("JWT_SECRET"))

	// Access token expiration (15 minutes)
	accessTokenExpiration := time.Now().Add(15 * time.Minute)
	// Refresh token expiration (7 days)
	refreshTokenExpiration := time.Now().Add(7 * 24 * time.Hour)

	// Create claims for the access token
	accessClaims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(accessTokenExpiration),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Subject:   userId,
		},
	}

	// Create claims for the refresh token
	refreshClaims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(refreshTokenExpiration),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Subject:   userId,
		},
	}

	// Generate the access token
	accessToken := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims)
	accessTokenString, err := accessToken.SignedString(secretKey)
	if err != nil {
		return "", "", fmt.Errorf("failed to sign access token: %v", err)
	}

	// Generate the refresh token
	refreshToken := jwt.NewWithClaims(jwt.SigningMethodHS256, refreshClaims)
	refreshTokenString, err := refreshToken.SignedString(secretKey)
	if err != nil {
		return "", "", fmt.Errorf("failed to sign refresh token: %v", err)
	}

	return accessTokenString, refreshTokenString, nil
}

func SignUp(u UserRegister) (UserResponse, error) {

	// Generate a salt for the password
	salt, err := GenerateSalt()
	if err != nil {
		return UserResponse{}, err
	}

	// Hash the password with Argon2
	hashedPassword := HashPassword(u.Password, salt)

	query := `
	INSERT INTO users (name, email, password_hash, salt)
	VALUES ($1, $2, $3, $4) 
	RETURNING id
	`

	var newUserID uuid.UUID

	// Execute the query and scan the returned id
	err = config.DB.QueryRow(query, u.Name, u.Email, hashedPassword, salt).Scan(&newUserID)
	if err != nil {
		return UserResponse{}, fmt.Errorf("failed to insert new user: %v", err)
	}

	// Return the user response
	return UserResponse{
		ID:    newUserID,
		Name:  u.Name,
		Email: u.Email,
	}, nil
}

func SignIn(u UserLogin) (TokenResponse, error) {
	query := `
	SELECT id, name, email, password_hash, salt FROM users WHERE email = $1
	`

	var user User
	err := config.DB.QueryRow(query, u.Email).Scan(&user.ID, &user.Name, &user.Email, &user.PasswordHash, &user.Salt)
	if err != nil {
		return TokenResponse{}, fmt.Errorf("failed to get user: %v", err)
	}

	// Hash the provided password with the stored salt
	hashedPassword := HashPassword(u.Password, user.Salt)

	// Compare the hashed password with the stored password
	if hashedPassword != user.PasswordHash {
		return TokenResponse{}, fmt.Errorf("invalid password")
	}

	// Generate the JWT tokens (access and refresh)
	accessToken, refreshToken, err := GenerateJWT(user.ID.String())
	if err != nil {
		return TokenResponse{}, fmt.Errorf("failed to generate tokens: %v", err)
	}

	// Return the tokens in the response
	return TokenResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
	}, nil
}
