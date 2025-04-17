package middleware

import (
	"github.com/gin-gonic/gin"
	orderApplication "tsb-service/internal/modules/order/application"
	productApplication "tsb-service/internal/modules/product/application"
	userApplication "tsb-service/internal/modules/user/application"
)

func DataLoaderMiddleware(
	os orderApplication.OrderService,
	ps productApplication.ProductService,
	us userApplication.UserService,
) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()

		// Attach DataLoaders into the context, passing in the actual ProductService
		ctx = productApplication.AttachDataLoaders(ctx, ps)
		ctx = orderApplication.AttachDataLoaders(ctx, os)
		// ctx = userApplication.AttachDataLoaders(ctx, us)

		// Update the request with the new context
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}
