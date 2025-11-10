package http

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	addressDomain "tsb-service/internal/modules/address/domain"
	"tsb-service/internal/modules/order/application"
	"tsb-service/internal/modules/order/domain"
	productDomain "tsb-service/internal/modules/product/domain"
	"tsb-service/pkg/pubsub"
	"tsb-service/services/deliveroo"
)

// DeliverooWebhookHandler handles webhooks from Deliveroo
type DeliverooWebhookHandler struct {
	orderService   application.OrderService
	deliverooSvc   *deliveroo.Service
	webhookHandler *deliveroo.WebhookHandler
	broker         *pubsub.Broker
	addressRepo    addressDomain.AddressRepository
	productRepo    productDomain.ProductRepository
}

// NewDeliverooWebhookHandler creates a new Deliveroo webhook handler
func NewDeliverooWebhookHandler(
	orderService application.OrderService,
	deliverooSvc *deliveroo.Service,
	webhookSecret string,
	broker *pubsub.Broker,
	addressRepo addressDomain.AddressRepository,
	productRepo productDomain.ProductRepository,
) *DeliverooWebhookHandler {
	return &DeliverooWebhookHandler{
		orderService:   orderService,
		deliverooSvc:   deliverooSvc,
		webhookHandler: deliveroo.NewWebhookHandler(webhookSecret),
		broker:         broker,
		addressRepo:    addressRepo,
		productRepo:    productRepo,
	}
}

// HandleOrderEvents handles order event webhooks from Deliveroo
func (h *DeliverooWebhookHandler) HandleOrderEvents(c *gin.Context) {
	// Parse the webhook event
	event, err := h.webhookHandler.ParseOrderEvent(c.Request)
	if err != nil {
		log.Printf("Failed to parse Deliveroo order webhook: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid webhook signature or payload"})
		return
	}

	ctx := context.Background()

	// Handle different event types
	switch event.Event {
	case deliveroo.OrderEventNew:
		h.handleNewOrder(ctx, event.Body.Order)

	case deliveroo.OrderEventStatusUpdate:
		h.handleOrderStatusUpdate(ctx, event.Body.Order)

	default:
		log.Printf("Received unknown Deliveroo order event: %s", event.Event)
	}

	c.JSON(http.StatusOK, gin.H{"status": "received"})
}

// HandleRiderEvents handles rider event webhooks from Deliveroo
func (h *DeliverooWebhookHandler) HandleRiderEvents(c *gin.Context) {
	// Parse the webhook event
	event, err := h.webhookHandler.ParseRiderEvent(c.Request)
	if err != nil {
		log.Printf("Failed to parse Deliveroo rider webhook: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid webhook signature or payload"})
		return
	}

	ctx := context.Background()

	// Publish rider updates to subscribers
	h.handleRiderUpdate(ctx, event.Body.OrderID, event.Body.Riders)

	c.JSON(http.StatusOK, gin.H{"status": "received"})
}

// HandleMenuEvents handles menu event webhooks from Deliveroo
func (h *DeliverooWebhookHandler) HandleMenuEvents(c *gin.Context) {
	// Parse the webhook event
	event, err := h.webhookHandler.ParseMenuEvent(c.Request)
	if err != nil {
		log.Printf("Failed to parse Deliveroo menu webhook: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid webhook signature or payload"})
		return
	}

	// Handle menu upload result
	result := event.Body.MenuUploadResult

	if result.HTTPStatus == 200 {
		log.Printf("✓ Menu upload successful for menu %s (brand: %s)", result.MenuID, result.BrandID)
		log.Printf("  Applied to sites: %v", result.SiteIDs)
	} else {
		log.Printf("✗ Menu upload failed for menu %s (brand: %s)", result.MenuID, result.BrandID)
		log.Printf("  HTTP Status: %d", result.HTTPStatus)

		if len(result.Errors) > 0 {
			log.Println("  Errors:")
			for _, err := range result.Errors {
				if err.Field != nil {
					log.Printf("    - [%s] %s (field: %s)", err.Code, err.Message, *err.Field)
				} else {
					log.Printf("    - [%s] %s", err.Code, err.Message)
				}
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{"status": "received"})
}

// handleNewOrder processes a new order from Deliveroo
func (h *DeliverooWebhookHandler) handleNewOrder(ctx context.Context, deliverooOrder deliveroo.Order) {
	log.Printf("Received new Deliveroo order: %s (Display ID: %s)", deliverooOrder.ID, deliverooOrder.DisplayID)

	// Check if order already exists
	existingOrder, err := h.orderService.GetOrderByPlatformID(ctx, deliverooOrder.ID, domain.OrderSourceDeliveroo)
	if err == nil && existingOrder != nil {
		log.Printf("Order %s already exists, skipping creation", deliverooOrder.ID)
		return
	}

	// Convert Deliveroo order to local order
	order := h.convertDeliverooOrderToLocal(deliverooOrder)

	// Marshal platform data
	platformDataJSON, err := json.Marshal(deliverooOrder)
	if err != nil {
		log.Printf("Failed to marshal platform data: %v", err)
		return
	}
	order.PlatformData = domain.NullableJSON(platformDataJSON)

	// Convert Deliveroo items to order products
	orderProducts := h.convertDeliverooItemsToOrderProducts(ctx, deliverooOrder.Items)
	log.Printf("Mapped %d/%d Deliveroo items to internal products", len(orderProducts), len(deliverooOrder.Items))

	// Create order in database with order products
	createdOrder, err := h.orderService.CreatePlatformOrderWithProducts(ctx, order, orderProducts)
	if err != nil {
		log.Printf("Failed to create platform order: %v", err)
		// Report failure to Deliveroo
		errMsg := fmt.Sprintf("Failed to create order: %v", err)
		go h.reportSyncStatus(deliverooOrder.ID, false, &errMsg)
		return
	}

	log.Printf("Created platform order with ID: %s (%d products)", createdOrder.ID, len(orderProducts))

	// Publish to PubSub for GraphQL subscriptions
	h.broker.Publish("platformOrder:new:DELIVEROO", createdOrder)
	h.broker.Publish(fmt.Sprintf("platformOrder:update:%s", createdOrder.ID), createdOrder)

	// Report success sync status to Deliveroo
	go h.reportSyncStatus(deliverooOrder.ID, true, nil)
}

// handleOrderStatusUpdate processes order status updates from Deliveroo
func (h *DeliverooWebhookHandler) handleOrderStatusUpdate(ctx context.Context, deliverooOrder deliveroo.Order) {
	log.Printf("Received status update for Deliveroo order: %s - Status: %s", deliverooOrder.ID, deliverooOrder.Status)

	// Get existing order
	order, err := h.orderService.GetOrderByPlatformID(ctx, deliverooOrder.ID, domain.OrderSourceDeliveroo)
	if err != nil {
		log.Printf("Order not found for status update: %v", err)
		return
	}

	// Update platform data
	platformDataJSON, err := json.Marshal(deliverooOrder)
	if err != nil {
		log.Printf("Failed to marshal platform data: %v", err)
		return
	}
	order.PlatformData = domain.NullableJSON(platformDataJSON)

	// Map Deliveroo status to local status
	newStatus := domain.MapDeliverooStatusToLocal(string(deliverooOrder.Status))

	// Update order status
	updatedOrder, err := h.orderService.UpdatePlatformOrderStatus(
		ctx,
		deliverooOrder.ID,
		domain.OrderSourceDeliveroo,
		newStatus,
	)
	if err != nil {
		log.Printf("Failed to update order status: %v", err)
		return
	}

	// Update platform data field
	updatedOrder.PlatformData = platformDataJSON

	// Publish update to subscribers
	h.broker.Publish(fmt.Sprintf("platformOrder:update:%s", updatedOrder.ID), updatedOrder)

	log.Printf("Updated order %s to status %s", order.ID, newStatus)
}

// handleRiderUpdate publishes rider updates to subscribers
func (h *DeliverooWebhookHandler) handleRiderUpdate(ctx context.Context, orderID string, riders []deliveroo.RiderInfo) {
	log.Printf("Received rider update for order: %s", orderID)

	// Get order to get our internal ID
	order, err := h.orderService.GetOrderByPlatformID(ctx, orderID, domain.OrderSourceDeliveroo)
	if err != nil {
		log.Printf("Order not found for rider update: %v", err)
		return
	}

	// Create rider update payload
	riderUpdate := map[string]interface{}{
		"orderId": order.ID,
		"riders":  riders,
	}

	// Publish to subscribers
	h.broker.Publish(fmt.Sprintf("platformRider:update:%s", order.ID), riderUpdate)
}

// convertDeliverooOrderToLocal converts a Deliveroo order to local order format
func (h *DeliverooWebhookHandler) convertDeliverooOrderToLocal(deliverooOrder deliveroo.Order) *domain.Order {
	ctx := context.Background()

	// Map order type
	orderType := domain.MapDeliverooFulfillmentToType(string(deliverooOrder.FulfillmentType))

	// Map status
	orderStatus := domain.MapDeliverooStatusToLocal(string(deliverooOrder.Status))

	// Convert total price (Deliveroo uses fractional cents)
	totalPrice := decimal.NewFromInt(int64(deliverooOrder.TotalPrice.Fractional)).Div(decimal.NewFromInt(100))

	// Extract delivery fee if present
	var deliveryFee *decimal.Decimal
	if deliverooOrder.Delivery != nil {
		fee := decimal.NewFromInt(int64(deliverooOrder.Delivery.DeliveryFee.Fractional)).Div(decimal.NewFromInt(100))
		deliveryFee = &fee
	}

	// Calculate discount amount from offer discount
	discountAmount := decimal.NewFromInt(int64(deliverooOrder.OfferDiscount.Fractional)).Div(decimal.NewFromInt(100))

	// Try to find address in database, fallback to address_extra
	var addressID *string
	var addressExtra *string
	if deliverooOrder.Delivery != nil && deliverooOrder.Delivery.Address != nil {
		addr := deliverooOrder.Delivery.Address
		foundAddressID := h.lookupAddress(ctx, addr)

		if foundAddressID != nil {
			addressID = foundAddressID
			log.Printf("Found matching address ID: %s for Deliveroo order %s", *addressID, deliverooOrder.ID)
		} else {
			// No match found, store full address in address_extra
			fullAddress := h.buildAddressString(addr)
			addressExtra = &fullAddress
			log.Printf("No matching address found, storing in address_extra for order %s", deliverooOrder.ID)
		}
	}

	// Extract order note (combine order notes and cutlery notes if present)
	var orderNote *string
	notes := []string{}
	if deliverooOrder.OrderNotes != "" {
		notes = append(notes, deliverooOrder.OrderNotes)
	}
	if deliverooOrder.CutleryNotes != "" {
		notes = append(notes, fmt.Sprintf("Cutlery: %s", deliverooOrder.CutleryNotes))
	}
	if len(notes) > 0 {
		combinedNotes := strings.Join(notes, " | ")
		orderNote = &combinedNotes
	}

	// Set estimated ready time
	var estimatedReadyTime *time.Time
	if !deliverooOrder.PrepareFor.IsZero() {
		estimatedReadyTime = &deliverooOrder.PrepareFor
	}

	platformOrderID := deliverooOrder.ID

	return &domain.Order{
		UserID:             nil, // Platform orders don't have a user initially
		OrderStatus:        orderStatus,
		OrderType:          orderType,
		IsOnlinePayment:    true, // Deliveroo orders are always paid online
		TotalPrice:         totalPrice,
		DiscountAmount:     discountAmount,
		DeliveryFee:        deliveryFee,
		AddressID:          addressID,
		AddressExtra:       addressExtra,
		OrderNote:          orderNote,
		EstimatedReadyTime: estimatedReadyTime,
		Source:             domain.OrderSourceDeliveroo,
		PlatformOrderID:    &platformOrderID,
	}
}

// lookupAddress attempts to find a matching address in the database
// Returns the address ID if found, nil otherwise
func (h *DeliverooWebhookHandler) lookupAddress(ctx context.Context, addr *deliveroo.DeliveryAddress) *string {
	if addr == nil {
		return nil
	}

	// First, search for matching street name
	streets, err := h.addressRepo.SearchStreetNames(ctx, addr.Street)
	if err != nil || len(streets) == 0 {
		log.Printf("No streets found for: %s", addr.Street)
		return nil
	}

	// Try to find exact match by postcode and municipality
	var matchedStreet *addressDomain.Street
	for _, street := range streets {
		if street.Postcode == addr.PostalCode {
			matchedStreet = street
			break
		}
	}

	// If no exact match by postcode, take the first result (best match by similarity)
	if matchedStreet == nil {
		matchedStreet = streets[0]
		log.Printf("Using fuzzy match for street: %s -> %s", addr.Street, matchedStreet.StreetName)
	}

	// Parse box number if present in AddressLine2
	var boxNumber *string
	if addr.AddressLine2 != "" {
		boxNumber = &addr.AddressLine2
	}

	// Get the final address
	finalAddr, err := h.addressRepo.GetFinalAddress(ctx, matchedStreet.ID, addr.Number, boxNumber)
	if err != nil {
		log.Printf("Failed to get final address for street %s, number %s: %v", matchedStreet.ID, addr.Number, err)
		return nil
	}

	return &finalAddr.ID
}

// buildAddressString constructs a full address string from Deliveroo address components
func (h *DeliverooWebhookHandler) buildAddressString(addr *deliveroo.DeliveryAddress) string {
	if addr == nil {
		return ""
	}

	parts := []string{}

	if addr.AddressLine1 != "" {
		parts = append(parts, addr.AddressLine1)
	} else if addr.Street != "" && addr.Number != "" {
		parts = append(parts, fmt.Sprintf("%s %s", addr.Street, addr.Number))
	}

	if addr.AddressLine2 != "" {
		parts = append(parts, addr.AddressLine2)
	}

	if addr.PostalCode != "" && addr.City != "" {
		parts = append(parts, fmt.Sprintf("%s %s", addr.PostalCode, addr.City))
	}

	return strings.Join(parts, ", ")
}

// convertDeliverooItemsToOrderProducts converts Deliveroo order items to internal order products
// Only maps top-level items, modifiers are stored in platform_data
func (h *DeliverooWebhookHandler) convertDeliverooItemsToOrderProducts(ctx context.Context, items []deliveroo.OrderItem) []domain.OrderProductRaw {
	orderProducts := []domain.OrderProductRaw{}

	for _, item := range items {
		// Try to parse the PosItemID as UUID (should match synced product IDs)
		if item.PosItemID == nil || *item.PosItemID == "" {
			log.Printf("Skipping item without PosItemID: %s", item.Name)
			continue
		}

		productID, err := uuid.Parse(*item.PosItemID)
		if err != nil {
			log.Printf("Invalid product UUID for item %s (PosItemID: %s): %v", item.Name, *item.PosItemID, err)
			continue
		}

		// Verify product exists in database
		product, err := h.productRepo.FindByID(ctx, productID)
		if err != nil {
			log.Printf("Product not found for PosItemID %s: %v", *item.PosItemID, err)
			continue
		}

		// Convert prices from fractional (cents) to decimal
		unitPrice := decimal.NewFromInt(int64(item.UnitPrice.Fractional)).Div(decimal.NewFromInt(100))
		totalPrice := decimal.NewFromInt(int64(item.TotalPrice.Fractional)).Div(decimal.NewFromInt(100))

		orderProduct := domain.OrderProductRaw{
			ProductID:  product.ID,
			Quantity:   int64(item.Quantity),
			UnitPrice:  unitPrice,
			TotalPrice: totalPrice,
		}

		orderProducts = append(orderProducts, orderProduct)
		log.Printf("Mapped Deliveroo item '%s' to product ID %s (qty: %d)", item.Name, product.ID, item.Quantity)
	}

	return orderProducts
}

// reportSyncStatus reports sync status back to Deliveroo
func (h *DeliverooWebhookHandler) reportSyncStatus(orderID string, success bool, errorMessage *string) {
	ctx := context.Background()
	adapter := h.deliverooSvc.GetAdapter()

	var status deliveroo.SyncStatus
	var reason *deliveroo.SyncStatusReason
	var notes *string

	if success {
		status = deliveroo.SyncStatusSucceeded
	} else {
		status = deliveroo.SyncStatusFailed
		failureReason := deliveroo.SyncReasonWebhookFailed
		reason = &failureReason
		notes = errorMessage
	}

	req := deliveroo.CreateSyncStatusRequest{
		Status:     status,
		Reason:     reason,
		Notes:      notes,
		OccurredAt: time.Now(),
	}

	err := adapter.CreateSyncStatus(ctx, orderID, req)
	if err != nil {
		log.Printf("Failed to report sync status to Deliveroo: %v", err)
	}
}
