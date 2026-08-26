package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/edspc/golang-mock-api-server/internal/mock"
	"github.com/edspc/golang-mock-api-server/internal/uuid"
)

// requestID reads the header off a response, insisting on the documented
// spelling: Header.Get is case-insensitive, the map key is not, and the name
// is what users paste into the console.
func requestID(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	values, ok := w.Header()[RequestIDHeader]
	if !ok {
		t.Fatalf("no %s header; got %v", RequestIDHeader, w.Header())
	}
	if len(values) != 1 {
		t.Fatalf("%s = %v, want exactly one value", RequestIDHeader, values)
	}
	if _, err := uuid.Parse(values[0]); err != nil {
		t.Errorf("%s = %q, want a UUID: %v", RequestIDHeader, values[0], err)
	}
	return values[0]
}

// Every answer an endpoint gives carries the id, including the ones a caller
// is most likely to ask about afterwards.
func TestEveryCallbackAnswerCarriesARequestID(t *testing.T) {
	srv := newTestServer(t)
	view := createEndpoint(t, srv, `{"spec":{
		"validation": {"requireHeaders": ["X-Signature"]},
		"rules": [
			{"name": "ack", "request": {"path": "/ok"}, "response": {"status": 202}},
			{"name": "broken", "request": {"path": "/broken"}, "response": {"body": {"x": "{{.Nope"}}}
		],
		"response": {"status": 204}
	}}`)

	signed := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, view.URL+path, nil)
		r.Header.Set("X-Signature", "sig")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}

	tests := []struct {
		name       string
		response   *httptest.ResponseRecorder
		wantStatus int
	}{
		{"a matched rule", signed("/ok"), 202},
		{"the default response", signed("/anything"), 204},
		{"a validation failure", do(t, srv, http.MethodPost, view.URL+"/ok", ""), 400},
		{"a broken response template", signed("/broken"), 500},
	}

	seen := map[string]bool{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", tt.response.Code, tt.wantStatus)
			}
			id := requestID(t, tt.response)
			if seen[id] {
				t.Errorf("id %s was handed out twice", id)
			}
			seen[id] = true
		})
	}
}

// The id in the header is the id of the row that was recorded — that is the
// whole point of returning it.
func TestRequestIDNamesTheCapturedRequest(t *testing.T) {
	srv := newTestServer(t)
	view := createEndpoint(t, srv, "")

	first := do(t, srv, http.MethodPost, view.URL, `{"n":1}`)
	second := do(t, srv, http.MethodPost, view.URL, `{"n":2}`)
	wantFirst, wantSecond := requestID(t, first), requestID(t, second)

	entries := listRequests(t, srv, view.ID, "")
	if len(entries) != 2 {
		t.Fatalf("captured %d requests, want 2", len(entries))
	}
	if entries[0].ID != wantFirst || entries[1].ID != wantSecond {
		t.Fatalf("ids = %s, %s; want %s, %s", entries[0].ID, entries[1].ID, wantFirst, wantSecond)
	}

	// And each one can be looked up on its own.
	for _, tc := range []struct{ id, wantBody string }{
		{wantFirst, `{"n":1}`},
		{wantSecond, `{"n":2}`},
		{strings.ToUpper(wantSecond), `{"n":2}`}, // ids are quoted back from logs
	} {
		found := listRequests(t, srv, view.ID, "?requestId="+tc.id)
		if len(found) != 1 {
			t.Fatalf("requestId=%s returned %d entries, want 1", tc.id, len(found))
		}
		if found[0].Body != tc.wantBody {
			t.Errorf("body = %s, want %s", found[0].Body, tc.wantBody)
		}
	}
}

// An id that finds nothing is an empty listing, not an error: the history is
// bounded, so a real id can age out of it.
func TestUnknownRequestIDIsAnEmptyListing(t *testing.T) {
	srv := newTestServer(t)
	view := createEndpoint(t, srv, "")
	do(t, srv, http.MethodPost, view.URL, "")

	for _, id := range []string{"01a03d40-2b0a-8000-ac90-a89c884021ee", "not-a-uuid"} {
		if got := listRequests(t, srv, view.ID, "?requestId="+id); len(got) != 0 {
			t.Errorf("requestId=%s returned %d entries, want none", id, len(got))
		}
	}
}

// Each endpoint keeps its own history, so an id from one must not be found
// under another.
func TestRequestIDDoesNotCrossEndpoints(t *testing.T) {
	srv := newTestServer(t)
	mine := createEndpoint(t, srv, "")
	theirs := createEndpoint(t, srv, "")

	id := requestID(t, do(t, srv, http.MethodPost, mine.URL, ""))
	if got := listRequests(t, srv, theirs.ID, "?requestId="+id); len(got) != 0 {
		t.Errorf("found %d entries under the other endpoint, want none", len(got))
	}
}

// A spec that sets the header itself replaces ours rather than joining it: two
// values under one name, differing only in case, is not an answer.
func TestSpecCanOverrideTheRequestIDHeader(t *testing.T) {
	srv := newTestServer(t)
	view := createEndpoint(t, srv, `{"spec":{"rules":[
		{"name": "mine", "response": {"status": 200, "headers": {"X-Mock-API-RequestID": "chosen"}}}
	]}}`)

	w := do(t, srv, http.MethodPost, view.URL, "")
	if got := w.Header().Values(RequestIDHeader); len(got) != 1 || got[0] != "chosen" {
		t.Errorf("%s = %v, want [chosen]", RequestIDHeader, got)
	}
	var total int
	for name, values := range w.Header() {
		if strings.EqualFold(name, RequestIDHeader) {
			total += len(values)
		}
	}
	if total != 1 {
		t.Errorf("the response carries %d request-id headers, want 1", total)
	}

	// The request is still recorded under the id the server minted.
	entries := listRequests(t, srv, view.ID, "")
	if len(entries) != 1 || entries[0].ID == "" {
		t.Errorf("entries = %+v, want one with an id of its own", entries)
	}
}

// The id has to survive the JSON the console reads.
func TestRequestIDIsInTheListing(t *testing.T) {
	srv := newTestServer(t)
	view := createEndpoint(t, srv, "")
	id := requestID(t, do(t, srv, http.MethodPost, view.URL, ""))

	w := do(t, srv, http.MethodGet, "/api/endpoints/"+view.ID+"/requests", "")
	if !strings.Contains(w.Body.String(), `"id": "`+id+`"`) {
		t.Errorf("listing = %s, want it to carry %s", w.Body, id)
	}
	var entries []mock.Entry
	if err := json.Unmarshal(w.Body.Bytes(), &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ID != id {
		t.Errorf("decoded %+v, want one entry with id %s", entries, id)
	}
}
