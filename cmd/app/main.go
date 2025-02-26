package main

import (
	"log"
	"os"

	orderHandler "tsb-service/internal/order/handler"
	orderRepository "tsb-service/internal/order/repository"
	orderService "tsb-service/internal/order/service"
	productHandler "tsb-service/internal/product/handler"
	productRepository "tsb-service/internal/product/repository"
	productService "tsb-service/internal/product/service"
	"tsb-service/internal/router"
	userHandler "tsb-service/internal/user/handler"
	userRepository "tsb-service/internal/user/repository"
	userService "tsb-service/internal/user/service"
	"tsb-service/pkg/db"
	"tsb-service/pkg/oauth2"

	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
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
	mollieConfig := mollie.NewAPITestingConfig(true)
	mollieClient, err := mollie.NewClient(nil, mollieConfig)
	if err != nil {
		log.Fatalf("Failed to initialize Mollie client: %v", err)
	}

	// 5) Set up user layers.
	oRepo := orderRepository.NewOrderRepository(dbConn)
	oSvc := orderService.NewOrderService(oRepo)

	pRepo := productRepository.NewProductRepository(dbConn)
	pSvc := productService.NewProductService(pRepo)

	uRepo := userRepository.NewUserRepository(dbConn)
	uSvc := userService.NewUserService(uRepo)

	// 6) Initialize the new user, product, order, and admin handlers.
	oHandler := orderHandler.NewHandler(oSvc, mollieClient)
	pHandler := productHandler.NewHandler(pSvc)
	uHandler := userHandler.NewHandler(uSvc)

	// 7) Create route registrars.
	publicRoutes := router.NewPublicRoutes(oHandler, pHandler, uHandler)
	protectedRoutes := router.NewProtectedRoutes(oHandler, pHandler)

	registrars := []router.RouteRegistrar{
		publicRoutes,
		protectedRoutes,
	}

	// 8) Setup the router with the registrars.
	r := router.SetupRouter(jwtSecret, registrars)

	// 9) Start the server.
	if err := r.Run(":8080"); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
