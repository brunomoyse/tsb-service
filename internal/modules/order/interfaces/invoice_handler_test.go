package interfaces

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	orderApplication "tsb-service/internal/modules/order/application"
	"tsb-service/internal/modules/order/domain"
	productApplication "tsb-service/internal/modules/product/application"
	productDomain "tsb-service/internal/modules/product/domain"
	userApplication "tsb-service/internal/modules/user/application"
	userDomain "tsb-service/internal/modules/user/domain"
	"tsb-service/pkg/invoice/invoicetest"
	"tsb-service/pkg/utils"
)

type fakeOrders struct {
	orderApplication.OrderService
	order    *domain.Order
	products *[]domain.OrderProductRaw
	err      error
}

func (f *fakeOrders) GetOrderByID(context.Context, uuid.UUID) (*domain.Order, *[]domain.OrderProductRaw, error) {
	return f.order, f.products, f.err
}

type fakeProducts struct {
	productApplication.ProductService
	names     []*productDomain.ProductOrderDetails
	namesErr  error
	choice    *productDomain.ProductChoice
	choiceErr error
	gotLang   string
	gotIDs    []string
}

func (f *fakeProducts) GetProductNamesForInvoice(ctx context.Context, ids []string) ([]*productDomain.ProductOrderDetails, error) {
	f.gotLang = utils.GetLang(ctx)
	f.gotIDs = ids
	return f.names, f.namesErr
}

func (f *fakeProducts) GetChoiceByID(context.Context, uuid.UUID) (*productDomain.ProductChoice, error) {
	return f.choice, f.choiceErr
}

type fakeUsers struct {
	userApplication.UserService
	user *userDomain.User
	err  error
}

func (f *fakeUsers) GetUserByID(context.Context, string) (*userDomain.User, error) {
	return f.user, f.err
}

type invoiceEnv struct {
	userID   uuid.UUID
	orderID  uuid.UUID
	prodID   uuid.UUID
	orders   *fakeOrders
	products *fakeProducts
	users    *fakeUsers
}

func newInvoiceEnv() *invoiceEnv {
	d := decimal.RequireFromString
	e := &invoiceEnv{userID: uuid.New(), orderID: uuid.New(), prodID: uuid.New()}
	code := "A1"
	phone := "+32470000000"
	e.orders = &fakeOrders{
		order: &domain.Order{
			ID:          e.orderID,
			UserID:      e.userID,
			OrderStatus: domain.OrderStatusDelivered,
			OrderType:   domain.OrderTypePickUp,
			Language:    "fr",
			TotalPrice:  d("21.00"),
			CreatedAt:   time.Date(2025, 12, 2, 12, 0, 0, 0, time.UTC),
		},
		products: &[]domain.OrderProductRaw{{
			ID: uuid.New(), ProductID: e.prodID, Quantity: 2,
			UnitPrice: d("10.50"), TotalPrice: d("21.00"), VatRateApplied: d("6"),
		}},
	}
	e.products = &fakeProducts{names: []*productDomain.ProductOrderDetails{{ID: e.prodID, Code: &code, Name: "Sushi"}}}
	e.users = &fakeUsers{user: &userDomain.User{ID: e.userID, FirstName: "Jean Paul", LastName: "Dupont", Email: "j@x.test", PhoneNumber: &phone}}
	return e
}

func (e *invoiceEnv) do(t *testing.T, authedAs string, idParam string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := NewOrderHandler(e.orders, e.users, e.products)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodGet, "/orders/"+idParam+"/invoice", nil)
	if authedAs != "" {
		req = req.WithContext(utils.SetUserID(req.Context(), authedAs))
	}
	c.Request = req
	c.Params = gin.Params{{Key: "id", Value: idParam}}
	h.DownloadInvoice(c)
	return rec
}

func errorOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	return rec.Body.String()
}

func TestDownloadInvoice_Rejections(t *testing.T) {
	t.Run("unauthenticated is 401", func(t *testing.T) {
		e := newInvoiceEnv()
		rec := e.do(t, "", e.orderID.String())
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.Contains(t, errorOf(t, rec), "unauthorized")
	})
	t.Run("malformed order id is 400", func(t *testing.T) {
		e := newInvoiceEnv()
		rec := e.do(t, e.userID.String(), "not-a-uuid")
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, errorOf(t, rec), "invalid order ID")
	})
	t.Run("lookup error is 404", func(t *testing.T) {
		e := newInvoiceEnv()
		e.orders.err = errors.New("boom")
		rec := e.do(t, e.userID.String(), e.orderID.String())
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})
	t.Run("nil order is 404", func(t *testing.T) {
		e := newInvoiceEnv()
		e.orders.order = nil
		rec := e.do(t, e.userID.String(), e.orderID.String())
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})
	t.Run("someone else's order is 403", func(t *testing.T) {
		e := newInvoiceEnv()
		rec := e.do(t, uuid.NewString(), e.orderID.String())
		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.NotContains(t, rec.Header().Get("Content-Type"), "pdf")
	})
	for _, st := range []domain.OrderStatus{
		domain.OrderStatusPending, domain.OrderStatusConfirmed, domain.OrderStatusPreparing,
		domain.OrderStatusCanceled, domain.OrderStatusOutForDelivery,
	} {
		t.Run("status "+string(st)+" is 400", func(t *testing.T) {
			e := newInvoiceEnv()
			e.orders.order.OrderStatus = st
			rec := e.do(t, e.userID.String(), e.orderID.String())
			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Contains(t, errorOf(t, rec), "only available for completed")
		})
	}
	t.Run("product lookup failure is 500", func(t *testing.T) {
		e := newInvoiceEnv()
		e.products.namesErr = errors.New("db down")
		rec := e.do(t, e.userID.String(), e.orderID.String())
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		assert.Contains(t, errorOf(t, rec), "failed to generate invoice")
	})
	t.Run("user lookup failure is 500", func(t *testing.T) {
		e := newInvoiceEnv()
		e.users.err = errors.New("db down")
		rec := e.do(t, e.userID.String(), e.orderID.String())
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})
	t.Run("order with no priced lines and no total is refused", func(t *testing.T) {
		e := newInvoiceEnv()
		e.orders.order.TotalPrice = decimal.Zero
		e.orders.products = &[]domain.OrderProductRaw{{ProductID: e.prodID, Quantity: 1}}
		rec := e.do(t, e.userID.String(), e.orderID.String())
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		assert.Contains(t, errorOf(t, rec), "invoice data incomplete")
	})
	t.Run("order without lines is refused", func(t *testing.T) {
		e := newInvoiceEnv()
		e.orders.products = &[]domain.OrderProductRaw{}
		rec := e.do(t, e.userID.String(), e.orderID.String())
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})
}

func assertPDF(t *testing.T, rec *httptest.ResponseRecorder, wantFilename string) {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "application/pdf", rec.Header().Get("Content-Type"))
	assert.Equal(t, `attachment; filename="`+wantFilename+`"`, rec.Header().Get("Content-Disposition"))
	assert.True(t, strings.HasPrefix(rec.Body.String(), "%PDF-"), "body must be a PDF")
}

// invoiceLines returns, in drawing order, the text a reader sees on the PDF returned by the handler.
func invoiceLines(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	return invoicetest.PDFTextLines(t, rec.Body.Bytes())
}

// requireRun asserts that want appears as one contiguous run of printed lines, in that order.
func requireRun(t *testing.T, lines []string, want ...string) {
	t.Helper()
	for i := 0; i+len(want) <= len(lines); i++ {
		if slices.Equal(lines[i:i+len(want)], want) {
			return
		}
	}
	require.Failf(t, "run of invoice lines not found", "want %q in %q", want, lines)
}

func TestDownloadInvoice_Success(t *testing.T) {
	d := decimal.RequireFromString

	t.Run("delivered order returns a French pdf and loads names in the order language", func(t *testing.T) {
		e := newInvoiceEnv()
		e.orders.order.Language = "en"
		rec := e.do(t, e.userID.String(), e.orderID.String())
		assertPDF(t, rec, "facture-02-12-2025-jean-paul-dupont.pdf")
		requireRun(t, invoiceLines(t, rec), "Facture")
		assert.Equal(t, "en", e.products.gotLang)
		assert.Equal(t, []string{e.prodID.String()}, e.products.gotIDs)
	})

	t.Run("picked up order is allowed", func(t *testing.T) {
		e := newInvoiceEnv()
		e.orders.order.OrderStatus = domain.OrderStatusPickedUp
		assertPDF(t, e.do(t, e.userID.String(), e.orderID.String()), "facture-02-12-2025-jean-paul-dupont.pdf")
	})

	t.Run("delivery order with fees, discounts, coupon, choice and address", func(t *testing.T) {
		e := newInvoiceEnv()
		fee := d("2.50")
		street, num, mun, zip, box := "Rue Neuve", "12", "Liège", "4000", "3B"
		cid := uuid.New()
		coupon := "WELCOME"
		o := e.orders.order
		o.OrderType = domain.OrderTypeDelivery
		o.DeliveryFee = &fee
		o.TakeawayDiscount = d("1.00")
		o.CouponDiscount = d("2.00")
		o.CouponCode = &coupon
		o.TotalPrice = d("24.50") // 25.00 of lines - 1.00 - 2.00 + 2.50 delivery
		o.StreetName, o.HouseNumber, o.MunicipalityName, o.Postcode, o.BoxNumber = &street, &num, &mun, &zip, &box
		(*e.orders.products)[0].ProductChoiceID = &cid
		*e.orders.products = append(*e.orders.products, domain.OrderProductRaw{
			ID: uuid.New(), ProductID: e.prodID, Quantity: 1, UnitPrice: d("3.00"), TotalPrice: d("3.00"), VatRateApplied: d("21"),
		}, domain.OrderProductRaw{
			ID: uuid.New(), ProductID: e.prodID, Quantity: 1, UnitPrice: d("1.00"), TotalPrice: d("1.00"), VatRateApplied: d("0"),
		})
		e.products.choice = &productDomain.ProductChoice{Translations: []productDomain.ChoiceTranslation{{Locale: "fr", Name: "Piquant"}}}
		rec := e.do(t, e.userID.String(), e.orderID.String())
		assertPDF(t, rec, "facture-02-12-2025-jean-paul-dupont.pdf")
		lines := invoiceLines(t, rec)
		requireRun(t, lines, "Type de commande: Livraison", "Adresse de livraison: Rue Neuve 12 / 3B, 4000 Liège")
		// Lines (choice name appended, 0 % VAT line printed without VAT), then the money block.
		requireRun(t, lines,
			"A1 — Sushi — Piquant", "2", "10,50 €", "21,00 €",
			"A1 — Sushi", "1", "3,00 €", "3,00 €",
			"A1 — Sushi", "1", "1,00 €", "1,00 €",
			"Sous-total", "25,00 €",
			// VAT is extracted from the gross: 3 × 21/121 = 0.52 and 21 × 6/106 = 1.19; 0 % adds none.
			"TVA (21.00%)", "0,52 €",
			"TVA (6.00%)", "1,19 €",
			"Total TVA", "1,71 €",
			"Remise à emporter", "- 1,00 €",
			"Code promo (WELCOME)", "- 2,00 €",
			"Frais de livraison", "2,50 €",
			"Total", "24,50 €",
		)
	})

	t.Run("choice lookup failure does not block the invoice", func(t *testing.T) {
		e := newInvoiceEnv()
		cid := uuid.New()
		(*e.orders.products)[0].ProductChoiceID = &cid
		e.products.choiceErr = errors.New("gone")
		rec := e.do(t, e.userID.String(), e.orderID.String())
		assertPDF(t, rec, "facture-02-12-2025-jean-paul-dupont.pdf")
		// The line is printed with the bare product name and the right amounts.
		requireRun(t, invoiceLines(t, rec), "A1 — Sushi", "2", "10,50 €", "21,00 €")
	})

	t.Run("choice without translation keeps the bare product name", func(t *testing.T) {
		e := newInvoiceEnv()
		cid := uuid.New()
		(*e.orders.products)[0].ProductChoiceID = &cid
		e.products.choice = &productDomain.ProductChoice{}
		rec := e.do(t, e.userID.String(), e.orderID.String())
		assertPDF(t, rec, "facture-02-12-2025-jean-paul-dupont.pdf")
		requireRun(t, invoiceLines(t, rec), "A1 — Sushi", "2", "10,50 €", "21,00 €")
	})

	t.Run("product without code is accepted", func(t *testing.T) {
		e := newInvoiceEnv()
		e.products.names[0].Code = nil
		rec := e.do(t, e.userID.String(), e.orderID.String())
		assertPDF(t, rec, "facture-02-12-2025-jean-paul-dupont.pdf")
		lines := invoiceLines(t, rec)
		requireRun(t, lines, "Sushi", "2", "10,50 €", "21,00 €")
		assert.NotContains(t, lines, "A1 — Sushi")
	})

	t.Run("zero stored total is recomputed from the line totals", func(t *testing.T) {
		e := newInvoiceEnv()
		fee := d("1.00")
		e.orders.order.TotalPrice = decimal.Zero
		e.orders.order.DeliveryFee = &fee
		rec := e.do(t, e.userID.String(), e.orderID.String())
		assertPDF(t, rec, "facture-02-12-2025-jean-paul-dupont.pdf")
		// 21.00 of lines + 1.00 delivery.
		requireRun(t, invoiceLines(t, rec), "Sous-total", "21,00 €", "TVA (6.00%)", "1,19 €", "Total TVA", "1,19 €",
			"Frais de livraison", "1,00 €", "Total", "22,00 €")
	})

	// BUG(product decision pending): when every stored line total is zero the invoice falls back to
	// unit price x quantity for the subtotal and the total (21,00 / 22,00) but still prints the lines
	// with their stored zero total ("0,00 €", quantity folded into the name), so the printed lines do
	// not add up to the printed subtotal and no VAT is shown. Update this expectation when the
	// owner decides how such a legacy order should be invoiced.
	t.Run("lines with zero totals fall back to unit price times quantity for the totals only", func(t *testing.T) {
		e := newInvoiceEnv()
		fee := d("1.00")
		e.orders.order.TotalPrice = decimal.Zero
		e.orders.order.DeliveryFee = &fee
		(*e.orders.products)[0].TotalPrice = decimal.Zero
		rec := e.do(t, e.userID.String(), e.orderID.String())
		assertPDF(t, rec, "facture-02-12-2025-jean-paul-dupont.pdf")
		lines := invoiceLines(t, rec)
		requireRun(t, lines, "A1 — 2 × Sushi", "1", "0,00 €", "0,00 €")
		requireRun(t, lines, "Sous-total", "21,00 €", "Frais de livraison", "1,00 €", "Total", "22,00 €")
		assert.NotContains(t, lines, "Total TVA")
	})

	t.Run("lines with zero totals and no stored total are refused", func(t *testing.T) {
		e := newInvoiceEnv()
		e.orders.order.TotalPrice = decimal.Zero
		(*e.orders.products)[0].TotalPrice = decimal.Zero
		(*e.orders.products)[0].UnitPrice = decimal.Zero
		rec := e.do(t, e.userID.String(), e.orderID.String())
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})
}

// The invoice is issued in French only (owner decision), whatever language the order was placed in:
// labels, VAT line, dd/mm/yyyy 24 h date and the "facture" file name.
func TestDownloadInvoice_AlwaysFrench(t *testing.T) {
	for _, lang := range []string{"fr", "en", "nl", "zh", "de", ""} {
		t.Run("order language "+lang, func(t *testing.T) {
			e := newInvoiceEnv()
			e.orders.order.Language = lang
			rec := e.do(t, e.userID.String(), e.orderID.String())
			assertPDF(t, rec, "facture-02-12-2025-jean-paul-dupont.pdf")
			lines := invoiceLines(t, rec)
			for _, want := range []string{"Facture", "Sous-total", "TVA (6.00%)", "Total TVA", "TVA comprise", "Merci pour votre commande !"} {
				assert.Contains(t, lines, want)
			}
			for _, forbidden := range []string{"Invoice", "Factuur", "Subtotal", "Subtotaal", "VAT included", "Btw inbegrepen", "Total VAT", "Totaal btw"} {
				assert.NotContains(t, lines, forbidden)
			}
		})
	}
}

func TestVatHelpers(t *testing.T) {
	d := decimal.RequireFromString
	assert.Equal(t, "TVA", vatLabel)
	assert.Equal(t, "", deref(nil))
	s := "x"
	assert.Equal(t, "x", deref(&s))
	assert.True(t, vatAmountFromGross(d("10.60"), d("6")).Equal(d("0.6")))
	assert.True(t, vatAmountFromGross(d("10"), decimal.Zero).IsZero())
}
