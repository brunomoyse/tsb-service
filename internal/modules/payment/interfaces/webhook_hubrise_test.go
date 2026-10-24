package interfaces

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise"
	hubtest "tsb-service/internal/modules/hubrise/testhelpers"
	"tsb-service/pkg/hubrise/hubrisetest"
)

// readyRecorder is an OrderReadyHook that remembers its calls.
type readyRecorder struct {
	mu  sync.Mutex
	ids []uuid.UUID
}

func (r *readyRecorder) OrderReady(_ context.Context, id uuid.UUID) {
	r.mu.Lock()
	r.ids = append(r.ids, id)
	r.mu.Unlock()
}

func (r *readyRecorder) calls() []uuid.UUID {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]uuid.UUID(nil), r.ids...)
}

// pushHook adapts the HubRise order pusher to the webhook's hook the way the resolver does.
type pushHook struct{ push *hubrise.Push }

func (h pushHook) OrderReady(ctx context.Context, id uuid.UUID) {
	_ = h.push.Pusher.OrderReady(ctx, id)
}

func TestWebhookTellsTheHookWhenAnOrderIsPaid(t *testing.T) {
	rec := &readyRecorder{}
	s := newDBStackWithHook(t, rec)
	salmon := s.fx.SalmonSushi.ID

	t.Run("paid: told once, however often Mollie redelivers", func(t *testing.T) {
		order, _ := s.newOnlineOrder(salmon, "tr_hook_paid", false)
		s.mollie.set("paid")
		require.Equal(t, http.StatusOK, s.deliver("tr_hook_paid").Code)
		for range 3 {
			require.Contains(t, s.deliver("tr_hook_paid").Body.String(), "already processed")
		}
		assert.Equal(t, []uuid.UUID{order.ID}, rec.calls())
	})
	t.Run("an unpaid outcome never tells it", func(t *testing.T) {
		before := len(rec.calls())
		for _, st := range []string{"open", "pending", "expired", "failed", "canceled"} {
			s.newOnlineOrder(salmon, "tr_hook_"+st, false)
			s.mollie.set(st)
			require.Equal(t, http.StatusOK, s.deliver("tr_hook_"+st).Code)
		}
		assert.Len(t, rec.calls(), before)
	})
	t.Run("an order staff already cancelled is refunded, not announced", func(t *testing.T) {
		before := len(rec.calls())
		order, _ := s.newOnlineOrder(salmon, "tr_hook_late", false)
		s.mollie.set("paid")
		_, err := s.sql.Exec(`UPDATE orders SET order_status = 'CANCELLED' WHERE id = $1`, order.ID)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, s.deliver("tr_hook_late").Code)
		assert.Len(t, rec.calls(), before)
	})
}

// A paid order reaches HubRise once: the webhook may be delivered several times and the hook is
// idempotent on its own.
func TestPaidWebhookQueuesOneHubriseOrderCreate(t *testing.T) {
	srv := hubrisetest.New(t)
	var stack *dbStack
	hook := &lateHook{}
	stack = newDBStackWithHook(t, hook)
	cfg := hubrise.Config{
		ClientID: hubrisetest.ClientID, ClientSecret: "secret", TokenKey: hubtest.TestTokenKey,
		APIBaseURL: srv.APIBaseURL(), OAuthBaseURL: srv.OAuthBaseURL(),
	}
	mod, err := hubrise.NewModule(cfg, stack.sql, nil)
	require.NoError(t, err)
	hubtest.SeedConnection(t, stack.sql, hubtest.WithToken(hubrisetest.Token), hubtest.WithPushOrdersSince(time.Now().Add(-time.Hour)))
	push := mod.NewPush(hubrise.PushOptions{})
	hook.set(pushHook{push})

	order, _ := stack.newOnlineOrder(stack.fx.SalmonSushi.ID, "tr_hubrise_paid", false)
	count := func() int {
		var n int
		require.NoError(t, stack.sql.Get(&n, `SELECT count(*) FROM hubrise_outbox WHERE order_id = $1 AND kind = 'order_create'`, order.ID))
		return n
	}
	assert.Zero(t, count(), "an unpaid order is not pushed")

	stack.mollie.set("paid")
	for range 3 {
		require.Equal(t, http.StatusOK, stack.deliver("tr_hubrise_paid").Code)
	}
	assert.Equal(t, 1, count())

	// Even a hook called again by hand (a reconciler, a retry) queues nothing more.
	for range 3 {
		pushHook{push}.OrderReady(t.Context(), order.ID)
	}
	assert.Equal(t, 1, count())
	assert.Empty(t, srv.Requests(), "queueing never calls HubRise")
}

// lateHook lets the stack be built before the module that needs its database exists.
type lateHook struct {
	mu   sync.Mutex
	hook OrderReadyHook
}

func (l *lateHook) set(h OrderReadyHook) { l.mu.Lock(); l.hook = h; l.mu.Unlock() }

func (l *lateHook) OrderReady(ctx context.Context, id uuid.UUID) {
	l.mu.Lock()
	h := l.hook
	l.mu.Unlock()
	if h != nil {
		h.OrderReady(ctx, id)
	}
}
