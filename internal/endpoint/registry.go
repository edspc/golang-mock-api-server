package endpoint

import (
	"errors"
	"sort"
	"sync"

	"github.com/edspc/golang-mock-api-server/internal/mock"
	"github.com/edspc/golang-mock-api-server/internal/uuid"
)

// ErrNotFound is returned for an unknown endpoint ID.
var ErrNotFound = errors.New("endpoint not found")

// DefaultHistory is how many captured requests an endpoint keeps by default.
const DefaultHistory = mock.DefaultRecorderCapacity

// Registry holds the live callback endpoints. It is in-memory only: endpoints
// and their captured traffic do not survive a restart.
type Registry struct {
	mu      sync.RWMutex
	byID    map[uuid.UUID]*Endpoint
	history int
}

// NewRegistry returns an empty Registry whose endpoints keep history requests
// each.
func NewRegistry(history int) *Registry {
	return &Registry{byID: make(map[uuid.UUID]*Endpoint), history: history}
}

// Create registers a new endpoint with a fresh UUIDv8.
func (r *Registry) Create(name string) (*Endpoint, error) {
	e, err := New(name, r.history)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID[e.ID] = e
	return e, nil
}

// Get resolves an endpoint by its canonical UUID string.
func (r *Registry) Get(id string) (*Endpoint, error) {
	u, err := uuid.Parse(id)
	if err != nil {
		return nil, ErrNotFound
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.byID[u]
	if !ok {
		return nil, ErrNotFound
	}
	return e, nil
}

// Delete removes an endpoint and everything it captured.
func (r *Registry) Delete(id string) error {
	u, err := uuid.Parse(id)
	if err != nil {
		return ErrNotFound
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byID[u]; !ok {
		return ErrNotFound
	}
	delete(r.byID, u)
	return nil
}

// List returns every endpoint, newest first. IDs are time-ordered, so sorting
// by ID string is sorting by creation time.
func (r *Registry) List() []*Endpoint {
	r.mu.RLock()
	out := make([]*Endpoint, 0, len(r.byID))
	for _, e := range r.byID {
		out = append(out, e)
	}
	r.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool {
		return out[i].ID.String() > out[j].ID.String()
	})
	return out
}

// Len is the number of registered endpoints.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byID)
}
