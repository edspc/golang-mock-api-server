package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// eventKeepalive is how often an idle stream sends a comment line. Proxies and
// load balancers drop a connection that has been silent for too long, and an
// endpoint that sees no traffic for an hour is entirely normal here.
const eventKeepalive = 25 * time.Second

// eventRetry is the reconnect delay handed to the browser. EventSource
// reconnects on its own; this only makes it prompt about it.
const eventRetry = 3 * time.Second

// serveEvents streams changes as Server-Sent Events, so the console learns
// about a callback the moment it arrives instead of on its next poll.
//
// Events carry what changed, not the change itself: the console re-reads the
// affected resource over the ordinary API. That keeps this stream from
// becoming a second, subtly different copy of the endpoint API — and keeps
// dropped events (see the broker in internal/endpoint) harmless.
func (s *Server) serveEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "this server cannot stream events",
		})
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	// Ask nginx not to buffer. A buffering proxy holds the whole stream back —
	// which for a connection that never ends means forever.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// The broker is registry-wide, so the stream is where events are matched
	// to the account watching: an event reaches the endpoint's owner and
	// whoever it is shared with. Without this, one account would learn that
	// another is receiving callbacks — and re-read its own listing for
	// nothing on every one of them.
	caller := s.auth.Caller(r)

	events, unsubscribe := s.endpoints.Subscribe()
	defer unsubscribe()

	// Open the stream immediately, so the client's onopen fires even while
	// nothing is happening.
	if _, err := fmt.Fprintf(w, "retry: %d\n: connected\n\n", eventRetry.Milliseconds()); err != nil {
		return
	}
	flusher.Flush()

	ping := time.NewTicker(eventKeepalive)
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.closing:
			// The process is going away. Without this a graceful shutdown
			// would wait out its whole timeout on a console that is simply
			// idle, and then report failure.
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			if !ev.Reaches(caller) {
				continue
			}
			payload, err := json.Marshal(ev)
			if err != nil {
				s.log.Error("encode event", "type", ev.Type, "error", err)
				continue
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
				return
			}
			flusher.Flush()
		case <-ping.C:
			// A comment line: it keeps the connection alive without the client
			// having to know about a heartbeat message.
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
