// Package events provides a small in-process publish/subscribe bus used to push
// deploy-event updates to connected SSE clients.
package events

import (
	"sync"

	"github.com/timothydodd/tagalong/internal/model"
)

// Bus fans out deploy events to all active subscribers.
type Bus struct {
	mu   sync.Mutex
	subs map[int]chan model.DeployEvent
	next int
}

// NewBus creates an empty Bus.
func NewBus() *Bus {
	return &Bus{subs: make(map[int]chan model.DeployEvent)}
}

// Subscribe registers a new subscriber and returns its channel and an
// unsubscribe function. The channel is buffered; slow consumers drop events
// rather than blocking publishers.
func (b *Bus) Subscribe() (<-chan model.DeployEvent, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.next
	b.next++
	ch := make(chan model.DeployEvent, 16)
	b.subs[id] = ch
	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if c, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(c)
		}
	}
}

// Publish delivers an event to all subscribers, dropping it for any subscriber
// whose buffer is full.
func (b *Bus) Publish(e model.DeployEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}
