package infrastructure_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/testhelpers"
	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/internal/modules/hubrise/infrastructure"
)

type sourceOrder struct {
	status string
	online bool
	paid   bool
	test   bool
	agoMin int
}

// sourceOrder inserts an order of one Salmon Sushi.
func (e *env) sourceOrder(t *testing.T, o sourceOrder) uuid.UUID {
	t.Helper()
	if o.status == "" {
		o.status = "PENDING"
	}
	var id uuid.UUID
	require.NoError(t, e.db.GetContext(t.Context(), &id, `
		INSERT INTO orders (user_id, order_status, order_type, is_online_payment, is_test, total_price, language, created_at)
		VALUES ($1, $2, 'PICKUP', $3, $4, 12.50, 'fr', now() - make_interval(mins => $5)) RETURNING id`,
		e.fx.RegularUser.ID, o.status, o.online, o.test, o.agoMin))
	_, err := e.db.ExecContext(t.Context(), `
		INSERT INTO order_product (order_id, product_id, unit_price, quantity, total_price, vat_rate_applied)
		VALUES ($1, $2, 12.50, 1, 12.50, 6)`, id, e.fx.SalmonSushi.ID)
	require.NoError(t, err)
	if o.online {
		status := "open"
		if o.paid {
			status = "paid"
		}
		_, err := e.db.ExecContext(t.Context(),
			`INSERT INTO mollie_payments (mollie_payment_id, status, order_id, amount) VALUES ($1, $2, $3, 12.50)`,
			"tr_"+uuid.NewString()[:8], status, id)
		require.NoError(t, err)
	}
	return id
}

// sentLink makes an order a pushed one.
func (e *env) sentLink(t *testing.T, id uuid.UUID, remoteStatus string, pushedAgo time.Duration) {
	t.Helper()
	_, err := e.db.ExecContext(t.Context(), `
		INSERT INTO hubrise_orders (order_id, hubrise_order_id, push_state, last_remote_status, pushed_at)
		VALUES ($1, $2, 'sent', $3, now() - make_interval(secs => $4))`,
		id, "h-"+id.String()[:8], remoteStatus, pushedAgo.Seconds())
	require.NoError(t, err)
}

func TestSourceSnapshotReadsTheWholeOrder(t *testing.T) {
	e := newEnv(t)
	src := infrastructure.NewOrderSource(e.db)
	salmon := e.fx.SalmonSushi.ID
	group := testhelpers.SeedChoiceGroup(t, e.db, salmon, 0, 3, map[string]string{"fr": "Sauce", "en": "Sauce EN"})
	soy := testhelpers.SeedChoice(t, e.db, salmon, group, "0.50", map[string]string{"en": "Soy", "nl": "Soja"})
	bare := testhelpers.SeedChoice(t, e.db, salmon, group, "0", nil)

	var id uuid.UUID
	require.NoError(t, e.db.GetContext(t.Context(), &id, `
		INSERT INTO orders (user_id, order_status, order_type, is_online_payment, channel, total_price, language,
			order_note, coupon_code, cash_payment_amount, delivery_fee, transaction_fee, takeaway_discount, coupon_discount,
			order_extra, street_name, house_number, box_number, postcode, municipality_name, address_lat, address_lng, address_extra)
		VALUES ($1, 'CONFIRMED', 'DELIVERY', false, 'app', 25.00, 'fr', 'ring twice', 'WELCOME', 30, 2.5, 0.3, 1, 0.5,
			'[{"name":"Chopsticks","options":["2"]}]'::jsonb, 'Rue du Test', '12', '3', '4000', 'Liège', 50.6326, 5.5797, 'Second floor')
		RETURNING id`, e.fx.RegularUser.ID))
	var lineID uuid.UUID
	require.NoError(t, e.db.GetContext(t.Context(), &lineID, `
		INSERT INTO order_product (order_id, product_id, unit_price, quantity, total_price, vat_rate_applied)
		VALUES ($1, $2, 12.50, 2, 26.00, 6) RETURNING id`, id, salmon))
	for _, c := range []struct {
		choice uuid.UUID
		qty    int
	}{{soy, 2}, {bare, 1}} {
		_, err := e.db.ExecContext(t.Context(), `INSERT INTO order_product_choices (order_product_id, product_choice_group_id, product_choice_id, quantity)
			VALUES ($1, $2, $3, $4)`, lineID, group, c.choice, c.qty)
		require.NoError(t, err)
	}
	_, err := e.db.ExecContext(t.Context(), `INSERT INTO mollie_payments (mollie_payment_id, status, order_id, amount) VALUES ('tr_old', 'canceled', $1, 25), ('tr_paid', 'paid', $1, 25)`, id)
	require.NoError(t, err)

	snap, err := src.Snapshot(t.Context(), id)
	require.NoError(t, err)
	assert.Equal(t, id, snap.OrderID)
	assert.Equal(t, "CONFIRMED", snap.Status)
	assert.Equal(t, domain.TypeDelivery, snap.Type)
	assert.Equal(t, "app", snap.Channel)
	assert.Equal(t, "ring twice", snap.Note)
	assert.Equal(t, "WELCOME", snap.CouponCode)
	assert.Equal(t, "tr_paid", snap.MolliePaymentID, "the paid payment wins")
	require.NotNil(t, snap.CashAmount)
	assert.Equal(t, "30", snap.CashAmount.String())
	assert.Equal(t, "2.5", snap.DeliveryFee.String())
	assert.Equal(t, "0.3", snap.TransactionFee.String())
	assert.Equal(t, "25", snap.TotalPrice.String())
	assert.Equal(t, []domain.OrderExtra{{Name: "Chopsticks", Options: []string{"2"}}}, snap.Extras)
	assert.Equal(t, "Rue du Test", snap.StreetName)
	assert.Equal(t, "Liège", snap.Municipality)
	require.NotNil(t, snap.AddressLat)
	assert.InDelta(t, 50.6326, *snap.AddressLat, 1e-6)
	assert.Equal(t, e.fx.RegularUser.Email, snap.Email)

	require.Len(t, snap.Lines, 1)
	line := snap.Lines[0]
	assert.Equal(t, salmon, line.ProductID)
	assert.Equal(t, "Sushi au Saumon", line.ProductName, "French first")
	assert.Equal(t, "12.5", line.BasePrice.String())
	assert.EqualValues(t, 2, line.Quantity)
	assert.Equal(t, "26", line.TotalPrice.String())
	assert.Equal(t, "6", line.VatRate.String())
	require.Len(t, line.Selections, 2)
	got := map[uuid.UUID]domain.SnapshotSelection{}
	for _, s := range line.Selections {
		got[s.ChoiceID] = s
		assert.Equal(t, group, s.GroupID)
	}
	assert.Equal(t, "Soy", got[soy].Name, "the first translation in fr, en, nl, zh order")
	assert.Equal(t, "0.5", got[soy].Modifier.String())
	assert.Equal(t, 2, got[soy].Quantity)
	assert.Equal(t, "Sauce", got[soy].GroupName)
	assert.Empty(t, got[bare].Name, "a choice without translations has an empty name")
}

func TestSourceSnapshotToleratesBadDataAndMissingOrders(t *testing.T) {
	e := newEnv(t)
	src := infrastructure.NewOrderSource(e.db)

	_, err := src.Snapshot(t.Context(), uuid.New())
	assert.ErrorIs(t, err, domain.ErrNotFound)
	_, err = src.State(t.Context(), uuid.New())
	assert.ErrorIs(t, err, domain.ErrNotFound)

	id := e.sourceOrder(t, sourceOrder{})
	_, err = e.db.ExecContext(t.Context(), `UPDATE orders SET order_extra = '{"not":"a list"}'::jsonb WHERE id = $1`, id)
	require.NoError(t, err)
	snap, err := src.Snapshot(t.Context(), id)
	require.NoError(t, err, "malformed extras never stop the push")
	assert.Empty(t, snap.Extras)
	assert.Empty(t, snap.Channel, "an order from before the channel column reads as no channel")
	assert.Nil(t, snap.CashAmount)

	// An order without lines has a snapshot too (the payload builder decides what that means).
	_, err = e.db.ExecContext(t.Context(), `DELETE FROM order_product WHERE order_id = $1`, id)
	require.NoError(t, err)
	snap, err = src.Snapshot(t.Context(), id)
	require.NoError(t, err)
	assert.Empty(t, snap.Lines)

	state, err := src.State(t.Context(), id)
	require.NoError(t, err)
	assert.Equal(t, "PENDING", state.Status)
	assert.Equal(t, domain.TypePickUp, state.Type)
	assert.True(t, state.Total.Equal(decimal.RequireFromString("12.5")))
}

func TestSourceOpenUnpushed(t *testing.T) {
	e := newEnv(t)
	src := infrastructure.NewOrderSource(e.db)
	cash := e.sourceOrder(t, sourceOrder{agoMin: 10})
	paid := e.sourceOrder(t, sourceOrder{online: true, paid: true, agoMin: 5})
	e.sourceOrder(t, sourceOrder{online: true}) // unpaid
	e.sourceOrder(t, sourceOrder{test: true})
	for _, s := range []string{"CANCELLED", "FAILED", "DELIVERED", "PICKED_UP"} {
		e.sourceOrder(t, sourceOrder{status: s})
	}
	e.sourceOrder(t, sourceOrder{agoMin: 600}) // too old
	known := e.sourceOrder(t, sourceOrder{})
	e.sentLink(t, known, "new", time.Minute)

	got, err := src.OpenUnpushed(t.Context(), time.Now().Add(-time.Hour), 10)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{cash, paid}, got, "oldest first")

	got, err = src.OpenUnpushed(t.Context(), time.Now().Add(-time.Hour), 1)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{cash}, got, "limited")
}

func TestSourceStatusMismatches(t *testing.T) {
	e := newEnv(t)
	src := infrastructure.NewOrderSource(e.db)
	pairs := []domain.StatusPair{{Local: "CONFIRMED", Remote: "accepted"}, {Local: "PREPARING", Remote: "in_preparation"}}

	behind := e.sourceOrder(t, sourceOrder{status: "CONFIRMED"})
	e.sentLink(t, behind, "new", time.Minute)
	inSync := e.sourceOrder(t, sourceOrder{status: "CONFIRMED"})
	e.sentLink(t, inSync, "accepted", time.Minute)
	finalRemote := e.sourceOrder(t, sourceOrder{status: "PREPARING"})
	e.sentLink(t, finalRemote, "cancelled", time.Minute)
	queued := e.sourceOrder(t, sourceOrder{status: "CONFIRMED"})
	e.sentLink(t, queued, "new", time.Minute)
	_, err := e.db.ExecContext(t.Context(), `INSERT INTO hubrise_outbox (kind, order_id, payload) VALUES ('order_status', $1, '{}')`, queued)
	require.NoError(t, err)
	unmapped := e.sourceOrder(t, sourceOrder{status: "PENDING"})
	e.sentLink(t, unmapped, "new", time.Minute)
	neverRead := e.sourceOrder(t, sourceOrder{status: "PREPARING"})
	e.sentLink(t, neverRead, "", time.Minute)
	_, err = e.db.ExecContext(t.Context(), `UPDATE hubrise_orders SET last_remote_status = NULL WHERE order_id = $1`, neverRead)
	require.NoError(t, err)

	got, err := src.StatusMismatches(t.Context(), pairs, 10)
	require.NoError(t, err)
	var orders []uuid.UUID
	for _, l := range got {
		orders = append(orders, l.OrderID)
	}
	assert.ElementsMatch(t, []uuid.UUID{behind, neverRead}, orders)
	for _, l := range got {
		if l.OrderID == behind {
			assert.Equal(t, "CONFIRMED", l.LocalStatus)
			assert.Equal(t, "new", l.LastRemoteStatus)
			assert.Equal(t, "12.5", l.LocalTotal.String())
			assert.NotEmpty(t, l.HubriseOrderID)
		}
	}

	got, err = src.StatusMismatches(t.Context(), pairs, 1)
	require.NoError(t, err)
	assert.Len(t, got, 1)
}

func TestSourceRefreshCandidatesOldestReadFirst(t *testing.T) {
	e := newEnv(t)
	src := infrastructure.NewOrderSource(e.db)
	read := e.sourceOrder(t, sourceOrder{})
	e.sentLink(t, read, "new", 10*time.Minute)
	_, err := e.db.ExecContext(t.Context(), `UPDATE hubrise_orders SET last_remote_read_at = now() WHERE order_id = $1`, read)
	require.NoError(t, err)
	never := e.sourceOrder(t, sourceOrder{})
	e.sentLink(t, never, "new", 5*time.Minute)
	done := e.sourceOrder(t, sourceOrder{status: "DELIVERED"})
	e.sentLink(t, done, "completed", 5*time.Minute)
	unsent := e.sourceOrder(t, sourceOrder{})
	_, err = e.db.ExecContext(t.Context(), `INSERT INTO hubrise_orders (order_id, push_state) VALUES ($1, 'pending')`, unsent)
	require.NoError(t, err)

	got, err := src.RefreshCandidates(t.Context(), 5)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, never, got[0].OrderID, "an order never read comes first")
	assert.Equal(t, read, got[1].OrderID)
}

func TestSourceStuckNew(t *testing.T) {
	e := newEnv(t)
	src := infrastructure.NewOrderSource(e.db)
	stuck := e.sourceOrder(t, sourceOrder{})
	e.sentLink(t, stuck, "new", 10*time.Minute)
	recent := e.sourceOrder(t, sourceOrder{})
	e.sentLink(t, recent, "new", time.Minute)
	accepted := e.sourceOrder(t, sourceOrder{})
	e.sentLink(t, accepted, "accepted", 10*time.Minute)
	flagged := e.sourceOrder(t, sourceOrder{})
	e.sentLink(t, flagged, "new", 10*time.Minute)
	lesser := e.sourceOrder(t, sourceOrder{})
	e.sentLink(t, lesser, "new", 20*time.Minute)
	_, err := e.db.ExecContext(t.Context(), `
		UPDATE hubrise_orders SET needs_attention = CASE order_id WHEN $1 THEN 'NOT_ACCEPTED' ELSE 'UNLINKED_ITEM' END
		WHERE order_id IN ($1, $2)`, flagged, lesser)
	require.NoError(t, err)
	alerted := e.sourceOrder(t, sourceOrder{})
	e.sentLink(t, alerted, "new", 10*time.Minute)
	_, err = e.db.ExecContext(t.Context(), `UPDATE hubrise_orders SET stuck_alerted_at = now() WHERE order_id = $1`, alerted)
	require.NoError(t, err)
	finished := e.sourceOrder(t, sourceOrder{status: "DELIVERED"})
	e.sentLink(t, finished, "new", 10*time.Minute)

	got, err := src.StuckNew(t.Context(), time.Now().Add(-5*time.Minute), 10)
	require.NoError(t, err)
	var orders []uuid.UUID
	for _, l := range got {
		orders = append(orders, l.OrderID)
	}
	assert.Equal(t, []uuid.UUID{lesser, stuck, flagged}, orders, "oldest first; a lesser flag or a stuck flag whose alert is still to go out does not hide the order, an alert already sent does")
	assert.Equal(t, "UNLINKED_ITEM", got[0].NeedsAttention)

	got, err = src.StuckNew(t.Context(), time.Now().Add(-5*time.Minute), 1)
	require.NoError(t, err)
	assert.Len(t, got, 1)
}

func TestSourceReportsAStoreThatIsDown(t *testing.T) {
	src := infrastructure.NewOrderSource(testhelpers.ClosedDB(t))
	ctx := t.Context()
	id := uuid.New()
	calls := map[string]func() error{
		"snapshot": func() error { _, err := src.Snapshot(ctx, id); return err },
		"state":    func() error { _, err := src.State(ctx, id); return err },
		"open":     func() error { _, err := src.OpenUnpushed(ctx, time.Now(), 1); return err },
		"mismatch": func() error { _, err := src.StatusMismatches(ctx, nil, 1); return err },
		"refresh":  func() error { _, err := src.RefreshCandidates(ctx, 1); return err },
		"stuck":    func() error { _, err := src.StuckNew(ctx, time.Now(), 1); return err },
	}
	for name, call := range calls {
		err := call()
		if assert.Error(t, err, name) {
			assert.NotErrorIs(t, err, domain.ErrNotFound, name)
		}
	}
}

// The line and selection queries run after the order query: a failure of either is an error, not a
// partial snapshot. Every test owns its database, so a table can be renamed away.
func TestSourceSnapshotFailsWhenALineQueryFails(t *testing.T) {
	for name, rename := range map[string]string{
		"lines":      `ALTER TABLE order_product RENAME TO order_product_gone`,
		"selections": `ALTER TABLE order_product_choices RENAME TO order_product_choices_gone`,
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			id := e.sourceOrder(t, sourceOrder{})
			_, err := e.db.ExecContext(t.Context(), rename)
			require.NoError(t, err)
			_, err = infrastructure.NewOrderSource(e.db).Snapshot(t.Context(), id)
			require.Error(t, err)
		})
	}
}
