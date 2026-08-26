package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// stream opens the event stream against a real listener. Unlike the other
// handler tests this one cannot use a ResponseRecorder: the whole point is
// what arrives while the handler is still running.
type stream struct {
	t      *testing.T
	reader *bufio.Reader
	cancel context.CancelFunc
	body   interface{ Close() error }
}

// liveServer returns a server behind a real listener. Its shutdown is
// registered as a cleanup rather than deferred, so that streams opened
// afterwards are closed first — httptest.Server.Close waits for open handlers,
// and an event stream stays open until its client hangs up.
func liveServer(t *testing.T) (*Server, string) {
	t.Helper()
	srv := newTestServer(t)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return srv, ts.URL
}

func openStream(t *testing.T, base string) *stream {
	t.Helper()
	return openStreamAs(t, base, nil)
}

// openStreamAs opens the stream as one account, or as nobody when cookie is
// nil.
func openStreamAs(t *testing.T, base string, cookie *http.Cookie) *stream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/events", nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if got := res.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		cancel()
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
	s := &stream{t: t, reader: bufio.NewReader(res.Body), cancel: cancel, body: res.Body}
	t.Cleanup(s.close)
	return s
}

func (s *stream) close() {
	s.cancel()
	_ = s.body.Close()
}

// watchdog closes the stream if the test is still reading after 5s, so a
// failure surfaces as a message rather than a hung suite.
func (s *stream) watchdog() func() {
	done := make(chan struct{})
	go func() {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			s.close()
		}
	}()
	return func() { close(done) }
}

// drain reads to the end of the response, failing if it does not come.
func (s *stream) drain() {
	s.t.Helper()
	defer s.watchdog()()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := s.reader.ReadString('\n'); err != nil {
			return
		}
		if time.Now().After(deadline) {
			s.t.Fatal("the stream never ended")
		}
	}
}

// next returns the next data event, failing the test if none arrives. Comment
// lines (the keepalive and the greeting) are skipped.
func (s *stream) next() endpointEvent {
	s.t.Helper()
	defer s.watchdog()()

	for {
		line, err := s.reader.ReadString('\n')
		if err != nil {
			s.t.Fatalf("reading the stream: %v", err)
		}
		line = strings.TrimRight(line, "\n")
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev endpointEvent
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			s.t.Fatalf("event %q is not JSON: %v", line, err)
		}
		return ev
	}
}

type endpointEvent struct {
	Type     string `json:"type"`
	Endpoint string `json:"endpoint"`
	Received int64  `json:"received"`
}

// The stream exists so the console does not have to poll: a callback must show
// up on it without anyone asking for it.
func TestEventStreamAnnouncesCallbacks(t *testing.T) {
	srv, base := liveServer(t)

	view := createEndpoint(t, srv, `{"name":"live"}`)
	es := openStream(t, base)

	res, err := http.Post(base+view.URL, "application/json", strings.NewReader(`{"event":"paid"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()

	ev := es.next()
	if ev.Type != "request" {
		t.Errorf("type = %q, want request", ev.Type)
	}
	if ev.Endpoint != view.ID {
		t.Errorf("endpoint = %q, want %q", ev.Endpoint, view.ID)
	}
	if ev.Received != 1 {
		t.Errorf("received = %d, want 1", ev.Received)
	}
}

// Everything that changes what the console is showing has to be announced, or
// the console shows something stale until its next safety poll.
func TestEventStreamAnnouncesEndpointChanges(t *testing.T) {
	srv, base := liveServer(t)

	es := openStream(t, base)

	view := createEndpoint(t, srv, `{"name":"before"}`)
	if ev := es.next(); ev.Type != "created" || ev.Endpoint != view.ID {
		t.Errorf("first event = %+v, want created for %s", ev, view.ID)
	}

	if w := rename(t, srv, view.ID, `{"name":"after"}`); w.Code != http.StatusOK {
		t.Fatalf("rename status = %d", w.Code)
	}
	if ev := es.next(); ev.Type != "updated" {
		t.Errorf("after rename = %+v, want updated", ev)
	}

	if w := do(t, srv, http.MethodPost, "/api/endpoints/"+view.ID+"/reset", ""); w.Code != http.StatusOK {
		t.Fatalf("reset status = %d", w.Code)
	}
	if ev := es.next(); ev.Type != "reset" {
		t.Errorf("after reset = %+v, want reset", ev)
	}

	if w := do(t, srv, http.MethodDelete, "/api/endpoints/"+view.ID, ""); w.Code != http.StatusOK {
		t.Fatalf("delete status = %d", w.Code)
	}
	if ev := es.next(); ev.Type != "deleted" {
		t.Errorf("after delete = %+v, want deleted", ev)
	}
}

// A console watching an idle stream is behaving correctly, so shutdown has to
// end the stream rather than wait for it: otherwise every restart burns the
// full graceful-shutdown timeout and then reports failure.
func TestCloseStreamsEndsOpenStreams(t *testing.T) {
	srv, base := liveServer(t)
	es := openStream(t, base)
	createEndpoint(t, srv, "")
	es.next() // connected

	srv.CloseStreams()
	srv.CloseStreams() // idempotent: Shutdown may be called more than once

	deadline := time.Now().Add(2 * time.Second)
	for srv.endpoints.Subscribers() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("subscribers after CloseStreams = %d, want 0", srv.endpoints.Subscribers())
		}
		time.Sleep(5 * time.Millisecond)
	}
	// And the client sees the end of the response, not a connection left
	// hanging. What is already buffered still reads back first.
	es.drain()
}

// A subscriber that hangs up must not leave anything behind — a callback still
// has to be answered whether or not a console is watching.
func TestEventStreamUnsubscribesOnDisconnect(t *testing.T) {
	srv, base := liveServer(t)

	es := openStream(t, base)
	createEndpoint(t, srv, "")
	es.next() // the handler is running, so the subscription is registered

	if got := srv.endpoints.Subscribers(); got != 1 {
		t.Fatalf("subscribers while connected = %d, want 1", got)
	}

	es.close()
	deadline := time.Now().Add(2 * time.Second)
	for srv.endpoints.Subscribers() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("subscribers after disconnect = %d, want 0", srv.endpoints.Subscribers())
		}
		time.Sleep(5 * time.Millisecond)
	}

	// And the endpoint still answers with nobody listening.
	view := createEndpoint(t, srv, "")
	if w := do(t, srv, http.MethodPost, view.URL, ""); w.Code != http.StatusOK {
		t.Errorf("callback status with no subscriber = %d, want 200", w.Code)
	}
}
