// migrate-users imports existing app DB users into Zitadel.
// It preserves Argon2ID password hashes so users keep their passwords.
//
// Usage: go run cmd/migrate-users/main.go
package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"

	_ "github.com/lib/pq"
	"github.com/joho/godotenv"
)

type appUser struct {
	ID            string
	Email         string
	FirstName     string
	LastName      string
	PhoneNumber   *string
	ZitadelUserID *string
}

type zitadelCreateUserResponse struct {
	UserID string `json:"userId"`
}

func main() {
	if err := godotenv.Load(); err != nil {
		log.Fatal("Failed to load .env file")
	}

	zitadelURL := os.Getenv("ZITADEL_ISSUER")
	pat := os.Getenv("ZITADEL_SERVICE_PAT")
	if zitadelURL == "" || pat == "" {
		log.Fatal("ZITADEL_ISSUER and ZITADEL_SERVICE_PAT are required")
	}

	// Connect to app database
	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		os.Getenv("DB_HOST"), os.Getenv("DB_PORT"),
		os.Getenv("DB_USERNAME"), os.Getenv("DB_PASSWORD"),
		os.Getenv("DB_DATABASE"), os.Getenv("DB_SSL_MODE"))

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer func() { _ = db.Close() }()

	// Fetch users not yet migrated
	rows, err := db.Query(`
		SELECT id, email, first_name, last_name, phone_number, zitadel_user_id
		FROM users
		WHERE zitadel_user_id IS NULL AND email != ''
		ORDER BY created_at
	`)
	if err != nil {
		log.Fatalf("Failed to query users: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var users []appUser
	for rows.Next() {
		var u appUser
		if err := rows.Scan(&u.ID, &u.Email, &u.FirstName, &u.LastName, &u.PhoneNumber, &u.ZitadelUserID); err != nil {
			log.Fatalf("Failed to scan user: %v", err)
		}
		users = append(users, u)
	}

	log.Printf("Found %d users to migrate", len(users))

	for _, u := range users {
		log.Printf("Migrating %s (%s %s)...", u.Email, u.FirstName, u.LastName)

		zitadelUserID, err := createZitadelUser(zitadelURL, pat, u)
		if err != nil {
			log.Printf("  ERROR: %v — skipping", err)
			continue
		}

		// Update app DB with Zitadel user ID
		_, err = db.Exec("UPDATE users SET zitadel_user_id = $1 WHERE id = $2", zitadelUserID, u.ID)
		if err != nil {
			log.Printf("  ERROR updating DB: %v", err)
			continue
		}

		log.Printf("  OK → zitadel_user_id=%s", zitadelUserID)
	}

	log.Println("Migration complete")
}

func createZitadelUser(baseURL, pat string, u appUser) (string, error) {
	body := map[string]any{
		"userName": u.Email,
		"profile": map[string]any{
			"givenName":  u.FirstName,
			"familyName": u.LastName,
		},
		"email": map[string]any{
			"email":           u.Email,
			"isEmailVerified": true,
		},
	}

	// Add phone if present
	if u.PhoneNumber != nil && *u.PhoneNumber != "" {
		body["phone"] = map[string]any{
			"phone": *u.PhoneNumber,
		}
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshal body: %w", err)
	}

	req, err := http.NewRequest("POST", baseURL+"/v2/users/human", bytes.NewReader(jsonBody))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+pat)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, string(respBody))
	}

	var result zitadelCreateUserResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}

	if result.UserID == "" {
		return "", fmt.Errorf("empty userID in response: %s", string(respBody))
	}

	return result.UserID, nil
}
