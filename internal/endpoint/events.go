package endpoint

import "sync"

// Event kinds. They name what changed, never what it changed to: a subscriber
// re-reads the affected resource, so the stream stays a notification channel
// rather than a second, subtly different copy of the API.
const (
	EventRequest = "request" // a callback arrived
	EventCreated = "created"
	EventUpdated = "updated" // renamed, or its spec replaced
	EventDeleted = "deleted"
	EventReset   = "reset" // captured history cleared
)

// Reaches reports whether caller is in the event's audience.
func (e Event) Reaches(caller string) bool {
	for _, who := range e.Audience {
		if who == caller {
			return true
		}
	}
	return false
}

// Event is one change worth telling a live console about.
type Event struct {
	Type     string `json:"type"`
	Endpoint string `json:"endpoint"`
	// Audience is everyone the endpoint is visible to: its owner and whoever
	// it is shared with. It never reaches a subscriber — it is what the
	// stream handler filters on, so one account's traffic is not announced to
	// another.
	Audience []string `json:"-"`
	// Received is the endpoint's lifetime counter at the time of the event.
	// It is what the listing shows, so carrying it lets a subscriber update a
	// counter without a round trip.
	Received int64 `json:"received"`
}

// broker fans events out to subscribers, each of which is one open stream.
//
// Delivery is best-effort by design. A callback has to be answered whatever
// the console is doing, so a subscriber that is not draining its buffer loses
// events rather than blocking the request that produced them — and since an
// event only says that something changed, a dropped one costs latency, not
// correctness.
type broker struct {
	mu   sync.Mutex
	next int64
	subs map[int64]chan Event
}

// subscribe returns a channel of events and a function that stops the
// subscription. The channel is closed by that function, never by the broker,
// so the caller owns its lifetime.
func (b *broker) subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 32)

	b.mu.Lock()
	if b.subs == nil {
		b.subs = make(map[int64]chan Event)
	}
	id := b.next
	b.next++
	b.subs[id] = ch
	b.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			// Under the same lock publish holds, so a send can never race a
			// close.
			b.mu.Lock()
			defer b.mu.Unlock()
			delete(b.subs, id)
			close(ch)
		})
	}
}

func (b *broker) publish(ev Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- ev:
		default: // a subscriber that cannot keep up loses this one
		}
	}
}

// subscribers is the number of open subscriptions, for tests.
func (b *broker) subscribers() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}

// Subscribe registers for changes to any endpoint in the registry. Call the
// returned function to unsubscribe; it is safe to call more than once.
func (r *Registry) Subscribe() (<-chan Event, func()) {
	return r.events.subscribe()
}

// Subscribers reports how many live subscriptions the registry has.
func (r *Registry) Subscribers() int { return r.events.subscribers() }

func (r *Registry) publish(e *Endpoint, kind string) {
	r.events.publish(Event{
		Type:     kind,
		Endpoint: e.ID.String(),
		Audience: e.Audience(),
		Received: e.Received(),
	})
}
