package repository

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/testhelpers"
	"tsb-service/internal/modules/order/domain"
)

func TestOrderRepositoryListings(t *testing.T) {
	e := newEnv(t)
	salmon := e.fx.SalmonSushi.ID
	alice, _ := testhelpers.SeedCustomer(t, e.tdb.DB, "Alice")
	bob, _ := testhelpers.SeedCustomer(t, e.tdb.DB, "Bobby")
	forUser := func(u uuid.UUID, typ domain.OrderType, status domain.OrderStatus, online bool, mutate func(*domain.Order)) *domain.Order {
		return e.save(t, e.newOrder(status, func(o *domain.Order) {
			o.UserID, o.OrderType, o.IsOnlinePayment = u, typ, online
			if mutate != nil {
				mutate(o)
			}
		}), e.line(salmon, 1, "10.00", "10.00"))
	}

	cash1 := forUser(alice, domain.OrderTypePickUp, domain.OrderStatusConfirmed, false, nil)
	cash2 := forUser(alice, domain.OrderTypeDelivery, domain.OrderStatusDelivered, false, nil)
	paid := forUser(bob, domain.OrderTypePickUp, domain.OrderStatusPickedUp, true, nil)
	unpaid := forUser(bob, domain.OrderTypePickUp, domain.OrderStatusPending, true, nil)
	cancelled := forUser(bob, domain.OrderTypePickUp, domain.OrderStatusCanceled, false, nil)
	test := forUser(alice, domain.OrderTypePickUp, domain.OrderStatusConfirmed, false, func(o *domain.Order) { o.IsTest = true })
	e.pay(t, paid.ID, "paid")
	e.pay(t, unpaid.ID, "open")
	// Deterministic chronology: cash1 is the oldest (10h ago), test the newest (5h ago).
	for i, o := range []*domain.Order{cash1, cash2, paid, unpaid, cancelled, test} {
		e.backdate(t, o.ID, time.Duration(10-i)*time.Hour)
	}
	ids := func(os []*domain.Order) []uuid.UUID {
		out := make([]uuid.UUID, len(os))
		for i, o := range os {
			out[i] = o.ID
		}
		return out
	}

	t.Run("FindPaginated for staff hides unpaid online orders and test orders, newest first", func(t *testing.T) {
		got, err := e.repo.FindPaginated(e.ctx, 1, 50, nil)
		require.NoError(t, err)
		assert.Equal(t, []uuid.UUID{cancelled.ID, paid.ID, cash2.ID, cash1.ID}, ids(got))
	})

	t.Run("FindPaginated for a customer keeps their own test orders but not their unpaid online ones", func(t *testing.T) {
		got, err := e.repo.FindPaginated(e.ctx, 1, 50, &alice)
		require.NoError(t, err)
		assert.Equal(t, []uuid.UUID{test.ID, cash2.ID, cash1.ID}, ids(got))
		got, err = e.repo.FindPaginated(e.ctx, 1, 50, &bob)
		require.NoError(t, err)
		assert.Equal(t, []uuid.UUID{cancelled.ID, paid.ID}, ids(got))
	})

	t.Run("FindPaginated clamps page and limit", func(t *testing.T) {
		got, err := e.repo.FindPaginated(e.ctx, 0, 0, nil) // page 1, limit 10
		require.NoError(t, err)
		assert.Len(t, got, 4)

		first, err := e.repo.FindPaginated(e.ctx, 1, 3, nil)
		require.NoError(t, err)
		second, err := e.repo.FindPaginated(e.ctx, 2, 3, nil)
		require.NoError(t, err)
		assert.Equal(t, []uuid.UUID{cancelled.ID, paid.ID, cash2.ID}, ids(first))
		assert.Equal(t, []uuid.UUID{cash1.ID}, ids(second))
		beyond, err := e.repo.FindPaginated(e.ctx, 3, 3, nil)
		require.NoError(t, err)
		assert.Empty(t, beyond)
	})

	t.Run("FindFiltered always excludes cancelled and test orders and summarises what it lists", func(t *testing.T) {
		got, sum, err := e.repo.FindFiltered(e.ctx, domain.OrderHistoryFilter{})
		require.NoError(t, err)
		// cash1, cash2, paid and unpaid: filtered by status and test flag, not by payment.
		assert.ElementsMatch(t, []uuid.UUID{cash1.ID, cash2.ID, paid.ID, unpaid.ID}, ids(got))
		assert.Equal(t, 4, sum.TotalOrders)
		assert.True(t, dec(sum.TotalRevenue).Equal(dec("40")), sum.TotalRevenue)
		assert.True(t, dec(sum.AverageOrder).Equal(dec("10")), sum.AverageOrder)
	})

	t.Run("FindFiltered applies every filter and paginates", func(t *testing.T) {
		now := time.Now()
		start, end := now.Add(-9*time.Hour-30*time.Minute), now.Add(-7*time.Hour-30*time.Minute) // cash2 (9h) and paid (8h)
		st := domain.OrderStatusDelivered
		typ := domain.OrderTypeDelivery
		search := "alic"

		got, sum, err := e.repo.FindFiltered(e.ctx, domain.OrderHistoryFilter{StartDate: &start, EndDate: &end})
		require.NoError(t, err)
		assert.ElementsMatch(t, []uuid.UUID{cash2.ID, paid.ID}, ids(got))
		assert.Equal(t, 2, sum.TotalOrders)

		got, _, err = e.repo.FindFiltered(e.ctx, domain.OrderHistoryFilter{Status: &st})
		require.NoError(t, err)
		assert.Equal(t, []uuid.UUID{cash2.ID}, ids(got))

		got, _, err = e.repo.FindFiltered(e.ctx, domain.OrderHistoryFilter{OrderType: &typ})
		require.NoError(t, err)
		assert.Equal(t, []uuid.UUID{cash2.ID}, ids(got))

		got, sum, err = e.repo.FindFiltered(e.ctx, domain.OrderHistoryFilter{Search: &search})
		require.NoError(t, err)
		assert.ElementsMatch(t, []uuid.UUID{cash1.ID, cash2.ID}, ids(got))
		assert.Equal(t, 2, sum.TotalOrders)

		empty := ""
		got, _, err = e.repo.FindFiltered(e.ctx, domain.OrderHistoryFilter{Search: &empty})
		require.NoError(t, err)
		assert.Len(t, got, 4, "an empty search is ignored")

		p1, sum, err := e.repo.FindFiltered(e.ctx, domain.OrderHistoryFilter{Page: 1, Limit: 3})
		require.NoError(t, err)
		p2, _, err := e.repo.FindFiltered(e.ctx, domain.OrderHistoryFilter{Page: 2, Limit: 3})
		require.NoError(t, err)
		assert.Len(t, p1, 3)
		assert.Len(t, p2, 1)
		assert.Equal(t, 4, sum.TotalOrders, "the summary covers all pages")
	})

	t.Run("FindFiltered on an empty window returns a zero summary", func(t *testing.T) {
		far := time.Now().Add(24 * time.Hour)
		got, sum, err := e.repo.FindFiltered(e.ctx, domain.OrderHistoryFilter{StartDate: &far})
		require.NoError(t, err)
		assert.Empty(t, got)
		assert.Equal(t, 0, sum.TotalOrders)
		assert.True(t, dec(sum.TotalRevenue).IsZero())
	})

	t.Run("GetCustomerStats aggregates per customer and ignores cancelled orders", func(t *testing.T) {
		rows, err := e.repo.GetCustomerStats(e.ctx, nil, nil, nil, nil)
		require.NoError(t, err)
		by := map[uuid.UUID]*domain.CustomerStatsRow{}
		for _, r := range rows {
			by[r.UserID] = r
		}
		a, b := by[alice], by[bob]
		require.NotNil(t, a)
		require.NotNil(t, b)
		assert.Equal(t, 3, a.TotalOrders) // cash1, cash2, test (test orders are not filtered here)
		assert.True(t, a.TotalAmount.Equal(dec("30.00")))
		assert.True(t, a.AverageAmount.Equal(dec("10.00")))
		assert.Equal(t, 1, a.DeliveryCount)
		assert.Equal(t, 2, a.PickupCount)
		assert.Equal(t, "Alice", a.LastName)
		assert.Equal(t, 2, b.TotalOrders) // paid + unpaid; cancelled ignored
		assert.True(t, a.FirstOrderDate.Before(a.LastOrderDate))
		// Ordered by total amount, descending.
		assert.GreaterOrEqual(t, rows[0].TotalAmount.Cmp(rows[len(rows)-1].TotalAmount), 0)
	})

	t.Run("GetCustomerStats filters by date, order type and minimum orders", func(t *testing.T) {
		start, end := time.Now().Add(-9*time.Hour-30*time.Minute), time.Now().Add(-7*time.Hour-30*time.Minute)
		typ := string(domain.OrderTypeDelivery)
		min2, min1 := 2, 1

		rows, err := e.repo.GetCustomerStats(e.ctx, &start, &end, nil, nil)
		require.NoError(t, err)
		require.Len(t, rows, 2) // cash2 (alice) and paid (bob)

		rows, err = e.repo.GetCustomerStats(e.ctx, nil, nil, &typ, nil)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		assert.Equal(t, alice, rows[0].UserID)
		assert.Equal(t, 1, rows[0].TotalOrders)

		rows, err = e.repo.GetCustomerStats(e.ctx, nil, nil, nil, &min2)
		require.NoError(t, err)
		for _, r := range rows {
			assert.GreaterOrEqual(t, r.TotalOrders, 2)
		}
		assert.Len(t, rows, 2)

		withOne, err := e.repo.GetCustomerStats(e.ctx, nil, nil, nil, &min1)
		require.NoError(t, err)
		all, err := e.repo.GetCustomerStats(e.ctx, nil, nil, nil, nil)
		require.NoError(t, err)
		assert.Len(t, withOne, len(all), "minOrders <= 1 does not filter")
	})
}
