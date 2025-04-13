package scaleway

import (
	"bytes"
	"embed"
	"fmt"
	"github.com/shopspring/decimal"
	"html/template"
	orderDomain "tsb-service/internal/modules/order/domain"
	userDomain "tsb-service/internal/modules/user/domain"
	"tsb-service/pkg/utils"
)

//go:embed templates/*/*.html
var emailFS embed.FS

// loadTemplate loads the template file from the embedded file system using the provided path.
func loadTemplate(path string) (*template.Template, error) {
	tmpl, err := template.ParseFS(emailFS, path)
	if err != nil {
		return nil, fmt.Errorf("failed to parse template %q: %w", path, err)
	}
	return tmpl, nil
}

func renderVerifyEmail(path string, user userDomain.User, verifyLink string) (string, error) {
	tmpl, err := loadTemplate(path)

	if err != nil {
		return "", err
	}

	data := struct {
		UserName   string
		VerifyLink string
	}{
		UserName:   fmt.Sprintf("%s %s", user.FirstName, user.LastName),
		VerifyLink: verifyLink,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute verify template: %w", err)
	}
	return buf.String(), nil
}

func renderWelcomeEmail(path string, user userDomain.User, menuLink string) (string, error) {
	tmpl, err := loadTemplate(path)
	if err != nil {
		return "", err
	}

	data := struct {
		UserName string
		MenuLink string
	}{
		UserName: fmt.Sprintf("%s %s", user.FirstName, user.LastName),
		MenuLink: menuLink,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute welcome template: %w", err)
	}
	return buf.String(), nil
}

func renderOrderPendingEmail(path string, u userDomain.User, op []orderDomain.OrderProduct, o orderDomain.Order) (string, error) {
	tmpl, err := loadTemplate(path)
	if err != nil {
		return "", err
	}

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
		TakeawayDiscount: utils.FormatDecimal(decimal.NewFromFloat(0)), // @TODO
		DeliveryFee:      utils.FormatDecimal(*o.DeliveryFee),
		TotalPrice:       utils.FormatDecimal(o.TotalPrice),
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute order pending template: %w", err)
	}
	return buf.String(), nil
}
