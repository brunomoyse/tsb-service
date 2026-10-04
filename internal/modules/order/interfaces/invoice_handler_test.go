package interfaces

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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

func TestDownloadInvoice_Success(t *testing.T) {
	d := decimal.RequireFromString

	t.Run("delivered order returns a localized pdf and loads names in the order language", func(t *testing.T) {
		e := newInvoiceEnv()
		e.orders.order.Language = "en"
		rec := e.do(t, e.userID.String(), e.orderID.String())
		assertPDF(t, rec, "invoice-02-12-2025-jean-paul-dupont.pdf")
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
		o.StreetName, o.HouseNumber, o.MunicipalityName, o.Postcode, o.BoxNumber = &street, &num, &mun, &zip, &box
		(*e.orders.products)[0].ProductChoiceID = &cid
		*e.orders.products = append(*e.orders.products, domain.OrderProductRaw{
			ID: uuid.New(), ProductID: e.prodID, Quantity: 1, UnitPrice: d("3.00"), TotalPrice: d("3.00"), VatRateApplied: d("21"),
		}, domain.OrderProductRaw{
			ID: uuid.New(), ProductID: e.prodID, Quantity: 1, UnitPrice: d("1.00"), TotalPrice: d("1.00"), VatRateApplied: d("0"),
		})
		e.products.choice = &productDomain.ProductChoice{Translations: []productDomain.ChoiceTranslation{{Locale: "fr", Name: "Piquant"}}}
		assertPDF(t, e.do(t, e.userID.String(), e.orderID.String()), "facture-02-12-2025-jean-paul-dupont.pdf")
	})

	t.Run("choice lookup failure does not block the invoice", func(t *testing.T) {
		e := newInvoiceEnv()
		cid := uuid.New()
		(*e.orders.products)[0].ProductChoiceID = &cid
		e.products.choiceErr = errors.New("gone")
		assertPDF(t, e.do(t, e.userID.String(), e.orderID.String()), "facture-02-12-2025-jean-paul-dupont.pdf")
	})

	t.Run("choice without translation keeps the bare product name", func(t *testing.T) {
		e := newInvoiceEnv()
		cid := uuid.New()
		(*e.orders.products)[0].ProductChoiceID = &cid
		e.products.choice = &productDomain.ProductChoice{}
		assertPDF(t, e.do(t, e.userID.String(), e.orderID.String()), "facture-02-12-2025-jean-paul-dupont.pdf")
	})

	t.Run("product without code is accepted", func(t *testing.T) {
		e := newInvoiceEnv()
		e.products.names[0].Code = nil
		assertPDF(t, e.do(t, e.userID.String(), e.orderID.String()), "facture-02-12-2025-jean-paul-dupont.pdf")
	})

	t.Run("zero stored total is recomputed from the line totals", func(t *testing.T) {
		e := newInvoiceEnv()
		fee := d("1.00")
		e.orders.order.TotalPrice = decimal.Zero
		e.orders.order.DeliveryFee = &fee
		assertPDF(t, e.do(t, e.userID.String(), e.orderID.String()), "facture-02-12-2025-jean-paul-dupont.pdf")
	})

	t.Run("lines with zero totals fall back to unit price times quantity", func(t *testing.T) {
		e := newInvoiceEnv()
		fee := d("1.00")
		e.orders.order.TotalPrice = decimal.Zero
		e.orders.order.DeliveryFee = &fee
		(*e.orders.products)[0].TotalPrice = decimal.Zero
		assertPDF(t, e.do(t, e.userID.String(), e.orderID.String()), "facture-02-12-2025-jean-paul-dupont.pdf")
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

func TestVatHelpers(t *testing.T) {
	d := decimal.RequireFromString
	assert.Equal(t, "VAT", vatLabel("en"))
	assert.Equal(t, "TVA", vatLabel("fr"))
	assert.Equal(t, "TVA", vatLabel("nl"))
	assert.Equal(t, "", deref(nil))
	s := "x"
	assert.Equal(t, "x", deref(&s))
	assert.True(t, vatAmountFromGross(d("10.60"), d("6")).Equal(d("0.6")))
	assert.True(t, vatAmountFromGross(d("10"), decimal.Zero).IsZero())
}
