package pubsub

import "sync"

type Broker struct {
	mu     sync.RWMutex
	topics map[string][]chan interface{}
}

func NewBroker() *Broker {
	return &Broker{topics: make(map[string][]chan interface{})}
}

func (b *Broker) Subscribe(topic string) <-chan interface{} {
	ch := make(chan interface{}, 1)
	b.mu.Lock()
	b.topics[topic] = append(b.topics[topic], ch)
	b.mu.Unlock()
	return ch
}

func (b *Broker) Publish(topic string, msg interface{}) {
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

func (b *Broker) Unsubscribe(topic string, subCh <-chan interface{}) {
	b.mu.Lock()
	defer b.mu.Unlock()
	list := b.topics[topic]
	for i, ch := range list {
		if ch == subCh {
			b.topics[topic] = append(list[:i], list[i+1:]...)
			close(ch)
			break
		}
	}
}
