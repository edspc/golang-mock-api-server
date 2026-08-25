package store

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/edspc/golang-mock-api-server/internal/mock"
)

func endpointsDB(t *testing.T) *Endpoints {
	t.Helper()
	db, err := OpenEndpoints(filepath.Join(t.TempDir(), "endpoints.db"))
	if err != nil {
		t.Fatalf("OpenEndpoints() error = %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func requestsDB(t *testing.T, limit int) *Requests {
	t.Helper()
	db, err := OpenRequests(filepath.Join(t.TempDir(), "requests.db"), limit)
	if err != nil {
		t.Fatalf("OpenRequests() error = %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestEndpointsRoundTrip(t *testing.T) {
	db := endpointsDB(t)
	created := time.Date(2026, 8, 24, 10, 0, 0, 123456789, time.UTC)
	want := Record{
		ID:        "01a03340-3fd9-8000-9550-59d35674b509",
		Name:      "stripe",
		CreatedAt: created,
		Spec:      json.RawMessage(`{"response":{"status":204}}`),
		Received:  7,
	}

	if err := db.Save(want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := db.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("List() returned %d records, want 1", len(got))
	}
	if got[0].ID != want.ID || got[0].Name != want.Name || got[0].Received != want.Received {
		t.Errorf("List()[0] = %+v, want %+v", got[0], want)
	}
	if !got[0].CreatedAt.Equal(created) {
		t.Errorf("CreatedAt = %v, want %v (nanoseconds must survive)", got[0].CreatedAt, created)
	}
	if string(got[0].Spec) != string(want.Spec) {
		t.Errorf("Spec = %s, want %s", got[0].Spec, want.Spec)
	}
}

func TestEndpointsSaveIsUpsert(t *testing.T) {
	db := endpointsDB(t)
	rec := Record{ID: "id-1", Name: "before", CreatedAt: time.Now(), Spec: json.RawMessage(`{}`)}
	if err := db.Save(rec); err != nil {
		t.Fatal(err)
	}
	rec.Name = "after"
	rec.Spec = json.RawMessage(`{"response":{"status":201}}`)
	if err := db.Save(rec); err != nil {
		t.Fatal(err)
	}

	got, err := db.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("List() returned %d records, want the row replaced, not duplicated", len(got))
	}
	if got[0].Name != "after" {
		t.Errorf("Name = %q, want the updated value", got[0].Name)
	}
}

// SetReceived runs on every callback, so it must not disturb the spec.
func TestSetReceivedLeavesSpecAlone(t *testing.T) {
	db := endpointsDB(t)
	spec := json.RawMessage(`{"response":{"status":204}}`)
	if err := db.Save(Record{ID: "id-1", CreatedAt: time.Now(), Spec: spec}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetReceived("id-1", 42); err != nil {
		t.Fatalf("SetReceived() error = %v", err)
	}

	got, err := db.List()
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Received != 42 {
		t.Errorf("Received = %d, want 42", got[0].Received)
	}
	if string(got[0].Spec) != string(spec) {
		t.Errorf("Spec = %s, want it untouched", got[0].Spec)
	}
}

func TestEndpointsDelete(t *testing.T) {
	db := endpointsDB(t)
	if err := db.Save(Record{ID: "id-1", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := db.Delete("id-1"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	got, err := db.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("List() = %+v, want empty after Delete()", got)
	}
}

func TestRequestsRoundTrip(t *testing.T) {
	db := requestsDB(t, 10)
	log := db.For("ep-1")
	at := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	want := mock.Entry{
		Time:             at,
		Method:           "POST",
		Path:             "/cb/ep-1",
		Query:            map[string][]string{"source": {"stripe"}},
		Headers:          map[string][]string{"X-Signature": {"sig"}},
		Body:             `{"event":"paid"}`,
		Rule:             "paid",
		Status:           202,
		ValidationErrors: []string{"missing header X-Env"},
	}

	if err := log.Append(want); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	got, err := log.Entries()
	if err != nil {
		t.Fatalf("Entries() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Entries() returned %d, want 1", len(got))
	}
	e := got[0]
	if !e.Time.Equal(at) || e.Method != want.Method || e.Path != want.Path || e.Body != want.Body ||
		e.Rule != want.Rule || e.Status != want.Status {
		t.Errorf("Entries()[0] = %+v, want %+v", e, want)
	}
	if e.Query["source"][0] != "stripe" || e.Headers["X-Signature"][0] != "sig" {
		t.Errorf("query/headers = %v / %v, want them preserved", e.Query, e.Headers)
	}
	if len(e.ValidationErrors) != 1 || e.ValidationErrors[0] != want.ValidationErrors[0] {
		t.Errorf("ValidationErrors = %q, want %q", e.ValidationErrors, want.ValidationErrors)
	}
}

// The log keeps the most recent requests and reports them oldest first, like
// the in-memory ring it replaces.
func TestRequestsLimitKeepsNewestOldestFirst(t *testing.T) {
	db := requestsDB(t, 3)
	log := db.For("ep-1")
	for i := 0; i < 5; i++ {
		if err := log.Append(mock.Entry{Time: time.Now(), Body: string(rune('a' + i))}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := log.Entries()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("Entries() returned %d, want the 3-request limit", len(got))
	}
	if got[0].Body != "c" || got[2].Body != "e" {
		t.Errorf("bodies = %q..%q, want c..e (newest kept, oldest first)", got[0].Body, got[2].Body)
	}
}

func TestRequestsAreScopedPerEndpoint(t *testing.T) {
	db := requestsDB(t, 10)
	if err := db.For("ep-1").Append(mock.Entry{Time: time.Now(), Body: "one"}); err != nil {
		t.Fatal(err)
	}
	if err := db.For("ep-2").Append(mock.Entry{Time: time.Now(), Body: "two"}); err != nil {
		t.Fatal(err)
	}

	first, err := db.For("ep-1").Entries()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].Body != "one" {
		t.Errorf("ep-1 entries = %+v, want only its own request", first)
	}

	if err := db.For("ep-1").Reset(); err != nil {
		t.Fatalf("Reset() error = %v", err)
	}
	if got, _ := db.For("ep-1").Entries(); len(got) != 0 {
		t.Errorf("ep-1 entries after Reset() = %+v, want empty", got)
	}
	if got, _ := db.For("ep-2").Entries(); len(got) != 1 {
		t.Errorf("ep-2 entries = %+v, want another endpoint's reset to leave them alone", got)
	}
}

// Reopening must find the data, which is the whole point of the store.
func TestDataSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "endpoints.db")

	db, err := OpenEndpoints(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Save(Record{ID: "id-1", Name: "kept", CreatedAt: time.Now(), Spec: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	db.Close()

	again, err := OpenEndpoints(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer again.Close()
	got, err := again.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "kept" {
		t.Errorf("List() after reopen = %+v, want the saved endpoint", got)
	}
}
