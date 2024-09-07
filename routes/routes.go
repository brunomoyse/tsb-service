package routes

import (
	"tsb-service/controllers"
	"tsb-service/middleware"

	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/gin-gonic/gin"
)

func SetupRouter(client *mollie.Client, jwtSecret string) *gin.Engine {
	r := gin.Default()

	// Apply the language extractor middleware globally
	r.Use(middleware.LanguageExtractor())

	// Create a new handler that holds the Mollie client
	h := controllers.NewHandler(client)

	// Define public routes (no authentication required)
	r.GET("/products", controllers.GetProducts)
	r.POST("/sign-up", controllers.SignUp)
	r.POST("/sign-in", controllers.SignIn)

	// Define the refresh token route, passing the jwtSecret
	r.POST("/refresh-token", func(c *gin.Context) {
		controllers.RefreshToken(c, jwtSecret)
	})

	// Define routes that require authentication
	authorized := r.Group("/")
	authorized.Use(middleware.AuthMiddleware(jwtSecret)) // Apply auth middleware only for this group

	// Define routes that require authentication within the group
	authorized.POST("/orders/", h.CreateOrder)
	authorized.PUT("/product/:id", controllers.UpdateProduct)
	authorized.POST("/product/:id", controllers.CreateProduct)

	return r
}
