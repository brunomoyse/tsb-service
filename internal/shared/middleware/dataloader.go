package middleware

import (
	"github.com/gin-gonic/gin"
	addressApplication "tsb-service/internal/modules/address/application"
	orderApplication "tsb-service/internal/modules/order/application"
	paymentApplication "tsb-service/internal/modules/payment/application"
	productApplication "tsb-service/internal/modules/product/application"
	userApplication "tsb-service/internal/modules/user/application"
)

func DataLoaderMiddleware(
	ads addressApplication.AddressService,
	ors orderApplication.OrderService,
	pas paymentApplication.PaymentService,
	prs productApplication.ProductService,
	uss userApplication.UserService,
) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()

		// Attach DataLoaders into the context, passing in the actual ProductService
		ctx = addressApplication.AttachDataLoaders(ctx, ads)
		ctx = productApplication.AttachDataLoaders(ctx, prs)
		ctx = paymentApplication.AttachDataLoaders(ctx, pas)
		ctx = orderApplication.AttachDataLoaders(ctx, ors)
		ctx = userApplication.AttachDataLoaders(ctx, uss)

		// Update the request with the new context
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}
