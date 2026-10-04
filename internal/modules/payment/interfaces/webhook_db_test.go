package interfaces

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"tsb-service/pkg/email/smtptest"

	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/testhelpers"
	couponApplication "tsb-service/internal/modules/coupon/application"
	couponDomain "tsb-service/internal/modules/coupon/domain"
	couponInfra "tsb-service/internal/modules/coupon/infrastructure"
	orderApplication "tsb-service/internal/modules/order/application"
	orderDomain "tsb-service/internal/modules/order/domain"
	orderInfra "tsb-service/internal/modules/order/infrastructure"
	paymentApplication "tsb-service/internal/modules/payment/application"
	paymentDomain "tsb-service/internal/modules/payment/domain"
	paymentInfra "tsb-service/internal/modules/payment/infrastructure"
	productApplication "tsb-service/internal/modules/product/application"
	productInfra "tsb-service/internal/modules/product/infrastructure"
	userApplication "tsb-service/internal/modules/user/application"
	userInfra "tsb-service/internal/modules/user/infrastructure"
	"tsb-service/pkg/db"
	"tsb-service/pkg/pubsub"
)

// dbStack wires the real repositories (PostgreSQL in Docker) and services under
// the webhook handler; only Mollie, SMTP and push are faked.
type dbStack struct {
	t        *testing.T
	sql      *sqlx.DB
	router   *gin.Engine
	mollie   *fakeMollie
	smtp     *smtptest.Server
	notifier *countingNotifier
	broker   *pubsub.Broker
	orders   orderApplication.OrderService
	coupons  couponApplication.CouponService
	payments paymentDomain.PaymentRepository
	fx       *testhelpers.TestFixtures
}

func newDBStack(t *testing.T) *dbStack {
	t.Helper()
	gin.SetMode(gin.TestMode)
	tdb := testhelpers.SetupTestDatabase(t)
	fx := testhelpers.SeedTestData(t, tdb.DB)
	pool := &db.DBPool{Customer: tdb.DB, Admin: tdb.DB}

	fm := &fakeMollie{}
	srv := httptest.NewServer(http.HandlerFunc(fm.handler))
	t.Cleanup(srv.Close)
	client, err := mollie.NewClient(srv.Client(), mollie.NewAPITestingConfig(false))
	require.NoError(t, err)
	require.NoError(t, client.WithAuthenticationValue("test_dummydummydummydummydummydummy"))
	client.BaseURL, _ = url.Parse(srv.URL + "/")

	couponSvc := couponApplication.NewCouponService(couponInfra.NewCouponRepository(pool))
	orderSvc := orderApplication.NewOrderService(orderInfra.NewOrderRepository(pool), couponSvc)
	productSvc := productApplication.NewProductService(productInfra.NewProductRepository(pool))
	userSvc := userApplication.NewUserService(userInfra.NewUserRepository(pool), nil)
	payRepo := paymentInfra.NewPaymentRepository(pool)
	paySvc := paymentApplication.NewPaymentService(payRepo, *client, orderSvc, userSvc, productSvc)

	broker := pubsub.NewBroker()
	t.Cleanup(broker.Shutdown)
	notifier := &countingNotifier{}
	h := NewPaymentHandler(paySvc, broker, notifier)
	router := gin.New()
	router.POST("/webhook", h.UpdatePaymentStatusHandler)

	return &dbStack{
		t: t, sql: tdb.DB, router: router, mollie: fm, smtp: startSMTPSink(t), notifier: notifier,
		broker: broker, orders: orderSvc, coupons: couponSvc, payments: payRepo, fx: fx,
	}
}

// newOnlineOrder creates a PENDING online-payment order with one line of the
// given product and an open Mollie payment for it. When withCoupon is set, a
// coupon use is reserved for the order exactly like CreateOrder does.
func (s *dbStack) newOnlineOrder(productID uuid.UUID, mollieID string, withCoupon bool) (*orderDomain.Order, uuid.UUID) {
	s.t.Helper()
	ctx := s.t.Context()
	// A fresh customer per order: the schema allows one active coupon order per user.
	userID := uuid.New()
	_, err := s.sql.Exec(`INSERT INTO users (id, created_at, updated_at, first_name, last_name, email, zitadel_user_id)
		VALUES ($1::uuid, now(), now(), 'T', 'U', $2, $1::text)`, userID, userID.String()+"@example.test")
	require.NoError(s.t, err)
	var code *string
	var couponID uuid.UUID
	if withCoupon {
		couponID = uuid.New()
		c := "SAVE-" + strings.ToUpper(couponID.String()[:6])
		code = &c
		require.NoError(s.t, s.coupons.CreateCoupon(ctx, &couponDomain.Coupon{
			ID: couponID, Code: c, DiscountType: couponDomain.DiscountTypeFixed,
			DiscountValue: decimal.NewFromInt(2), IsActive: true,
		}))
		ok, err := s.coupons.IncrementUsageAtomic(ctx, couponID, userID)
		require.NoError(s.t, err)
		require.True(s.t, ok)
	}
	o := &orderDomain.Order{
		UserID: userID, OrderStatus: orderDomain.OrderStatusPending, OrderType: orderDomain.OrderTypePickUp,
		IsOnlinePayment: true, Language: "fr", CouponCode: code,
	}
	saved, _, err := s.orders.CreateOrder(ctx, o, &[]orderDomain.OrderProductRaw{{
		ProductID: productID, Quantity: 1, UnitPrice: decimal.RequireFromString("12.50"), TotalPrice: decimal.RequireFromString("12.50"),
	}})
	require.NoError(s.t, err)

	created := time.Now().UTC().Truncate(time.Second)
	require.NoError(s.t, s.payments.Save(ctx, &paymentDomain.MolliePayment{
		MolliePaymentID: mollieID, Status: paymentDomain.PaymentStatusOpen, OrderID: saved.ID,
		IsCancelable: true, Metadata: []byte("null"), Links: []byte("{}"), CreatedAt: created,
		Amount: saved.TotalPrice,
	}))
	return saved, couponID
}

func (s *dbStack) deliver(id string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader("id="+id))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	return w
}

func (s *dbStack) history(orderID uuid.UUID) []string {
	var rows []string
	require.NoError(s.t, s.sql.Select(&rows, `SELECT status FROM order_status_history WHERE order_id = $1 ORDER BY changed_at, id`, orderID))
	return rows
}

func (s *dbStack) orderStatus(orderID uuid.UUID) string {
	var st string
	require.NoError(s.t, s.sql.Get(&st, `SELECT order_status FROM orders WHERE id = $1`, orderID))
	return st
}

func (s *dbStack) paymentStatus(mollieID string) string {
	var st string
	require.NoError(s.t, s.sql.Get(&st, `SELECT status FROM mollie_payments WHERE mollie_payment_id = $1`, mollieID))
	return st
}

func (s *dbStack) couponUses(couponID, userID uuid.UUID) (global, perUser int) {
	require.NoError(s.t, s.sql.Get(&global, `SELECT used_count FROM coupons WHERE id = $1`, couponID))
	err := s.sql.Get(&perUser, `SELECT used_count FROM coupon_users WHERE coupon_id = $1 AND user_id = $2`, couponID, userID)
	require.NoError(s.t, err)
	return global, perUser
}

// TestWebhookWithDatabase runs the webhook against the real repositories: the
// status history rows, order status, payment status and coupon counters are
// read straight from PostgreSQL.
func TestWebhookWithDatabase(t *testing.T) {
	s := newDBStack(t)
	salmon := s.fx.SalmonSushi.ID

	t.Run("paid: order stays PENDING, payment persisted, one email, one push", func(t *testing.T) {
		order, couponID := s.newOnlineOrder(salmon, "tr_db_paid", true)
		s.mollie.set("paid")
		emailsBefore, pushesBefore := s.smtp.Count(), s.notifier.count()

		require.Equal(t, http.StatusOK, s.deliver("tr_db_paid").Code)
		require.Equal(t, "paid", s.paymentStatus("tr_db_paid"))
		require.Equal(t, "PENDING", s.orderStatus(order.ID))
		require.Equal(t, []string{"PENDING"}, s.history(order.ID))
		require.Equal(t, emailsBefore+1, s.smtp.Count())
		require.Equal(t, pushesBefore+1, s.notifier.count())
		g, u := s.couponUses(couponID, order.UserID)
		require.Equal(t, 1, g)
		require.Equal(t, 1, u)

		// Replays change nothing.
		for range 3 {
			require.Contains(t, s.deliver("tr_db_paid").Body.String(), "already processed")
		}
		require.Equal(t, emailsBefore+1, s.smtp.Count())
		require.Equal(t, pushesBefore+1, s.notifier.count())
	})

	t.Run("expired: order CANCELLED with history row, coupon released exactly once", func(t *testing.T) {
		order, couponID := s.newOnlineOrder(salmon, "tr_db_expired", true)
		s.mollie.set("expired")
		emailsBefore := s.smtp.Count()

		require.Equal(t, http.StatusOK, s.deliver("tr_db_expired").Code)
		require.Equal(t, "expired", s.paymentStatus("tr_db_expired"))
		require.Equal(t, "CANCELLED", s.orderStatus(order.ID))
		require.Equal(t, []string{"PENDING", "CANCELLED"}, s.history(order.ID))
		g, u := s.couponUses(couponID, order.UserID)
		require.Equal(t, 0, g, "global coupon counter must be released")
		require.Equal(t, 0, u, "per-user coupon counter must be released")

		for range 3 {
			require.Contains(t, s.deliver("tr_db_expired").Body.String(), "already processed")
		}
		require.Equal(t, []string{"PENDING", "CANCELLED"}, s.history(order.ID))
		g, u = s.couponUses(couponID, order.UserID)
		require.Equal(t, 0, g)
		require.Equal(t, 0, u)
		require.Equal(t, emailsBefore, s.smtp.Count(), "no email for a failed attempt")
	})

	t.Run("failed and canceled behave like expired", func(t *testing.T) {
		for _, st := range []string{"failed", "canceled"} {
			order, couponID := s.newOnlineOrder(salmon, "tr_db_"+st, true)
			s.mollie.set(st)
			require.Equal(t, http.StatusOK, s.deliver("tr_db_"+st).Code)
			require.Equal(t, st, s.paymentStatus("tr_db_"+st))
			require.Equal(t, "CANCELLED", s.orderStatus(order.ID))
			g, _ := s.couponUses(couponID, order.UserID)
			require.Equal(t, 0, g)
		}
	})

	t.Run("a retry after the same payment sequence open, pending, expired keeps one history row", func(t *testing.T) {
		order, _ := s.newOnlineOrder(salmon, "tr_db_seq", false)
		for _, st := range []string{"pending", "expired"} {
			s.mollie.set(st)
			require.Equal(t, http.StatusOK, s.deliver("tr_db_seq").Code)
		}
		require.Equal(t, []string{"PENDING", "CANCELLED"}, s.history(order.ID))
	})

	t.Run("paid for an order staff already cancelled: refunded once, not announced", func(t *testing.T) {
		order, _ := s.newOnlineOrder(salmon, "tr_db_late", false)
		canceled := orderDomain.OrderStatusCanceled
		require.NoError(t, s.orders.UpdateOrder(t.Context(), order.ID, &canceled, nil, nil))
		s.mollie.set("paid")
		pushesBefore := s.notifier.count()
		_, refundsBefore := s.mollie.counts()

		require.Equal(t, http.StatusOK, s.deliver("tr_db_late").Code)
		require.Equal(t, http.StatusOK, s.deliver("tr_db_late").Code)

		_, refunds := s.mollie.counts()
		require.Equal(t, refundsBefore+1, refunds)
		require.Equal(t, "paid", s.paymentStatus("tr_db_late"))
		var refunded decimal.Decimal
		require.NoError(t, s.sql.Get(&refunded, `SELECT amount_refunded FROM mollie_payments WHERE mollie_payment_id = 'tr_db_late'`))
		require.True(t, refunded.Equal(order.TotalPrice), "refunded %s of %s", refunded, order.TotalPrice)
		require.Equal(t, pushesBefore, s.notifier.count())
		require.Equal(t, "CANCELLED", s.orderStatus(order.ID))
	})

	t.Run("unknown payment id returns 200 and touches nothing", func(t *testing.T) {
		before := s.smtp.Count()
		w := s.deliver("tr_db_unknown")
		require.Equal(t, http.StatusOK, w.Code)
		require.Contains(t, w.Body.String(), "unknown payment")
		require.Equal(t, before, s.smtp.Count())
	})

	t.Run("Mollie error returns 500 and leaves the stored status", func(t *testing.T) {
		_, _ = s.newOnlineOrder(salmon, "tr_db_mollie_down", false)
		s.mollie.mu.Lock()
		s.mollie.getCode = http.StatusServiceUnavailable
		s.mollie.mu.Unlock()
		w := s.deliver("tr_db_mollie_down")
		s.mollie.mu.Lock()
		s.mollie.getCode = 0
		s.mollie.mu.Unlock()
		require.Equal(t, http.StatusInternalServerError, w.Code)
		require.Equal(t, "open", s.paymentStatus("tr_db_mollie_down"))
	})

	t.Run("product with a single translation in another language still processes (name fallback)", func(t *testing.T) {
		// A product that only has a Chinese translation: the paid webhook loads
		// product names in the request language (fr) and must fall back.
		productID := uuid.New()
		now := time.Now()
		_, err := s.sql.Exec(`INSERT INTO products (id, created_at, updated_at, category_id, price, is_visible, is_available, code, slug, vat_category)
			VALUES ($1,$2,$2,$3,9.00,true,true,'ZH-ONLY','zh-only-'||$4,'food')`, productID, now, s.fx.SushiCategory.ID, productID.String()[:8])
		require.NoError(t, err)
		_, err = s.sql.Exec(`INSERT INTO product_translations (id, product_id, language, name, description, created_at, updated_at)
			VALUES ($1,$2,'zh','寿司','d',$3,$3)`, uuid.New(), productID, now)
		require.NoError(t, err)

		order, _ := s.newOnlineOrder(productID, "tr_db_zh", false)
		s.mollie.set("paid")
		emailsBefore := s.smtp.Count()
		require.Equal(t, http.StatusOK, s.deliver("tr_db_zh").Code)
		require.Equal(t, "paid", s.paymentStatus("tr_db_zh"))
		require.Equal(t, emailsBefore+1, s.smtp.Count())
		require.Equal(t, "PENDING", s.orderStatus(order.ID))
	})

	t.Run("product without any translation does not break a paid webhook", func(t *testing.T) {
		productID := uuid.New()
		now := time.Now()
		_, err := s.sql.Exec(`INSERT INTO products (id, created_at, updated_at, category_id, price, is_visible, is_available, code, slug, vat_category)
			VALUES ($1,$2,$2,$3,9.00,true,true,'NO-TR','no-tr-'||$4,'food')`, productID, now, s.fx.SushiCategory.ID, productID.String()[:8])
		require.NoError(t, err)

		order, _ := s.newOnlineOrder(productID, "tr_db_notr", false)
		s.mollie.set("paid")
		require.Equal(t, http.StatusOK, s.deliver("tr_db_notr").Code)
		require.Equal(t, "paid", s.paymentStatus("tr_db_notr"))
		require.Equal(t, "PENDING", s.orderStatus(order.ID))
	})

	t.Run("sold-out product does not block a paid webhook", func(t *testing.T) {
		// The customer already paid: availability at webhook time is irrelevant.
		productID := uuid.New()
		now := time.Now()
		_, err := s.sql.Exec(`INSERT INTO products (id, created_at, updated_at, category_id, price, is_visible, is_available, code, slug, vat_category)
			VALUES ($1,$2,$2,$3,9.00,true,true,'SOLDOUT','soldout-'||$4,'food')`, productID, now, s.fx.SushiCategory.ID, productID.String()[:8])
		require.NoError(t, err)
		order, _ := s.newOnlineOrder(productID, "tr_db_soldout", false)
		_, err = s.sql.Exec(`UPDATE products SET is_available = false WHERE id = $1`, productID)
		require.NoError(t, err)

		s.mollie.set("paid")
		w := s.deliver("tr_db_soldout")
		require.Equal(t, http.StatusOK, w.Code)
		require.Equal(t, "paid", s.paymentStatus("tr_db_soldout"))
		require.Equal(t, "PENDING", s.orderStatus(order.ID))
	})

	t.Run("concurrent paid webhooks (real advisory lock) process once", func(t *testing.T) {
		order, _ := s.newOnlineOrder(salmon, "tr_db_conc", false)
		s.mollie.set("paid")
		emailsBefore, pushesBefore := s.smtp.Count(), s.notifier.count()

		var wg sync.WaitGroup
		codes := make([]int, 10)
		start := make(chan struct{})
		for i := range codes {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				codes[i] = s.deliver("tr_db_conc").Code
			}()
		}
		close(start)
		wg.Wait()
		for i, c := range codes {
			require.Equal(t, http.StatusOK, c, "delivery %d", i)
		}
		require.Equal(t, emailsBefore+1, s.smtp.Count())
		require.Equal(t, pushesBefore+1, s.notifier.count())
		require.Equal(t, "paid", s.paymentStatus("tr_db_conc"))
		require.Equal(t, "PENDING", s.orderStatus(order.ID))
	})

	t.Run("concurrent expired webhooks release the coupon once", func(t *testing.T) {
		order, couponID := s.newOnlineOrder(salmon, "tr_db_conc_exp", true)
		s.mollie.set("expired")
		var wg sync.WaitGroup
		for range 10 {
			wg.Go(func() { s.deliver("tr_db_conc_exp") })
		}
		wg.Wait()
		require.Equal(t, []string{"PENDING", "CANCELLED"}, s.history(order.ID))
		g, u := s.couponUses(couponID, order.UserID)
		require.Equal(t, 0, g)
		require.Equal(t, 0, u)
	})
}
