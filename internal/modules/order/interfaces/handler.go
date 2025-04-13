package interfaces

import (
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"log"
	"net/http"
	"sort"
	"strconv"
	addressApplication "tsb-service/internal/modules/address/application"
	"tsb-service/internal/modules/order/application"
	"tsb-service/internal/modules/order/domain"
	paymentApplication "tsb-service/internal/modules/payment/application"
	paymentDomain "tsb-service/internal/modules/payment/domain"
	productApplication "tsb-service/internal/modules/product/application"
	productDomain "tsb-service/internal/modules/product/domain"
	userDomain "tsb-service/internal/modules/user/domain"
	"tsb-service/pkg/utils"
	es "tsb-service/services/email/scaleway"
)

type OrderHandler struct {
	service        application.OrderService
	productService productApplication.ProductService
	paymentService paymentApplication.PaymentService
	addressService addressApplication.AddressService
}

func NewOrderHandler(
	service application.OrderService,
	productService productApplication.ProductService,
	paymentService paymentApplication.PaymentService,
	addressService addressApplication.AddressService,
) *OrderHandler {
	return &OrderHandler{service: service, productService: productService, paymentService: paymentService, addressService: addressService}
}

func (h *OrderHandler) CreateOrderHandler(c *gin.Context) {
	var req CreateOrderRequest
	var orderResponse OrderResponse

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
	orderProductsPricing := make([]domain.OrderProductRaw, 0, len(req.OrderProducts))
	var computedOrderTotal = decimal.NewFromInt(0)

	for _, item := range req.OrderProducts {
		unitPrice, ok := prices[item.ProductID]
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("product %s not found", item.ProductID)})
			return
		}
		totalPrice := unitPrice.Mul(decimal.NewFromInt(item.Quantity))
		computedOrderTotal = computedOrderTotal.Add(totalPrice)

		orderProductsPricing = append(orderProductsPricing, domain.OrderProductRaw{
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

	deliveryFee := decimal.NewFromInt(0)
	if req.OrderType == domain.OrderTypeDelivery {
		address, err := h.addressService.GetAddressByID(c.Request.Context(), *req.AddressID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to retrieve address"})
			return
		}

		if address == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "address not found"})
			return
		}

		orderResponse.Address = address

		// Calculate the delivery fees
		var fee int64
		switch {
		case address.Distance < 4000:
			fee = 0
		case address.Distance < 5000:
			fee = 1
		case address.Distance < 6000:
			fee = 2
		case address.Distance < 7000:
			fee = 3
		case address.Distance < 8000:
			fee = 4
		case address.Distance < 9000:
			fee = 5
		default:
			fee = 0
		}
		deliveryFee = decimal.NewFromInt(fee)
		computedOrderTotal = computedOrderTotal.Add(deliveryFee)
	}

	// Build the domain order object.
	tempOrder := domain.NewOrder(
		userID,
		req.OrderType,
		req.IsOnlinePayment,
		req.AddressID,
		req.AddressExtra,
		req.OrderNote,
		req.OrderExtra,
		&deliveryFee,
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

	orderProductsResponse := make([]domain.OrderProduct, len(*orderProducts))

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

	orderResponse.Order = *order
	orderResponse.OrderProducts = orderProductsResponse

	// Handle payment creation if needed.
	if req.IsOnlinePayment {
		molliePayment, err := h.paymentService.CreatePayment(c.Request.Context(), *order, orderProductsResponse)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"failed to create payment": err.Error()})
			return
		}

		if molliePayment == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "payment is nil"})
			return
		}

		orderResponse.MolliePayment = &MolliePayment{
			ID:        molliePayment.ID,
			OrderID:   order.ID,
			Status:    molliePayment.Status,
			CreatedAt: molliePayment.CreatedAt,
			PaidAt:    molliePayment.PaidAt,
		}
		var parsedLinks paymentDomain.PaymentLinks
		if err := json.Unmarshal(molliePayment.Links, &parsedLinks); err == nil {
			orderResponse.MolliePayment.PaymentURL = parsedLinks.Checkout.Href
		}
	}

	go func() {
		// FOR TEST
		user := userDomain.User{
			FirstName: "Bruno",
			LastName:  "Moyse",
			Email:     "moyse94@gmail.com",
		}
		err = es.SendOrderPendingEmail(user, utils.GetLang(c.Request.Context()), orderResponse.Order, orderResponse.OrderProducts)
		if err != nil {
			log.Printf("failed to send order pending email: %v", err)
		}
	}()

	c.JSON(http.StatusOK, orderResponse)
}

func (h *OrderHandler) GetOrderHandler(c *gin.Context) {
	// 1. Parse order ID from URL param.
	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid order ID"})
		return
	}

	// 2. Retrieve the logged-in user.
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

	// 3. Fetch the order and related products.
	order, orderProducts, err := h.service.GetOrderByID(c.Request.Context(), orderID)
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

	// 4. Validate ownership.
	if order.UserID != userID {
		c.JSON(http.StatusForbidden, gin.H{"error": "access denied"})
		return
	}

	// 5. Load product details for the products in the order.
	productIDs := make([]string, len(*orderProducts))
	for i, op := range *orderProducts {
		productIDs[i] = op.ProductID.String()
	}

	products, err := h.productService.GetProductsByIDs(c.Request.Context(), productIDs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to retrieve products"})
		return
	}

	// Build a lookup map: productID -> product details.
	productMap := make(map[uuid.UUID]productDomain.ProductOrderDetails, len(products))
	for _, p := range products {
		productMap[p.ID] = *p
	}

	// 6. Enrich order products with product details.
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

	// 7. Construct a response with order + enriched products.
	orderResponse := OrderResponse{
		Order:         *order,
		OrderProducts: orderProductsResponse,
	}

	// Optionally fetch payment info if it's an online payment.
	if order.IsOnlinePayment {
		molliePayment, err := h.paymentService.GetPaymentByOrderID(c.Request.Context(), order.ID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to retrieve payment"})
			return
		}
		if molliePayment == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "payment not found"})
			return
		}

		// Populate the Mollie payment details in the response.
		orderResponse.MolliePayment = &MolliePayment{
			ID:        molliePayment.ID,
			OrderID:   order.ID,
			Status:    molliePayment.Status,
			CreatedAt: molliePayment.CreatedAt,
			PaidAt:    molliePayment.PaidAt,
		}
		var parsedLinks paymentDomain.PaymentLinks
		if err := json.Unmarshal(molliePayment.Links, &parsedLinks); err == nil {
			orderResponse.MolliePayment.PaymentURL = parsedLinks.Checkout.Href
		}
	}

	if order.OrderType == domain.OrderTypeDelivery {
		address, err := h.addressService.GetAddressByID(c.Request.Context(), *order.AddressID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to retrieve address"})
			return
		}

		if address == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "address not found"})
			return
		}

		orderResponse.Address = address
	}

	// 8. Return the final OrderResponse.
	c.JSON(http.StatusOK, orderResponse)
}

func (h *OrderHandler) GetUserOrdersHandler(c *gin.Context) {
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

	// 1) Parse the logged-in user
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

	// 2) Get all orders for the user in a single query
	orders, err := h.service.GetPaginatedOrders(c.Request.Context(), page, pageSize, &userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to retrieve orders"})
		return
	}

	// If no orders found, return an empty array
	if len(orders) == 0 {
		c.JSON(http.StatusOK, []OrderResponse{})
		return
	}

	// 3) Gather all order IDs
	var orderIDs []uuid.UUID
	for _, o := range orders {
		orderIDs = append(orderIDs, o.ID)
	}

	// 4) Fetch all order products (raw) in a single query
	productsByOrder, err := h.service.GetOrderProductsByOrderIDs(c.Request.Context(), orderIDs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to retrieve order products"})
		return
	}

	// 5) Collect all unique product IDs across all orders
	uniqueProductIDs := make(map[uuid.UUID]struct{})
	for _, opList := range productsByOrder {
		for _, op := range opList {
			uniqueProductIDs[op.ProductID] = struct{}{}
		}
	}

	// Convert set to slice of strings (your productService expects []string)
	var productIDStrs []string
	for pid := range uniqueProductIDs {
		productIDStrs = append(productIDStrs, pid.String())
	}

	// 6) Fetch product details in one query
	allProducts, err := h.productService.GetProductsByIDs(c.Request.Context(), productIDStrs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to retrieve products"})
		return
	}

	// Build a lookup map: productID -> product details
	productMap := make(map[uuid.UUID]productDomain.ProductOrderDetails, len(allProducts))
	for _, p := range allProducts {
		productMap[p.ID] = *p
	}

	// 7) Build responses in memory
	responses := make([]OrderResponse, 0, len(orders))
	for _, ord := range orders {
		// a) Enrich the raw order products with product details
		opList := productsByOrder[ord.ID] // slice of OrderProductRaw
		enrichedOP := make([]domain.OrderProduct, len(opList))

		for i, rawOP := range opList {
			prodDetails, ok := productMap[rawOP.ProductID]
			if !ok {
				c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("product %s not found", rawOP.ProductID)})
				return
			}

			// Build the final domain.OrderProduct with product details
			enrichedOP[i] = domain.OrderProduct{
				Product: domain.Product{
					ID:           prodDetails.ID,
					Code:         prodDetails.Code,
					CategoryName: prodDetails.CategoryName,
					Name:         prodDetails.Name,
				},
				Quantity:   rawOP.Quantity,
				UnitPrice:  rawOP.UnitPrice,
				TotalPrice: rawOP.TotalPrice,
			}
		}

		// Sort the enrichedOP by code.
		// Code is domainOrderProduct.Product.Code (a *string).
		sort.Slice(enrichedOP, func(i, j int) bool {
			alphaI, numI := utils.ParseCode(enrichedOP[i].Product.Code)
			alphaJ, numJ := utils.ParseCode(enrichedOP[j].Product.Code)

			if alphaI != alphaJ {
				return alphaI < alphaJ
			}
			return numI < numJ
		})

		// b) Construct a single OrderResponse
		orderResp := OrderResponse{
			Order:         *ord,       // domain.Order
			OrderProducts: enrichedOP, // []domain.OrderProduct
		}

		// c) If online payment, fetch Mollie payment info
		if ord.IsOnlinePayment {
			molliePayment, err := h.paymentService.GetPaymentByOrderID(c.Request.Context(), ord.ID)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to retrieve payment"})
				return
			}
			if molliePayment != nil {
				// Minimal example of building a MolliePayment for the response
				mp := &MolliePayment{
					ID:        molliePayment.ID,
					OrderID:   ord.ID,
					Status:    molliePayment.Status,
					CreatedAt: molliePayment.CreatedAt,
					PaidAt:    molliePayment.PaidAt,
				}
				// Optionally parse links to get PaymentURL
				var parsedLinks paymentDomain.PaymentLinks
				if err := json.Unmarshal(molliePayment.Links, &parsedLinks); err == nil {
					mp.PaymentURL = parsedLinks.Checkout.Href
				}
				orderResp.MolliePayment = mp
			}
		}

		if ord.OrderType == domain.OrderTypeDelivery {
			address, err := h.addressService.GetAddressByID(c.Request.Context(), *ord.AddressID)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to retrieve address"})
				return
			}

			if address == nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "address not found"})
				return
			}

			orderResp.Address = address
		}

		responses = append(responses, orderResp)
	}

	// 8) Return the slice of OrderResponse
	c.JSON(http.StatusOK, responses)
}

func (h *OrderHandler) GetAdminOrdersHandler(c *gin.Context) {
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

	// 2) Get all orders for the user in a single query
	orders, err := h.service.GetPaginatedOrders(c.Request.Context(), page, pageSize, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to retrieve orders"})
		return
	}

	// If no orders found, return an empty array
	if len(orders) == 0 {
		c.JSON(http.StatusOK, []OrderResponse{})
		return
	}

	// 3) Gather all order IDs
	var orderIDs []uuid.UUID
	for _, o := range orders {
		orderIDs = append(orderIDs, o.ID)
	}

	// 4) Fetch all order products (raw) in a single query
	productsByOrder, err := h.service.GetOrderProductsByOrderIDs(c.Request.Context(), orderIDs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to retrieve order products"})
		return
	}

	// 5) Collect all unique product IDs across all orders
	uniqueProductIDs := make(map[uuid.UUID]struct{})
	for _, opList := range productsByOrder {
		for _, op := range opList {
			uniqueProductIDs[op.ProductID] = struct{}{}
		}
	}

	// Convert set to slice of strings (your productService expects []string)
	var productIDStrs []string
	for pid := range uniqueProductIDs {
		productIDStrs = append(productIDStrs, pid.String())
	}

	// 6) Fetch product details in one query
	allProducts, err := h.productService.GetProductsByIDs(c.Request.Context(), productIDStrs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to retrieve products"})
		return
	}

	// Build a lookup map: productID -> product details
	productMap := make(map[uuid.UUID]productDomain.ProductOrderDetails, len(allProducts))
	for _, p := range allProducts {
		productMap[p.ID] = *p
	}

	// 7) Build responses in memory
	responses := make([]OrderResponse, 0, len(orders))
	for _, ord := range orders {
		// a) Enrich the raw order products with product details
		opList := productsByOrder[ord.ID] // slice of OrderProductRaw
		enrichedOP := make([]domain.OrderProduct, len(opList))

		for i, rawOP := range opList {
			prodDetails, ok := productMap[rawOP.ProductID]
			if !ok {
				c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("product %s not found", rawOP.ProductID)})
				return
			}

			// Build the final domain.OrderProduct with product details
			enrichedOP[i] = domain.OrderProduct{
				Product: domain.Product{
					ID:           prodDetails.ID,
					Code:         prodDetails.Code,
					CategoryName: prodDetails.CategoryName,
					Name:         prodDetails.Name,
				},
				Quantity:   rawOP.Quantity,
				UnitPrice:  rawOP.UnitPrice,
				TotalPrice: rawOP.TotalPrice,
			}
		}

		// Sort the enrichedOP by code.
		// Code is domainOrderProduct.Product.Code (a *string).
		sort.Slice(enrichedOP, func(i, j int) bool {
			alphaI, numI := utils.ParseCode(enrichedOP[i].Product.Code)
			alphaJ, numJ := utils.ParseCode(enrichedOP[j].Product.Code)

			if alphaI != alphaJ {
				return alphaI < alphaJ
			}
			return numI < numJ
		})

		// b) Construct a single OrderResponse
		orderResp := OrderResponse{
			Order:         *ord,       // domain.Order
			OrderProducts: enrichedOP, // []domain.OrderProduct
		}

		// c) If online payment, fetch Mollie payment info
		if ord.IsOnlinePayment {
			molliePayment, err := h.paymentService.GetPaymentByOrderID(c.Request.Context(), ord.ID)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to retrieve payment"})
				return
			}
			if molliePayment != nil {
				// Minimal example of building a MolliePayment for the response
				mp := &MolliePayment{
					ID:        molliePayment.ID,
					OrderID:   ord.ID,
					Status:    molliePayment.Status,
					CreatedAt: molliePayment.CreatedAt,
					PaidAt:    molliePayment.PaidAt,
				}
				// Optionally parse links to get PaymentURL
				var parsedLinks paymentDomain.PaymentLinks
				if err := json.Unmarshal(molliePayment.Links, &parsedLinks); err == nil {
					mp.PaymentURL = parsedLinks.Checkout.Href
				}
				orderResp.MolliePayment = mp
			}
		}

		if ord.OrderType == domain.OrderTypeDelivery {
			address, err := h.addressService.GetAddressByID(c.Request.Context(), *ord.AddressID)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to retrieve address"})
				return
			}

			if address == nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "address not found"})
				return
			}

			orderResp.Address = address
		}

		responses = append(responses, orderResp)
	}

	// 8) Return the slice of OrderResponse
	c.JSON(http.StatusOK, responses)
}

func (h *OrderHandler) UpdateOrderStatusHandler(c *gin.Context) {
	// 1. Parse order ID from URL param.
	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid order ID"})
		return
	}

	// 2. Parse new status from request body.
	var req UpdateOrderRequest
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request payload"})
		return
	}

	// 3. Update the order status.
	err = h.service.UpdateOrderStatus(c.Request.Context(), orderID, req.OrderStatus)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update order status"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "order status updated successfully"})
}

func (h *OrderHandler) GetAdminOrderHandler(c *gin.Context) {
	// 1. Parse order ID from URL param.
	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid order ID"})
		return
	}

	// 2. Fetch the order and related products.
	order, orderProducts, err := h.service.GetOrderByID(c.Request.Context(), orderID)
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

	products, err := h.productService.GetProductsByIDs(c.Request.Context(), productIDs)
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

	// Sort the enrichedOP by code.
	// Code is domainOrderProduct.Product.Code (a *string).
	sort.Slice(orderProductsResponse, func(i, j int) bool {
		alphaI, numI := utils.ParseCode(orderProductsResponse[i].Product.Code)
		alphaJ, numJ := utils.ParseCode(orderProductsResponse[j].Product.Code)

		if alphaI != alphaJ {
			return alphaI < alphaJ
		}
		return numI < numJ
	})

	// 5. Construct a response with order + enriched products.
	orderResponse := OrderResponse{
		Order:         *order,
		OrderProducts: orderProductsResponse,
	}

	// Optionally fetch payment info if it's an online payment.
	if order.IsOnlinePayment {
		molliePayment, err := h.paymentService.GetPaymentByOrderID(c.Request.Context(), order.ID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to retrieve payment"})
			return
		}
		if molliePayment == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "payment not found"})
			return
		}

		// Populate the Mollie payment details in the response.
		orderResponse.MolliePayment = &MolliePayment{
			ID:        molliePayment.ID,
			OrderID:   order.ID,
			Status:    molliePayment.Status,
			CreatedAt: molliePayment.CreatedAt,
			PaidAt:    molliePayment.PaidAt,
		}
		var parsedLinks paymentDomain.PaymentLinks
		if err := json.Unmarshal(molliePayment.Links, &parsedLinks); err == nil {
			orderResponse.MolliePayment.PaymentURL = parsedLinks.Checkout.Href
		}
	}

	if order.OrderType == domain.OrderTypeDelivery {
		address, err := h.addressService.GetAddressByID(c.Request.Context(), *order.AddressID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to retrieve address"})
			return
		}

		if address == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "address not found"})
			return
		}

		orderResponse.Address = address
	}

	// 6. Return the final OrderResponse.
	c.JSON(http.StatusOK, orderResponse)
}
