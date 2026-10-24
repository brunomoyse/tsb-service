package graphql_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/model"
	orderDomain "tsb-service/internal/modules/order/domain"
	"tsb-service/pkg/utils"
)

// statusRecorder is the order status observer: it remembers every change it is told about.
type statusRecorder struct {
	mu    sync.Mutex
	calls []struct {
		orderID   uuid.UUID
		status    orderDomain.OrderStatus
		historyID uuid.UUID
	}
}

func (s *statusRecorder) OrderStatusChanged(_ context.Context, orderID uuid.UUID, status orderDomain.OrderStatus, historyID uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, struct {
		orderID   uuid.UUID
		status    orderDomain.OrderStatus
		historyID uuid.UUID
	}{orderID, status, historyID})
	return nil
}

func (s *statusRecorder) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *statusRecorder) last() (uuid.UUID, orderDomain.OrderStatus, uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.calls[len(s.calls)-1]
	return c.orderID, c.status, c.historyID
}

func (e *covEnv) lastHistoryID(t *testing.T, orderID uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, e.DB.DB.GetContext(t.Context(), &id,
		`SELECT id FROM order_status_history WHERE order_id = $1 ORDER BY changed_at DESC, id DESC LIMIT 1`, orderID))
	return id
}

// The observer hangs on the order service, so the dashboard mutation reaches it without the
// resolver knowing: once per real transition (with the id of its history row), never for an update
// that keeps the status.
func TestUpdateOrderMutationReportsTransitionsToTheStatusObserver(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	obs := &statusRecorder{}
	env.Resolver.OrderService.SetStatusObserver(obs)
	c := env.newPushCustomer(t, "observer", false)
	id := env.seedOrderRow(t, c.id, "PENDING", "PICKUP", "en")

	env.mustUpdateOrder(t, id, map[string]any{"status": "CONFIRMED", "estimatedReadyTime": inMinutes(20)})
	require.Equal(t, 1, obs.count())
	gotID, gotStatus, gotHistory := obs.last()
	assert.Equal(t, id, gotID)
	assert.Equal(t, orderDomain.OrderStatusConfirmed, gotStatus)
	assert.Equal(t, env.lastHistoryID(t, id), gotHistory)

	env.mustUpdateOrder(t, id, map[string]any{"status": "CONFIRMED"})
	env.mustUpdateOrder(t, id, map[string]any{"estimatedReadyTime": inMinutes(45)})
	assert.Equal(t, 1, obs.count(), "no status change, no report")

	env.mustUpdateOrder(t, id, map[string]any{"status": "PREPARING"})
	env.mustUpdateOrder(t, id, map[string]any{"status": "CONFIRMED"})
	assert.Equal(t, 3, obs.count(), "going back is a transition too")
	_, gotStatus, gotHistory = obs.last()
	assert.Equal(t, orderDomain.OrderStatusConfirmed, gotStatus)
	assert.Equal(t, env.lastHistoryID(t, id), gotHistory)
}

// A cancellation that cannot be settled fails before the order changes, so there is nothing to report.
func TestRefusedUpdatesAreNotReportedToTheStatusObserver(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	obs := &statusRecorder{}
	env.Resolver.OrderService.SetStatusObserver(obs)

	_, oerr := env.updateOrderAs(t, uuid.New(), map[string]any{"status": "CONFIRMED"})
	require.NotNil(t, oerr)
	assert.Zero(t, obs.count())
}

// ApplyOrderUpdate is the whole updateOrder orchestration for a caller that is not a GraphQL
// client. With the HubRise origin marker it does everything the dashboard does (the payment is
// settled, the order saved and recorded, the customer told, the dashboards notified) except
// reporting the change to the observer, which would send it straight back to HubRise.
func TestApplyOrderUpdateWithTheHubriseOriginIsNotEchoed(t *testing.T) {
	env := setupCovEnv(t, covOptions{Push: true})
	obs := &statusRecorder{}
	env.Resolver.OrderService.SetStatusObserver(obs)
	c := env.newPushCustomer(t, "origin", true)
	env.registerDevices(t, c, false)

	all, stop := context.WithCancel(t.Context())
	defer stop()
	updatedAll, err := env.Resolver.Subscription().OrderUpdated(all)
	require.NoError(t, err)

	t.Run("a confirmation tells the customer and the dashboards, the observer hears nothing", func(t *testing.T) {
		order := env.placeOrder(t, c, "en")
		id := uuid.MustParse(order.ID)
		env.Mail.WaitSubject(t, c.email, "Order received, awaiting confirmation")
		confirmed := orderDomain.OrderStatusConfirmed
		eta := time.Now().Add(30 * time.Minute)

		got, err := env.Resolver.ApplyOrderUpdate(utils.WithOrigin(env.ctxFor("", false, "en"), utils.OriginHubrise), id,
			model.UpdateOrderInput{Status: &confirmed, EstimatedReadyTime: &eta})

		require.NoError(t, err)
		assert.Equal(t, "CONFIRMED", string(got.Status))
		assert.Equal(t, "CONFIRMED", orderStatusInDB(t, env, id))
		assert.Equal(t, []string{"PENDING", "CONFIRMED"}, env.historyOf(t, id))
		assert.Equal(t, id, recvOrder(t, updatedAll).ID)
		env.Mail.WaitSubject(t, c.email, "Order confirmed")
		require.Eventually(t, func() bool { return len(alertsFor(env.APNs, c.ios, order.ID)) == 1 }, 20*time.Second, 20*time.Millisecond)
		assert.Zero(t, obs.count())
	})

	t.Run("a cancellation refunds a paid order first, the observer still hears nothing", func(t *testing.T) {
		order := env.placeOnlineOrder(t, c)
		env.markPaid(t, order.ID)
		id := uuid.MustParse(order.ID)
		payID := order.Payment.MolliePaymentID
		canceled := orderDomain.OrderStatusCanceled
		reason := orderDomain.OrderCancellationReasonOther

		_, err := env.Resolver.ApplyOrderUpdate(utils.WithOrigin(env.ctxFor("", false, "en"), utils.OriginHubrise), id,
			model.UpdateOrderInput{Status: &canceled, CancellationReason: &reason})

		require.NoError(t, err)
		assert.Equal(t, "CANCELLED", orderStatusInDB(t, env, id))
		assert.Equal(t, []string{"POST /v2/payments/" + payID + "/refunds"}, env.Mollie.CallsMatching("POST /v2/payments/"+payID))
		env.Mail.WaitSubject(t, c.email, "Your refund has been issued")
		assert.Zero(t, obs.count())
	})

	t.Run("a refused cancellation is an error and leaves the order alone", func(t *testing.T) {
		order := env.placeOnlineOrder(t, c)
		env.markPaid(t, order.ID)
		id := uuid.MustParse(order.ID)
		env.Mollie.SetNotRefundable(order.Payment.MolliePaymentID)
		canceled := orderDomain.OrderStatusCanceled

		_, err := env.Resolver.ApplyOrderUpdate(utils.WithOrigin(env.ctxFor("", false, "en"), utils.OriginHubrise), id,
			model.UpdateOrderInput{Status: &canceled})

		require.Error(t, err)
		assert.NotEqual(t, "CANCELLED", orderStatusInDB(t, env, id))
		assert.Zero(t, obs.count())
	})

	t.Run("without the marker the same call is reported", func(t *testing.T) {
		id := env.seedOrderRow(t, c.id, "PENDING", "PICKUP", "en")
		confirmed := orderDomain.OrderStatusConfirmed

		_, err := env.Resolver.ApplyOrderUpdate(env.ctxFor("", false, "en"), id, model.UpdateOrderInput{Status: &confirmed})

		require.NoError(t, err)
		require.Equal(t, 1, obs.count())
		gotID, gotStatus, gotHistory := obs.last()
		assert.Equal(t, id, gotID)
		assert.Equal(t, orderDomain.OrderStatusConfirmed, gotStatus)
		assert.Equal(t, env.lastHistoryID(t, id), gotHistory)
	})
}
