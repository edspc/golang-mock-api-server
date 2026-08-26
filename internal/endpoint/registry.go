package endpoint

import (
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/edspc/golang-mock-api-server/internal/mock"
	"github.com/edspc/golang-mock-api-server/internal/uuid"
)

// ErrNotFound is returned for an unknown endpoint ID.
var ErrNotFound = errors.New("endpoint not found")

// DefaultHistory is how many captured requests an endpoint keeps by default.
const DefaultHistory = mock.DefaultRecorderCapacity

// Stored is one persisted endpoint, as the store hands it back.
type Stored struct {
	ID        string
	Owner     string
	Shared    []string
	Name      string
	CreatedAt time.Time
	Spec      json.RawMessage
	Received  int64
}

// Store persists endpoint settings. It is optional: without one the registry
// is memory-only and everything disappears with the process.
type Store interface {
	Save(Stored) error
	SetReceived(id string, received int64) error
	Delete(id string) error
	List() ([]Stored, error)
}

// Registry holds the live callback endpoints. Endpoints are always served from
// memory; a Store, when configured, is written through to and read back at
// startup.
type Registry struct {
	mu   sync.RWMutex
	byID map[uuid.UUID]*Endpoint

	// historyFor builds the history of a new endpoint, in memory by default.
	historyFor func(id string) History
	store      Store
	log        *slog.Logger

	// events notifies live subscribers — the console's stream — of anything
	// that changes what they are showing.
	events broker
}

// NewRegistry returns an empty, memory-only Registry whose endpoints keep
// history requests each.
func NewRegistry(history int) *Registry {
	return &Registry{
		byID:       make(map[uuid.UUID]*Endpoint),
		historyFor: func(string) History { return mock.NewRecorder(history) },
		log:        slog.New(slog.NewTextHandler(discard{}, nil)),
	}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// Persist makes the registry write endpoint settings through to store, and use
// historyFor (when non-nil) for captured traffic. Call it before Restore.
func (r *Registry) Persist(store Store, historyFor func(id string) History, log *slog.Logger) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.store = store
	if historyFor != nil {
		r.historyFor = historyFor
	}
	if log != nil {
		r.log = log
	}
}

// Restore loads previously stored endpoints. A stored spec that no longer
// validates is reported and skipped rather than failing startup, so one bad
// row cannot make the service unbootable.
func (r *Registry) Restore() error {
	if r.store == nil {
		return nil
	}
	records, err := r.store.List()
	if err != nil {
		return err
	}
	for _, rec := range records {
		id, err := uuid.Parse(rec.ID)
		if err != nil {
			r.log.Warn("skipping stored endpoint with an unparseable id", "id", rec.ID, "error", err)
			continue
		}
		e := newEndpoint(id, rec.Owner, rec.Name, rec.CreatedAt, r.historyFor(rec.ID))
		if err := e.SetShared(rec.Shared); err != nil {
			r.log.Warn("dropping an unreadable share list", "id", rec.ID, "error", err)
		}
		var spec Spec
		if len(rec.Spec) > 0 {
			if err := json.Unmarshal(rec.Spec, &spec); err != nil {
				r.log.Warn("skipping stored endpoint with an unreadable spec", "id", rec.ID, "error", err)
				continue
			}
		}
		if err := e.SetSpec(spec); err != nil {
			r.log.Warn("skipping stored endpoint whose spec no longer validates", "id", rec.ID, "error", err)
			continue
		}
		e.received.Store(rec.Received)
		r.attach(e)

		r.mu.Lock()
		r.byID[id] = e
		r.mu.Unlock()
	}
	return nil
}

// attach wires an endpoint's hooks: change notifications always, persistence
// only when a store is configured. Persistence failures are logged rather than
// returned: a callback still has to be answered.
func (r *Registry) attach(e *Endpoint) {
	e.onEvent = func(ep *Endpoint, kind string) {
		r.publish(ep, kind)
	}
	if r.store == nil {
		return
	}
	e.onSave = func(ep *Endpoint) {
		if err := r.store.Save(r.record(ep)); err != nil {
			r.log.Error("persist endpoint settings", "id", ep.ID.String(), "error", err)
		}
	}
	e.onRecv = func(ep *Endpoint) {
		if err := r.store.SetReceived(ep.ID.String(), ep.Received()); err != nil {
			r.log.Error("persist received counter", "id", ep.ID.String(), "error", err)
		}
	}
}

func (r *Registry) record(e *Endpoint) Stored {
	spec, err := json.Marshal(e.Spec())
	if err != nil {
		r.log.Error("encode endpoint spec", "id", e.ID.String(), "error", err)
		spec = json.RawMessage("{}")
	}
	return Stored{
		ID:        e.ID.String(),
		Owner:     e.Owner,
		Shared:    e.Shared(),
		Name:      e.Name(),
		CreatedAt: e.CreatedAt,
		Spec:      spec,
		Received:  e.Received(),
	}
}

// Create registers a new endpoint with a fresh UUIDv8, owned by owner. An
// empty owner is the anonymous one, used when sign-in is off.
func (r *Registry) Create(owner, name string) (*Endpoint, error) {
	id, err := uuid.NewV8()
	if err != nil {
		return nil, err
	}
	r.mu.RLock()
	historyFor := r.historyFor
	r.mu.RUnlock()

	e := newEndpoint(id, owner, name, id.Time(), historyFor(id.String()))
	r.attach(e)

	if r.store != nil {
		if err := r.store.Save(r.record(e)); err != nil {
			return nil, err
		}
	}

	r.mu.Lock()
	r.byID[e.ID] = e
	r.mu.Unlock()

	r.publish(e, EventCreated)
	return e, nil
}

// Get resolves an endpoint by its canonical UUID string, whoever owns it.
// This is the callback path: a third party posting to /cb/{id} has no
// identity, and demanding one would defeat the service.
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

// GetFor resolves an endpoint the way the control API must: one that caller
// neither owns nor has been given is not found, rather than forbidden. Its URL
// is public anyway, so there is nothing to be gained by distinguishing the
// two.
func (r *Registry) GetFor(caller, id string) (*Endpoint, error) {
	e, err := r.Get(id)
	if err != nil {
		return nil, err
	}
	if !e.AccessibleBy(caller) {
		return nil, ErrNotFound
	}
	return e, nil
}

// Delete removes an endpoint and everything it captured. Unlike the rest of
// the control surface it is the owner's alone: sharing hands over the traffic
// and the spec, not the right to destroy them.
func (r *Registry) Delete(owner, id string) error {
	u, err := uuid.Parse(id)
	if err != nil {
		return ErrNotFound
	}
	r.mu.Lock()
	e, ok := r.byID[u]
	if !ok || e.Owner != owner {
		r.mu.Unlock()
		return ErrNotFound
	}
	delete(r.byID, u)
	r.mu.Unlock()

	// Drop the captured traffic too, so a stored request log does not outlive
	// the endpoint it belongs to. Detached first: the endpoint is already gone
	// from the registry, and a "reset" event for it would be noise.
	e.onEvent = nil
	e.history.Reset()

	if r.store != nil {
		if err := r.store.Delete(id); err != nil {
			r.log.Error("delete stored endpoint", "id", id, "error", err)
		}
	}
	r.publish(e, EventDeleted)
	return nil
}

// List returns the endpoints caller can reach — their own and the ones shared
// with them — newest first. IDs are time-ordered, so sorting by ID string is
// sorting by creation time.
func (r *Registry) List(caller string) []*Endpoint {
	r.mu.RLock()
	out := make([]*Endpoint, 0, len(r.byID))
	for _, e := range r.byID {
		if e.AccessibleBy(caller) {
			out = append(out, e)
		}
	}
	r.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool {
		return out[i].ID.String() > out[j].ID.String()
	})
	return out
}

// Len is the number of registered endpoints, all owners together. It is a
// process-wide figure — for what one account can see, use Count.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byID)
}

// Count is how many endpoints caller can reach.
func (r *Registry) Count(caller string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, e := range r.byID {
		if e.AccessibleBy(caller) {
			n++
		}
	}
	return n
}

// CountOwned is how many endpoints caller owns outright, shared ones excluded.
func (r *Registry) CountOwned(owner string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, e := range r.byID {
		if e.Owner == owner {
			n++
		}
	}
	return n
}
