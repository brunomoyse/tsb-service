package graphql_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/model"
	"tsb-service/internal/api/graphql/resolver"
	"tsb-service/internal/api/graphql/testhelpers"
	assistantApplication "tsb-service/internal/modules/assistant/application"
	assistantDomain "tsb-service/internal/modules/assistant/domain"
	"tsb-service/pkg/pubsub"
)

// Every subscription resolver follows one contract: it relays what is published on its topic, and
// nothing else, until its request ends; it then closes its channel and releases the broker. The
// order of a customer's own subscription is checked against its owner before it starts.

// subscriptionContract runs the shared checks against one subscription resolver.
func subscriptionContract[T any](t *testing.T, r *resolver.Resolver, subscribe func(ctx context.Context, r *resolver.Resolver) (<-chan T, error), topic string, good any, check func(T)) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ch, err := subscribe(ctx, r)
	require.NoError(t, err)

	// A message of another type on the topic is skipped; the next good one is relayed.
	r.Broker.Publish(topic, "not what the topic carries")
	deadline := time.After(10 * time.Second)
	received := false
	for !received {
		r.Broker.Publish(topic, good)
		select {
		case got, ok := <-ch:
			require.True(t, ok, "channel closed early")
			check(got)
			received = true
		case <-time.After(30 * time.Millisecond):
		case <-deadline:
			require.FailNow(t, "the published message was never relayed")
		}
	}

	// A subscriber that stops reading does not hold the request: ending it closes the channel.
	for len(ch) == 0 {
		r.Broker.Publish(topic, good)
		time.Sleep(5 * time.Millisecond)
	}
	r.Broker.Publish(topic, good)
	time.Sleep(20 * time.Millisecond) // let the relay block on the full channel
	cancel()
	waitClosed(t, ch, "the channel stayed open after the request ended")

	// Shutting the broker down closes the subscription too.
	other := pubsub.NewBroker()
	copied := *r
	copied.Broker = other
	ch2, err := subscribe(t.Context(), &copied)
	require.NoError(t, err)
	other.Shutdown()
	waitClosed(t, ch2, "the channel stayed open after the broker shut down")
}

// waitClosed drains the channel until it is closed.
func waitClosed[T any](t *testing.T, ch <-chan T, failure string) {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-timeout:
			require.FailNow(t, failure)
		}
	}
}

func TestSubscriptionResolvers(t *testing.T) {
	agent := &fakeAgent{conn: assistantDomain.Connection{State: "connected", Account: "owner"}}
	env := setupCovEnv(t, covOptions{Agent: agent})
	owner, _ := testhelpers.SeedCustomer(t, env.DB.DB, "subscriber")
	orderID := env.seedOrderRow(t, owner, "CONFIRMED", "PICKUP", "en")
	r := env.Resolver

	t.Run("orderCreated", func(t *testing.T) {
		msg := &model.Order{ID: uuid.New()}
		subscriptionContract(t, r, func(ctx context.Context, r *resolver.Resolver) (<-chan *model.Order, error) {
			return r.Subscription().OrderCreated(ctx)
		},
			"orderCreated", msg, func(got *model.Order) { assert.Equal(t, msg.ID, got.ID) })
	})
	t.Run("orderUpdated", func(t *testing.T) {
		msg := &model.Order{ID: uuid.New()}
		subscriptionContract(t, r, func(ctx context.Context, r *resolver.Resolver) (<-chan *model.Order, error) {
			return r.Subscription().OrderUpdated(ctx)
		},
			"orderUpdated", msg, func(got *model.Order) { assert.Equal(t, msg.ID, got.ID) })
	})
	t.Run("myOrderUpdated relays the topic of the caller's own order", func(t *testing.T) {
		msg := &model.Order{ID: orderID}
		subscriptionContract(t, r, func(ctx context.Context, r *resolver.Resolver) (<-chan *model.Order, error) {
			ctx = env.ctxForCancel(ctx, owner.String(), false, "en")
			return r.Subscription().MyOrderUpdated(ctx, orderID)
		}, "orderUpdated:"+orderID.String(), msg, func(got *model.Order) { assert.Equal(t, orderID, got.ID) })
	})
	t.Run("productUpdated", func(t *testing.T) {
		msg := &model.Product{ID: uuid.New()}
		subscriptionContract(t, r, func(ctx context.Context, r *resolver.Resolver) (<-chan *model.Product, error) {
			return r.Subscription().ProductUpdated(ctx)
		},
			"productUpdated", msg, func(got *model.Product) { assert.Equal(t, msg.ID, got.ID) })
	})
	t.Run("couponUpdated", func(t *testing.T) {
		msg := &model.Coupon{ID: uuid.New(), Code: "LIVE"}
		subscriptionContract(t, r, func(ctx context.Context, r *resolver.Resolver) (<-chan *model.Coupon, error) {
			return r.Subscription().CouponUpdated(ctx)
		},
			"couponUpdated", msg, func(got *model.Coupon) { assert.Equal(t, "LIVE", got.Code) })
	})
	t.Run("restaurantConfigUpdated", func(t *testing.T) {
		msg := &model.RestaurantConfig{PreparationMinutes: 25}
		subscriptionContract(t, r, func(ctx context.Context, r *resolver.Resolver) (<-chan *model.RestaurantConfig, error) {
			return r.Subscription().RestaurantConfigUpdated(ctx)
		}, "restaurantConfigUpdated", msg, func(got *model.RestaurantConfig) { assert.Equal(t, 25, got.PreparationMinutes) })
	})
	t.Run("scheduleOverridesUpdated", func(t *testing.T) {
		msg := []*model.ScheduleOverride{{Closed: true}}
		subscriptionContract(t, r, func(ctx context.Context, r *resolver.Resolver) (<-chan []*model.ScheduleOverride, error) {
			return r.Subscription().ScheduleOverridesUpdated(ctx)
		}, "scheduleOverridesUpdated", msg, func(got []*model.ScheduleOverride) { require.Len(t, got, 1); assert.True(t, got[0].Closed) })
	})
	t.Run("assistantConnectionUpdated", func(t *testing.T) {
		msg := assistantDomain.Connection{State: "expired", EverConnected: true}
		subscriptionContract(t, r, func(ctx context.Context, r *resolver.Resolver) (<-chan *model.AssistantConnection, error) {
			return r.Subscription().AssistantConnectionUpdated(ctx)
		}, assistantApplication.Topic, msg, func(got *model.AssistantConnection) {
			assert.True(t, got.Enabled)
			assert.Equal(t, model.AssistantConnectionStateExpired, got.State)
			assert.True(t, got.EverConnected)
		})
	})

	t.Run("myOrderUpdated refuses somebody else's order and an unknown one", func(t *testing.T) {
		stranger, _ := testhelpers.SeedCustomer(t, env.DB.DB, "stranger")
		_, err := r.Subscription().MyOrderUpdated(env.ctxFor(stranger.String(), false, "en"), orderID)
		require.ErrorContains(t, err, "FORBIDDEN: order does not belong to caller")
		_, err = r.Subscription().MyOrderUpdated(env.ctxFor(owner.String(), false, "en"), uuid.New())
		require.ErrorContains(t, err, "NOT_FOUND: order not found")
	})
}
