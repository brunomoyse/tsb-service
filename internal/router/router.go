package router

import (
	"net/http"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"tsb-service/internal/middleware"
)

// RouteRegistrar defines an interface for registering routes.
type RouteRegistrar interface {
	RegisterRoutes(r *gin.Engine, jwtSecret string)
}

// SetupRouter accepts a slice of RouteRegistrar implementations and registers their routes.
func SetupRouter(jwtSecret string, registrars []RouteRegistrar) *gin.Engine {
	r := gin.Default()
	r.RedirectTrailingSlash = true
	r.RedirectFixedPath = true

	// CORS Middleware (Allow Specific Origins)
	r.Use(cors.New(cors.Config{
		AllowOrigins:     []string{"http://localhost:3000", "https://nuagemagique.dev"},
		AllowMethods:     []string{"HEAD", "GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization", "Accept-Language"},
		ExposeHeaders:    []string{"Content-Length", "Authorization"},
		AllowCredentials: true,
	}))

	// Handle CORS preflight requests (OPTIONS method)
	r.OPTIONS("/*any", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	// Apply language extractor middleware
	r.Use(middleware.LanguageExtractor())

	// Health check for HEAD requests
	r.HEAD("/", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	// Register routes from each registrar
	for _, registrar := range registrars {
		registrar.RegisterRoutes(r, jwtSecret)
	}

	return r
}