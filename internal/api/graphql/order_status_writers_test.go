package graphql_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/model"
	orderDomain "tsb-service/internal/modules/order/domain"
	"tsb-service/pkg/utils"
)

// Characterization of the writers of orders.order_status. The order service's UpdateOrder is the
// single chokepoint (see TestOrderStatusHasExactlyTwoSQLWriters in the order repository package):
// it is reached from the updateOrder mutation (dashboard and POS handhelds, MCP never writes
// orders), from the Mollie webhook through paymentService.HandlePaymentFailed (covered in
// payment/interfaces/webhook_db_test.go) and from nowhere else; CancelStaleTestOrders is the
// other SQL writer. These tests pin what a status change does today so the later extraction of
// the orchestration and the status observer prove they changed nothing.

var allStatuses = []string{
	"PENDING", "CONFIRMED", "PREPARING", "AWAITING_PICK_UP", "PICKED_UP",
	"OUT_FOR_DELIVERY", "DELIVERED", "CANCELLED", "FAILED",
}

func (e *covEnv) historyOf(t *testing.T, id uuid.UUID) []string {
	t.Helper()
	var out []string
	require.NoError(t, e.DB.DB.SelectContext(t.Context(), &out, `SELECT status FROM order_status_history WHERE order_id = $1 ORDER BY changed_at, id`, id))
	return out
}

// Every status to every status is accepted for an order without a settled payment (cancelling a
// cash order has nothing to settle), stores the new status, appends one history row only when the
// status really changed, and publishes the order on both the dashboard topic and the order topic,
// even when nothing changed.
func TestUpdateOrderEveryTransitionIsStoredRecordedAndPublished(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	c := env.newPushCustomer(t, "matrix", false)

	all, stopAll := context.WithCancel(t.Context())
	defer stopAll()
	updatedAll, err := env.Resolver.Subscription().OrderUpdated(all)
	require.NoError(t, err)

	for _, from := range allStatuses {
		for _, to := range allStatuses {
			t.Run(from+" to "+to, func(t *testing.T) {
				id := env.seedOrderRow(t, c.id, from, "PICKUP", "en")
				mine, err := env.Resolver.Subscription().MyOrderUpdated(env.ctxForCancel(all, c.id.String(), false, "en"), id)
				require.NoError(t, err)

				got := env.mustUpdateOrder(t, id, map[string]any{"status": to})

				assert.Equal(t, to, got.Status)
				assert.Equal(t, to, orderStatusInDB(t, env, id))
				if from == to {
					assert.Empty(t, env.historyOf(t, id), "a no-op update writes no history row")
				} else {
					assert.Equal(t, []string{to}, env.historyOf(t, id), "a change writes exactly one history row")
				}

				pub := recvOrder(t, mine)
				assert.Equal(t, id, pub.ID)
				assert.Equal(t, to, string(pub.Status))
				for {
					p := recvOrder(t, updatedAll)
					if p.ID == id {
						assert.Equal(t, to, string(p.Status))
						break
					}
				}
			})
		}
	}
}

// A POS handheld reaches the very same mutation (updateOrder is @staff, which a POS device
// satisfies); nothing in the orchestration depends on who calls.
func TestUpdateOrderFromAPOSDeviceBehavesLikeFromTheDashboard(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	c := env.newPushCustomer(t, "pos", false)
	id := env.seedOrderRow(t, c.id, "PENDING", "PICKUP", "en")

	all, stop := context.WithCancel(t.Context())
	defer stop()
	updatedAll, err := env.Resolver.Subscription().OrderUpdated(all)
	require.NoError(t, err)

	preparing := orderDomain.OrderStatusPreparing
	ctx := utils.SetIsPOS(env.ctxFor(uuid.NewString(), false, "en"), true)
	got, err := env.Resolver.Mutation().UpdateOrder(ctx, id, model.UpdateOrderInput{Status: &preparing})
	require.NoError(t, err)

	assert.Equal(t, "PREPARING", string(got.Status))
	assert.Equal(t, []string{"PREPARING"}, env.historyOf(t, id))
	assert.Equal(t, id, recvOrder(t, updatedAll).ID)
}

// updatePaymentStatus only writes the payment row: the order keeps its status and its history,
// so it is not a writer of orders.order_status.
func TestUpdatePaymentStatusNeverTouchesTheOrderStatus(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	c := env.newPushCustomer(t, "paywriter", false)
	order := env.placeOnlineOrder(t, c)
	id := uuid.MustParse(order.ID)
	before := env.historyOf(t, id)
	require.Equal(t, "PENDING", orderStatusInDB(t, env, id))

	for _, status := range []string{"paid", "expired", "canceled", "failed", "open"} {
		resp := gqlAs(t, env.TestContext, adminToken(t, env.TestContext), "fr",
			`mutation ($o: ID!, $s: String!) { updatePaymentStatus(orderId: $o, status: $s) { status } }`,
			map[string]any{"o": order.ID, "s": status})
		require.Empty(t, resp.Errors, "%s: %+v", status, resp.Errors)
		assert.Equal(t, "PENDING", orderStatusInDB(t, env, id), status)
		assert.Equal(t, before, env.historyOf(t, id), status)
	}
}
