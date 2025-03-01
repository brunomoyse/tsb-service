package main

import (
	"log"
	"os"

	productApplication "tsb-service/internal/modules/product/application"
	productInfrastructure "tsb-service/internal/modules/product/infrastructure"
	productInterfaces "tsb-service/internal/modules/product/interfaces"

	orderApplication "tsb-service/internal/modules/order/application"
	orderInfrastructure "tsb-service/internal/modules/order/infrastructure"
	orderInterfaces "tsb-service/internal/modules/order/interfaces"

	"tsb-service/internal/shared/middleware"
	"tsb-service/pkg/db"
	"tsb-service/pkg/oauth2"

	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/gin-gonic/gin"
)

func main() {
	// Connect to the DB.
	dbConn, err := db.ConnectDatabase()
	if err != nil {
		log.Fatalf("Failed to connect to DB: %v", err)
	}
	defer dbConn.Close()

	// Check required environment variables.
	mollieApiKey := os.Getenv("MOLLIE_API_TOKEN")
	if mollieApiKey == "" {
		log.Fatal("MOLLIE_API_TOKEN is required")
	}
	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		log.Fatal("JWT_SECRET is required")
	}

	// Load Google OAuth credentials.
	oauth2.LoadGoogleOAuth()

	// Initialize the Mollie client.
	mollieConfig := mollie.NewAPITestingConfig(true)
	mollieClient, err := mollie.NewClient(nil, mollieConfig)
	if err != nil {
		log.Fatalf("Failed to initialize Mollie client: %v", err)
	}

	productRepo := productInfrastructure.NewProductRepository(dbConn)
	productService := productApplication.NewProductService(productRepo)
	productHandler := productInterfaces.NewProductHandler(productService)

	orderRepo := orderInfrastructure.NewOrderRepository(dbConn)
	orderService := orderApplication.NewOrderService(orderRepo, mollieClient)
	orderHandler := orderInterfaces.NewOrderHandler(orderService)

	// Initialize Gin router
	router := gin.Default()

	// Setup routes (grouped by API version or module as needed)
	api := router.Group("/api/v1")
	api.Use(middleware.AuthMiddleware(jwtSecret))
	api.Use(middleware.LanguageExtractor())
	{
		// Product routes
		api.GET("/products", productHandler.GetProductsHandler)
		api.GET("/products/:id", productHandler.GetProductHandler)
		api.GET("/categories", productHandler.GetCategoriesHandler)
		api.GET("/categories/:categoryID/products", productHandler.GetProductsByCategoryHandler)

		// Orders routes
		api.GET("/me/orders", orderHandler.GetUserOrdersHandler)

	}

	if err := router.Run(":8080"); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
