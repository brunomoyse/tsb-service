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
	Execute(w io.Writer, data any) error
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
func renderEmail(path string, data any, loader func(string) (templateExecutor, error)) (string, error) {
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
// Logo URL Helper
// --------------------------------------------------------------------------------

func logoURL() string {
	return fmt.Sprintf("%s/images/tsb-logo-w.png", os.Getenv("APP_BASE_URL"))
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
		return ""
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
	LogoURL    string
} {
	return struct {
		UserName   string
		VerifyLink string
		LogoURL    string
	}{
		UserName:   fmt.Sprintf("%s %s", user.FirstName, user.LastName),
		VerifyLink: verifyLink,
		LogoURL:    logoURL(),
	}
}

// prepareWelcomeEmailData prepares the data for welcome emails.
func prepareWelcomeEmailData(user userDomain.User, menuLink string) struct {
	UserName string
	MenuLink string
	LogoURL  string
} {
	return struct {
		UserName string
		MenuLink string
		LogoURL  string
	}{
		UserName: fmt.Sprintf("%s %s", user.FirstName, user.LastName),
		MenuLink: menuLink,
		LogoURL:  logoURL(),
	}
}

// prepareResetPasswordEmailData prepares the data for password reset emails.
func prepareResetPasswordEmailData(user userDomain.User, resetLink string) struct {
	UserName  string
	ResetLink string
	LogoURL   string
} {
	return struct {
		UserName  string
		ResetLink string
		LogoURL   string
	}{
		UserName:  fmt.Sprintf("%s %s", user.FirstName, user.LastName),
		ResetLink: resetLink,
		LogoURL:   logoURL(),
	}
}

// prepareOrderPendingData prepares the data for order pending emails.
func prepareOrderPendingData(u userDomain.User, op []orderDomain.OrderProduct, o orderDomain.Order) (any, error) {
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

	var deliveryFee decimal.Decimal
	if o.DeliveryFee != nil {
		deliveryFee = *o.DeliveryFee
	}

	var couponCode string
	if o.CouponCode != nil {
		couponCode = *o.CouponCode
	}

	data := struct {
		UserName           string
		OrderItems         []OrderProductView
		OrderType          string
		SubtotalPrice      string
		TakeawayDiscount   string
		HasTakeaway        bool
		CouponDiscount     string
		HasCoupon          bool
		CouponCode         string
		DeliveryFee        string
		TotalPrice         string
		LogoURL            string
	}{
		UserName:           fmt.Sprintf("%s %s", u.FirstName, u.LastName),
		OrderItems:         orderViews,
		OrderType:          string(o.OrderType),
		SubtotalPrice:      utils.FormatDecimal(subtotal),
		TakeawayDiscount:   utils.FormatDecimal(o.TakeawayDiscount),
		HasTakeaway:        o.TakeawayDiscount.GreaterThan(decimal.Zero),
		CouponDiscount:     utils.FormatDecimal(o.CouponDiscount),
		HasCoupon:          o.CouponDiscount.GreaterThan(decimal.Zero),
		CouponCode:         couponCode,
		DeliveryFee:        utils.FormatDecimal(deliveryFee),
		TotalPrice:         utils.FormatDecimal(o.TotalPrice),
		LogoURL:            logoURL(),
	}

	return data, nil
}

func prepareOrderCanceledData(u userDomain.User) (any, error) {
	data := struct {
		UserName string
		LogoURL  string
	}{
		UserName: fmt.Sprintf("%s %s", u.FirstName, u.LastName),
		LogoURL:  logoURL(),
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
) (any, error) {
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
	var deliveryFee decimal.Decimal
	if o.DeliveryFee != nil {
		deliveryFee = *o.DeliveryFee
	}

	var couponCode string
	if o.CouponCode != nil {
		couponCode = *o.CouponCode
	}

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
		UserName           string
		OrderItems         []OrderProductView
		OrderType          string
		SubtotalPrice      string
		TakeawayDiscount   string
		HasTakeaway        bool
		CouponDiscount     string
		HasCoupon          bool
		CouponCode         string
		DeliveryFee        string
		TotalPrice         string
		StatusLink         string
		EstimatedReadyTime string
		Address            *AddressView
		LogoURL            string
	}{
		UserName:           u.FirstName + " " + u.LastName,
		OrderItems:         orderViews,
		OrderType:          string(o.OrderType),
		SubtotalPrice:      utils.FormatDecimal(subtotal),
		TakeawayDiscount:   utils.FormatDecimal(o.TakeawayDiscount),
		HasTakeaway:        o.TakeawayDiscount.GreaterThan(decimal.Zero),
		CouponDiscount:     utils.FormatDecimal(o.CouponDiscount),
		HasCoupon:          o.CouponDiscount.GreaterThan(decimal.Zero),
		CouponCode:         couponCode,
		DeliveryFee:        utils.FormatDecimal(deliveryFee),
		TotalPrice:         utils.FormatDecimal(o.TotalPrice),
		StatusLink:         fmt.Sprintf("%s/me?followOrder=%s", os.Getenv("APP_BASE_URL"), o.ID),
		EstimatedReadyTime: estimatedReadyTime,
		Address:            addrView,
		LogoURL:            logoURL(),
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

// renderResetPasswordEmailHTML renders the HTML version of the password reset email.
func renderResetPasswordEmailHTML(path string, user userDomain.User, resetLink string) (string, error) {
	data := prepareResetPasswordEmailData(user, resetLink)
	return renderEmail(path, data, loadHTMLTemplate)
}

// renderResetPasswordEmailText renders the plain text version of the password reset email.
func renderResetPasswordEmailText(path string, user userDomain.User, resetLink string) (string, error) {
	data := prepareResetPasswordEmailData(user, resetLink)
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

// --------------------------------------------------------------------------------
// Order Ready
// --------------------------------------------------------------------------------

func prepareOrderReadyData(u userDomain.User, o orderDomain.Order) any {
	return struct {
		UserName   string
		OrderType  string
		StatusLink string
		LogoURL    string
	}{
		UserName:   fmt.Sprintf("%s %s", u.FirstName, u.LastName),
		OrderType:  string(o.OrderType),
		StatusLink: fmt.Sprintf("%s/me?followOrder=%s", os.Getenv("APP_BASE_URL"), o.ID),
		LogoURL:    logoURL(),
	}
}

func renderOrderReadyEmailHTML(path string, u userDomain.User, o orderDomain.Order) (string, error) {
	data := prepareOrderReadyData(u, o)
	return renderEmail(path, data, loadHTMLTemplate)
}

func renderOrderReadyEmailText(path string, u userDomain.User, o orderDomain.Order) (string, error) {
	data := prepareOrderReadyData(u, o)
	return renderEmail(path, data, loadTextTemplate)
}

// --------------------------------------------------------------------------------
// Order Completed
// --------------------------------------------------------------------------------

func prepareOrderCompletedData(u userDomain.User) any {
	return struct {
		UserName string
		MenuLink string
		LogoURL  string
	}{
		UserName: fmt.Sprintf("%s %s", u.FirstName, u.LastName),
		MenuLink: os.Getenv("APP_BASE_URL"),
		LogoURL:  logoURL(),
	}
}

func renderOrderCompletedEmailHTML(path string, u userDomain.User) (string, error) {
	data := prepareOrderCompletedData(u)
	return renderEmail(path, data, loadHTMLTemplate)
}

func renderOrderCompletedEmailText(path string, u userDomain.User) (string, error) {
	data := prepareOrderCompletedData(u)
	return renderEmail(path, data, loadTextTemplate)
}

// --------------------------------------------------------------------------------
// Payment Failed
// --------------------------------------------------------------------------------

func preparePaymentFailedData(u userDomain.User) any {
	return struct {
		UserName string
		MenuLink string
		LogoURL  string
	}{
		UserName: fmt.Sprintf("%s %s", u.FirstName, u.LastName),
		MenuLink: os.Getenv("APP_BASE_URL"),
		LogoURL:  logoURL(),
	}
}

func renderPaymentFailedEmailHTML(path string, u userDomain.User) (string, error) {
	data := preparePaymentFailedData(u)
	return renderEmail(path, data, loadHTMLTemplate)
}

func renderPaymentFailedEmailText(path string, u userDomain.User) (string, error) {
	data := preparePaymentFailedData(u)
	return renderEmail(path, data, loadTextTemplate)
}

// --------------------------------------------------------------------------------
// Refund Issued
// --------------------------------------------------------------------------------

func prepareRefundIssuedData(u userDomain.User, refundAmount string) any {
	return struct {
		UserName     string
		RefundAmount string
		LogoURL      string
	}{
		UserName:     fmt.Sprintf("%s %s", u.FirstName, u.LastName),
		RefundAmount: refundAmount,
		LogoURL:      logoURL(),
	}
}

func renderRefundIssuedEmailHTML(path string, u userDomain.User, refundAmount string) (string, error) {
	data := prepareRefundIssuedData(u, refundAmount)
	return renderEmail(path, data, loadHTMLTemplate)
}

func renderRefundIssuedEmailText(path string, u userDomain.User, refundAmount string) (string, error) {
	data := prepareRefundIssuedData(u, refundAmount)
	return renderEmail(path, data, loadTextTemplate)
}

// --------------------------------------------------------------------------------
// Account Linked
// --------------------------------------------------------------------------------

func prepareAccountLinkedData(u userDomain.User) any {
	return struct {
		UserName string
		LogoURL  string
	}{
		UserName: fmt.Sprintf("%s %s", u.FirstName, u.LastName),
		LogoURL:  logoURL(),
	}
}

func renderAccountLinkedEmailHTML(path string, u userDomain.User) (string, error) {
	data := prepareAccountLinkedData(u)
	return renderEmail(path, data, loadHTMLTemplate)
}

func renderAccountLinkedEmailText(path string, u userDomain.User) (string, error) {
	data := prepareAccountLinkedData(u)
	return renderEmail(path, data, loadTextTemplate)
}

// --------------------------------------------------------------------------------
// Ready Time Updated
// --------------------------------------------------------------------------------

func prepareReadyTimeUpdatedData(u userDomain.User, o orderDomain.Order, lang string) any {
	return struct {
		UserName           string
		OrderType          string
		EstimatedReadyTime string
		StatusLink         string
		LogoURL            string
	}{
		UserName:           fmt.Sprintf("%s %s", u.FirstName, u.LastName),
		OrderType:          string(o.OrderType),
		EstimatedReadyTime: formatEstimatedReadyTime(o.EstimatedReadyTime, lang),
		StatusLink:         fmt.Sprintf("%s/me?followOrder=%s", os.Getenv("APP_BASE_URL"), o.ID),
		LogoURL:            logoURL(),
	}
}

func renderReadyTimeUpdatedEmailHTML(path string, u userDomain.User, o orderDomain.Order, lang string) (string, error) {
	data := prepareReadyTimeUpdatedData(u, o, lang)
	return renderEmail(path, data, loadHTMLTemplate)
}

func renderReadyTimeUpdatedEmailText(path string, u userDomain.User, o orderDomain.Order, lang string) (string, error) {
	data := prepareReadyTimeUpdatedData(u, o, lang)
	return renderEmail(path, data, loadTextTemplate)
}

// --------------------------------------------------------------------------------
// Deletion Request
// --------------------------------------------------------------------------------

func prepareDeletionRequestData(u userDomain.User) any {
	return struct {
		UserName  string
		UserEmail string
		UserID    string
		LogoURL   string
	}{
		UserName:  fmt.Sprintf("%s %s", u.FirstName, u.LastName),
		UserEmail: u.Email,
		UserID:    u.ID.String(),
		LogoURL:   logoURL(),
	}
}

func renderDeletionRequestEmailHTML(path string, u userDomain.User) (string, error) {
	data := prepareDeletionRequestData(u)
	return renderEmail(path, data, loadHTMLTemplate)
}

func renderDeletionRequestEmailText(path string, u userDomain.User) (string, error) {
	data := prepareDeletionRequestData(u)
	return renderEmail(path, data, loadTextTemplate)
}

// --------------------------------------------------------------------------------
// Re-engagement
// --------------------------------------------------------------------------------

func prepareReengagementData(u userDomain.User) any {
	return struct {
		UserName string
		MenuLink string
		LogoURL  string
	}{
		UserName: fmt.Sprintf("%s %s", u.FirstName, u.LastName),
		MenuLink: os.Getenv("APP_BASE_URL"),
		LogoURL:  logoURL(),
	}
}

func renderReengagementEmailHTML(path string, u userDomain.User) (string, error) {
	data := prepareReengagementData(u)
	return renderEmail(path, data, loadHTMLTemplate)
}

func renderReengagementEmailText(path string, u userDomain.User) (string, error) {
	data := prepareReengagementData(u)
	return renderEmail(path, data, loadTextTemplate)
}
