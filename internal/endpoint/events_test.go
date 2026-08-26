package endpoint

import (
	"net/http/httptest"
	"sync"
	"testing"
)

func TestSubscribeReceivesEveryKind(t *testing.T) {
	r := NewRegistry(10)
	events, unsubscribe := r.Subscribe()
	defer unsubscribe()

	ep, err := r.Create("", "first")
	if err != nil {
		t.Fatal(err)
	}
	if ev := <-events; ev.Type != EventCreated || ev.Endpoint != ep.ID.String() {
		t.Errorf("got = %+v, want a created event for %s", ev, ep.ID)
	}

	if err := ep.SetName("second"); err != nil {
		t.Fatal(err)
	}
	if ev := <-events; ev.Type != EventUpdated {
		t.Errorf("after SetName got = %+v, want %s", ev, EventUpdated)
	}

	if err := ep.SetSpec(Spec{}); err != nil {
		t.Fatal(err)
	}
	if ev := <-events; ev.Type != EventUpdated {
		t.Errorf("after SetSpec got = %+v, want %s", ev, EventUpdated)
	}

	ep.Handle(httptest.NewRequest("POST", "/", nil), "/", nil)
	ev := <-events
	if ev.Type != EventRequest {
		t.Errorf("after a callback got = %+v, want %s", ev, EventRequest)
	}
	// The counter travels with the event so a subscriber can update a listing
	// without a round trip.
	if ev.Received != 1 {
		t.Errorf("received = %d, want 1", ev.Received)
	}

	ep.ResetRequests()
	if ev := <-events; ev.Type != EventReset {
		t.Errorf("after ResetRequests got = %+v, want %s", ev, EventReset)
	}

	if err := r.Delete("", ep.ID.String()); err != nil {
		t.Fatal(err)
	}
	// Deleting drops the captured traffic, but that is part of the deletion —
	// a reset event for an endpoint that no longer exists would be noise.
	if ev := <-events; ev.Type != EventDeleted {
		t.Errorf("after Delete got = %+v, want %s", ev, EventDeleted)
	}
}

// Unsubscribing has to stop delivery and close the channel, or a console that
// closed its tab would leak a subscription for the life of the process.
func TestUnsubscribe(t *testing.T) {
	r := NewRegistry(10)
	events, unsubscribe := r.Subscribe()
	if got := r.Subscribers(); got != 1 {
		t.Fatalf("Subscribers() = %d, want 1", got)
	}

	unsubscribe()
	unsubscribe() // idempotent: the stream handler may unwind more than once

	if got := r.Subscribers(); got != 0 {
		t.Errorf("Subscribers() after unsubscribe = %d, want 0", got)
	}
	if _, open := <-events; open {
		t.Error("the channel is still delivering after unsubscribe")
	}
	if _, err := r.Create("", "after"); err != nil {
		t.Errorf("Create with no subscribers: %v", err)
	}
}

// A callback must be answered whatever the console is doing, so publishing
// never blocks on a subscriber that is not reading.
func TestPublishDoesNotBlockOnASlowSubscriber(t *testing.T) {
	r := NewRegistry(10)
	_, unsubscribe := r.Subscribe() // never drained
	defer unsubscribe()

	ep, err := r.Create("", "busy")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/", nil)
	for i := 0; i < 1000; i++ { // far past the subscriber's buffer
		ep.Handle(req, "/", nil)
	}
	if got := ep.Received(); got != 1000 {
		t.Errorf("Received() = %d, want 1000: every callback must be answered", got)
	}
}

// Subscribers come and go while traffic is arriving. Run with -race.
func TestBrokerIsRaceFree(t *testing.T) {
	r := NewRegistry(10)
	ep, err := r.Create("", "racy")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/", nil)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(3)
		go func() { defer wg.Done(); ep.Handle(req, "/", nil) }()
		go func() {
			defer wg.Done()
			events, stop := r.Subscribe()
			go func() {
				for range events {
				}
			}()
			stop()
		}()
		go func() { defer wg.Done(); _ = r.Subscribers() }()
	}
	wg.Wait()
}
