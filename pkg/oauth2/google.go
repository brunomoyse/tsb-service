package oauth2

import (
	"context"
	"log"
	"os"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// GoogleOAuthConfig holds the OAuth2 configuration for Google.
var GoogleOAuthConfig *oauth2.Config

// LoadGoogleOAuth initializes the Google OAuth2 configuration using environment variables.
// It ensures that all required variables are set.
func LoadGoogleOAuth() {
	clientID := os.Getenv("GOOGLE_CLIENT_ID")
	clientSecret := os.Getenv("GOOGLE_CLIENT_SECRET")
	redirectURI := os.Getenv("GOOGLE_REDIRECT_URI")

	if clientID == "" || clientSecret == "" || redirectURI == "" {
		log.Fatal("GOOGLE_CLIENT_ID, GOOGLE_CLIENT_SECRET, and GOOGLE_REDIRECT_URI must be set")
	}

	GoogleOAuthConfig = &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURI,
		Scopes:       []string{"openid", "profile", "email"},
		Endpoint:     google.Endpoint,
	}

	log.Println("Google OAuth configuration loaded successfully")
}

// GetGoogleAuthURL returns the Google authentication URL for a given state.
// The state parameter helps prevent CSRF attacks.
func GetGoogleAuthURL(state string) string {
	return GoogleOAuthConfig.AuthCodeURL(state, oauth2.AccessTypeOffline)
}

// ExchangeGoogleCode exchanges an authorization code for an OAuth2 token.
func ExchangeGoogleCode(code string) (*oauth2.Token, error) {
	return GoogleOAuthConfig.Exchange(context.Background(), code)
}
