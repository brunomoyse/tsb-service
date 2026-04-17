package invoice

import (
	"bytes"
	_ "embed"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/go-pdf/fpdf"

	"tsb-service/pkg/timezone"
)

//go:embed fonts/DejaVuSans.ttf
var dejaVuSansRegular []byte

//go:embed fonts/DejaVuSans-Bold.ttf
var dejaVuSansBold []byte

//go:embed logo.png
var logoPNG []byte

const (
	RestaurantName    = "Tokyo Sushi Bar — SRL"
	RestaurantAddress = "Rue de la Cathédrale 59, 4000 Liège, Belgique"
	RestaurantCompany = "BE0772.499.585"
	RestaurantPhone   = "+32 4 222 98 88"
	RestaurantEmail   = "tokyosushibar888@gmail.com"
)

type InvoiceData struct {
	CustomerName  string
	CustomerEmail string
	CustomerPhone *string

	OrderID   string
	OrderDate time.Time
	OrderType string // "DELIVERY", "PICKUP" or "DINE_IN"
	Language  string // "fr", "en" or "zh"

	Items []InvoiceItem

	Subtotal         string // sum of line totals before discounts/fees
	TakeawayDiscount *string
	CouponDiscount   *string
	CouponCode       *string
	DeliveryFee      *string
	Total            string // final total

	Address *InvoiceAddress
}

type InvoiceItem struct {
	Name      string
	Code      string
	Quantity  int64
	UnitPrice string
	LineTotal string
}

type InvoiceAddress struct {
	StreetName       string
	HouseNumber      string
	BoxNumber        *string
	MunicipalityName string
	Postcode         string
}

func (a *InvoiceAddress) Format() string {
	addr := a.StreetName + " " + a.HouseNumber
	if a.BoxNumber != nil && *a.BoxNumber != "" {
		addr += " / " + *a.BoxNumber
	}
	addr += ", " + a.Postcode + " " + a.MunicipalityName
	return addr
}

// formatOrderRef returns a short human-readable reference from the order UUID.
func formatOrderRef(orderID string, orderDate time.Time) string {
	id := strings.ReplaceAll(orderID, "-", "")
	short := id
	if len(id) > 8 {
		short = id[len(id)-8:]
	}
	return fmt.Sprintf("TSB-%d-%s", orderDate.Year(), strings.ToUpper(short))
}

// GeneratePDF generates a PDF invoice and returns the raw bytes.
func GeneratePDF(data InvoiceData) ([]byte, error) {
	l := getLabels(data.Language)

	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(20, 20, 20)
	pdf.SetAutoPageBreak(true, 20)

	pdf.AddUTF8FontFromBytes("DejaVu", "", dejaVuSansRegular)
	pdf.AddUTF8FontFromBytes("DejaVu", "B", dejaVuSansBold)

	pdf.AddPage()
	pageW, _ := pdf.GetPageSize()
	leftMargin, _, rightMargin, _ := pdf.GetMargins()
	usableW := pageW - leftMargin - rightMargin

	// === HEADER ===
	// Logo
	logoSize := 14.0 // mm
	headerY := pdf.GetY()
	logoReader := io.NopCloser(bytes.NewReader(logoPNG))
	pdf.RegisterImageOptionsReader("logo", fpdf.ImageOptions{ImageType: "PNG"}, logoReader)
	pdf.ImageOptions("logo", leftMargin, headerY, logoSize, logoSize, false, fpdf.ImageOptions{}, 0, "")

	// Restaurant name + invoice title vertically centered with logo
	textH := 10.0 // line height for 18pt text
	textY := headerY + (logoSize-textH)/2
	pdf.SetY(textY)
	pdf.SetX(leftMargin + logoSize + 3)
	pdf.SetFont("DejaVu", "B", 18)
	pdf.SetTextColor(30, 30, 30)
	nameW := usableW - logoSize - 3 - usableW*0.30
	pdf.CellFormat(nameW, textH, RestaurantName, "", 0, "L", false, 0, "")

	// Invoice title right-aligned
	pdf.SetFont("DejaVu", "B", 18)
	pdf.SetTextColor(200, 50, 50)
	pdf.CellFormat(usableW*0.30, textH, l.InvoiceTitle, "", 1, "R", false, 0, "")

	// Details below logo
	pdf.SetY(headerY + logoSize + 2)
	pdf.SetTextColor(100, 100, 100)
	pdf.SetFont("DejaVu", "", 9)
	pdf.CellFormat(usableW, 5, RestaurantAddress, "", 1, "L", false, 0, "")
	pdf.CellFormat(usableW, 5, fmt.Sprintf("%s: %s  |  %s: %s", l.Phone, RestaurantPhone, l.Email, RestaurantEmail), "", 1, "L", false, 0, "")
	pdf.CellFormat(usableW, 5, fmt.Sprintf("%s: %s", l.CompanyNumber, RestaurantCompany), "", 1, "L", false, 0, "")

	pdf.Ln(6)
	pdf.SetDrawColor(220, 220, 220)
	pdf.Line(leftMargin, pdf.GetY(), pageW-rightMargin, pdf.GetY())
	pdf.Ln(6)

	// === ORDER INFO ===
	orderRef := formatOrderRef(data.OrderID, data.OrderDate)

	dateFormat := "02/01/2006 15:04"
	if data.Language == "en" {
		dateFormat = "01/02/2006 3:04 PM"
	}

	pdf.SetTextColor(30, 30, 30)
	pdf.SetFont("DejaVu", "B", 10)
	pdf.CellFormat(usableW/2, 6, fmt.Sprintf("%s: %s", l.OrderRef, orderRef), "", 0, "L", false, 0, "")
	pdf.SetFont("DejaVu", "", 10)
	pdf.CellFormat(usableW/2, 6, fmt.Sprintf("%s: %s", l.Date, timezone.In(data.OrderDate).Format(dateFormat)), "", 1, "R", false, 0, "")

	orderTypeLabel := l.TypePickup
	switch data.OrderType {
	case "DELIVERY":
		orderTypeLabel = l.TypeDelivery
	case "DINE_IN":
		orderTypeLabel = l.TypeDineIn
	}
	pdf.SetFont("DejaVu", "", 10)
	pdf.CellFormat(usableW, 6, fmt.Sprintf("%s: %s", l.OrderType, orderTypeLabel), "", 1, "L", false, 0, "")

	if data.Address != nil {
		pdf.CellFormat(usableW, 6, fmt.Sprintf("%s: %s", l.DeliveryAddress, data.Address.Format()), "", 1, "L", false, 0, "")
	}

	pdf.Ln(4)

	// === CUSTOMER ===
	pdf.SetFont("DejaVu", "B", 10)
	pdf.CellFormat(usableW, 6, l.Customer, "", 1, "L", false, 0, "")
	pdf.SetFont("DejaVu", "", 10)
	pdf.CellFormat(usableW, 5, data.CustomerName, "", 1, "L", false, 0, "")
	pdf.CellFormat(usableW, 5, data.CustomerEmail, "", 1, "L", false, 0, "")
	if data.CustomerPhone != nil && *data.CustomerPhone != "" {
		pdf.CellFormat(usableW, 5, *data.CustomerPhone, "", 1, "L", false, 0, "")
	}

	pdf.Ln(6)

	// === ITEMS TABLE ===
	colProduct := usableW * 0.50
	colQty := usableW * 0.10
	colUnit := usableW * 0.20
	colTotal := usableW * 0.20

	// Table header
	pdf.SetFillColor(245, 245, 242)
	pdf.SetTextColor(60, 60, 60)
	pdf.SetFont("DejaVu", "B", 9)
	pdf.CellFormat(colProduct, 8, l.Product, "", 0, "L", true, 0, "")
	pdf.CellFormat(colQty, 8, l.Qty, "", 0, "C", true, 0, "")
	pdf.CellFormat(colUnit, 8, l.UnitPrice, "", 0, "R", true, 0, "")
	pdf.CellFormat(colTotal, 8, l.Total, "", 1, "R", true, 0, "")

	// Table rows
	pdf.SetFont("DejaVu", "", 9)
	pdf.SetTextColor(30, 30, 30)
	for i, item := range data.Items {
		fill := i%2 == 1
		if fill {
			pdf.SetFillColor(252, 252, 250)
		}

		name := item.Name
		if item.Code != "" {
			name = item.Code + " — " + item.Name
		}

		pdf.CellFormat(colProduct, 7, name, "", 0, "L", fill, 0, "")
		pdf.CellFormat(colQty, 7, fmt.Sprintf("%d", item.Quantity), "", 0, "C", fill, 0, "")
		pdf.CellFormat(colUnit, 7, item.UnitPrice+" €", "", 0, "R", fill, 0, "")
		pdf.CellFormat(colTotal, 7, item.LineTotal+" €", "", 1, "R", fill, 0, "")
	}

	pdf.Ln(4)
	pdf.SetDrawColor(220, 220, 220)
	pdf.Line(leftMargin, pdf.GetY(), pageW-rightMargin, pdf.GetY())
	pdf.Ln(4)

	// === TOTALS ===
	totalsX := leftMargin + usableW*0.50
	totalsW := usableW * 0.50
	labelW := totalsW * 0.60
	valueW := totalsW * 0.40

	renderTotalLine := func(label, value string, bold bool) {
		if bold {
			pdf.SetFont("DejaVu", "B", 10)
		} else {
			pdf.SetFont("DejaVu", "", 9)
		}
		pdf.SetX(totalsX)
		pdf.CellFormat(labelW, 6, label, "", 0, "L", false, 0, "")
		pdf.CellFormat(valueW, 6, value+" €", "", 1, "R", false, 0, "")
	}

	// Subtotal
	pdf.SetTextColor(60, 60, 60)
	renderTotalLine(l.Subtotal, data.Subtotal, false)

	// Discounts
	if data.TakeawayDiscount != nil {
		pdf.SetTextColor(0, 150, 80)
		renderTotalLine(l.TakeawayDiscount, "- "+*data.TakeawayDiscount, false)
	}
	if data.CouponDiscount != nil {
		pdf.SetTextColor(0, 150, 80)
		couponLabel := l.CouponDiscount
		if data.CouponCode != nil {
			couponLabel += " (" + *data.CouponCode + ")"
		}
		renderTotalLine(couponLabel, "- "+*data.CouponDiscount, false)
	}
	if data.DeliveryFee != nil {
		pdf.SetTextColor(60, 60, 60)
		renderTotalLine(l.DeliveryFee, *data.DeliveryFee, false)
	}

	pdf.Ln(2)
	pdf.SetDrawColor(200, 50, 50)
	pdf.Line(totalsX, pdf.GetY(), pageW-rightMargin, pdf.GetY())
	pdf.Ln(2)

	// Total (bold)
	pdf.SetTextColor(30, 30, 30)
	renderTotalLine(l.Total, data.Total, true)

	// VAT included note
	pdf.Ln(2)
	pdf.SetTextColor(130, 130, 130)
	pdf.SetFont("DejaVu", "", 8)
	pdf.SetX(totalsX)
	pdf.CellFormat(totalsW, 5, l.VATIncluded, "", 1, "R", false, 0, "")

	// === FOOTER ===
	pdf.Ln(12)
	pdf.SetTextColor(150, 150, 150)
	pdf.SetFont("DejaVu", "", 10)
	pdf.CellFormat(usableW, 6, l.ThankYou, "", 1, "C", false, 0, "")

	// Output
	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, fmt.Errorf("failed to generate PDF: %w", err)
	}
	return buf.Bytes(), nil
}
