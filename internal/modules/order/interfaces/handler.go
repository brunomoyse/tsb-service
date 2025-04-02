package interfaces

import (
	"encoding/json"
	"fmt"
	"github.com/shopspring/decimal"
	"net/http"
	"strconv"
	"tsb-service/internal/modules/order/application"
	"tsb-service/internal/modules/order/domain"
	productApplication "tsb-service/internal/modules/product/application"
	productDomain "tsb-service/internal/modules/product/domain"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type OrderHandler struct {
	service        application.OrderService
	productService productApplication.ProductService
}

func NewOrderHandler(service application.OrderService, productService productApplication.ProductService) *OrderHandler {
	return &OrderHandler{service: service, productService: productService}
}

func (h *OrderHandler) CreateOrderHandler(c *gin.Context) {
	var req CreateOrderRequest
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"invalid request payload": err.Error()})
		return
	}

	// Retrieve the logged-in user's ID from the Gin context.
	userIDStr := c.GetString("userID")
	if userIDStr == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user not authenticated"})
		return
	}

	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "invalid user ID"})
		return
	}

	// Collect the product IDs from the request.
	productIDs := make([]string, 0, len(req.OrderProducts))
	for _, item := range req.OrderProducts {
		productIDs = append(productIDs, item.ProductID.String())
	}

	// Retrieve products from the product service.
	products, err := h.productService.GetProductsByIDs(c, productIDs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to retrieve products"})
		return
	}

	// Build a lookup map of product ID to unit price.
	prices := make(map[uuid.UUID]decimal.Decimal, len(products))
	for _, p := range products {
		prices[p.ID] = p.Price
	}

	// Enrich order products with pricing details.
	orderProductsPricing := make([]domain.OrderProduct, 0, len(req.OrderProducts))
	var computedOrderTotal = decimal.NewFromInt(0)

	for _, item := range req.OrderProducts {
		unitPrice, ok := prices[item.ProductID]
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("product %s not found", item.ProductID)})
			return
		}
		totalPrice := unitPrice.Mul(decimal.NewFromInt(item.Quantity))
		computedOrderTotal = computedOrderTotal.Add(totalPrice)

		orderProductsPricing = append(orderProductsPricing, domain.OrderProduct{
			ProductID:  item.ProductID,
			Quantity:   item.Quantity,
			UnitPrice:  unitPrice,
			TotalPrice: totalPrice,
		})
	}

	// If computedOrderTotal is less than 20 and OrderType is PICKUP, return an error.
	if computedOrderTotal.LessThan(decimal.NewFromInt(20)) && req.OrderType == domain.OrderTypePickUp {
		c.JSON(http.StatusBadRequest, gin.H{"error": "minimum order amount for pickup is 20"})
		return
	}

	// If computedOrderTotal is less than 25 and OrderType is DELIVERY, return an error.
	if computedOrderTotal.LessThan(decimal.NewFromInt(25)) && req.OrderType == domain.OrderTypeDelivery {
		c.JSON(http.StatusBadRequest, gin.H{"error": "minimum order amount for delivery is 25"})
		return
	}

	// Build the domain order object.
	tempOrder := domain.NewOrder(
		userID,
		req.OrderType,
		req.IsOnlinePayment,
		req.AddressID,
		req.AddressExtra,
		req.ExtraComment,
		req.OrderExtra,
	)

	// Perform the order creation.
	order, orderProducts, err := h.service.CreateOrder(
		c.Request.Context(),
		tempOrder,
		&orderProductsPricing,
	)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"failed to create order": err.Error()})
		return
	}

	if order == nil || orderProducts == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "order or order products are nil"})
		return
	}

	orderProductsResponse := make([]OrderProductResponse, len(*orderProducts))

	// Build a lookup map: productID -> product details.
	productMap := make(map[uuid.UUID]productDomain.ProductOrderDetails, len(products))
	for _, p := range products {
		productMap[p.ID] = *p
	}

	// Populate the OrderProductResponse slice with detailed product info.
	for i, op := range *orderProducts {
		// Retrieve the product details from the map.
		prod, ok := productMap[op.ProductID]
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("product %s not found", op.ProductID)})
			return
		}

		orderProductsResponse[i] = OrderProductResponse{
			Product: ProductResponse{
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

	orderResponse := OrderResponse{
		Order:         *order,
		OrderProducts: orderProductsResponse,
	}

	// Handle payment creation if needed.
	/*
		if req.IsOnlinePayment {
			molliePayment, err := h.paymentService.CreatePayment(c.Request.Context(), order)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"failed to create payment": err.Error()})
				return
			}

			if molliePayment == nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "payment is nil"})
				return
			}

			orderResponse.MolliePayment = &molliePayment
		}
	*/

	c.JSON(http.StatusOK, orderResponse)
}

func (h *OrderHandler) GetOrderHandler(c *gin.Context) {
	orderIDStr := c.Param("id")
	orderID, err := uuid.Parse(orderIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid order ID"})
		return
	}

	order, err := h.service.GetOrderByID(c.Request.Context(), orderID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to retrieve order"})
		return
	}

	// Check if the order belongs to the logged-in user.
	userIDStr := c.GetString("userID")
	if userIDStr == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user not authenticated"})
		return
	}

	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "invalid user ID"})
		return
	}

	if order.UserID != userID {
		c.JSON(http.StatusForbidden, gin.H{"error": "access denied"})
		return
	}

	c.JSON(http.StatusOK, order)
}

// GetUserOrdersHandler handles GET requests to retrieve orders for the logged-in user.
func (h *OrderHandler) GetUserOrdersHandler(c *gin.Context) {
	// Retrieve the logged-in user's ID from the Gin context.
	userIDStr := c.GetString("userID")
	if userIDStr == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "handler: user not authenticated"})
		return
	}

	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "invalid user ID"})
		return
	}

	// Call the order service to fetch orders for this user.
	orders, err := h.service.GetOrdersByUserID(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to retrieve orders"})
		return
	}

	// Return the orders as JSON.
	c.JSON(http.StatusOK, orders)
}

func (h *OrderHandler) GetAdminPaginatedOrdersHandler(c *gin.Context) {
	pageStr := c.DefaultQuery("page", "1")
	pageSizeStr := c.DefaultQuery("page_size", "20")

	page, err := strconv.Atoi(pageStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid page number"})
		return
	}

	pageSize, err := strconv.Atoi(pageSizeStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid page size"})
		return
	}

	orders, err := h.service.GetPaginatedOrders(c.Request.Context(), page, pageSize)
	if err != nil {
		// Log the error.
		fmt.Println(err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to retrieve orders"})
		return
	}

	c.JSON(http.StatusOK, orders)
}

func (h *OrderHandler) UpdateOrderStatusHandler(c *gin.Context) {
	//
}

func (h *OrderHandler) GetAdminOrderHandler(c *gin.Context) {
	orderIDStr := c.Param("id")
	orderID, err := uuid.Parse(orderIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid order ID"})
		return
	}

	order, err := h.service.GetOrderByID(c.Request.Context(), orderID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to retrieve order"})
		return
	}

	c.JSON(http.StatusOK, order)
}
