package application_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise/domain"
	pkghubrise "tsb-service/pkg/hubrise"
	"tsb-service/pkg/hubrise/hubrisetest"
)

// HubRise answers 400 and 422 for a request it will never accept as it is: the worker parks the row
// instead of retrying forever (section 6.1 of the plan), for every kind of row, and a human retry
// resumes it. The mapping of the statuses to a kind is tested in pkg/hubrise; this drives the worker.
func TestRowsHubRiseRefusesAsInvalidAreParkedWhateverTheKind(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnprocessableEntity} {
		t.Run(fmt.Sprintf("an order status PATCH answered %d", status), func(t *testing.T) {
			e := newEnv(t)
			e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
			id := e.order(orderSpec{})
			e.ready(id)
			e.drainAll()
			require.Equal(t, domain.PushSent, e.link(id).PushState)

			e.srv.AddFault(hubrisetest.Fault{Method: http.MethodPatch, Path: e.ordersPath(), Status: status, Times: 1})
			e.setStatus(id, "CONFIRMED")
			e.drainAll()

			rows := e.outboxRows(id, domain.OutboxOrderStatus)
			require.Len(t, rows, 1)
			assert.Equal(t, domain.OutboxParked, rows[0].State)
			require.NotNil(t, rows[0].LastError)
			assert.Contains(t, *rows[0].LastError, fmt.Sprint(status))
			assert.EqualValues(t, 1, rows[0].Attempts, "parked at the first answer, not retried")
			link := e.link(id)
			require.NotNil(t, link.NeedsAttention)
			assert.Equal(t, domain.AttentionPushParked, *link.NeedsAttention)
			assert.Equal(t, domain.PushSent, link.PushState, "the order itself reached HubRise")
			assert.Equal(t, pkghubrise.StatusNew, e.remote(id).Status, "the refused PATCH changed nothing")

			n, err := e.mod.Outbox.RetryParked(t.Context(), rows[0].ID, e.clock.Now())
			require.NoError(t, err)
			require.EqualValues(t, 1, n)
			e.drainAll()
			assert.Equal(t, domain.OutboxSent, e.outboxRows(id, domain.OutboxOrderStatus)[0].State)
			assert.Equal(t, pkghubrise.StatusAccepted, e.remote(id).Status, "the manual retry is sent")
		})

		t.Run(fmt.Sprintf("an order create answered %d", status), func(t *testing.T) {
			e := newEnv(t)
			e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
			id := e.order(orderSpec{})
			e.ready(id)
			e.srv.AddFault(hubrisetest.Fault{Method: http.MethodPost, Path: e.ordersPath(), Status: status, Times: 1})

			e.drainAll()

			create := e.outboxRows(id, domain.OutboxOrderCreate)[0]
			assert.Equal(t, domain.OutboxParked, create.State)
			assert.EqualValues(t, 1, create.Attempts)
			assert.Equal(t, domain.PushParked, e.link(id).PushState)
			assert.Empty(t, e.srv.Orders())
		})

		t.Run(fmt.Sprintf("a customer update answered %d", status), func(t *testing.T) {
			e := newCustEnv(t)
			e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
			e.placed(orderSpec{})
			e.drainAll()
			require.Len(t, e.srv.Customers(), 1)

			e.srv.AddFault(hubrisetest.Fault{Method: http.MethodPatch, Path: customersPath, Status: status, Times: 1})
			hopper := "Hopper"
			_, err := e.users.UpdateMe(t.Context(), e.user.String(), nil, &hopper, nil, nil, nil, nil, nil)
			require.NoError(t, err)
			e.drainAll()

			rows := e.customerRows(domain.OutboxCustomer)
			require.Len(t, rows, 1)
			assert.Equal(t, domain.OutboxParked, rows[0].State)
			require.NotNil(t, rows[0].LastError)
			assert.Contains(t, *rows[0].LastError, fmt.Sprint(status))
			assert.EqualValues(t, 1, rows[0].Attempts)
			assert.NotEqual(t, "Hopper", e.srv.Customers()[0].LastName)

			n, err := e.mod.Outbox.RetryParked(t.Context(), rows[0].ID, e.clock.Now())
			require.NoError(t, err)
			require.EqualValues(t, 1, n)
			e.drainAll()
			assert.Equal(t, domain.OutboxSent, e.customerRows(domain.OutboxCustomer)[0].State)
			assert.Equal(t, "Hopper", e.srv.Customers()[0].LastName)
		})
	}
}
