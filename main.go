package main

import (
	"log"
	"os"
	"tsb-service/config"
	"tsb-service/routes"

	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
)

func main() {
	// Initialize database connection
	config.ConnectDatabase()

	mollieApiKey := os.Getenv("MOLLIE_API_TOKEN")
	if mollieApiKey == "" {
		log.Fatal("MOLLIE_API_TOKEN")
	}

	// Check if JWT_SECRET is set
	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		log.Fatal("JWT_SECRET is required")
	}

	// Create a configuration object with idempotency enabled.
	config := mollie.NewAPITestingConfig(true)

	// Initialize the Mollie client
	mollieClient, err := mollie.NewClient(nil, config)
	if err != nil {
		log.Fatal(err)
	}

	// Set up router and pass the client
	router := routes.SetupRouter(mollieClient, jwtSecret)

	// Start the server on a specific port
	if err := router.Run(":8080"); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
