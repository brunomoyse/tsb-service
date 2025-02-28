package main

import (
	"log"
	"os"

	productApplication "tsb-service/internal/modules/product/application"
	productInfrastructure "tsb-service/internal/modules/product/infrastructure"
	productInterfaces "tsb-service/internal/modules/product/interfaces"
	"tsb-service/pkg/db"
	"tsb-service/pkg/oauth2"

	"github.com/gin-gonic/gin"
)

func main() {
	// 1) Connect to the DB.
	dbConn, err := db.ConnectDatabase()
	if err != nil {
		log.Fatalf("Failed to connect to DB: %v", err)
	}
	defer dbConn.Close()

	// 2) Check required environment variables.
	mollieApiKey := os.Getenv("MOLLIE_API_TOKEN")
	if mollieApiKey == "" {
		log.Fatal("MOLLIE_API_TOKEN is required")
	}
	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		log.Fatal("JWT_SECRET is required")
	}

	// 3) Load Google OAuth credentials.
	oauth2.LoadGoogleOAuth()

	// 4) Initialize the Mollie client.
	// mollieConfig := mollie.NewAPITestingConfig(true)
	// mollieClient, err := mollie.NewClient(nil, mollieConfig)
	// if err != nil {
	// 	log.Fatalf("Failed to initialize Mollie client: %v", err)
	// }

	productRepo := productInfrastructure.NewProductRepository(dbConn)
	productService := productApplication.NewProductService(productRepo)
	productHandler := productInterfaces.NewProductHandler(productService)

	// Initialize Gin router
	router := gin.Default()

	// Setup routes (grouped by API version or module as needed)
	api := router.Group("/api/v1")
	{
		api.GET("/products", productHandler.GetProductsHandler)
		api.GET("/products/:id", productHandler.GetProductHandler)
		api.GET("/categories", productHandler.GetCategoriesHandler)
		api.GET("/categories/:categoryID/products", productHandler.GetProductsByCategoryHandler)
	}

	if err := router.Run(":8080"); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
