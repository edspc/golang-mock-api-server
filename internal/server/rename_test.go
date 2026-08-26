package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/edspc/golang-mock-api-server/internal/endpoint"
)

func rename(t *testing.T, srv *Server, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, srv, http.MethodPut, "/api/endpoints/"+id+"/name", body)
}

func TestRenameEndpoint(t *testing.T) {
	srv := newTestServer(t)
	view := createEndpoint(t, srv, `{"name":"before"}`)

	w := rename(t, srv, view.ID, `{"name":"after"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s, want 200", w.Code, w.Body)
	}

	var got endpointView
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "after" {
		t.Errorf("returned name = %q, want after", got.Name)
	}
	if got.ID != view.ID || got.URL != view.URL {
		t.Errorf("rename changed the identity: id %q url %q, want %q / %q", got.ID, got.URL, view.ID, view.URL)
	}

	// And it stuck.
	fresh := do(t, srv, http.MethodGet, "/api/endpoints/"+view.ID, "")
	if !strings.Contains(fresh.Body.String(), `"name": "after"`) {
		t.Errorf("re-read = %s, want the new name", fresh.Body)
	}
}

// The URL is the endpoint's identity, so a rename must not disturb traffic.
func TestRenameKeepsTheEndpointServing(t *testing.T) {
	srv := newTestServer(t)
	view := createEndpoint(t, srv, `{"name":"before","spec":{"rules":[{"name":"ack","response":{"status":202}}]}}`)

	if w := do(t, srv, http.MethodPost, view.URL, ""); w.Code != 202 {
		t.Fatalf("status before rename = %d, want 202", w.Code)
	}
	if w := rename(t, srv, view.ID, `{"name":"after"}`); w.Code != http.StatusOK {
		t.Fatalf("rename status = %d", w.Code)
	}
	if w := do(t, srv, http.MethodPost, view.URL, ""); w.Code != 202 {
		t.Errorf("status after rename = %d, want the same 202 on the same URL", w.Code)
	}
}

func TestRenameToEmptyIsAllowed(t *testing.T) {
	srv := newTestServer(t)
	view := createEndpoint(t, srv, `{"name":"titled"}`)

	w := rename(t, srv, view.ID, `{"name":"   "}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: clearing a label is legitimate", w.Code)
	}
	var got endpointView
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "" {
		t.Errorf("name = %q, want it trimmed to empty", got.Name)
	}
}

func TestRenameRejectsBadInput(t *testing.T) {
	srv := newTestServer(t)
	view := createEndpoint(t, srv, "")

	tests := []struct {
		name    string
		body    string
		wantSub string
	}{
		{"too long", `{"name":"` + strings.Repeat("x", endpoint.MaxNameLength+1) + `"}`, "maximum"},
		{"misspelled key", `{"nmae":"x"}`, "unknown field"},
		{"not an object", `"just a string"`, "parse name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := rename(t, srv, view.ID, tt.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", w.Code)
			}
			if !strings.Contains(w.Body.String(), tt.wantSub) {
				t.Errorf("body = %s, want it to mention %q", w.Body, tt.wantSub)
			}
		})
	}
}

func TestRenameUnknownEndpoint(t *testing.T) {
	srv := newTestServer(t)
	if w := rename(t, srv, "0192f0c8-3f4e-8a1b-9c2d-4e5f60718293", `{"name":"x"}`); w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// A name is read on every listing while a rename may be in flight, so the
// field lives behind the lock. Run with -race.
func TestRenameIsRaceFree(t *testing.T) {
	srv := newTestServer(t)
	view := createEndpoint(t, srv, "")
	ep, err := srv.endpoints.Get(view.ID)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(3)
		go func() { defer wg.Done(); _ = ep.SetName("renamed") }()
		go func() { defer wg.Done(); _ = ep.Name() }()
		go func() { defer wg.Done(); do(t, srv, http.MethodGet, "/api/endpoints", "") }()
	}
	wg.Wait()

	if got := ep.Name(); got != "renamed" {
		t.Errorf("Name() = %q, want renamed", got)
	}
}
