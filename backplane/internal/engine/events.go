package engine

import (
	"sync"
	"time"

	"safisolutions.org/backplane/internal/providers"
	"safisolutions.org/backplane/internal/providers/cloudflare"
)

// Event is pushed to the UI over server-sent events.
type Event struct {
	Type    string    `json:"type"` // log, run, op, check, report, monitor, toast
	Project string    `json:"project,omitempty"`
	Env     string    `json:"env,omitempty"`
	RunID   string    `json:"runId,omitempty"`
	At      time.Time `json:"at"`
	Data    any       `json:"data,omitempty"`
}

// Bus fans events out to subscribers. Slow subscribers drop events rather
// than block the engine.
type Bus struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

// NewBus creates a bus.
func NewBus() *Bus { return &Bus{subs: map[chan Event]struct{}{}} }

// Subscribe returns a channel of events and an unsubscribe func.
func (b *Bus) Subscribe() (chan Event, func()) {
	ch := make(chan Event, 256)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		if _, ok := b.subs[ch]; ok {
			delete(b.subs, ch)
			close(ch)
		}
		b.mu.Unlock()
	}
}

// Publish sends an event to every subscriber.
func (b *Bus) Publish(ev Event) {
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func dnsFor(c *providers.Conn) providers.DNSManager { return cloudflare.DNS{Conn: c} }
