package scaleway

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io"
	"os"
	"time"
	addressDomain "tsb-service/internal/modules/address/domain"

	textTemplate "text/template"

	"github.com/shopspring/decimal"
	orderDomain "tsb-service/internal/modules/order/domain"
	userDomain "tsb-service/internal/modules/user/domain"
	"tsb-service/pkg/utils"
)

// --------------------------------------------------------------------------------
// Embedded Templates for HTML & plain text versions.
// --------------------------------------------------------------------------------

//go:embed templates/*/*.html
var htmlEmailFS embed.FS

//go:embed templates/*/*.txt
var textEmailFS embed.FS

// --------------------------------------------------------------------------------
// templateExecutor is a common interface satisfied by both html/template.Template
// and text/template.Template (they both expose the Execute method).
// --------------------------------------------------------------------------------

type templateExecutor interface {
	Execute(w io.Writer, data interface{}) error
}

// --------------------------------------------------------------------------------
// Template loaders
// --------------------------------------------------------------------------------

// loadHTMLTemplate loads an HTML template from the embedded HTML FS.
func loadHTMLTemplate(path string) (templateExecutor, error) {
	htmlPath := fmt.Sprintf("%s.html", path)
	tmpl, err := template.ParseFS(htmlEmailFS, htmlPath)
	if err != nil {
		return nil, fmt.Errorf("failed to parse HTML template %q: %w", htmlPath, err)
	}
	return tmpl, nil
}

// loadTextTemplate loads a plain text template from the embedded text FS.
func loadTextTemplate(path string) (templateExecutor, error) {
	textPath := fmt.Sprintf("%s.txt", path)
	tmpl, err := textTemplate.ParseFS(textEmailFS, textPath)
	if err != nil {
		return nil, fmt.Errorf("failed to parse text template %q: %w", textPath, err)
	}
	return tmpl, nil
}

// --------------------------------------------------------------------------------
// Generic render function
// --------------------------------------------------------------------------------

// renderEmail loads a template using the provided loader, executes it with data,
// and returns the rendered string.
func renderEmail(path string, data interface{}, loader func(string) (templateExecutor, error)) (string, error) {
	tmpl, err := loader(path)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute template %q: %w", path, err)
	}
	return buf.String(), nil
}

// --------------------------------------------------------------------------------
// Common Data Preparation Helpers
// --------------------------------------------------------------------------------

// formatEstimatedReadyTime formats the estimated ready time based on the language
// French: "lundi 15 janvier 2025 à 18:30"
// English: "Monday, January 15, 2025 at 6:30 PM"
// Chinese: "2025年1月15日 星期一 18:30"
func formatEstimatedReadyTime(t *time.Time, lang string) string {
	if t == nil {
		return "À définir / To be defined / 待定"
	}

	// Map for day names
	frenchDays := []string{"dimanche", "lundi", "mardi", "mercredi", "jeudi", "vendredi", "samedi"}
	englishDays := []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}
	chineseDays := []string{"星期日", "星期一", "星期二", "星期三", "星期四", "星期五", "星期六"}

	// Map for month names
	frenchMonths := []string{"", "janvier", "février", "mars", "avril", "mai", "juin", "juillet", "août", "septembre", "octobre", "novembre", "décembre"}
	englishMonths := []string{"", "January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"}

	weekday := int(t.Weekday())
	day := t.Day()
	month := int(t.Month())
	year := t.Year()
	hour := t.Hour()
	minute := t.Minute()

	switch lang {
	case "fr":
		return fmt.Sprintf("%s %d %s %d à %02d:%02d",
			frenchDays[weekday], day, frenchMonths[month], year, hour, minute)
	case "en":
		// English format with AM/PM
		period := "AM"
		displayHour := hour
		if hour >= 12 {
			period = "PM"
			if hour > 12 {
				displayHour = hour - 12
			}
		}
		if displayHour == 0 {
			displayHour = 12
		}
		return fmt.Sprintf("%s, %s %d, %d at %d:%02d %s",
			englishDays[weekday], englishMonths[month], day, year, displayHour, minute, period)
	case "zh":
		return fmt.Sprintf("%d年%d月%d日 %s %02d:%02d",
			year, month, day, chineseDays[weekday], hour, minute)
	default:
		// Default to French
		return fmt.Sprintf("%s %d %s %d à %02d:%02d",
			frenchDays[weekday], day, frenchMonths[month], year, hour, minute)
	}
}

// prepareVerifyEmailData prepares the data structure common for verify emails.
func prepareVerifyEmailData(user userDomain.User, verifyLink string) struct {
	UserName   string
	VerifyLink string
} {
	return struct {
		UserName   string
		VerifyLink string
	}{
		UserName:   fmt.Sprintf("%s %s", user.FirstName, user.LastName),
		VerifyLink: verifyLink,
	}
}

// prepareWelcomeEmailData prepares the data for welcome emails.
func prepareWelcomeEmailData(user userDomain.User, menuLink string) struct {
	UserName string
	MenuLink string
} {
	return struct {
		UserName string
		MenuLink string
	}{
		UserName: fmt.Sprintf("%s %s", user.FirstName, user.LastName),
		MenuLink: menuLink,
	}
}

// prepareOrderPendingData prepares the data for order pending emails.
func prepareOrderPendingData(u userDomain.User, op []orderDomain.OrderProduct, o orderDomain.Order) (interface{}, error) {
	type OrderProductView struct {
		Name       string
		Quantity   int64
		TotalPrice string
	}

	var orderViews []OrderProductView
	for _, item := range op {
		orderViews = append(orderViews, OrderProductView{
			Name:       fmt.Sprintf("%s - %s", item.Product.CategoryName, item.Product.Name),
			Quantity:   item.Quantity,
			TotalPrice: utils.FormatDecimal(item.TotalPrice),
		})
	}

	subtotal := decimal.NewFromInt(0)
	for _, item := range op {
		subtotal = subtotal.Add(item.TotalPrice)
	}

	data := struct {
		UserName         string
		OrderItems       []OrderProductView
		OrderType        string
		SubtotalPrice    string
		TakeawayDiscount string
		DeliveryFee      string
		TotalPrice       string
	}{
		UserName:         fmt.Sprintf("%s %s", u.FirstName, u.LastName),
		OrderItems:       orderViews,
		OrderType:        string(o.OrderType),
		SubtotalPrice:    utils.FormatDecimal(subtotal),
		TakeawayDiscount: utils.FormatDecimal(decimal.NewFromFloat(0)), // @TODO: adjust discount if needed
		DeliveryFee:      utils.FormatDecimal(*o.DeliveryFee),
		TotalPrice:       utils.FormatDecimal(o.TotalPrice),
	}

	return data, nil
}

func prepareOrderCanceledData(u userDomain.User) (interface{}, error) {
	data := struct {
		UserName string
	}{
		UserName: fmt.Sprintf("%s %s", u.FirstName, u.LastName),
	}

	return data, nil
}

// prepareOrderConfirmedData prepares the data for order confirmed emails.
func prepareOrderConfirmedData(
	u userDomain.User,
	op []orderDomain.OrderProduct,
	o orderDomain.Order,
	a *addressDomain.Address,
	lang string,
) (interface{}, error) {
	type OrderProductView struct {
		Name       string
		Quantity   int64
		TotalPrice string
	}
	type AddressView struct {
		StreetName       string
		HouseNumber      string
		BoxNumber        string
		MunicipalityName string
		Postcode         string
	}
	// 1) build product lines
	orderViews := make([]OrderProductView, len(op))
	subtotal := decimal.Zero
	for i, item := range op {
		orderViews[i] = OrderProductView{
			Name:       fmt.Sprintf("%s – %s", item.Product.CategoryName, item.Product.Name),
			Quantity:   item.Quantity,
			TotalPrice: utils.FormatDecimal(item.TotalPrice),
		}
		subtotal = subtotal.Add(item.TotalPrice)
	}

	// 2) delivery fee & discount
	var deliveryFee, discount decimal.Decimal
	if o.DeliveryFee != nil {
		deliveryFee = *o.DeliveryFee
	}
	discount = o.DiscountAmount

	// 3) maybe build AddressView
	var addrView *AddressView
	if a != nil {
		box := ""
		if a.BoxNumber != nil {
			box = *a.BoxNumber
		}
		addrView = &AddressView{
			StreetName:       a.StreetName,
			HouseNumber:      a.HouseNumber,
			BoxNumber:        box,
			MunicipalityName: a.MunicipalityName,
			Postcode:         a.Postcode,
		}
	}

	// 4) format estimated ready time based on language
	estimatedReadyTime := formatEstimatedReadyTime(o.EstimatedReadyTime, lang)

	// 5) assemble data
	data := struct {
		UserName            string
		OrderItems          []OrderProductView
		OrderType           string
		SubtotalPrice       string
		TakeawayDiscount    string
		DeliveryFee         string
		TotalPrice          string
		StatusLink          string
		EstimatedReadyTime  string
		Address             *AddressView
	}{
		UserName:           u.FirstName + " " + u.LastName,
		OrderItems:         orderViews,
		OrderType:          string(o.OrderType),
		SubtotalPrice:      utils.FormatDecimal(subtotal),
		TakeawayDiscount:   utils.FormatDecimal(discount),
		DeliveryFee:        utils.FormatDecimal(deliveryFee),
		TotalPrice:         utils.FormatDecimal(o.TotalPrice),
		StatusLink:         fmt.Sprintf("%s/me?followOrder=%s", os.Getenv("APP_BASE_URL"), o.ID),
		EstimatedReadyTime: estimatedReadyTime,
		Address:            addrView,
	}

	return data, nil
}

// --------------------------------------------------------------------------------
// Specific Render Functions
// --------------------------------------------------------------------------------

// renderVerifyEmailHTML renders the HTML version of the verification email.
func renderVerifyEmailHTML(path string, user userDomain.User, verifyLink string) (string, error) {
	data := prepareVerifyEmailData(user, verifyLink)
	return renderEmail(path, data, loadHTMLTemplate)
}

// renderVerifyEmailText renders the plain text version of the verification email.
func renderVerifyEmailText(path string, user userDomain.User, verifyLink string) (string, error) {
	data := prepareVerifyEmailData(user, verifyLink)
	return renderEmail(path, data, loadTextTemplate)
}

// renderWelcomeEmailHTML renders the HTML version of the welcome email.
func renderWelcomeEmailHTML(path string, user userDomain.User, menuLink string) (string, error) {
	data := prepareWelcomeEmailData(user, menuLink)
	return renderEmail(path, data, loadHTMLTemplate)
}

// renderWelcomeEmailText renders the plain text version of the welcome email.
func renderWelcomeEmailText(path string, user userDomain.User, menuLink string) (string, error) {
	data := prepareWelcomeEmailData(user, menuLink)
	return renderEmail(path, data, loadTextTemplate)
}

// renderOrderPendingEmailHTML renders the HTML version of the order pending email.
func renderOrderPendingEmailHTML(path string, u userDomain.User, op []orderDomain.OrderProduct, o orderDomain.Order) (string, error) {
	data, err := prepareOrderPendingData(u, op, o)
	if err != nil {
		return "", err
	}
	return renderEmail(path, data, loadHTMLTemplate)
}

// renderOrderPendingEmailText renders the plain text version of the order pending email.
func renderOrderPendingEmailText(path string, u userDomain.User, op []orderDomain.OrderProduct, o orderDomain.Order) (string, error) {
	data, err := prepareOrderPendingData(u, op, o)
	if err != nil {
		return "", err
	}
	return renderEmail(path, data, loadTextTemplate)
}

// renderOrderCanceledEmailHTML renders the HTML version of the order canceled email.
func renderOrderCanceledEmailHTML(path string, u userDomain.User) (string, error) {
	data, err := prepareOrderCanceledData(u)
	if err != nil {
		return "", err
	}
	return renderEmail(path, data, loadHTMLTemplate)
}

// renderOrderCanceledEmailText renders the plain text version of the order canceled email.
func renderOrderCanceledEmailText(path string, u userDomain.User) (string, error) {
	data, err := prepareOrderCanceledData(u)
	if err != nil {
		return "", err
	}
	return renderEmail(path, data, loadTextTemplate)
}

// renderOrderConfirmedEmailHTML renders the HTML version of the order confirmed email.
func renderOrderConfirmedEmailHTML(path string, u userDomain.User, op []orderDomain.OrderProduct, o orderDomain.Order, a *addressDomain.Address, lang string) (string, error) {
	data, err := prepareOrderConfirmedData(u, op, o, a, lang)

	if err != nil {
		return "", err
	}
	return renderEmail(path, data, loadHTMLTemplate)
}

// renderOrderConfirmedEmailText renders the plain text version of the order confirmed email.
func renderOrderConfirmedEmailText(path string, u userDomain.User, op []orderDomain.OrderProduct, o orderDomain.Order, a *addressDomain.Address, lang string) (string, error) {
	data, err := prepareOrderConfirmedData(u, op, o, a, lang)
	if err != nil {
		return "", err
	}
	return renderEmail(path, data, loadTextTemplate)
}
