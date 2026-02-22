package pubsub

import (
	"slices"
	"sync"
)

type Broker struct {
	mu     sync.RWMutex
	topics map[string][]chan any
}

func NewBroker() *Broker {
	return &Broker{topics: make(map[string][]chan any)}
}

func (b *Broker) Subscribe(topic string) <-chan any {
	ch := make(chan any, 1)
	b.mu.Lock()
	b.topics[topic] = append(b.topics[topic], ch)
	b.mu.Unlock()
	return ch
}

func (b *Broker) Publish(topic string, msg any) {
	b.mu.RLock()
	subs := b.topics[topic]
	b.mu.RUnlock()
	for _, ch := range subs {
		select {
		case ch <- msg:
		default:
		}
	}
}

func (b *Broker) Unsubscribe(topic string, subCh <-chan any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	list := b.topics[topic]
	for i, ch := range list {
		if ch == subCh {
			close(ch)
			b.topics[topic] = slices.Delete(list, i, i+1)
			break
		}
	}
}

func (b *Broker) Shutdown() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for topic, subs := range b.topics {
		for _, ch := range subs {
			close(ch)
		}
		delete(b.topics, topic)
	}
}
