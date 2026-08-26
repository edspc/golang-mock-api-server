// Package persist adapts the SQLite store to the interfaces the endpoint
// registry consumes. It exists so neither of those packages has to import the
// other: endpoint declares what it needs, store knows only about SQLite.
package persist

import (
	"log/slog"

	"github.com/edspc/golang-mock-api-server/internal/endpoint"
	"github.com/edspc/golang-mock-api-server/internal/mock"
	"github.com/edspc/golang-mock-api-server/internal/store"
)

// Endpoints adapts the endpoint database to endpoint.Store.
type Endpoints struct{ db *store.Endpoints }

// NewEndpoints wraps an open endpoint database.
func NewEndpoints(db *store.Endpoints) Endpoints { return Endpoints{db: db} }

// Save implements endpoint.Store.
func (e Endpoints) Save(s endpoint.Stored) error {
	return e.db.Save(store.Record{
		ID:        s.ID,
		Owner:     s.Owner,
		Shared:    s.Shared,
		Name:      s.Name,
		CreatedAt: s.CreatedAt,
		Spec:      s.Spec,
		Received:  s.Received,
	})
}

// SetReceived implements endpoint.Store.
func (e Endpoints) SetReceived(id string, received int64) error {
	return e.db.SetReceived(id, received)
}

// Delete implements endpoint.Store.
func (e Endpoints) Delete(id string) error { return e.db.Delete(id) }

// List implements endpoint.Store.
func (e Endpoints) List() ([]endpoint.Stored, error) {
	records, err := e.db.List()
	if err != nil {
		return nil, err
	}
	out := make([]endpoint.Stored, 0, len(records))
	for _, r := range records {
		out = append(out, endpoint.Stored{
			ID:        r.ID,
			Owner:     r.Owner,
			Shared:    r.Shared,
			Name:      r.Name,
			CreatedAt: r.CreatedAt,
			Spec:      r.Spec,
			Received:  r.Received,
		})
	}
	return out, nil
}

// History adapts one endpoint's request log to endpoint.History.
//
// The interface has no error returns because a callback has to be answered
// whatever the database does, so failures are logged instead. A dropped write
// loses one recorded request; it never turns into a failed callback.
type History struct {
	log    *store.Log
	id     string
	logger *slog.Logger
}

// HistoryFor returns a factory the registry uses to build each endpoint's
// history from the request database.
func HistoryFor(db *store.Requests, logger *slog.Logger) func(id string) endpoint.History {
	return func(id string) endpoint.History {
		return History{log: db.For(id), id: id, logger: logger}
	}
}

// Record implements endpoint.History.
func (h History) Record(e mock.Entry) {
	if err := h.log.Append(e); err != nil {
		h.logger.Error("persist captured request", "endpoint", h.id, "error", err)
	}
}

// Entries implements endpoint.History.
func (h History) Entries() []mock.Entry {
	entries, err := h.log.Entries()
	if err != nil {
		h.logger.Error("read captured requests", "endpoint", h.id, "error", err)
		return nil
	}
	return entries
}

// Reset implements endpoint.History.
func (h History) Reset() {
	if err := h.log.Reset(); err != nil {
		h.logger.Error("clear captured requests", "endpoint", h.id, "error", err)
	}
}
