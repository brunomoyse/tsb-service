package types

import "github.com/golang-jwt/jwt/v4"

// JwtClaims defines the custom claims used in access and refresh JWT tokens.
type JwtClaims struct {
	jwt.RegisteredClaims
	Type    string `json:"type"`     // "access" or "refresh"
	ID      string `json:"jti"`      // Unique token identifier
	IsAdmin bool   `json:"is_admin"` // Admin role flag
}
