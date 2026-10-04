package pubsub

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func recv(t *testing.T, ch <-chan any) (any, bool) {
	t.Helper()
	select {
	case v, ok := <-ch:
		return v, ok
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a message")
		return nil, false
	}
}

func TestPublishReachesEverySubscriberOfTheTopicOnly(t *testing.T) {
	b := NewBroker()
	a1, a2 := b.Subscribe("orders"), b.Subscribe("orders")
	other := b.Subscribe("payments")

	b.Publish("orders", "o1")

	for _, ch := range []<-chan any{a1, a2} {
		v, ok := recv(t, ch)
		assert.True(t, ok)
		assert.Equal(t, "o1", v)
	}
	select {
	case v := <-other:
		t.Fatalf("a subscriber of another topic received %v", v)
	default:
	}
	b.Publish("nobody", "x") // no subscribers: no panic, no block
}

func TestPublishNeverBlocksOnASlowSubscriber(t *testing.T) {
	b := NewBroker()
	slow := b.Subscribe("t") // buffer of one, never drained
	fast := b.Subscribe("t")

	done := make(chan struct{})
	go func() {
		b.Publish("t", 1)
		b.Publish("t", 2) // the slow subscriber's buffer is full: this one is dropped for it
		b.Publish("t", 3)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a full subscriber")
	}

	v, _ := recv(t, slow)
	assert.Equal(t, 1, v, "the slow subscriber kept its first message and lost the rest")
	v, _ = recv(t, fast)
	assert.Equal(t, 1, v, "the other subscriber is not affected by the slow one")
}

func TestUnsubscribeClosesOnlyThatChannel(t *testing.T) {
	b := NewBroker()
	keep, drop := b.Subscribe("t"), b.Subscribe("t")

	b.Unsubscribe("t", drop)

	_, ok := recv(t, drop)
	assert.False(t, ok, "the unsubscribed channel is closed")
	b.Publish("t", "after")
	v, ok := recv(t, keep)
	assert.True(t, ok)
	assert.Equal(t, "after", v)
	assert.Len(t, b.topics["t"], 1)

	b.Unsubscribe("t", drop)             // already gone: no double close
	b.Unsubscribe("unknown", keep)       // unknown topic
	b.Unsubscribe("t", make(<-chan any)) // a channel that was never subscribed
	b.Publish("t", "still works")
	v, _ = recv(t, keep)
	assert.Equal(t, "still works", v)
}

func TestShutdownClosesEverything(t *testing.T) {
	b := NewBroker()
	subs := []<-chan any{b.Subscribe("a"), b.Subscribe("a"), b.Subscribe("b")}

	b.Shutdown()

	for _, ch := range subs {
		_, ok := recv(t, ch)
		assert.False(t, ok)
	}
	assert.Empty(t, b.topics)
	assert.NotPanics(t, func() { b.Publish("a", "late") }, "publishing after shutdown is harmless")
	assert.NotPanics(t, b.Shutdown, "shutting down twice is harmless")
}

func TestConcurrentPublishSubscribeUnsubscribe(t *testing.T) {
	b := NewBroker()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for i := range 200 {
				b.Publish("t", i)
			}
		})
		wg.Go(func() {
			for range 50 {
				ch := b.Subscribe("t")
				select {
				case <-ch:
				default:
				}
				b.Unsubscribe("t", ch)
			}
		})
	}
	wg.Wait()
	b.Shutdown()
	require.Empty(t, b.topics)
}
