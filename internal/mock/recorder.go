package mock

import (
	"sync"
	"time"
)

// DefaultRecorderCapacity is how many requests a Recorder keeps by default.
const DefaultRecorderCapacity = 200

// Entry is one recorded request/response pair, served at /api/requests so tests
// can assert on what their subject actually sent.
type Entry struct {
	Time    time.Time           `json:"time"`
	Method  string              `json:"method"`
	Path    string              `json:"path"`
	Query   map[string][]string `json:"query,omitempty"`
	Headers map[string][]string `json:"headers,omitempty"`
	Body    string              `json:"body,omitempty"`
	// Rule is the name of the rule that matched, empty when none did.
	Rule   string `json:"rule"`
	Status int    `json:"status"`
	// ValidationErrors lists why the request failed the endpoint's validation
	// rules. Empty means it passed (or that nothing was configured).
	ValidationErrors []string `json:"validationErrors,omitempty"`
}

// Recorder is a bounded, concurrency-safe ring of recent requests. When it is
// full the oldest entry is dropped.
type Recorder struct {
	mu       sync.Mutex
	capacity int
	entries  []Entry
}

// NewRecorder returns a Recorder holding at most capacity entries; capacity
// <= 0 uses DefaultRecorderCapacity.
func NewRecorder(capacity int) *Recorder {
	if capacity <= 0 {
		capacity = DefaultRecorderCapacity
	}
	return &Recorder{capacity: capacity}
}

// Record appends e, evicting the oldest entry if the recorder is full.
func (r *Recorder) Record(e Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.entries) == r.capacity {
		copy(r.entries, r.entries[1:])
		r.entries[len(r.entries)-1] = e
		return
	}
	r.entries = append(r.entries, e)
}

// Entries returns a copy of the recorded entries, oldest first.
func (r *Recorder) Entries() []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Entry, len(r.entries))
	copy(out, r.entries)
	return out
}

// Reset discards every recorded entry.
func (r *Recorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = nil
}
