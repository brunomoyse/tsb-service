package db

import (
	"database/sql"
	"fmt"
	"log"
	"os"

	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
)

func ConnectDatabase() (*sql.DB, error) {
	// Load .env
	if err := godotenv.Load(); err != nil {
		log.Printf("Warning: error loading .env file: %v", err)
	}

	// Construct connection string
	dbHost := os.Getenv("DB_HOST")
	dbPort := os.Getenv("DB_PORT")
	dbUser := os.Getenv("DB_USERNAME")
	dbPassword := os.Getenv("DB_PASSWORD")
	dbName := os.Getenv("DB_DATABASE")

	connectionString := fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		dbHost, dbPort, dbUser, dbPassword, dbName,
	)

	// Connect
	dbConn, err := sql.Open("postgres", connectionString)
	if err != nil {
		return nil, fmt.Errorf("error connecting to database: %v", err)
	}

	// Ping
	if err = dbConn.Ping(); err != nil {
		return nil, fmt.Errorf("error pinging database: %v", err)
	}

	log.Println("Database connection established")
	return dbConn, nil
}
