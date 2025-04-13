package scaleway

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io"
	"os"
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

// prepareOrderConfirmedData prepares the data for order confirmed emails.
func prepareOrderConfirmedData(u userDomain.User, op []orderDomain.OrderProduct, o orderDomain.Order, a *addressDomain.Address) (interface{}, error) {
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

	var boxNumber string
	if a != nil && a.BoxNumber != nil {
		boxNumber = *a.BoxNumber
	}

	// Prepare address fields safely.
	var streetName, houseNumber, municipalityName, postcode string
	if a != nil {
		streetName = a.StreetName
		houseNumber = a.HouseNumber
		municipalityName = a.MunicipalityName
		postcode = a.Postcode
	}

	data := struct {
		UserName         string
		OrderItems       []OrderProductView
		OrderType        string
		SubtotalPrice    string
		TakeawayDiscount string
		DeliveryFee      string
		TotalPrice       string
		StatusLink       string
		DeliveryTime     string
		Address          struct {
			StreetName       string
			HouseNumber      string
			BoxNumber        string
			MunicipalityName string
			Postcode         string
		}
	}{
		UserName:         fmt.Sprintf("%s %s", u.FirstName, u.LastName),
		OrderItems:       orderViews,
		OrderType:        string(o.OrderType),
		SubtotalPrice:    utils.FormatDecimal(subtotal),
		TakeawayDiscount: utils.FormatDecimal(decimal.NewFromFloat(0)), // @TODO: adjust discount
		DeliveryFee:      utils.FormatDecimal(*o.DeliveryFee),
		TotalPrice:       utils.FormatDecimal(o.TotalPrice),
		StatusLink:       fmt.Sprintf("%s/me?followOrder=%s", os.Getenv("APP_BASE_URL"), o.ID),
		DeliveryTime:     "19:30", // @TODO: Implement when o.EstimatedDeliveryTime is available
		Address: struct {
			StreetName       string
			HouseNumber      string
			BoxNumber        string
			MunicipalityName string
			Postcode         string
		}{
			StreetName:       streetName,
			HouseNumber:      houseNumber,
			BoxNumber:        boxNumber,
			MunicipalityName: municipalityName,
			Postcode:         postcode,
		},
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

// renderOrderConfirmedEmailHTML renders the HTML version of the order confirmed email.
func renderOrderConfirmedEmailHTML(path string, u userDomain.User, op []orderDomain.OrderProduct, o orderDomain.Order, a *addressDomain.Address) (string, error) {
	data, err := prepareOrderConfirmedData(u, op, o, a)

	if err != nil {
		return "", err
	}
	return renderEmail(path, data, loadHTMLTemplate)
}

// renderOrderConfirmedEmailText renders the plain text version of the order confirmed email.
func renderOrderConfirmedEmailText(path string, u userDomain.User, op []orderDomain.OrderProduct, o orderDomain.Order, a *addressDomain.Address) (string, error) {
	data, err := prepareOrderConfirmedData(u, op, o, a)
	if err != nil {
		return "", err
	}
	return renderEmail(path, data, loadTextTemplate)
}
