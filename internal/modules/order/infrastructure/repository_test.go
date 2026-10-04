package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/testhelpers"
	"tsb-service/internal/modules/order/domain"
	"tsb-service/pkg/db"
	"tsb-service/pkg/types"
	"tsb-service/pkg/utils"
)

var dec = decimal.RequireFromString

// closedRepo returns a repository whose connections are already closed, so every query fails
// with a driver error: the way to exercise the error branches without a flaky database.
func closedRepo(t *testing.T) domain.OrderRepository {
	t.Helper()
	conn := testhelpers.ClosedDB(t)
	return NewOrderRepository(&db.DBPool{Customer: conn, Admin: conn})
}

type env struct {
	tdb    *testhelpers.TestDatabase
	repo   domain.OrderRepository
	fx     *testhelpers.TestFixtures
	ctx    context.Context
	userID uuid.UUID
}

func newEnv(t *testing.T) *env {
	t.Helper()
	tdb := testhelpers.SetupTestDatabase(t)
	fx := testhelpers.SeedTestData(t, tdb.DB)
	pool := &db.DBPool{Customer: tdb.DB, Admin: tdb.DB}
	return &env{tdb: tdb, repo: NewOrderRepository(pool), fx: fx, ctx: t.Context(), userID: fx.RegularUser.ID}
}

func (e *env) newOrder(status domain.OrderStatus, mutate func(*domain.Order)) *domain.Order {
	o := &domain.Order{
		UserID:      e.userID,
		OrderStatus: status,
		OrderType:   domain.OrderTypePickUp,
		Language:    "fr",
	}
	if mutate != nil {
		mutate(o)
	}
	return o
}

func (e *env) save(t *testing.T, o *domain.Order, lines ...domain.OrderProductRaw) *domain.Order {
	t.Helper()
	saved, _, err := e.repo.Save(e.ctx, o, &lines)
	require.NoError(t, err)
	return saved
}

func (e *env) line(product uuid.UUID, qty int64, unit, total string) domain.OrderProductRaw {
	return domain.OrderProductRaw{ProductID: product, Quantity: qty, UnitPrice: dec(unit), TotalPrice: dec(total), VatRateApplied: dec("6")}
}

func (e *env) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	_, err := e.tdb.DB.ExecContext(e.ctx, q, args...)
	require.NoError(t, err, q)
}

func (e *env) backdate(t *testing.T, orderID uuid.UUID, age time.Duration) {
	e.exec(t, `UPDATE orders SET created_at = now() - make_interval(secs => $2) WHERE id = $1`, orderID, age.Seconds())
}

func (e *env) pay(t *testing.T, orderID uuid.UUID, status string) {
	e.exec(t, `INSERT INTO mollie_payments (mollie_payment_id, status, order_id, amount) VALUES ($1, $2, $3, 10)`,
		"tr_"+uuid.NewString()[:8], status, orderID)
}

func TestOrderRepository(t *testing.T) {
	e := newEnv(t)
	salmon, tuna, tea := e.fx.SalmonSushi.ID, e.fx.TunaSushi.ID, e.fx.GreenTea.ID

	t.Run("Save prices the order, stores lines and selections, and FindByID returns them sorted", func(t *testing.T) {
		group := testhelpers.SeedChoiceGroup(t, e.tdb.DB, salmon, 0, 2, map[string]string{"fr": "Sauce"})
		choice := testhelpers.SeedChoice(t, e.tdb.DB, salmon, group, "0.50", map[string]string{"fr": "Soja"})
		fee := dec("2.50")
		place := "place-1"
		lat, lng, dist := 50.6, 5.5, 1200.0
		o := e.newOrder(domain.OrderStatusPending, func(o *domain.Order) {
			o.OrderType = domain.OrderTypeDelivery
			o.IsOnlinePayment = true
			o.DeliveryFee = &fee
			o.TakeawayDiscount = dec("1.00")
			o.CouponDiscount = dec("0.00")
			o.TransactionFee = dec("0.32")
			o.AddressPlaceID, o.AddressLat, o.AddressLng, o.AddressDistance = &place, &lat, &lng, &dist
			o.OrderExtra = types.NullableJSON(`[{"name":"chopsticks","quantity":2}]`)
			o.IsTest = false
		})
		drinkLine := e.line(tea, 1, "3.50", "3.50")
		sushiLine := e.line(salmon, 2, "13.00", "26.00")
		sushiLine.ProductChoiceID = &choice
		sushiLine.Selections = []domain.OrderProductSelection{{GroupID: group, ChoiceID: choice, Quantity: 1}}

		saved := e.save(t, o, drinkLine, sushiLine)
		assert.NotEqual(t, uuid.Nil, saved.ID)
		assert.False(t, saved.CreatedAt.IsZero())
		// 3.50 + 26.00 + 2.50 - 1.00 + 0.32 = 31.32, snapped to 0.10 by domain.OrderTotal
		assert.True(t, saved.TotalPrice.Equal(domain.OrderTotal(dec("29.50"), fee, dec("1.00"), decimal.Zero, dec("0.32"))), saved.TotalPrice.String())

		got, lines, err := e.repo.FindByID(e.ctx, saved.ID)
		require.NoError(t, err)
		assert.Equal(t, domain.OrderTypeDelivery, got.OrderType)
		assert.True(t, got.TotalPrice.Equal(saved.TotalPrice))
		assert.True(t, got.DeliveryFee.Equal(fee))
		assert.Equal(t, "place-1", *got.AddressPlaceID)
		assert.JSONEq(t, `[{"name":"chopsticks","quantity":2}]`, string(got.OrderExtra))
		require.Len(t, *lines, 2)
		// Sorted by category order: sushi (category 1) before drinks (category 2).
		assert.Equal(t, salmon, (*lines)[0].ProductID)
		assert.Equal(t, tea, (*lines)[1].ProductID)
		assert.Equal(t, int64(2), (*lines)[0].Quantity)
		assert.True(t, (*lines)[0].VatRateApplied.Equal(dec("6")))
		require.NotNil(t, (*lines)[0].ProductChoiceID)
		assert.Equal(t, choice, *(*lines)[0].ProductChoiceID)
		assert.Equal(t, []domain.OrderProductSelection{{GroupID: group, ChoiceID: choice, Quantity: 1}}, (*lines)[0].Selections)
		assert.Empty(t, (*lines)[1].Selections)
	})

	t.Run("Save without lines keeps the caller supplied total", func(t *testing.T) {
		o := e.newOrder(domain.OrderStatusConfirmed, func(o *domain.Order) { o.TotalPrice = dec("12.30") })
		saved, lines, err := e.repo.Save(e.ctx, o, nil)
		require.NoError(t, err)
		assert.Nil(t, lines)
		assert.True(t, saved.TotalPrice.Equal(dec("12.30")))
		got, gotLines, err := e.repo.FindByID(e.ctx, saved.ID)
		require.NoError(t, err)
		assert.True(t, got.TotalPrice.Equal(dec("12.30")))
		assert.Empty(t, *gotLines)
	})

	t.Run("Save rejects an unknown customer", func(t *testing.T) {
		o := e.newOrder(domain.OrderStatusPending, func(o *domain.Order) { o.UserID = uuid.New() })
		_, _, err := e.repo.Save(e.ctx, o, &[]domain.OrderProductRaw{})
		require.ErrorContains(t, err, "failed to insert order")
	})

	t.Run("Save rolls back everything when a line references an unknown product", func(t *testing.T) {
		before := e.count(t, "orders")
		o := e.newOrder(domain.OrderStatusPending, nil)
		_, _, err := e.repo.Save(e.ctx, o, &[]domain.OrderProductRaw{e.line(uuid.New(), 1, "1.00", "1.00")})
		require.ErrorContains(t, err, "failed to insert order product")
		assert.Equal(t, before, e.count(t, "orders"), "the order row must be rolled back")
	})

	t.Run("Save rolls back when a selection references an unknown choice", func(t *testing.T) {
		before := e.count(t, "orders")
		l := e.line(salmon, 1, "1.00", "1.00")
		l.Selections = []domain.OrderProductSelection{{GroupID: uuid.New(), ChoiceID: uuid.New(), Quantity: 1}}
		_, _, err := e.repo.Save(e.ctx, e.newOrder(domain.OrderStatusPending, nil), &[]domain.OrderProductRaw{l})
		require.ErrorContains(t, err, "failed to insert order product selection")
		assert.Equal(t, before, e.count(t, "orders"))
	})

	t.Run("Update changes status, estimate and cancellation reason", func(t *testing.T) {
		saved := e.save(t, e.newOrder(domain.OrderStatusPending, nil), e.line(tuna, 1, "14.00", "14.00"))
		eta := time.Now().Add(30 * time.Minute).UTC().Truncate(time.Second)
		reason := domain.OrderCancellationReasonOutOfStock
		saved.OrderStatus = domain.OrderStatusCanceled
		saved.EstimatedReadyTime = &eta
		saved.CancellationReason = &reason
		require.NoError(t, e.repo.Update(e.ctx, saved))

		got, _, err := e.repo.FindByID(e.ctx, saved.ID)
		require.NoError(t, err)
		assert.Equal(t, domain.OrderStatusCanceled, got.OrderStatus)
		require.NotNil(t, got.EstimatedReadyTime)
		assert.True(t, eta.Equal(*got.EstimatedReadyTime))
		require.NotNil(t, got.CancellationReason)
		assert.Equal(t, reason, *got.CancellationReason)
	})

	t.Run("FindByID reports an unknown order", func(t *testing.T) {
		got, lines, err := e.repo.FindByID(e.ctx, uuid.New())
		require.ErrorContains(t, err, "failed to query order")
		assert.Nil(t, got)
		assert.Nil(t, lines)
	})

	t.Run("FindByID orders lines by translation of the request language, falling back", func(t *testing.T) {
		cat := testhelpers.SeedCategory(t, e.tdb.DB, 50, map[string]string{"fr": "Zeta"})
		pb := testhelpers.SeedProduct(t, e.tdb.DB, testhelpers.ProductSpec{CategoryID: cat, Code: "Z1", Price: "1.00", Names: map[string]string{"fr": "Beta", "en": "Alpha"}})
		pa := testhelpers.SeedProduct(t, e.tdb.DB, testhelpers.ProductSpec{CategoryID: cat, Code: "Z1", Price: "1.00", Names: map[string]string{"fr": "Alpha", "en": "Beta"}})
		saved := e.save(t, e.newOrder(domain.OrderStatusPending, nil), e.line(pb, 1, "1.00", "1.00"), e.line(pa, 1, "1.00", "1.00"))
		// Same category and code: the tie is broken by the name in the request language.
		_, fr, err := e.repo.FindByID(utils.SetLang(e.ctx, "fr"), saved.ID)
		require.NoError(t, err)
		assert.Equal(t, []uuid.UUID{pa, pb}, []uuid.UUID{(*fr)[0].ProductID, (*fr)[1].ProductID})
		_, en, err := e.repo.FindByID(utils.SetLang(e.ctx, "en"), saved.ID)
		require.NoError(t, err)
		assert.Equal(t, []uuid.UUID{pb, pa}, []uuid.UUID{(*en)[0].ProductID, (*en)[1].ProductID})
	})

	t.Run("FindByOrderIDs groups lines and selections per order", func(t *testing.T) {
		group := testhelpers.SeedChoiceGroup(t, e.tdb.DB, tuna, 0, 3, map[string]string{"fr": "Extra"})
		c1 := testhelpers.SeedChoice(t, e.tdb.DB, tuna, group, "0.00", map[string]string{"fr": "Gingembre"})
		c2 := testhelpers.SeedChoice(t, e.tdb.DB, tuna, group, "1.00", map[string]string{"fr": "Wasabi"})
		l := e.line(tuna, 1, "14.00", "14.00")
		l.Selections = []domain.OrderProductSelection{{GroupID: group, ChoiceID: c1, Quantity: 1}, {GroupID: group, ChoiceID: c2, Quantity: 2}}
		o1 := e.save(t, e.newOrder(domain.OrderStatusPending, nil), l, e.line(tea, 2, "3.50", "7.00"))
		o2 := e.save(t, e.newOrder(domain.OrderStatusPending, nil), e.line(salmon, 1, "12.50", "12.50"))
		o3 := e.save(t, e.newOrder(domain.OrderStatusPending, nil))

		res, err := e.repo.FindByOrderIDs(e.ctx, []string{o1.ID.String(), o2.ID.String(), o3.ID.String()})
		require.NoError(t, err)
		require.Len(t, res[o1.ID.String()], 2)
		require.Len(t, res[o2.ID.String()], 1)
		assert.NotContains(t, res, o3.ID.String(), "an order without lines has no entry")
		assert.Equal(t, tuna, res[o1.ID.String()][0].ProductID)
		assert.ElementsMatch(t, l.Selections, res[o1.ID.String()][0].Selections)
		assert.True(t, res[o1.ID.String()][1].TotalPrice.Equal(dec("7.00")))
		assert.Equal(t, salmon, res[o2.ID.String()][0].ProductID)
	})

	t.Run("FindByOrderIDs with no ids returns an empty map without querying", func(t *testing.T) {
		res, err := closedRepo(t).FindByOrderIDs(e.ctx, nil)
		require.NoError(t, err)
		assert.Empty(t, res)
	})

	t.Run("FindByUserIDs groups orders by user, newest first", func(t *testing.T) {
		other, _ := testhelpers.SeedCustomer(t, e.tdb.DB, "other")
		older := e.save(t, e.newOrder(domain.OrderStatusDelivered, func(o *domain.Order) { o.UserID = other }))
		newer := e.save(t, e.newOrder(domain.OrderStatusPending, func(o *domain.Order) { o.UserID = other }))
		e.backdate(t, older.ID, 2*time.Hour)

		res, err := e.repo.FindByUserIDs(e.ctx, []string{other.String(), uuid.NewString()})
		require.NoError(t, err)
		require.Len(t, res, 1)
		require.Len(t, res[other.String()], 2)
		assert.Equal(t, newer.ID, res[other.String()][0].ID)
		assert.Equal(t, older.ID, res[other.String()][1].ID)
	})

	t.Run("FindByUserIDs rejects an empty id list", func(t *testing.T) {
		_, err := e.repo.FindByUserIDs(e.ctx, nil)
		require.Error(t, err)
	})

	t.Run("InsertStatusHistory and FindStatusHistoryByOrderID keep chronological order", func(t *testing.T) {
		saved := e.save(t, e.newOrder(domain.OrderStatusPending, nil))
		for _, st := range []domain.OrderStatus{domain.OrderStatusPending, domain.OrderStatusConfirmed, domain.OrderStatusPreparing} {
			require.NoError(t, e.repo.InsertStatusHistory(e.ctx, saved.ID, st))
		}
		hist, err := e.repo.FindStatusHistoryByOrderID(e.ctx, saved.ID)
		require.NoError(t, err)
		require.Len(t, hist, 3)
		assert.Equal(t, domain.OrderStatusPending, hist[0].Status)
		assert.Equal(t, domain.OrderStatusConfirmed, hist[1].Status)
		assert.Equal(t, domain.OrderStatusPreparing, hist[2].Status)
		assert.Equal(t, saved.ID, hist[0].OrderID)

		none, err := e.repo.FindStatusHistoryByOrderID(e.ctx, uuid.New())
		require.NoError(t, err)
		assert.Empty(t, none)

		require.Error(t, e.repo.InsertStatusHistory(e.ctx, uuid.New(), domain.OrderStatusPending), "unknown order violates the foreign key")
	})

	t.Run("HasActiveCouponOrder only counts non-terminal orders holding a coupon", func(t *testing.T) {
		u, _ := testhelpers.SeedCustomer(t, e.tdb.DB, "coupon")
		has, err := e.repo.HasActiveCouponOrder(e.ctx, u)
		require.NoError(t, err)
		assert.False(t, has)

		code := "WELCOME"
		for _, st := range []domain.OrderStatus{domain.OrderStatusDelivered, domain.OrderStatusPickedUp, domain.OrderStatusCanceled, domain.OrderStatusFailed} {
			e.save(t, e.newOrder(st, func(o *domain.Order) { o.UserID = u; o.CouponCode = &code }))
		}
		e.save(t, e.newOrder(domain.OrderStatusPending, func(o *domain.Order) { o.UserID = u })) // active but no coupon
		has, err = e.repo.HasActiveCouponOrder(e.ctx, u)
		require.NoError(t, err)
		assert.False(t, has)

		e.save(t, e.newOrder(domain.OrderStatusPreparing, func(o *domain.Order) { o.UserID = u; o.CouponCode = &code }))
		has, err = e.repo.HasActiveCouponOrder(e.ctx, u)
		require.NoError(t, err)
		assert.True(t, has)
	})

	t.Run("UpdateActiveOrdersLanguage skips terminal orders and other users", func(t *testing.T) {
		u, _ := testhelpers.SeedCustomer(t, e.tdb.DB, "lang")
		active := e.save(t, e.newOrder(domain.OrderStatusOutForDelivery, func(o *domain.Order) { o.UserID = u; o.OrderType = domain.OrderTypeDelivery }))
		done := e.save(t, e.newOrder(domain.OrderStatusDelivered, func(o *domain.Order) { o.UserID = u }))
		stranger := e.save(t, e.newOrder(domain.OrderStatusPending, nil))

		got, err := e.repo.UpdateActiveOrdersLanguage(e.ctx, u, "nl")
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, active.ID, got[0].ID)
		assert.Equal(t, "nl", got[0].Language)
		assert.Equal(t, domain.OrderStatusOutForDelivery, got[0].OrderStatus)
		assert.Equal(t, domain.OrderTypeDelivery, got[0].OrderType)
		assert.Equal(t, u, got[0].UserID)

		for id, want := range map[uuid.UUID]string{done.ID: "fr", stranger.ID: "fr", active.ID: "nl"} {
			o, _, err := e.repo.FindByID(e.ctx, id)
			require.NoError(t, err)
			assert.Equal(t, want, o.Language)
		}

		none, err := e.repo.UpdateActiveOrdersLanguage(e.ctx, uuid.New(), "en")
		require.NoError(t, err)
		assert.Empty(t, none)
	})

	t.Run("CancelStaleTestOrders cancels only old, non-terminal test orders", func(t *testing.T) {
		code := "TESTCODE"
		stale := e.save(t, e.newOrder(domain.OrderStatusPending, func(o *domain.Order) { o.IsTest = true; o.CouponCode = &code }))
		fresh := e.save(t, e.newOrder(domain.OrderStatusPending, func(o *domain.Order) { o.IsTest = true }))
		terminal := e.save(t, e.newOrder(domain.OrderStatusFailed, func(o *domain.Order) { o.IsTest = true }))
		real := e.save(t, e.newOrder(domain.OrderStatusPending, nil))
		for _, id := range []uuid.UUID{stale.ID, terminal.ID, real.ID} {
			e.backdate(t, id, 3*time.Hour)
		}

		refs, err := e.repo.CancelStaleTestOrders(e.ctx, time.Hour)
		require.NoError(t, err)
		require.Len(t, refs, 1)
		assert.Equal(t, stale.ID, refs[0].ID)
		assert.Equal(t, e.userID, refs[0].UserID)
		require.NotNil(t, refs[0].CouponCode)
		assert.Equal(t, code, *refs[0].CouponCode)

		check := func(id uuid.UUID, want domain.OrderStatus) {
			o, _, err := e.repo.FindByID(e.ctx, id)
			require.NoError(t, err)
			assert.Equal(t, want, o.OrderStatus)
		}
		check(stale.ID, domain.OrderStatusCanceled)
		check(fresh.ID, domain.OrderStatusPending)
		check(terminal.ID, domain.OrderStatusFailed)
		check(real.ID, domain.OrderStatusPending)
		o, _, _ := e.repo.FindByID(e.ctx, stale.ID)
		require.NotNil(t, o.CancellationReason)
		assert.Equal(t, domain.OrderCancellationReasonOther, *o.CancellationReason)
	})

	t.Run("DeleteOrder removes the order with its lines, selections and history", func(t *testing.T) {
		group := testhelpers.SeedChoiceGroup(t, e.tdb.DB, tuna, 0, 1, map[string]string{"fr": "G"})
		choice := testhelpers.SeedChoice(t, e.tdb.DB, tuna, group, "0", map[string]string{"fr": "C"})
		l := e.line(tuna, 1, "14.00", "14.00")
		l.Selections = []domain.OrderProductSelection{{GroupID: group, ChoiceID: choice, Quantity: 1}}
		saved := e.save(t, e.newOrder(domain.OrderStatusPending, nil), l)
		require.NoError(t, e.repo.InsertStatusHistory(e.ctx, saved.ID, domain.OrderStatusPending))
		keep := e.save(t, e.newOrder(domain.OrderStatusPending, nil), e.line(tuna, 1, "14.00", "14.00"))

		require.NoError(t, e.repo.DeleteOrder(e.ctx, saved.ID))

		_, _, err := e.repo.FindByID(e.ctx, saved.ID)
		require.Error(t, err)
		assert.Equal(t, 0, e.countWhere(t, "order_product", "order_id = $1", saved.ID))
		assert.Equal(t, 0, e.countWhere(t, "order_status_history", "order_id = $1", saved.ID))
		assert.Equal(t, 1, e.countWhere(t, "order_product", "order_id = $1", keep.ID))
		require.NoError(t, e.repo.DeleteOrder(e.ctx, uuid.New()), "deleting an unknown order is a no-op")
	})
}

func (e *env) count(t *testing.T, from string) int {
	t.Helper()
	var n int
	require.NoError(t, e.tdb.DB.GetContext(e.ctx, &n, "SELECT count(*) FROM "+from))
	return n
}

func (e *env) countWhere(t *testing.T, table, where string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, e.tdb.DB.GetContext(e.ctx, &n, "SELECT count(*) FROM "+table+" WHERE "+where, args...))
	return n
}
