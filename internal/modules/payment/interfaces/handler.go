package interfaces

import (
	"context"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"log"
	"net/http"
	orderApplication "tsb-service/internal/modules/order/application"
	"tsb-service/internal/modules/order/domain"
	paymentApplication "tsb-service/internal/modules/payment/application"
	productApplication "tsb-service/internal/modules/product/application"
	productDomain "tsb-service/internal/modules/product/domain"
	userApplication "tsb-service/internal/modules/user/application"
	es "tsb-service/services/email/scaleway"
)

type PaymentHandler struct {
	service        paymentApplication.PaymentService
	orderService   orderApplication.OrderService
	userService    userApplication.UserService
	productService productApplication.ProductService
}

func NewPaymentHandler(
	service paymentApplication.PaymentService,
	orderService orderApplication.OrderService,
	userService userApplication.UserService,
	productService productApplication.ProductService,
) *PaymentHandler {
	return &PaymentHandler{
		service:        service,
		orderService:   orderService,
		userService:    userService,
		productService: productService,
	}
}

func (h *PaymentHandler) UpdatePaymentStatusHandler(c *gin.Context) {
	var req struct {
		ExternalMolliePaymentID string `form:"id" binding:"required"`
	}

	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request payload"})
		return
	}

	err := h.service.UpdatePaymentStatus(c, req.ExternalMolliePaymentID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update payment status"})
		return
	}

	go func() {
		_, externalPayment, err := h.service.GetExternalPaymentByID(context.Background(), req.ExternalMolliePaymentID)
		if err != nil {
			log.Printf("failed to retrieve external payment: %v", err)
			return
		}

		if externalPayment.Status == "paid" {
			payment, err := h.service.GetPaymentByExternalID(context.Background(), req.ExternalMolliePaymentID)
			if err != nil {
				log.Printf("failed to retrieve payment: %v", err)
				return
			}
			order, orderProducts, err := h.orderService.GetOrderByID(context.Background(), payment.OrderID)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to retrieve order"})
				return
			}
			if order == nil {
				// If you consider "not found" a 404
				c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
				return
			}
			if orderProducts == nil {
				// or handle the case if order was found but no products
				c.JSON(http.StatusNotFound, gin.H{"error": "no order products found"})
				return
			}

			// 3. Load product details for the products in the order.
			productIDs := make([]string, len(*orderProducts))
			for i, op := range *orderProducts {
				productIDs[i] = op.ProductID.String()
			}

			products, err := h.productService.GetProductsByIDs(context.Background(), productIDs)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to retrieve products"})
				return
			}

			// Build a lookup map: productID -> product details.
			productMap := make(map[uuid.UUID]productDomain.ProductOrderDetails, len(products))
			for _, p := range products {
				productMap[p.ID] = *p
			}

			// 4. Enrich order products with product details.
			orderProductsResponse := make([]domain.OrderProduct, len(*orderProducts))
			for i, op := range *orderProducts {
				prod, ok := productMap[op.ProductID]
				if !ok {
					c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("product %s not found", op.ProductID)})
					return
				}
				orderProductsResponse[i] = domain.OrderProduct{
					Product: domain.Product{
						ID:           prod.ID,
						Code:         prod.Code,
						CategoryName: prod.CategoryName,
						Name:         prod.Name,
					},
					Quantity:   op.Quantity,
					UnitPrice:  op.UnitPrice,
					TotalPrice: op.TotalPrice,
				}
			}

			u, err := h.userService.GetUserByID(context.Background(), order.UserID.String())

			if err != nil {
				log.Printf("failed to retrieve user: %v", err)
				return
			}
			err = es.SendOrderPendingEmail(*u, "fr", *order, orderProductsResponse)
			if err != nil {
				log.Printf("failed to send order pending email: %v", err)
			}
		}
	}()

	c.JSON(http.StatusOK, gin.H{"message": "payment status updated successfully"})
}
