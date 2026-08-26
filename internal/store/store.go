// Package store persists endpoints and their captured traffic in SQLite.
//
// The two live in separate databases on purpose: endpoint settings are small,
// long-lived and worth backing up, while captured requests are bulky and
// disposable. Either can be left unconfigured, in which case that half stays in
// memory and disappears with the process.
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, registered as "sqlite"

	"github.com/edspc/golang-mock-api-server/internal/mock"
)

// open connects to path and applies the pragmas a small embedded database
// wants: WAL so a reader never blocks the writer, and a busy timeout so a
// concurrent callback waits instead of failing.
func open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA foreign_keys = ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}
	return db, nil
}

// addColumn adds a column to an existing table, doing nothing when it is
// already there. SQLite has no "ADD COLUMN IF NOT EXISTS", and a database
// written by an older build must keep opening rather than fail on startup.
func addColumn(db *sql.DB, table, column, definition string) error {
	rows, err := db.Query("SELECT 1 FROM pragma_table_info(?) WHERE name = ?", table, column)
	if err != nil {
		return fmt.Errorf("inspect %s.%s: %w", table, column, err)
	}
	present := rows.Next()
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("inspect %s.%s: %w", table, column, err)
	}
	rows.Close()
	if present {
		return nil
	}
	if _, err := db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition)); err != nil {
		return fmt.Errorf("add %s.%s: %w", table, column, err)
	}
	return nil
}

/* ---------- endpoints ---------- */

// Endpoints stores the endpoints a user created and the spec each one serves.
type Endpoints struct{ db *sql.DB }

// Record is one stored endpoint. Spec is the raw JSON the user saved, so the
// store never needs to understand the spec format.
type Record struct {
	ID        string
	Name      string
	CreatedAt time.Time
	Spec      json.RawMessage
	Received  int64
}

// OpenEndpoints opens (creating if needed) the endpoint database at path.
func OpenEndpoints(path string) (*Endpoints, error) {
	db, err := open(path)
	if err != nil {
		return nil, err
	}
	const schema = `
CREATE TABLE IF NOT EXISTS endpoints (
	id         TEXT PRIMARY KEY,
	name       TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	spec       TEXT NOT NULL DEFAULT '{}',
	received   INTEGER NOT NULL DEFAULT 0
)`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create endpoints schema: %w", err)
	}
	return &Endpoints{db: db}, nil
}

// Close releases the database.
func (e *Endpoints) Close() error { return e.db.Close() }

// Save inserts or updates one endpoint.
func (e *Endpoints) Save(r Record) error {
	spec := r.Spec
	if len(spec) == 0 {
		spec = json.RawMessage("{}")
	}
	_, err := e.db.Exec(`
INSERT INTO endpoints (id, name, created_at, spec, received) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET name = excluded.name, spec = excluded.spec, received = excluded.received`,
		r.ID, r.Name, r.CreatedAt.UTC().Format(time.RFC3339Nano), string(spec), r.Received)
	if err != nil {
		return fmt.Errorf("save endpoint %s: %w", r.ID, err)
	}
	return nil
}

// SetReceived updates only the lifetime counter, which changes on every
// callback and must not rewrite the spec.
func (e *Endpoints) SetReceived(id string, received int64) error {
	if _, err := e.db.Exec(`UPDATE endpoints SET received = ? WHERE id = ?`, received, id); err != nil {
		return fmt.Errorf("update received for %s: %w", id, err)
	}
	return nil
}

// Delete removes one endpoint.
func (e *Endpoints) Delete(id string) error {
	if _, err := e.db.Exec(`DELETE FROM endpoints WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete endpoint %s: %w", id, err)
	}
	return nil
}

// List returns every stored endpoint, oldest first.
func (e *Endpoints) List() ([]Record, error) {
	rows, err := e.db.Query(`SELECT id, name, created_at, spec, received FROM endpoints ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list endpoints: %w", err)
	}
	defer rows.Close()

	var out []Record
	for rows.Next() {
		var r Record
		var created, spec string
		if err := rows.Scan(&r.ID, &r.Name, &created, &spec, &r.Received); err != nil {
			return nil, fmt.Errorf("scan endpoint: %w", err)
		}
		if r.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
			return nil, fmt.Errorf("parse created_at for %s: %w", r.ID, err)
		}
		r.Spec = json.RawMessage(spec)
		out = append(out, r)
	}
	return out, rows.Err()
}

/* ---------- requests ---------- */

// Requests stores the traffic captured on every endpoint.
type Requests struct {
	db    *sql.DB
	limit int
}

// OpenRequests opens (creating if needed) the request database at path. limit
// is how many recent requests a single endpoint reports.
func OpenRequests(path string, limit int) (*Requests, error) {
	db, err := open(path)
	if err != nil {
		return nil, err
	}
	const schema = `
CREATE TABLE IF NOT EXISTS requests (
	id                INTEGER PRIMARY KEY AUTOINCREMENT,
	request_id        TEXT NOT NULL DEFAULT '',
	endpoint_id       TEXT NOT NULL,
	at                TEXT NOT NULL,
	method            TEXT NOT NULL DEFAULT '',
	path              TEXT NOT NULL DEFAULT '',
	query             TEXT NOT NULL DEFAULT 'null',
	headers           TEXT NOT NULL DEFAULT 'null',
	body              TEXT NOT NULL DEFAULT '',
	rule              TEXT NOT NULL DEFAULT '',
	status            INTEGER NOT NULL DEFAULT 0,
	validation_errors TEXT NOT NULL DEFAULT 'null'
);
CREATE INDEX IF NOT EXISTS requests_by_endpoint ON requests (endpoint_id, id)`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create requests schema: %w", err)
	}
	// Databases written before request ids existed keep their rows; those
	// rows simply have no id to look up.
	if err := addColumn(db, "requests", "request_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		db.Close()
		return nil, err
	}
	if limit <= 0 {
		limit = mock.DefaultRecorderCapacity
	}
	return &Requests{db: db, limit: limit}, nil
}

// Close releases the database.
func (r *Requests) Close() error { return r.db.Close() }

// For returns the history of one endpoint.
func (r *Requests) For(endpointID string) *Log { return &Log{req: r, id: endpointID} }

// DeleteAll removes every request captured for one endpoint, used when the
// endpoint itself is deleted.
func (r *Requests) DeleteAll(endpointID string) error {
	if _, err := r.db.Exec(`DELETE FROM requests WHERE endpoint_id = ?`, endpointID); err != nil {
		return fmt.Errorf("delete requests for %s: %w", endpointID, err)
	}
	return nil
}

// Log is one endpoint's captured traffic, backed by the requests database.
type Log struct {
	req *Requests
	id  string
}

// Append stores one captured request.
func (l *Log) Append(e mock.Entry) error {
	query, err := json.Marshal(e.Query)
	if err != nil {
		return fmt.Errorf("encode query: %w", err)
	}
	headers, err := json.Marshal(e.Headers)
	if err != nil {
		return fmt.Errorf("encode headers: %w", err)
	}
	errs, err := json.Marshal(e.ValidationErrors)
	if err != nil {
		return fmt.Errorf("encode validation errors: %w", err)
	}
	_, err = l.req.db.Exec(`
INSERT INTO requests (request_id, endpoint_id, at, method, path, query, headers, body, rule, status, validation_errors)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, l.id, e.Time.UTC().Format(time.RFC3339Nano), e.Method, e.Path,
		string(query), string(headers), e.Body, e.Rule, e.Status, string(errs))
	if err != nil {
		return fmt.Errorf("append request for %s: %w", l.id, err)
	}
	return nil
}

// Entries returns the most recent requests, oldest first, capped at the
// configured limit.
func (l *Log) Entries() ([]mock.Entry, error) {
	rows, err := l.req.db.Query(`
SELECT request_id, at, method, path, query, headers, body, rule, status, validation_errors
FROM requests WHERE endpoint_id = ? ORDER BY id DESC LIMIT ?`, l.id, l.req.limit)
	if err != nil {
		return nil, fmt.Errorf("list requests for %s: %w", l.id, err)
	}
	defer rows.Close()

	var out []mock.Entry
	for rows.Next() {
		var e mock.Entry
		var at, query, headers, errs string
		if err := rows.Scan(&e.ID, &at, &e.Method, &e.Path, &query, &headers, &e.Body, &e.Rule, &e.Status, &errs); err != nil {
			return nil, fmt.Errorf("scan request: %w", err)
		}
		if e.Time, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return nil, fmt.Errorf("parse request time: %w", err)
		}
		if err := json.Unmarshal([]byte(query), &e.Query); err != nil {
			return nil, fmt.Errorf("decode query: %w", err)
		}
		if err := json.Unmarshal([]byte(headers), &e.Headers); err != nil {
			return nil, fmt.Errorf("decode headers: %w", err)
		}
		if err := json.Unmarshal([]byte(errs), &e.ValidationErrors); err != nil {
			return nil, fmt.Errorf("decode validation errors: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// The query returns newest first so LIMIT keeps the recent ones; callers
	// want oldest first, like the in-memory recorder.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// Reset discards this endpoint's captured requests.
func (l *Log) Reset() error { return l.req.DeleteAll(l.id) }
