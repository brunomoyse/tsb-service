package middleware

import (
	"github.com/gin-gonic/gin"
	productApplication "tsb-service/internal/modules/product/application"
)

func DataLoaderMiddleware(ps productApplication.ProductService) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()

		// Attach DataLoaders into the context, passing in the actual ProductService
		ctx = productApplication.AttachDataLoaders(ctx, ps)

		// Update the request with the new context
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}
