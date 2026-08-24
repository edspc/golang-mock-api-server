package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/edspc/golang-mock-api-server/internal/mock"
)

// createEndpoint calls the control API the way a user would and returns the
// new endpoint's view.
func createEndpoint(t *testing.T, srv *Server, body string) endpointView {
	t.Helper()
	w := do(t, srv, http.MethodPost, "/api/endpoints", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s, want 201", w.Code, w.Body)
	}
	var view endpointView
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode created endpoint: %v", err)
	}
	return view
}

func newCallbackServer(t *testing.T) *Server {
	t.Helper()
	return newTestServer(t)
}

func TestCreateEndpointReturnsUsableURL(t *testing.T) {
	srv := newCallbackServer(t)
	view := createEndpoint(t, srv, `{"name":"stripe webhooks"}`)

	if view.Name != "stripe webhooks" {
		t.Errorf("name = %q, want the requested name", view.Name)
	}
	if want := "/cb/" + view.ID; view.URL != want {
		t.Errorf("url = %q, want %q", view.URL, want)
	}
	if len(view.ID) != 36 || view.ID[14] != '8' {
		t.Errorf("id = %q, want a canonical v8 UUID", view.ID)
	}

	// The URL is live immediately, with no spec configured.
	w := do(t, srv, http.MethodPost, view.URL, `{"hello":"world"}`)
	if w.Code != http.StatusOK {
		t.Errorf("callback status = %d, want 200 from the default response", w.Code)
	}
	if !strings.Contains(w.Body.String(), "received") {
		t.Errorf("callback body = %s, want the default acknowledgement", w.Body)
	}
}

func TestCreateEndpointWithEmptyBody(t *testing.T) {
	srv := newCallbackServer(t)
	view := createEndpoint(t, srv, "")
	if view.ID == "" {
		t.Error("id is empty, want an endpoint created from an empty request body")
	}
}

func TestCreateEndpointWithSpec(t *testing.T) {
	srv := newCallbackServer(t)
	view := createEndpoint(t, srv, `{
	  "name":"with spec",
	  "spec":{"rules":[{"name":"ack","response":{"status":202,"body":{"ok":true}}}]}
	}`)

	w := do(t, srv, http.MethodPost, view.URL, "")
	if w.Code != 202 {
		t.Errorf("status = %d, want the spec supplied at creation to apply", w.Code)
	}
}

func TestCreateEndpointWithBadSpecIsRolledBack(t *testing.T) {
	srv := newCallbackServer(t)
	w := do(t, srv, http.MethodPost, "/api/endpoints", `{"spec":{"rules":[{"request":{"path":"bad"}}]}}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}

	list := do(t, srv, http.MethodGet, "/api/endpoints", "")
	if strings.TrimSpace(list.Body.String()) != "[]" {
		t.Errorf("endpoints = %s, want none left behind after a rejected spec", list.Body)
	}
}

func TestCallbackCapturesRequests(t *testing.T) {
	srv := newCallbackServer(t)
	view := createEndpoint(t, srv, "")

	r := httptest.NewRequest(http.MethodPost, view.URL+"?source=stripe", strings.NewReader(`{"event":"paid"}`))
	r.Header.Set("X-Signature", "sig-123")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)

	got := do(t, srv, http.MethodGet, "/api/endpoints/"+view.ID+"/requests", "")
	var entries []mock.Entry
	if err := json.Unmarshal(got.Body.Bytes(), &entries); err != nil {
		t.Fatalf("decode requests: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("captured %d requests, want 1", len(entries))
	}
	e := entries[0]
	if e.Method != http.MethodPost || e.Body != `{"event":"paid"}` {
		t.Errorf("entry = %+v, want the posted callback", e)
	}
	if e.Query["source"][0] != "stripe" {
		t.Errorf("query = %v, want the captured source parameter", e.Query)
	}
	if e.Headers["X-Signature"][0] != "sig-123" {
		t.Errorf("headers = %v, want the captured signature header", e.Headers)
	}
}

func TestUpdateSpecChangesSubsequentResponses(t *testing.T) {
	srv := newCallbackServer(t)
	view := createEndpoint(t, srv, "")

	// Before: default acknowledgement.
	if w := do(t, srv, http.MethodPost, view.URL, ""); w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 before the spec is written", w.Code)
	}

	spec := `{
	  "validation": {"requireHeaders":["X-Signature"],"requireFields":["event"]},
	  "rules": [
	    {"name":"paid","request":{"bodyContains":"\"event\":\"paid\""},
	     "response":{"status":202,"body":{"handled":"{{.Body.event}}","id":"{{.Body.data.id}}"}}}
	  ],
	  "response": {"status":204}
	}`
	if w := do(t, srv, http.MethodPut, "/api/endpoints/"+view.ID+"/spec", spec); w.Code != http.StatusOK {
		t.Fatalf("spec update status = %d, body = %s, want 200", w.Code, w.Body)
	}

	t.Run("valid callback hits the rule and renders the body", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, view.URL, strings.NewReader(`{"event":"paid","data":{"id":"evt_1"}}`))
		r.Header.Set("X-Signature", "sig")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)

		if w.Code != 202 {
			t.Fatalf("status = %d, want 202", w.Code)
		}
		if got := w.Body.String(); got != `{"handled":"paid","id":"evt_1"}` {
			t.Errorf("body = %s, want the request payload echoed back", got)
		}
	})

	t.Run("unsigned callback fails validation", func(t *testing.T) {
		w := do(t, srv, http.MethodPost, view.URL, `{"event":"paid"}`)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", w.Code)
		}
		if !strings.Contains(w.Body.String(), "missing header X-Signature") {
			t.Errorf("body = %s, want the validation reason", w.Body)
		}
	})

	t.Run("valid but unmatched callback gets the default response", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, view.URL, strings.NewReader(`{"event":"refunded"}`))
		r.Header.Set("X-Signature", "sig")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)

		if w.Code != http.StatusNoContent {
			t.Errorf("status = %d, want the spec's default 204", w.Code)
		}
	})

	t.Run("invalid filter lists only failures", func(t *testing.T) {
		w := do(t, srv, http.MethodGet, "/api/endpoints/"+view.ID+"/requests?invalid=true", "")
		var entries []mock.Entry
		if err := json.Unmarshal(w.Body.Bytes(), &entries); err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 {
			t.Fatalf("filtered to %d entries, want only the failed one", len(entries))
		}
		if len(entries[0].ValidationErrors) == 0 {
			t.Error("entry has no validationErrors, want the failure reasons recorded")
		}
	})
}

func TestBadSpecKeepsPreviousBehaviour(t *testing.T) {
	srv := newCallbackServer(t)
	view := createEndpoint(t, srv, `{"spec":{"rules":[{"name":"v1","response":{"status":201}}]}}`)

	w := do(t, srv, http.MethodPut, "/api/endpoints/"+view.ID+"/spec", `{"rules":[{"request":{"path":"nope"}}]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an invalid spec", w.Code)
	}
	if w := do(t, srv, http.MethodPost, view.URL, ""); w.Code != 201 {
		t.Errorf("status = %d, want the previous spec still serving", w.Code)
	}
}

// The console reads the spec back into its editor after every save, so the
// server must not echo fields the user never wrote.
func TestSpecRoundTripsWithoutZeroValueNoise(t *testing.T) {
	srv := newCallbackServer(t)
	view := createEndpoint(t, srv, "")

	spec := `{"rules":[{"name":"ack","response":{"status":202}}]}`
	w := do(t, srv, http.MethodPut, "/api/endpoints/"+view.ID+"/spec", spec)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s, want 200", w.Code, w.Body)
	}

	for _, noise := range []string{`"bodyFile"`, `"delay"`, `"headers":null`, `"query"`, `"bodyContains"`} {
		if strings.Contains(w.Body.String(), noise) {
			t.Errorf("spec echo contains %s, want zero values omitted:\n%s", noise, w.Body)
		}
	}

	// Re-saving exactly what was returned must be accepted and stay stable.
	var got endpointView
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	echoed, err := json.Marshal(got.Spec)
	if err != nil {
		t.Fatal(err)
	}
	again := do(t, srv, http.MethodPut, "/api/endpoints/"+view.ID+"/spec", string(echoed))
	if again.Code != http.StatusOK {
		t.Fatalf("re-saving the echoed spec: status = %d, body = %s, want 200", again.Code, again.Body)
	}
}

func TestSpecRejectsUnknownFields(t *testing.T) {
	srv := newCallbackServer(t)
	view := createEndpoint(t, srv, "")

	w := do(t, srv, http.MethodPut, "/api/endpoints/"+view.ID+"/spec", `{"ruls":[]}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a misspelled spec key", w.Code)
	}
}

func TestCallbackSubPathRouting(t *testing.T) {
	srv := newCallbackServer(t)
	view := createEndpoint(t, srv, `{"spec":{"rules":[
	  {"name":"success","request":{"path":"/success"},"response":{"status":200,"body":{"r":"ok"}}},
	  {"name":"failure","request":{"path":"/failure"},"response":{"status":500,"body":{"r":"fail"}}}
	]}}`)

	if w := do(t, srv, http.MethodPost, view.URL+"/success", ""); w.Code != 200 {
		t.Errorf("status = %d, want the /success rule", w.Code)
	}
	if w := do(t, srv, http.MethodPost, view.URL+"/failure", ""); w.Code != 500 {
		t.Errorf("status = %d, want the /failure rule", w.Code)
	}
}

func TestUnknownEndpointIs404(t *testing.T) {
	srv := newCallbackServer(t)
	for _, path := range []string{"/cb/", "/cb/not-a-uuid", "/cb/0192f0c8-3f4e-8a1b-9c2d-4e5f60718293"} {
		if w := do(t, srv, http.MethodPost, path, ""); w.Code != http.StatusNotFound {
			t.Errorf("%s status = %d, want 404", path, w.Code)
		}
	}
}

func TestDeleteEndpoint(t *testing.T) {
	srv := newCallbackServer(t)
	view := createEndpoint(t, srv, "")

	if w := do(t, srv, http.MethodDelete, "/api/endpoints/"+view.ID, ""); w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if w := do(t, srv, http.MethodPost, view.URL, ""); w.Code != http.StatusNotFound {
		t.Errorf("callback status = %d, want 404 after deletion", w.Code)
	}
}

func TestEndpointResetKeepsReceivedCount(t *testing.T) {
	srv := newCallbackServer(t)
	view := createEndpoint(t, srv, "")
	do(t, srv, http.MethodPost, view.URL, "")
	do(t, srv, http.MethodPost, view.URL, "")

	if w := do(t, srv, http.MethodPost, "/api/endpoints/"+view.ID+"/reset", ""); w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	w := do(t, srv, http.MethodGet, "/api/endpoints/"+view.ID, "")
	var got endpointView
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Received != 2 {
		t.Errorf("received = %d, want the lifetime count kept across a reset", got.Received)
	}
}

// Each endpoint keeps its own history, so one endpoint's traffic never shows
// up under another.
func TestEachEndpointHasItsOwnHistory(t *testing.T) {
	srv := newCallbackServer(t)
	first := createEndpoint(t, srv, `{"name":"first"}`)
	second := createEndpoint(t, srv, `{"name":"second"}`)

	do(t, srv, http.MethodPost, first.URL, `{"to":"first"}`)
	do(t, srv, http.MethodPost, second.URL, `{"to":"second"}`)
	do(t, srv, http.MethodPost, second.URL, `{"to":"second again"}`)

	for _, tc := range []struct {
		view endpointView
		want int
		body string
	}{
		{first, 1, `{"to":"first"}`},
		{second, 2, `{"to":"second"}`},
	} {
		w := do(t, srv, http.MethodGet, "/api/endpoints/"+tc.view.ID+"/requests", "")
		var entries []mock.Entry
		if err := json.Unmarshal(w.Body.Bytes(), &entries); err != nil {
			t.Fatal(err)
		}
		if len(entries) != tc.want {
			t.Errorf("%s captured %d requests, want %d", tc.view.Name, len(entries), tc.want)
			continue
		}
		if entries[0].Body != tc.body {
			t.Errorf("%s entry[0].Body = %q, want %q", tc.view.Name, entries[0].Body, tc.body)
		}
		if entries[0].Path != tc.view.URL {
			t.Errorf("%s entry[0].Path = %q, want its own URL %q", tc.view.Name, entries[0].Path, tc.view.URL)
		}
	}
}

func TestEndpointNotFoundOnAdminRoutes(t *testing.T) {
	srv := newCallbackServer(t)
	for _, path := range []string{
		"/api/endpoints/nope",
		"/api/endpoints/nope/requests",
	} {
		if w := do(t, srv, http.MethodGet, path, ""); w.Code != http.StatusNotFound {
			t.Errorf("%s status = %d, want 404", path, w.Code)
		}
	}
}
