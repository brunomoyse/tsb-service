// internal/user/repository/repository.go
package repository

import (
	"database/sql"
	"fmt"

	"tsb-service/internal/user"

	"github.com/google/uuid"
)

type UserRepository interface {
	CreateUser(u user.UserRegister, hashedPwd, salt string) (uuid.UUID, error)
	GetUserByEmail(email string) (user.User, error)
	GetUserByID(id string) (user.User, error)
	UpdateGoogleID(u user.GoogleUser) (*user.User, error)
	GetUserByGoogleID(googleID string) (user.User, error)
}

type userRepository struct {
	db *sql.DB
}

func NewUserRepository(dbConn *sql.DB) UserRepository {
	return &userRepository{db: dbConn}
}

func (r *userRepository) CreateUser(u user.UserRegister, hashedPwd, salt string) (uuid.UUID, error) {
	query := `
		INSERT INTO users (name, email, password_hash, salt)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`
	var newUserID uuid.UUID
	err := r.db.QueryRow(query, u.Name, u.Email, hashedPwd, salt).Scan(&newUserID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to create user: %v", err)
	}
	return newUserID, nil
}

func (r *userRepository) GetUserByEmail(email string) (user.User, error) {
	var u user.User
	query := `SELECT id, name, email, password_hash, salt FROM users WHERE email = $1`
	err := r.db.QueryRow(query, email).Scan(
		&u.ID, &u.Name, &u.Email, &u.PasswordHash, &u.Salt,
	)
	if err != nil {
		return u, fmt.Errorf("failed to get user by email: %v", err)
	}
	return u, nil
}

func (r *userRepository) GetUserByID(id string) (user.User, error) {
	var u user.User
	query := `SELECT id, name, email FROM users WHERE id = $1`
	err := r.db.QueryRow(query, id).Scan(&u.ID, &u.Name, &u.Email)
	if err != nil {
		return u, fmt.Errorf("failed to get user by ID: %v", err)
	}
	return u, nil
}

func (r *userRepository) UpdateGoogleID(u user.GoogleUser) (*user.User, error) {
	var existingUser user.User

	// Try to find by Google ID first
	err := r.db.QueryRow(
		`SELECT id, name, email FROM users WHERE google_id = $1`,
		u.GoogleID,
	).Scan(&existingUser.ID, &existingUser.Name, &existingUser.Email)

	if err == nil {
		return &existingUser, nil
	}

	// Try to find by email
	err = r.db.QueryRow(
		`SELECT id, name, email, google_id FROM users WHERE email = $1`,
		u.Email,
	).Scan(&existingUser.ID, &existingUser.Name, &existingUser.Email, &existingUser.GoogleID)

	if err == nil {
		if existingUser.GoogleID == nil {
			_, err = r.db.Exec(
				`UPDATE users SET google_id = $1 WHERE id = $2`,
				u.GoogleID, existingUser.ID,
			)
			if err != nil {
				return nil, fmt.Errorf("failed to update Google ID: %v", err)
			}
			existingUser.GoogleID = &u.GoogleID
		}
		return &existingUser, nil
	}

	// Create new user
	newUserID, err := r.CreateUser(user.UserRegister{
		Name:  u.Name,
		Email: u.Email,
	}, "", "")
	if err != nil {
		return nil, fmt.Errorf("failed to create new user: %v", err)
	}

	_, err = r.db.Exec(
		`UPDATE users SET google_id = $1 WHERE id = $2`,
		u.GoogleID, newUserID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to set Google ID: %v", err)
	}

	return &user.User{
		ID:       newUserID,
		Name:     u.Name,
		Email:    u.Email,
		GoogleID: &u.GoogleID,
	}, nil
}

func (r *userRepository) GetUserByGoogleID(googleID string) (user.User, error) {
	var u user.User
	query := `SELECT id, name, email FROM users WHERE google_id = $1`
	err := r.db.QueryRow(query, googleID).Scan(&u.ID, &u.Name, &u.Email)
	if err != nil {
		return u, fmt.Errorf("failed to get user by Google ID: %v", err)
	}
	return u, nil
}
