package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/edspc/golang-mock-api-server/internal/mock"
)

// filterServer captures one request of each shape the filters have to tell
// apart: a 202 POST, a 200 GET, a 404 POST and one that fails validation.
func filterServer(t *testing.T) (*Server, endpointView) {
	t.Helper()
	srv := newTestServer(t)
	view := createEndpoint(t, srv, `{"spec":{
		"validation": {"requireHeaders": ["X-Signature"], "onFailure": {"status": 401}},
		"rules": [
			{"name": "ack", "request": {"method": "POST", "path": "/ok"}, "response": {"status": 202}},
			{"name": "read", "request": {"method": "GET"}, "response": {"status": 200}},
			{"name": "gone", "request": {"method": "POST", "path": "/gone"}, "response": {"status": 404}}
		],
		"response": {"status": 204}
	}}`)

	signed := func(method, path string) {
		t.Helper()
		r := httptest.NewRequest(method, view.URL+path, nil)
		r.Header.Set("X-Signature", "sig")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
	}
	signed(http.MethodPost, "/ok")
	signed(http.MethodGet, "/anything")
	signed(http.MethodPost, "/gone")
	do(t, srv, http.MethodPost, view.URL+"/ok", "") // unsigned: 401
	return srv, view
}

func listRequests(t *testing.T, srv *Server, id, query string) []mock.Entry {
	t.Helper()
	w := do(t, srv, http.MethodGet, "/api/endpoints/"+id+"/requests"+query, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, body = %s, want 200", query, w.Code, w.Body)
	}
	var entries []mock.Entry
	if err := json.Unmarshal(w.Body.Bytes(), &entries); err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestRequestFilters(t *testing.T) {
	srv, view := filterServer(t)

	tests := []struct {
		name    string
		query   string
		want    int
		wantAll func(mock.Entry) bool
	}{
		{"unfiltered", "", 4, nil},
		{"by method", "?method=POST", 3, func(e mock.Entry) bool { return e.Method == "POST" }},
		{"method is case-insensitive", "?method=post", 3, func(e mock.Entry) bool { return e.Method == "POST" }},
		{"by exact status", "?status=202", 1, func(e mock.Entry) bool { return e.Status == 202 }},
		{"by status class", "?status=4xx", 2, func(e mock.Entry) bool { return e.Status/100 == 4 }},
		{"status class is case-insensitive", "?status=4XX", 2, nil},
		{"failures only", "?invalid=true", 1, func(e mock.Entry) bool { return len(e.ValidationErrors) > 0 }},
		{"invalid=false does not filter", "?invalid=false", 4, nil},
		{"combined", "?method=POST&status=4xx", 2, func(e mock.Entry) bool {
			return e.Method == "POST" && e.Status/100 == 4
		}},
		{"combined to nothing", "?method=GET&status=5xx", 0, nil},
		{"a filter matching nothing is not an error", "?status=418", 0, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries := listRequests(t, srv, view.ID, tt.query)
			if len(entries) != tt.want {
				t.Fatalf("got %d entries, want %d", len(entries), tt.want)
			}
			for _, e := range entries {
				if tt.wantAll != nil && !tt.wantAll(e) {
					t.Errorf("%s %s -> %d slipped through the filter", e.Method, e.Path, e.Status)
				}
			}
		})
	}
}

// A mistyped filter that silently returns everything is the same trap as a
// mistyped spec key that never matches: fail the request instead.
func TestRequestFilterRejectsNonsense(t *testing.T) {
	srv, view := filterServer(t)

	tests := []struct {
		name    string
		query   string
		wantSub string
	}{
		{"unknown key", "?methd=POST", "unknown filter methd"},
		{"unknown key among good ones", "?method=POST&limit=10", "unknown filter limit"},
		{"status is not a number", "?status=nope", "status must be"},
		{"status out of range", "?status=42", "status must be"},
		{"status class out of range", "?status=9xx", "status must be"},
		{"invalid is not a bool", "?invalid=yes", "invalid must be true or false"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := do(t, srv, http.MethodGet, "/api/endpoints/"+view.ID+"/requests"+tt.query, "")
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s, want 400", w.Code, w.Body)
			}
			if !strings.Contains(w.Body.String(), tt.wantSub) {
				t.Errorf("body = %s, want it to mention %q", w.Body, tt.wantSub)
			}
		})
	}
}

// Filtering must not disturb what a listing is: same entries, same order.
func TestFilteringPreservesOrder(t *testing.T) {
	srv, view := filterServer(t)

	all := listRequests(t, srv, view.ID, "")
	posts := listRequests(t, srv, view.ID, "?method=POST")

	var want []string
	for _, e := range all {
		if e.Method == http.MethodPost {
			want = append(want, e.Path)
		}
	}
	var got []string
	for _, e := range posts {
		got = append(got, e.Path)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got = %v, want %v", got, want)
	}
}
