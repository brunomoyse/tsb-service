package repository

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/order/domain"
)

func strPtr(s string) *string { return &s }

// orders.channel is nullable and unconstrained: orders placed before the column existed read back
// with a nil channel through every path that loads a domain.Order, and a new order keeps the channel
// it was placed from.
func TestOrderChannelRoundTripsAndOldOrdersReadAsNil(t *testing.T) {
	e := newEnv(t)
	salmon := e.fx.SalmonSushi.ID

	web := e.save(t, e.newOrder(domain.OrderStatusConfirmed, func(o *domain.Order) { o.Channel = strPtr("web") }), e.line(salmon, 1, "10.00", "10.00"))
	app := e.save(t, e.newOrder(domain.OrderStatusConfirmed, func(o *domain.Order) { o.Channel = strPtr("app") }), e.line(salmon, 1, "10.00", "10.00"))
	old := e.save(t, e.newOrder(domain.OrderStatusConfirmed, nil), e.line(salmon, 1, "10.00", "10.00"))
	e.exec(t, `UPDATE orders SET channel = NULL WHERE id = $1`, old.ID) // as written before the migration

	channelOf := func(o *domain.Order) string {
		if o.Channel == nil {
			return "<nil>"
		}
		return *o.Channel
	}
	want := map[string]string{web.ID.String(): "web", app.ID.String(): "app", old.ID.String(): "<nil>"}

	t.Run("FindByID", func(t *testing.T) {
		for id, ch := range map[string]*domain.Order{web.ID.String(): web, app.ID.String(): app, old.ID.String(): old} {
			got, _, err := e.repo.FindByID(e.ctx, ch.ID)
			require.NoError(t, err)
			assert.Equal(t, want[id], channelOf(got))
		}
	})
	t.Run("FindPaginated", func(t *testing.T) {
		got, err := e.repo.FindPaginated(e.ctx, 1, 50, nil)
		require.NoError(t, err)
		require.Len(t, got, 3)
		for _, o := range got {
			assert.Equal(t, want[o.ID.String()], channelOf(o))
		}
	})
	t.Run("FindFiltered", func(t *testing.T) {
		got, _, err := e.repo.FindFiltered(e.ctx, domain.OrderHistoryFilter{Page: 1, Limit: 50})
		require.NoError(t, err)
		require.Len(t, got, 3)
		for _, o := range got {
			assert.Equal(t, want[o.ID.String()], channelOf(o))
		}
	})
	t.Run("FindByUserIDs", func(t *testing.T) {
		got, err := e.repo.FindByUserIDs(e.ctx, []string{e.userID.String()})
		require.NoError(t, err)
		orders := got[e.userID.String()]
		require.Len(t, orders, 3)
		for _, o := range orders {
			assert.Equal(t, want[o.ID.String()], channelOf(o))
		}
	})
}
