package interfaces

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"

	orderApplication "tsb-service/internal/modules/order/application"
	"tsb-service/internal/modules/order/domain"
	productApplication "tsb-service/internal/modules/product/application"
	userApplication "tsb-service/internal/modules/user/application"
	"tsb-service/pkg/invoice"
	"tsb-service/pkg/logging"
	"tsb-service/pkg/utils"
)

type OrderHandler struct {
	orderService   orderApplication.OrderService
	userService    userApplication.UserService
	productService productApplication.ProductService
}

func NewOrderHandler(
	orderService orderApplication.OrderService,
	userService userApplication.UserService,
	productService productApplication.ProductService,
) *OrderHandler {
	return &OrderHandler{
		orderService:   orderService,
		userService:    userService,
		productService: productService,
	}
}

func (h *OrderHandler) DownloadInvoice(c *gin.Context) {
	ctx := c.Request.Context()
	log := logging.FromContext(ctx)

	// 1. Get authenticated user
	userID := utils.GetUserID(ctx)
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	// 2. Parse order ID
	orderIDStr := c.Param("id")
	orderID, err := uuid.Parse(orderIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid order ID"})
		return
	}

	// 3. Fetch order
	order, orderProducts, err := h.orderService.GetOrderByID(ctx, orderID)
	if err != nil {
		log.Error("invoice: failed to fetch order", zap.String("order_id", orderIDStr), zap.Error(err))
		c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
		return
	}
	if order == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
		return
	}

	// 4. Authorization: only the order owner can download
	if order.UserID.String() != userID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	// 5. Status gate: only completed orders
	if order.OrderStatus != domain.OrderStatusDelivered && order.OrderStatus != domain.OrderStatusPickedUp {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invoice is only available for completed orders"})
		return
	}

	// 6. Set language to order's language for product name translation
	ctx = utils.SetLang(ctx, order.Language)

	// 7. Fetch product names (without availability check)
	productIDs := make([]string, len(*orderProducts))
	for i, op := range *orderProducts {
		productIDs[i] = op.ProductID.String()
	}
	products, err := h.productService.GetProductNamesForInvoice(ctx, productIDs)
	if err != nil {
		log.Error("invoice: failed to fetch products", zap.String("order_id", orderIDStr), zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate invoice"})
		return
	}
	productMap := make(map[uuid.UUID]struct {
		Name string
		Code string
	}, len(products))
	for _, p := range products {
		code := ""
		if p.Code != nil {
			code = *p.Code
		}
		productMap[p.ID] = struct {
			Name string
			Code string
		}{Name: p.Name, Code: code}
	}

	// 8. Build invoice items with choice names
	items := make([]invoice.InvoiceItem, 0, len(*orderProducts))
	for _, op := range *orderProducts {
		prod := productMap[op.ProductID]
		name := prod.Name

		// Append choice name if present
		if op.ProductChoiceID != nil {
			choice, choiceErr := h.productService.GetChoiceByID(ctx, *op.ProductChoiceID)
			if choiceErr == nil && choice != nil {
				choiceName := choice.GetTranslationFor(order.Language)
				if choiceName != "" {
					name += " — " + choiceName
				}
			}
		}

		items = append(items, invoice.InvoiceItem{
			Name:      name,
			Code:      prod.Code,
			Quantity:  op.Quantity,
			UnitPrice: utils.FormatDecimal(op.UnitPrice),
			LineTotal: utils.FormatDecimal(op.TotalPrice),
		})
	}

	// 9. Fetch customer
	user, err := h.userService.GetUserByID(ctx, order.UserID.String())
	if err != nil {
		log.Error("invoice: failed to fetch user", zap.String("order_id", orderIDStr), zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate invoice"})
		return
	}

	// 10. Compute subtotal from line items (sum of item totals before discounts/fees)
	itemsSubtotal := decimal.Zero
	for _, op := range *orderProducts {
		itemsSubtotal = itemsSubtotal.Add(op.TotalPrice)
	}

	// Use order.TotalPrice if available, otherwise compute from items
	totalPrice := order.TotalPrice
	if totalPrice.IsZero() && !itemsSubtotal.IsZero() {
		totalPrice = itemsSubtotal.
			Sub(order.TakeawayDiscount).
			Sub(order.CouponDiscount)
		if order.DeliveryFee != nil {
			totalPrice = totalPrice.Add(*order.DeliveryFee)
		}
	}

	// If both are zero but items have unit prices, compute from unit_price * quantity
	if itemsSubtotal.IsZero() {
		for _, op := range *orderProducts {
			itemsSubtotal = itemsSubtotal.Add(op.UnitPrice.Mul(decimal.NewFromInt(op.Quantity)))
		}
		if !itemsSubtotal.IsZero() {
			totalPrice = itemsSubtotal.
				Sub(order.TakeawayDiscount).
				Sub(order.CouponDiscount)
			if order.DeliveryFee != nil {
				totalPrice = totalPrice.Add(*order.DeliveryFee)
			}
		}
	}

	// Guard: refuse to generate an invoice with zero totals
	if totalPrice.IsZero() || itemsSubtotal.IsZero() {
		log.Error("invoice: computed totals are zero, cannot generate",
			zap.String("order_id", orderIDStr),
			zap.String("order_total_price", order.TotalPrice.String()),
			zap.String("items_subtotal", itemsSubtotal.String()),
			zap.Int("item_count", len(*orderProducts)),
		)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "invoice data incomplete"})
		return
	}

	// 11. Build invoice data
	data := invoice.InvoiceData{
		CustomerName:  user.FirstName + " " + user.LastName,
		CustomerEmail: user.Email,
		CustomerPhone: user.PhoneNumber,
		OrderID:       order.ID.String(),
		OrderDate:     order.CreatedAt,
		OrderType:     string(order.OrderType),
		Language:      order.Language,
		Items:         items,
		Subtotal:      utils.FormatDecimal(itemsSubtotal),
		Total:         utils.FormatDecimal(totalPrice),
	}

	if !order.TakeawayDiscount.IsZero() {
		d := utils.FormatDecimal(order.TakeawayDiscount)
		data.TakeawayDiscount = &d
	}
	if !order.CouponDiscount.IsZero() {
		d := utils.FormatDecimal(order.CouponDiscount)
		data.CouponDiscount = &d
		data.CouponCode = order.CouponCode
	}
	if order.DeliveryFee != nil && !order.DeliveryFee.IsZero() {
		d := utils.FormatDecimal(*order.DeliveryFee)
		data.DeliveryFee = &d
	}

	// Build address from denormalized fields
	if order.OrderType == domain.OrderTypeDelivery && order.StreetName != nil {
		data.Address = &invoice.InvoiceAddress{
			StreetName:       *order.StreetName,
			HouseNumber:      deref(order.HouseNumber),
			BoxNumber:        order.BoxNumber,
			MunicipalityName: deref(order.MunicipalityName),
			Postcode:         deref(order.Postcode),
		}
	}

	// 12. Generate PDF
	pdfBytes, err := invoice.GeneratePDF(data)
	if err != nil {
		log.Error("invoice: failed to generate PDF", zap.String("order_id", orderIDStr), zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate invoice"})
		return
	}

	// 13. Build localized filename: e.g. "facture-02-12-2025-bruno-moyse.pdf"
	prefix := invoice.FilePrefix(order.Language)
	datePart := order.CreatedAt.Format("02-01-2006")
	namePart := strings.ToLower(strings.ReplaceAll(user.FirstName+"-"+user.LastName, " ", "-"))
	filename := fmt.Sprintf("%s-%s-%s.pdf", prefix, datePart, namePart)

	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	c.Data(http.StatusOK, "application/pdf", pdfBytes)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
